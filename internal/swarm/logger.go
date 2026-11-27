package swarm

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Logger writes one flushed JSON object per line following LOGSPEC.md.
type Logger struct {
	mu   sync.Mutex
	file *os.File
	w    *bufio.Writer
	peer string
}

type logEvent struct {
	Tns       int64  `json:"t_ns"`
	Peer      string `json:"peer"`
	Ev        string `json:"ev"`
	Idx       *int   `json:"idx,omitempty"`
	From      string `json:"from,omitempty"`
	To        string `json:"to,omitempty"`
	DownBytes *int64 `json:"down_bytes,omitempty"`
	UpBytes   *int64 `json:"up_bytes,omitempty"`
	DownWire  *int64 `json:"down_wire,omitempty"`
	UpWire    *int64 `json:"up_wire,omitempty"`
}

// NewLogger creates or truncates the JSONL log file at path.
// If path is empty, logging is a no-op.
func NewLogger(path, peer string) (*Logger, error) {
	if path == "" {
		return &Logger{peer: peer}, nil
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("logger: create dir: %w", err)
		}
	}
	f, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("logger: create %s: %w", path, err)
	}
	return &Logger{
		file: f,
		w:    bufio.NewWriter(f),
		peer: peer,
	}, nil
}

// Close flushes buffered output and closes the underlying log file.
func (l *Logger) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.w != nil {
		if err := l.w.Flush(); err != nil {
			if l.file != nil {
				l.file.Close()
			}
			return err
		}
	}
	if l.file != nil {
		return l.file.Close()
	}
	return nil
}

// Start logs the startup event before announcing or dialing.
func (l *Logger) Start() {
	l.write(logEvent{Ev: "start"})
}

// PieceOK logs a verified piece written to disk.
func (l *Logger) PieceOK(idx int, from string) {
	l.write(logEvent{Ev: "piece_ok", Idx: &idx, From: from})
}

// HashFail logs a piece that failed SHA-1 verification.
func (l *Logger) HashFail(idx int, from string) {
	l.write(logEvent{Ev: "hash_fail", Idx: &idx, From: from})
}

// PieceSent logs that the final block of piece idx was queued for sending to a peer.
func (l *Logger) PieceSent(idx int, to string) {
	l.write(logEvent{Ev: "piece_sent", Idx: &idx, To: to})
}

// Stats logs cumulative payload and wire byte counters.
func (l *Logger) Stats(s StatsSnapshot) {
	// Pointer fields guarantee 0 values are serialized instead of omitted.
	downBytes := s.DownBytes
	upBytes := s.UpBytes
	downWire := s.DownWire
	upWire := s.UpWire
	l.write(logEvent{
		Ev:        "stats",
		DownBytes: &downBytes,
		UpBytes:   &upBytes,
		DownWire:  &downWire,
		UpWire:    &upWire,
	})
}

// Complete logs completion once after the final piece verifies.
func (l *Logger) Complete() {
	l.write(logEvent{Ev: "complete"})
}

func (l *Logger) write(ev logEvent) {
	if l == nil || l.w == nil {
		return
	}
	ev.Tns = time.Now().UnixNano()
	ev.Peer = l.peer
	data, err := json.Marshal(ev)
	if err != nil {
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = l.w.Write(data)
	_ = l.w.WriteByte('\n')
	_ = l.w.Flush()
}
