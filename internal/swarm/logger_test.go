package swarm

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoggerEventsAndImmediateFlush(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "sub", "peer.jsonl")
	l, err := NewLogger(logPath, "l1")
	if err != nil {
		t.Fatalf("NewLogger error: %v", err)
	}
	defer l.Close()

	l.Start()
	l.PieceOK(0, "127.0.0.1:6881")
	l.HashFail(0, "127.0.0.1:6881")
	l.PieceSent(0, "127.0.0.1:6882")
	l.Stats(StatsSnapshot{DownBytes: 0, UpBytes: 0, DownWire: 68, UpWire: 68})
	l.Complete()

	// Read before Close() to prove each line is flushed immediately.
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile error: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 6 {
		t.Fatalf("got %d lines, want 6:\n%s", len(lines), raw)
	}

	var objs []map[string]any
	for i, line := range lines {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("line %d invalid JSON: %v (%s)", i, err, line)
		}
		if m["peer"] != "l1" {
			t.Fatalf("line %d peer = %v, want l1", i, m["peer"])
		}
		if _, ok := m["t_ns"].(float64); !ok {
			t.Fatalf("line %d missing numeric t_ns: %s", i, line)
		}
		objs = append(objs, m)
	}

	if objs[0]["ev"] != "start" || len(objs[0]) != 3 {
		t.Fatalf("unexpected start event: %#v", objs[0])
	}
	if objs[1]["ev"] != "piece_ok" || objs[1]["idx"] != float64(0) || objs[1]["from"] != "127.0.0.1:6881" {
		t.Fatalf("unexpected piece_ok event: %#v", objs[1])
	}
	if objs[2]["ev"] != "hash_fail" || objs[2]["idx"] != float64(0) || objs[2]["from"] != "127.0.0.1:6881" {
		t.Fatalf("unexpected hash_fail event: %#v", objs[2])
	}
	if objs[3]["ev"] != "piece_sent" || objs[3]["idx"] != float64(0) || objs[3]["to"] != "127.0.0.1:6882" {
		t.Fatalf("unexpected piece_sent event: %#v", objs[3])
	}
	if objs[4]["ev"] != "stats" ||
		objs[4]["down_bytes"] != float64(0) ||
		objs[4]["up_bytes"] != float64(0) ||
		objs[4]["down_wire"] != float64(68) ||
		objs[4]["up_wire"] != float64(68) {
		t.Fatalf("unexpected stats event: %#v", objs[4])
	}
	if objs[5]["ev"] != "complete" || len(objs[5]) != 3 {
		t.Fatalf("unexpected complete event: %#v", objs[5])
	}
}
