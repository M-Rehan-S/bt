package swarm

import (
	"bytes"
	"crypto/sha1"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/M-Rehan-S/bt/internal/wire"
)

func waitForCompleteEvent(t *testing.T, logPath string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(logPath)
		if err == nil {
			for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
				if line == "" {
					continue
				}
				var ev map[string]any
				if json.Unmarshal([]byte(line), &ev) == nil && ev["ev"] == "complete" {
					return
				}
			}
		}
		time.Sleep(15 * time.Millisecond)
	}
	raw, _ := os.ReadFile(logPath)
	t.Fatalf("timed out waiting for complete event in %s; log so far:\n%s", logPath, raw)
}

func TestSeedToLeecherEndToEnd(t *testing.T) {
	// 100,000 bytes with 40,000-byte pieces: two full pieces (3 blocks each)
	// and a shorter last piece of 20,000 bytes (16,384 + 3,616).
	tor, data := makeTestTorrentAndData(t, 100000, 40000)
	tor.InfoHash = sha1.Sum([]byte("end-to-end-test-torrent"))

	dir := t.TempDir()
	seedPath := filepath.Join(dir, "seed.bin")
	if err := os.WriteFile(seedPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	seedLog := filepath.Join(dir, "seed.jsonl")
	seedAddr, stopSeed := startTestSwarm(t, Config{
		Torrent: tor,
		OutPath: seedPath,
		Seed:    true,
		Port:    0,
		Name:    "seed",
		LogPath: seedLog,
		PeerID:  makePeerID('S'),
	})
	defer func() {
		if err := stopSeed(); err != nil {
			t.Errorf("seed Run error: %v", err)
		}
	}()

	_, seedPort, err := net.SplitHostPort(seedAddr)
	if err != nil {
		t.Fatal(err)
	}
	seedTarget := net.JoinHostPort("127.0.0.1", seedPort)

	leecherPath := filepath.Join(dir, "l1.bin")
	leecherLog := filepath.Join(dir, "l1.jsonl")
	_, stopLeecher := startTestSwarm(t, Config{
		Torrent:     tor,
		OutPath:     leecherPath,
		Port:        0,
		Name:        "l1",
		LogPath:     leecherLog,
		StaticPeers: []string{seedTarget},
		PeerID:      makePeerID('L'),
	})
	defer func() {
		if err := stopLeecher(); err != nil {
			t.Errorf("leecher Run error: %v", err)
		}
	}()

	waitForCompleteEvent(t, leecherLog, 5*time.Second)

	// Leecher must keep running after complete until explicitly stopped.
	time.Sleep(50 * time.Millisecond)

	if err := stopLeecher(); err != nil {
		t.Fatalf("stopLeecher error: %v", err)
	}
	if err := stopSeed(); err != nil {
		t.Fatalf("stopSeed error: %v", err)
	}

	gotData, err := os.ReadFile(leecherPath)
	if err != nil {
		t.Fatalf("read leecher output: %v", err)
	}
	if !bytes.Equal(gotData, data) {
		t.Fatalf("downloaded file bytes do not match seed (%d vs %d)", len(gotData), len(data))
	}

	rawLeech, err := os.ReadFile(leecherLog)
	if err != nil {
		t.Fatal(err)
	}
	pieceOKs := make(map[int]bool)
	var completeCount int
	var finalDownBytes, finalDownWire int64
	for _, line := range strings.Split(strings.TrimSpace(string(rawLeech)), "\n") {
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("invalid JSON line %s: %v", line, err)
		}
		switch ev["ev"] {
		case "piece_ok":
			pieceOKs[int(ev["idx"].(float64))] = true
			if ev["from"] != seedTarget {
				t.Fatalf("piece_ok from = %v, want %s", ev["from"], seedTarget)
			}
		case "complete":
			completeCount++
		case "stats":
			finalDownBytes = int64(ev["down_bytes"].(float64))
			finalDownWire = int64(ev["down_wire"].(float64))
		}
	}
	if len(pieceOKs) != 3 {
		t.Fatalf("piece_ok count = %d, want 3:\n%s", len(pieceOKs), rawLeech)
	}
	if completeCount != 1 {
		t.Fatalf("complete count = %d, want 1", completeCount)
	}
	if finalDownBytes != 100000 || finalDownWire <= finalDownBytes {
		t.Fatalf("unexpected final stats: down_bytes=%d down_wire=%d", finalDownBytes, finalDownWire)
	}

	rawSeed, err := os.ReadFile(seedLog)
	if err != nil {
		t.Fatal(err)
	}
	var finalUpBytes int64
	for _, line := range strings.Split(strings.TrimSpace(string(rawSeed)), "\n") {
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatal(err)
		}
		if ev["ev"] == "stats" {
			finalUpBytes = int64(ev["up_bytes"].(float64))
		}
	}
	if finalUpBytes != 100000 {
		t.Fatalf("seed final up_bytes = %d, want 100000", finalUpBytes)
	}
}

