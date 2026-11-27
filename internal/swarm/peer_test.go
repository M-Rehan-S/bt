package swarm

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/M-Rehan-S/bt/internal/wire"
)

func makePeerID(tag byte) [20]byte {
	var id [20]byte
	copy(id[:8], "-RZ0001-")
	id[8] = tag
	return id
}

func startTestSwarm(t *testing.T, cfg Config) (addr string, stop func() error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	addrCh := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		errCh <- run(ctx, cfg, func(a net.Addr) {
			addrCh <- a.String()
		})
	}()

	var (
		once    sync.Once
		stopErr error
	)
	stop = func() error {
		once.Do(func() {
			cancel()
			stopErr = <-errCh
		})
		return stopErr
	}

	select {
	case addr = <-addrCh:
		return addr, stop
	case err := <-errCh:
		cancel()
		t.Fatalf("swarm exited before listening: %v", err)
		return "", nil
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("timed out waiting for swarm listener")
		return "", nil
	}
}

func TestSeedHandshakeBitfieldUnchokeAndServeRequests(t *testing.T) {
	// 2 pieces of 32 bytes + 1 shorter piece of 10 bytes = 74 bytes.
	tor, data := makeTestTorrentAndData(t, 74, 32)
	tor.InfoHash = [20]byte{1, 2, 3, 4, 5}

	dir := t.TempDir()
	seedPath := filepath.Join(dir, "seed.bin")
	if err := os.WriteFile(seedPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "seed.jsonl")

	seedID := makePeerID('S')
	seedAddr, stopSeed := startTestSwarm(t, Config{
		Torrent: tor,
		OutPath: seedPath,
		Seed:    true,
		Port:    0,
		Name:    "seed",
		LogPath: logPath,
		PeerID:  seedID,
	})
	defer func() {
		if err := stopSeed(); err != nil {
			t.Errorf("seed Run error: %v", err)
		}
	}()

	conn, err := net.DialTimeout("tcp", seedAddr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial seed: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))

	clientID := makePeerID('L')
	if err := wire.WriteHandshake(conn, tor.InfoHash, clientID); err != nil {
		t.Fatalf("WriteHandshake: %v", err)
	}
	gotHash, gotID, err := wire.ReadHandshake(conn)
	if err != nil {
		t.Fatalf("ReadHandshake: %v", err)
	}
	if gotHash != tor.InfoHash || gotID != seedID {
		t.Fatalf("unexpected handshake (%x, %x)", gotHash, gotID)
	}

	// Seed must immediately send a full bitfield for 3 pieces (0xe0).
	msg, err := wire.ReadMessage(conn)
	if err != nil {
		t.Fatalf("ReadMessage(bitfield): %v", err)
	}
	if msg == nil || msg.ID != wire.MsgBitfield {
		t.Fatalf("expected bitfield message, got %#v", msg)
	}
	bf := wire.Bitfield(msg.Payload)
	if len(bf) != 1 || !bf.Has(0) || !bf.Has(1) || !bf.Has(2) || bf.Has(3) {
		t.Fatalf("unexpected seed bitfield: %08b", bf)
	}

	// Send client bitfield (all zero).
	if _, err := conn.Write((&wire.Message{ID: wire.MsgBitfield, Payload: wire.NewBitfield(3)}).Serialize()); err != nil {
		t.Fatal(err)
	}

	// While choked, a request must NOT be served. Let's send interested right after,
	// so we verify the response is Unchoke first (not a Piece for the choked request).
	if _, err := conn.Write(wire.NewRequest(0, 0, 16).Serialize()); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write((&wire.Message{ID: wire.MsgInterested}).Serialize()); err != nil {
		t.Fatal(err)
	}

	msg, err = wire.ReadMessage(conn)
	if err != nil {
		t.Fatalf("ReadMessage(unchoke): %v", err)
	}
	if msg == nil || msg.ID != wire.MsgUnchoke {
		t.Fatalf("expected unchoke (and choked request ignored), got %#v", msg)
	}

	// Invalid requests (length > 16384, out-of-bounds offset) must be ignored safely.
	if _, err := conn.Write(wire.NewRequest(0, 0, 16385).Serialize()); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write(wire.NewRequest(2, 4, 7).Serialize()); err != nil {
		t.Fatal(err)
	}

	// Request block 0 of piece 0 and the entire shorter last piece (piece 2, 10 bytes).
	if _, err := conn.Write(wire.NewRequest(0, 0, 16).Serialize()); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write(wire.NewRequest(2, 0, 10).Serialize()); err != nil {
		t.Fatal(err)
	}

	m1, err := wire.ReadMessage(conn)
	if err != nil {
		t.Fatalf("ReadMessage(piece 0): %v", err)
	}
	idx1, begin1, blk1, err := wire.ParsePiece(m1)
	if err != nil || idx1 != 0 || begin1 != 0 || !bytes.Equal(blk1, data[0:16]) {
		t.Fatalf("piece 0 block mismatch: idx=%d begin=%d err=%v", idx1, begin1, err)
	}

	m2, err := wire.ReadMessage(conn)
	if err != nil {
		t.Fatalf("ReadMessage(piece 2): %v", err)
	}
	idx2, begin2, blk2, err := wire.ParsePiece(m2)
	if err != nil || idx2 != 2 || begin2 != 0 || !bytes.Equal(blk2, data[64:74]) {
		t.Fatalf("piece 2 block mismatch: idx=%d begin=%d len=%d err=%v", idx2, begin2, len(blk2), err)
	}

	// Stop seed cleanly and verify final stats and piece_sent in seed log.
	if err := stopSeed(); err != nil {
		t.Fatalf("seed Run error: %v", err)
	}

	rawLog, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	var sawPieceSent2, sawStatsUp bool
	for _, line := range strings.Split(strings.TrimSpace(string(rawLog)), "\n") {
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("invalid log JSON %s: %v", line, err)
		}
		if ev["ev"] == "piece_sent" && ev["idx"] == float64(2) {
			sawPieceSent2 = true
		}
		if ev["ev"] == "stats" && ev["up_bytes"] == float64(26) {
			if wireBytes, ok := ev["up_wire"].(float64); ok && wireBytes > 26 {
				sawStatsUp = true
			}
		}
	}
	if !sawPieceSent2 {
		t.Fatalf("expected piece_sent event for final block of piece 2 in log:\n%s", rawLog)
	}
	if !sawStatsUp {
		t.Fatalf("expected final stats up_bytes=26 and up_wire>26 in log:\n%s", rawLog)
	}
}

