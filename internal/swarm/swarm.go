package swarm

import (
	"bytes"
	"context"
	"fmt"
	"math/rand/v2"
	"net"
	"sync"
	"time"

	"github.com/M-Rehan-S/bt/internal/metainfo"
	"github.com/M-Rehan-S/bt/internal/wire"
)

const (
	statsInterval        = time.Second
	timeoutSweepInterval = time.Second
	peerRetryInterval    = 2 * time.Second
	maxBlockLen          = 16384
	eventBufSize         = 256
)

// Config holds the runtime parameters for one BitTorrent client instance.
type Config struct {
	Torrent     *metainfo.Torrent
	OutPath     string
	Seed        bool
	NoUpload    bool
	Port        int
	Name        string
	LogPath     string
	TrackerURL  string   // override; empty means Torrent.Announce
	StaticPeers []string // "ip:port"; empty means use tracker
	PeerID      [20]byte // "-RZ0001-" + 12 random bytes
}

type peerConnectedEvent struct {
	conn     net.Conn
	peerID   [20]byte
	dialAddr string
	outbound bool
}

type dialFailedEvent struct {
	addr string
}

type peerGoneEvent struct {
	pc *peerConn
}

type msgReceivedEvent struct {
	pc  *peerConn
	msg *wire.Message
}

type peerState struct {
	pc             *peerConn
	peerID         [20]byte
	peerChoking    bool
	amChoking      bool
	peerInterested bool
	amInterested   bool
	bitfield       wire.Bitfield
	inflight       map[blockKey]time.Time
	currentPiece   int
	lastRequestAt  time.Time
}

type coordinator struct {
	ctx    context.Context
	cfg    Config
	store  *Storage
	logger *Logger
	stats  *Stats
	events chan any
	wg     *sync.WaitGroup
	rng    *rand.Rand

	numPieces   int
	ourBitfield wire.Bitfield
	pieces      []pieceDownload
	donePieces  int
	completed   bool

	peers    map[*peerConn]*peerState
	byPeerID map[[20]byte]*peerConn
	dialing  map[string]bool
}

// Run initializes storage, listener, peer networking, and the coordinator loop,
// blocking until ctx is cancelled.
func Run(ctx context.Context, cfg Config) error {
	return run(ctx, cfg, nil)
}

func run(ctx context.Context, cfg Config, onListen func(net.Addr)) error {
	store, err := OpenStorage(cfg.OutPath, cfg.Torrent, cfg.Seed)
	if err != nil {
		return err
	}
	defer store.Close()

	logger, err := NewLogger(cfg.LogPath, cfg.Name)
	if err != nil {
		return err
	}
	defer logger.Close()

	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.Port))
	if err != nil {
		return fmt.Errorf("listen on port %d: %w", cfg.Port, err)
	}
	if onListen != nil {
		onListen(ln.Addr())
	}

	stats := &Stats{}
	logger.Start()

	numPieces := store.NumPieces()
	ourBF := wire.NewBitfield(numPieces)
	donePieces := 0
	if cfg.Seed {
		for i := 0; i < numPieces; i++ {
			ourBF.Set(i)
		}
		donePieces = numPieces
	}

	var wg sync.WaitGroup
	events := make(chan any, eventBufSize)

	coord := &coordinator{
		ctx:         ctx,
		cfg:         cfg,
		store:       store,
		logger:      logger,
		stats:       stats,
		events:      events,
		wg:          &wg,
		rng:         newCoordinatorRNG(),
		numPieces:   numPieces,
		ourBitfield: ourBF,
		pieces:      initPieces(numPieces, cfg.Seed),
		donePieces:  donePieces,
		completed:   cfg.Seed,
		peers:       make(map[*peerConn]*peerState),
		byPeerID:    make(map[[20]byte]*peerConn),
		dialing:     make(map[string]bool),
	}

	wg.Add(1)
	go coord.acceptLoop(ln)

	// Dial configured static peers immediately and retry missing ones every 2s.
	coord.dialMissingStaticPeers()

	statsTicker := time.NewTicker(statsInterval)
	defer statsTicker.Stop()
	retryTicker := time.NewTicker(peerRetryInterval)
	defer retryTicker.Stop()
	timeoutTicker := time.NewTicker(timeoutSweepInterval)
	defer timeoutTicker.Stop()

	loopErr := coord.loop(statsTicker.C, retryTicker.C, timeoutTicker.C)

	// Shutdown: close listener and all peers, wait for goroutines, then log final stats.
	_ = ln.Close()
	for pc := range coord.peers {
		pc.close()
	}
	wg.Wait()

	logger.Stats(stats.Snapshot())
	return loopErr
}