func TestDownloadPipeliningAndChokeRecovery(t *testing.T) {
	// 1 piece of 81,920 bytes = exactly 5 blocks of 16,384 bytes -> tests full 5-request pipeline.
	tor, data := makeTestTorrentAndData(t, 81920, 81920)
	tor.InfoHash = sha1.Sum([]byte("pipeline-choke-test"))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	mockErrCh := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			mockErrCh <- err
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(4 * time.Second))

		if err := wire.WriteHandshake(conn, tor.InfoHash, makePeerID('M')); err != nil {
			mockErrCh <- err
			return
		}
		if _, _, err := wire.ReadHandshake(conn); err != nil {
			mockErrCh <- err
			return
		}
		// Read leecher empty bitfield.
		if _, err := wire.ReadMessage(conn); err != nil {
			mockErrCh <- err
			return
		}

		// Send full bitfield (piece 0).
		bf := wire.NewBitfield(1)
		bf.Set(0)
		if _, err := conn.Write((&wire.Message{ID: wire.MsgBitfield, Payload: bf}).Serialize()); err != nil {
			mockErrCh <- err
			return
		}

		// Read interested, then unchoke.
		msg, err := wire.ReadMessage(conn)
		if err != nil || msg == nil || msg.ID != wire.MsgInterested {
			mockErrCh <- os.ErrInvalid
			return
		}
		if _, err := conn.Write((&wire.Message{ID: wire.MsgUnchoke}).Serialize()); err != nil {
			mockErrCh <- err
			return
		}

		// Leecher must pipeline all 5 block requests before receiving any piece message!
		type reqSpec struct{ idx, begin, length uint32 }
		var firstBatch []reqSpec
		for i := 0; i < 5; i++ {
			m, err := wire.ReadMessage(conn)
			if err != nil {
				mockErrCh <- err
				return
			}
			idx, begin, length, err := wire.ParseRequest(m)
			if err != nil {
				mockErrCh <- err
				return
			}
			firstBatch = append(firstBatch, reqSpec{idx, begin, length})
		}

		// Send first 2 blocks, then choke! Leecher must discard inflight/partial piece.
		for i := 0; i < 2; i++ {
			r := firstBatch[i]
			blk := data[r.begin : r.begin+r.length]
			if _, err := conn.Write(wire.NewPiece(r.idx, r.begin, blk).Serialize()); err != nil {
				mockErrCh <- err
				return
			}
		}
		if _, err := conn.Write((&wire.Message{ID: wire.MsgChoke}).Serialize()); err != nil {
			mockErrCh <- err
			return
		}

		// Re-unchoke; leecher must re-request all 5 blocks of piece 0.
		if _, err := conn.Write((&wire.Message{ID: wire.MsgUnchoke}).Serialize()); err != nil {
			mockErrCh <- err
			return
		}
		for i := 0; i < 5; i++ {
			m, err := wire.ReadMessage(conn)
			if err != nil {
				mockErrCh <- err
				return
			}
			idx, begin, length, err := wire.ParseRequest(m)
			if err != nil {
				mockErrCh <- err
				return
			}
			blk := data[begin : begin+length]
			if _, err := conn.Write(wire.NewPiece(idx, begin, blk).Serialize()); err != nil {
				mockErrCh <- err
				return
			}
		}

		// Expect Have(0) and NotInterested after piece verifies.
		var sawHave, sawNotInt bool
		for !(sawHave && sawNotInt) {
			m, err := wire.ReadMessage(conn)
			if err != nil {
				mockErrCh <- err
				return
			}
			if m == nil {
				continue
			}
			if m.ID == wire.MsgHave {
				if idx, err := wire.ParseHave(m); err == nil && idx == 0 {
					sawHave = true
				}
			} else if m.ID == wire.MsgNotInterested {
				sawNotInt = true
			}
		}
		mockErrCh <- nil
	}()

	dir := t.TempDir()
	outPath := filepath.Join(dir, "l1.bin")
	logPath := filepath.Join(dir, "l1.jsonl")
	_, stopLeecher := startTestSwarm(t, Config{
		Torrent:     tor,
		OutPath:     outPath,
		Port:        0,
		Name:        "l1",
		LogPath:     logPath,
		StaticPeers: []string{ln.Addr().String()},
		PeerID:      makePeerID('L'),
	})
	defer func() {
		if err := stopLeecher(); err != nil {
			t.Errorf("leecher Run error: %v", err)
		}
	}()

	if err := <-mockErrCh; err != nil {
		t.Fatalf("mock peer error: %v", err)
	}
	waitForCompleteEvent(t, logPath, 3*time.Second)

	gotData, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotData, data) {
		t.Fatal("downloaded bytes mismatch after choke recovery")
	}
}

