package swarm

import (
	"context"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/M-Rehan-S/bt/internal/wire"
)

const (
	handshakeTimeout = 5 * time.Second
	outSendQueueSize = 128
)

type outItem struct {
	msg        *wire.Message
	pieceBytes int // > 0 only for MsgPiece; writer adds to stats.up_bytes after Write succeeds
}

type peerConn struct {
	conn      net.Conn
	addr      string
	dialAddr  string
	outbound  bool
	stats     *Stats
	outCh     chan outItem
	done      chan struct{}
	closeOnce sync.Once
}

func newPeerConn(conn net.Conn, dialAddr string, outbound bool, stats *Stats) *peerConn {
	return &peerConn{
		conn:     conn,
		addr:     conn.RemoteAddr().String(),
		dialAddr: dialAddr,
		outbound: outbound,
		stats:    stats,
		outCh:    make(chan outItem, outSendQueueSize),
		done:     make(chan struct{}),
	}
}

func (pc *peerConn) close() {
	pc.closeOnce.Do(func() {
		close(pc.done)
		_ = pc.conn.Close()
	})
}

// send queues an outgoing message for the peer's writer goroutine.
// If the send queue is full, the peer is dropped and false is returned.
func (pc *peerConn) send(item outItem) bool {
	select {
	case <-pc.done:
		return false
	default:
	}
	select {
	case pc.outCh <- item:
		return true
	default:
		pc.close()
		return false
	}
}

func (pc *peerConn) readLoop(ctx context.Context, events chan<- any) {
	defer func() {
		pc.close()
		select {
		case events <- peerGoneEvent{pc: pc}:
		case <-ctx.Done():
		}
	}()

	for {
		msg, err := wire.ReadMessage(pc.conn)
		if err != nil {
			return
		}
		if msg == nil {
			// Keep-alive frame.
			continue
		}
		select {
		case events <- msgReceivedEvent{pc: pc, msg: msg}:
		case <-pc.done:
			return
		case <-ctx.Done():
			return
		}
	}
}

func (pc *peerConn) writeLoop() {
	for {
		select {
		case <-pc.done:
			return
		case item := <-pc.outCh:
			raw := item.msg.Serialize()
			if err := writeFull(pc.conn, raw); err != nil {
				pc.close()
				return
			}
			if item.pieceBytes > 0 {
				pc.stats.AddUpBytes(int64(item.pieceBytes))
			}
		}
	}
}

func writeFull(w io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := w.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}

// handshakePeer wraps rawConn for wire accounting, exchanges 68-byte handshakes
// concurrently under a 5s deadline, and rejects mismatched info hashes or self-connections.
func handshakePeer(ctx context.Context, rawConn net.Conn, infoHash, peerID [20]byte, stats *Stats) (net.Conn, [20]byte, error) {
	conn := WrapConn(rawConn, stats)
	stopCancel := context.AfterFunc(ctx, func() {
		_ = conn.Close()
	})
	defer stopCancel()

	_ = conn.SetDeadline(time.Now().Add(handshakeTimeout))

	writeErrCh := make(chan error, 1)
	go func() {
		writeErrCh <- wire.WriteHandshake(conn, infoHash, peerID)
	}()

	gotHash, remoteID, readErr := wire.ReadHandshake(conn)
	if readErr != nil {
		_ = conn.Close()
		<-writeErrCh
		return nil, [20]byte{}, readErr
	}
	if writeErr := <-writeErrCh; writeErr != nil {
		_ = conn.Close()
		return nil, [20]byte{}, writeErr
	}

	// Clear the handshake-only deadline so idle peers do not time out.
	if err := conn.SetDeadline(time.Time{}); err != nil {
		_ = conn.Close()
		return nil, [20]byte{}, err
	}
	if gotHash != infoHash {
		_ = conn.Close()
		return nil, [20]byte{}, fmt.Errorf("peer info hash mismatch: got %x want %x", gotHash, infoHash)
	}
	if remoteID == peerID {
		_ = conn.Close()
		return nil, [20]byte{}, fmt.Errorf("self-connection detected (peer_id %x)", remoteID)
	}
	return conn, remoteID, nil
}