func (c *coordinator) loop(statsC, retryC, timeoutC <-chan time.Time) error {
	for {
		select {
		case <-c.ctx.Done():
			return nil
		case <-statsC:
			c.logger.Stats(c.stats.Snapshot())
		case <-retryC:
			c.dialMissingStaticPeers()
		case now := <-timeoutC:
			c.sweepRequestTimeouts(now)
		case rawEv := <-c.events:
			switch ev := rawEv.(type) {
			case peerConnectedEvent:
				c.handlePeerConnected(ev)
			case dialFailedEvent:
				delete(c.dialing, ev.addr)
			case peerGoneEvent:
				c.handlePeerGone(ev.pc)
			case msgReceivedEvent:
				if err := c.handleMessage(ev.pc, ev.msg); err != nil {
					return err
				}
			}
		}
	}
}

func (c *coordinator) acceptLoop(ln net.Listener) {
	defer c.wg.Done()
	var connWg sync.WaitGroup
	defer connWg.Wait()

	for {
		rawConn, err := ln.Accept()
		if err != nil {
			return
		}
		connWg.Add(1)
		go func(rc net.Conn) {
			defer connWg.Done()
			conn, remoteID, err := handshakePeer(c.ctx, rc, c.cfg.Torrent.InfoHash, c.cfg.PeerID, c.stats)
			if err != nil {
				return
			}
			select {
			case c.events <- peerConnectedEvent{
				conn:     conn,
				peerID:   remoteID,
				outbound: false,
			}:
			case <-c.ctx.Done():
				_ = conn.Close()
			}
		}(rawConn)
	}
}

func (c *coordinator) dialMissingStaticPeers() {
	for _, addr := range c.cfg.StaticPeers {
		if c.dialing[addr] || c.isConnectedToAddr(addr) {
			continue
		}
		c.dialing[addr] = true
		c.wg.Add(1)
		go c.dialPeer(addr)
	}
}

func (c *coordinator) isConnectedToAddr(addr string) bool {
	for pc := range c.peers {
		if pc.dialAddr == addr || pc.addr == addr {
			return true
		}
	}
	return false
}

func (c *coordinator) dialPeer(addr string) {
	defer c.wg.Done()
	dialer := &net.Dialer{Timeout: handshakeTimeout}
	rawConn, err := dialer.DialContext(c.ctx, "tcp", addr)
	if err != nil {
		select {
		case c.events <- dialFailedEvent{addr: addr}:
		case <-c.ctx.Done():
		}
		return
	}
	conn, remoteID, err := handshakePeer(c.ctx, rawConn, c.cfg.Torrent.InfoHash, c.cfg.PeerID, c.stats)
	if err != nil {
		select {
		case c.events <- dialFailedEvent{addr: addr}:
		case <-c.ctx.Done():
		}
		return
	}
	select {
	case c.events <- peerConnectedEvent{
		conn:     conn,
		peerID:   remoteID,
		dialAddr: addr,
		outbound: true,
	}:
	case <-c.ctx.Done():
		_ = conn.Close()
	}
}

func (c *coordinator) handlePeerConnected(ev peerConnectedEvent) {
	if ev.dialAddr != "" {
		delete(c.dialing, ev.dialAddr)
	}
	if existing, ok := c.byPeerID[ev.peerID]; ok {
		// Tie-break duplicate connections: keep the one initiated by the smaller peer ID.
		weSmaller := bytes.Compare(c.cfg.PeerID[:], ev.peerID[:]) < 0
		newWins := (weSmaller && ev.outbound) || (!weSmaller && !ev.outbound)
		if !newWins {
			_ = ev.conn.Close()
			return
		}
		if oldPS, ok := c.peers[existing]; ok {
			c.releasePeerWork(oldPS)
		}
		existing.close()
		delete(c.byPeerID, ev.peerID)
	}

	pc := newPeerConn(ev.conn, ev.dialAddr, ev.outbound, c.stats)
	ps := &peerState{
		pc:           pc,
		peerID:       ev.peerID,
		peerChoking:  true,
		amChoking:    true,
		inflight:     make(map[blockKey]time.Time),
		currentPiece: -1,
	}
	c.peers[pc] = ps
	c.byPeerID[ev.peerID] = pc

	c.wg.Add(2)
	go func() {
		defer c.wg.Done()
		pc.readLoop(c.ctx, c.events)
	}()
	go func() {
		defer c.wg.Done()
		pc.writeLoop()
	}()

	// Always send our bitfield immediately after the handshake (even if all zeros).
	bfCopy := append(wire.Bitfield(nil), c.ourBitfield...)
	pc.send(outItem{msg: &wire.Message{ID: wire.MsgBitfield, Payload: bfCopy}})
}