func TestSeedNoUploadDoesNotUnchoke(t *testing.T) {
	tor, data := makeTestTorrentAndData(t, 64, 32)
	tor.InfoHash = [20]byte{9, 9, 9}
	dir := t.TempDir()
	seedPath := filepath.Join(dir, "seed.bin")
	if err := os.WriteFile(seedPath, data, 0o644); err != nil {
		t.Fatal(err)
	}

	seedAddr, stopSeed := startTestSwarm(t, Config{
		Torrent:  tor,
		OutPath:  seedPath,
		Seed:     true,
		NoUpload: true,
		Port:     0,
		Name:     "seed",
		PeerID:   makePeerID('S'),
	})
	defer func() {
		if err := stopSeed(); err != nil {
			t.Errorf("seed Run error: %v", err)
		}
	}()

	conn, err := net.DialTimeout("tcp", seedAddr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))

	if err := wire.WriteHandshake(conn, tor.InfoHash, makePeerID('L')); err != nil {
		t.Fatal(err)
	}
	if _, _, err := wire.ReadHandshake(conn); err != nil {
		t.Fatal(err)
	}
	// Drain seed bitfield.
	if _, err := wire.ReadMessage(conn); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write((&wire.Message{ID: wire.MsgInterested}).Serialize()); err != nil {
		t.Fatal(err)
	}

	// No unchoke should arrive within 150ms.
	_ = conn.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
	if msg, err := wire.ReadMessage(conn); err == nil {
		t.Fatalf("expected no message in --no-upload mode, got %#v", msg)
	}
}