func TestDownloadHashFailAndPeerDisconnectRecovery(t *testing.T) {
	// 1 piece of 32,768 bytes (2 blocks of 16,384 bytes).
	tor, data := makeTestTorrentAndData(t, 32768, 32768)
	tor.InfoHash = sha1.Sum([]byte("hashfail-disconnect-test"))

	// Peer 1 sends corrupted block 0 -> triggers hash_fail -> disconnects mid-retry.
	ln1, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln1.Close()

	// Peer 2 stays connected and serves valid data cleanly after Peer 1 disconnects.
	ln2, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln2.Close()

	p1Done := make(chan error, 1)
	go func() {
		conn, err := ln1.Accept()
		if err != nil {
			p1Done <- err
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(4 * time.Second))

		if err := wire.WriteHandshake(conn, tor.InfoHash, makePeerID('1')); err != nil {
			p1Done <- err
			return
		}
		if _, _, err := wire.ReadHandshake(conn); err != nil {
			p1Done <- err
			return
		}
		if _, err := wire.ReadMessage(conn); err != nil { // bitfield
			p1Done <- err
			return
		}
		bf := wire.NewBitfield(1)
		bf.Set(0)
		if _, err := conn.Write((&wire.Message{ID: wire.MsgBitfield, Payload: bf}).Serialize()); err != nil {
			p1Done <- err
			return
		}
		if _, err := wire.ReadMessage(conn); err != nil { // interested
			p1Done <- err
			return
		}
		if _, err := conn.Write((&wire.Message{ID: wire.MsgUnchoke}).Serialize()); err != nil {
			p1Done <- err
			return
		}

		// Serve first attempt with 1 corrupted byte so SHA-1 fails.
		for i := 0; i < 2; i++ {
			m, err := wire.ReadMessage(conn)
			if err != nil {
				p1Done <- err
				return
			}
			idx, begin, length, err := wire.ParseRequest(m)
			if err != nil {
				p1Done <- err
				return
			}
			blk := append([]byte(nil), data[begin:begin+length]...)
			if i == 0 {
				blk[0] ^= 0xff
			}
			if _, err := conn.Write(wire.NewPiece(idx, begin, blk).Serialize()); err != nil {
				p1Done <- err
				return
			}
		}

		// Read one retry request, send 1 block, then drop connection!
		m, err := wire.ReadMessage(conn)
		if err != nil {
			p1Done <- err
			return
		}
		idx, begin, length, err := wire.ParseRequest(m)
		if err != nil {
			p1Done <- err
			return
		}
		if _, err := conn.Write(wire.NewPiece(idx, begin, data[begin:begin+length]).Serialize()); err != nil {
			p1Done <- err
			return
		}
		p1Done <- nil
	}()

	p2Done := make(chan error, 1)
	go func() {
		conn, err := ln2.Accept()
		if err != nil {
			p2Done <- err
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(4 * time.Second))

		if err := wire.WriteHandshake(conn, tor.InfoHash, makePeerID('2')); err != nil {
			p2Done <- err
			return
		}
		if _, _, err := wire.ReadHandshake(conn); err != nil {
			p2Done <- err
			return
		}
		if _, err := wire.ReadMessage(conn); err != nil { // bitfield
			p2Done <- err
			return
		}
		// Wait until Peer 1 fails & disconnects before announcing bitfield so Peer 1 gets first attempt.
		if err := <-p1Done; err != nil {
			p2Done <- err
			return
		}

		bf := wire.NewBitfield(1)
		bf.Set(0)
		if _, err := conn.Write((&wire.Message{ID: wire.MsgBitfield, Payload: bf}).Serialize()); err != nil {
			p2Done <- err
			return
		}
		if _, err := wire.ReadMessage(conn); err != nil { // interested
			p2Done <- err
			return
		}
		if _, err := conn.Write((&wire.Message{ID: wire.MsgUnchoke}).Serialize()); err != nil {
			p2Done <- err
			return
		}

		for i := 0; i < 2; i++ {
			m, err := wire.ReadMessage(conn)
			if err != nil {
				p2Done <- err
				return
			}
			idx, begin, length, err := wire.ParseRequest(m)
			if err != nil {
				p2Done <- err
				return
			}
			if _, err := conn.Write(wire.NewPiece(idx, begin, data[begin:begin+length]).Serialize()); err != nil {
				p2Done <- err
				return
			}
		}
		p2Done <- nil
	}()

	dir := t.TempDir()
	outPath := filepath.Join(dir, "l1.bin")
	logPath := filepath.Join(dir, "l1.jsonl")
	_, stopLeecher := startTestSwarm(t, Config{
		Torrent:     tor,
		OutPath:     outPath,
		Port:        0,
		Name:        "l1",
		LogPath:     logPath,
		StaticPeers: []string{ln1.Addr().String(), ln2.Addr().String()},
		PeerID:      makePeerID('L'),
	})
	defer func() {
		if err := stopLeecher(); err != nil {
			t.Errorf("leecher Run error: %v", err)
		}
	}()

	if err := <-p2Done; err != nil {
		t.Fatalf("peer 2 error: %v", err)
	}
	waitForCompleteEvent(t, logPath, 3*time.Second)
	if err := stopLeecher(); err != nil {
		t.Fatalf("stopLeecher error: %v", err)
	}

	gotData, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotData, data) {
		t.Fatal("downloaded bytes mismatch after hash_fail + disconnect recovery")
	}

	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	var sawHashFail, sawPieceOK bool
	var finalDownBytes int64
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatal(err)
		}
		if ev["ev"] == "hash_fail" {
			sawHashFail = true
		}
		if ev["ev"] == "piece_ok" {
			sawPieceOK = true
		}
		if ev["ev"] == "stats" {
			finalDownBytes = int64(ev["down_bytes"].(float64))
		}
	}
	if !sawHashFail || !sawPieceOK {
		t.Fatalf("expected both hash_fail and piece_ok in log:\n%s", raw)
	}
	// Corrupted piece blocks (32,768) + Peer 1 retry block (16,384) + Peer 2 valid piece (32,768) > 32,768.
	if finalDownBytes <= 32768 {
		t.Fatalf("expected down_bytes to include failed-piece blocks, got %d", finalDownBytes)
	}
}