func (c *coordinator) handlePeerGone(pc *peerConn) {
	ps, ok := c.peers[pc]
	if !ok {
		return
	}
	c.releasePeerWork(ps)
	if cur := c.byPeerID[ps.peerID]; cur == pc {
		delete(c.byPeerID, ps.peerID)
	}
	delete(c.peers, pc)
	c.fillAllPipelines()
}

func (c *coordinator) handleMessage(pc *peerConn, msg *wire.Message) error {
	ps, ok := c.peers[pc]
	if !ok {
		return nil
	}

	switch msg.ID {
	case wire.MsgChoke:
		ps.peerChoking = true
		c.releasePeerWork(ps)
		c.fillAllPipelines()
	case wire.MsgUnchoke:
		ps.peerChoking = false
		c.fillPipeline(ps)
	case wire.MsgInterested:
		ps.peerInterested = true
		// In M3 there is no choker yet: unchoke any interested peer unless --no-upload.
		if !c.cfg.NoUpload && ps.amChoking {
			ps.amChoking = false
			ps.pc.send(outItem{msg: &wire.Message{ID: wire.MsgUnchoke}})
		}
	case wire.MsgNotInterested:
		ps.peerInterested = false
	case wire.MsgHave:
		c.handleHave(ps, msg)
	case wire.MsgBitfield:
		c.handleBitfield(ps, msg)
	case wire.MsgRequest:
		c.handleRequest(ps, msg)
	case wire.MsgPiece:
		return c.handlePiece(ps, msg)
	default:
		// Ignore unknown message IDs per BEP 3 / PLAN.md 6.1.
	}
	return nil
}

func (c *coordinator) handleBitfield(ps *peerState, msg *wire.Message) {
	if len(msg.Payload) != len(c.ourBitfield) {
		ps.pc.close()
		return
	}
	bf := make(wire.Bitfield, len(msg.Payload))
	copy(bf, msg.Payload)
	ps.bitfield = bf
	c.updateInterest(ps)
	c.fillPipeline(ps)
}

func (c *coordinator) handleHave(ps *peerState, msg *wire.Message) {
	idx, err := wire.ParseHave(msg)
	if err != nil || idx >= uint32(c.numPieces) {
		return
	}
	if len(ps.bitfield) == 0 {
		ps.bitfield = wire.NewBitfield(c.numPieces)
	}
	ps.bitfield.Set(int(idx))
	c.updateInterest(ps)
	c.fillPipeline(ps)
}

func (c *coordinator) updateInterest(ps *peerState) {
	if c.cfg.Seed {
		return
	}
	want := false
	for i := 0; i < c.numPieces; i++ {
		if !c.ourBitfield.Has(i) && ps.bitfield.Has(i) {
			want = true
			break
		}
	}
	if want == ps.amInterested {
		return
	}
	ps.amInterested = want
	id := byte(wire.MsgNotInterested)
	if want {
		id = wire.MsgInterested
	}
	ps.pc.send(outItem{msg: &wire.Message{ID: id}})
}

func (c *coordinator) handleRequest(ps *peerState, msg *wire.Message) {
	if ps.amChoking || c.cfg.NoUpload {
		return
	}
	idx, begin, length, err := wire.ParseRequest(msg)
	if err != nil {
		return
	}
	if idx >= uint32(c.numPieces) {
		return
	}
	pIdx := int(idx)
	if !c.ourBitfield.Has(pIdx) {
		return
	}
	if length == 0 || length > maxBlockLen {
		return
	}
	psize := c.store.PieceSize(pIdx)
	if int64(begin)+int64(length) > int64(psize) {
		return
	}
	data, err := c.store.ReadBlock(pIdx, int(begin), int(length))
	if err != nil {
		return
	}
	ok := ps.pc.send(outItem{
		msg:        wire.NewPiece(idx, begin, data),
		pieceBytes: len(data),
	})
	if ok && int64(begin)+int64(length) == int64(psize) {
		c.logger.PieceSent(pIdx, ps.pc.addr)
	}
}