func TestLeecherDialsStaticPeerExchangesBitfieldAndSendsInterested(t *testing.T) {
	tor, _ := makeTestTorrentAndData(t, 64, 32)
	tor.InfoHash = [20]byte{7, 7, 7}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	peerResultCh := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			peerResultCh <- err
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))

		seedID := makePeerID('S')
		if err := wire.WriteHandshake(conn, tor.InfoHash, seedID); err != nil {
			peerResultCh <- err
			return
		}
		gotHash, _, err := wire.ReadHandshake(conn)
		if err != nil {
			peerResultCh <- err
			return
		}
		if gotHash != tor.InfoHash {
			peerResultCh <- err
			return
		}

		// Leecher must send an all-zero bitfield of 1 byte (for 2 pieces).
		msg, err := wire.ReadMessage(conn)
		if err != nil {
			peerResultCh <- err
			return
		}
		if msg == nil || msg.ID != wire.MsgBitfield || !bytes.Equal(msg.Payload, []byte{0x00}) {
			peerResultCh <- os.ErrInvalid
			return
		}

		// Send full bitfield (pieces 0 and 1 = 0xc0).
		bf := wire.NewBitfield(2)
		bf.Set(0)
		bf.Set(1)
		if _, err := conn.Write((&wire.Message{ID: wire.MsgBitfield, Payload: bf}).Serialize()); err != nil {
			peerResultCh <- err
			return
		}

		// Leecher must reply with MsgInterested.
		msg, err = wire.ReadMessage(conn)
		if err != nil {
			peerResultCh <- err
			return
		}
		if msg == nil || msg.ID != wire.MsgInterested {
			peerResultCh <- os.ErrInvalid
			return
		}
		peerResultCh <- nil
	}()

	outPath := filepath.Join(t.TempDir(), "l1.bin")
	_, stopLeecher := startTestSwarm(t, Config{
		Torrent:     tor,
		OutPath:     outPath,
		Port:        0,
		Name:        "l1",
		StaticPeers: []string{ln.Addr().String()},
		PeerID:      makePeerID('L'),
	})
	defer func() {
		if err := stopLeecher(); err != nil {
			t.Errorf("leecher Run error: %v", err)
		}
	}()

	if err := <-peerResultCh; err != nil {
		t.Fatalf("mock seed peer exchange failed: %v", err)
	}
}

func TestHandshakeRejectsBadHashAndSelfConnection(t *testing.T) {
	infoHash := [20]byte{1, 2, 3}
	wrongHash := [20]byte{9, 9, 9}
	idA := makePeerID('A')
	idB := makePeerID('B')

	// Wrong info hash rejected.
	c1, c2 := net.Pipe()
	var stats Stats
	go func() {
		_ = wire.WriteHandshake(c2, wrongHash, idB)
		_, _, _ = wire.ReadHandshake(c2)
		_ = c2.Close()
	}()
	if _, _, err := handshakePeer(context.Background(), c1, infoHash, idA, &stats); err == nil {
		t.Fatal("expected handshakePeer to reject mismatched info hash")
	}

	// Self-connection (identical peer ID) rejected.
	c3, c4 := net.Pipe()
	go func() {
		_ = wire.WriteHandshake(c4, infoHash, idA)
		_, _, _ = wire.ReadHandshake(c4)
		_ = c4.Close()
	}()
	if _, _, err := handshakePeer(context.Background(), c3, infoHash, idA, &stats); err == nil {
		t.Fatal("expected handshakePeer to reject self-connection")
	}
}
