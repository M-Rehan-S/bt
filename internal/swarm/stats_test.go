package swarm

import (
	"io"
	"net"
	"testing"
)

func TestStatsAndCountingConn(t *testing.T) {
	var senderStats, receiverStats Stats
	senderStats.AddUpBytes(16384)
	receiverStats.AddDownBytes(16384)

	c1, c2 := net.Pipe()
	w1 := WrapConn(c1, &senderStats)
	w2 := WrapConn(c2, &receiverStats)
	defer w1.Close()
	defer w2.Close()

	payload := []byte("hello wire counter")
	errCh := make(chan error, 1)
	go func() {
		_, err := w1.Write(payload)
		errCh <- err
	}()

	buf := make([]byte, len(payload))
	if _, err := io.ReadFull(w2, buf); err != nil {
		t.Fatalf("ReadFull error: %v", err)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("Write error: %v", err)
	}

	s1 := senderStats.Snapshot()
	if s1.UpBytes != 16384 || s1.UpWire != int64(len(payload)) || s1.DownWire != 0 {
		t.Fatalf("unexpected sender stats: %+v", s1)
	}
	s2 := receiverStats.Snapshot()
	if s2.DownBytes != 16384 || s2.DownWire != int64(len(payload)) || s2.UpWire != 0 {
		t.Fatalf("unexpected receiver stats: %+v", s2)
	}
}
