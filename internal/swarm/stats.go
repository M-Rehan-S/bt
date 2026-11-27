package swarm

import (
	"net"
	"sync/atomic"
)

// Stats holds cumulative atomic payload and wire byte counters for one client.
type Stats struct {
	downBytes atomic.Int64
	upBytes   atomic.Int64
	downWire  atomic.Int64
	upWire    atomic.Int64
}

// StatsSnapshot is a point-in-time copy of all four cumulative counters.
type StatsSnapshot struct {
	DownBytes int64
	UpBytes   int64
	DownWire  int64
	UpWire    int64
}

// AddDownBytes adds payload bytes received from piece messages.
func (s *Stats) AddDownBytes(n int64) {
	s.downBytes.Add(n)
}

// AddUpBytes adds payload bytes sent in piece messages.
func (s *Stats) AddUpBytes(n int64) {
	s.upBytes.Add(n)
}

// AddDownWire adds raw application-level bytes read from peer connections.
func (s *Stats) AddDownWire(n int64) {
	s.downWire.Add(n)
}

// AddUpWire adds raw application-level bytes written to peer connections.
func (s *Stats) AddUpWire(n int64) {
	s.upWire.Add(n)
}

// Snapshot reads all four cumulative counters.
func (s *Stats) Snapshot() StatsSnapshot {
	return StatsSnapshot{
		DownBytes: s.downBytes.Load(),
		UpBytes:   s.upBytes.Load(),
		DownWire:  s.downWire.Load(),
		UpWire:    s.upWire.Load(),
	}
}

type countingConn struct {
	net.Conn
	stats *Stats
}

// WrapConn wraps conn so all Read and Write calls update stats wire counters.
func WrapConn(conn net.Conn, stats *Stats) net.Conn {
	return &countingConn{Conn: conn, stats: stats}
}

func (c *countingConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 {
		c.stats.AddDownWire(int64(n))
	}
	return n, err
}

func (c *countingConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	if n > 0 {
		c.stats.AddUpWire(int64(n))
	}
	return n, err
}