func TestSweepRequestTimeoutsAndRejectBadPieceBlocks(t *testing.T) {
	tor, data := makeTestTorrentAndData(t, 32768, 32768)
	store, err := OpenStorage(filepath.Join(t.TempDir(), "out.bin"), tor, false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	stats := &Stats{}
	pc := newPeerConn(c1, "", true, stats)
	bf := wire.NewBitfield(1)
	bf.Set(0)

	ps := &peerState{
		pc:           pc,
		peerID:       makePeerID('P'),
		peerChoking:  false,
		amChoking:    true,
		bitfield:     bf,
		inflight:     map[blockKey]time.Time{{idx: 0, begin: 0}: time.Now().Add(-11 * time.Second)},
		currentPiece: 0,
	}

	pieces := initPieces(1, false)
	pieces[0].state = pieceInProgress
	pieces[0].owner = pc
	pieces[0].buf = make([]byte, 32768)
	pieces[0].blocksDone = make([]bool, 2)
	pieces[0].nextBlock = 1
	pieces[0].totalBlocks = 2

	coord := &coordinator{
		cfg:         Config{Torrent: tor},
		store:       store,
		logger:      &Logger{},
		stats:       stats,
		rng:         newCoordinatorRNG(),
		numPieces:   1,
		ourBitfield: wire.NewBitfield(1),
		pieces:      pieces,
		peers:       map[*peerConn]*peerState{pc: ps},
		byPeerID:    map[[20]byte]*peerConn{ps.peerID: pc},
	}

	// Reject unrequested / wrong-size / misaligned block before timeout sweep.
	if err := coord.handlePiece(ps, wire.NewPiece(0, 16384, data[16384:])); err != nil {
		t.Fatal(err)
	}
	if err := coord.handlePiece(ps, wire.NewPiece(0, 0, []byte{1, 2, 3})); err != nil {
		t.Fatal(err)
	}
	if pieces[0].receivedBlocks != 0 || stats.Snapshot().DownBytes != 0 {
		t.Fatalf("bad piece blocks should be rejected cleanly")
	}

	coord.sweepRequestTimeouts(time.Now())
	if pieces[0].state != pieceInProgress || len(ps.inflight) != 2 || pieces[0].nextBlock != 2 {
		t.Fatalf("unexpected state after timeout sweep: state=%d inflight=%d nextBlock=%d",
			pieces[0].state, len(ps.inflight), pieces[0].nextBlock)
	}
	if _, ok := ps.inflight[blockKey{idx: 0, begin: 0}]; !ok {
		t.Fatal("expected block (0, 0) to be re-requested after timeout")
	}
	if _, ok := ps.inflight[blockKey{idx: 0, begin: 16384}]; !ok {
		t.Fatal("expected block (0, 16384) to be re-requested after timeout")
	}
}
