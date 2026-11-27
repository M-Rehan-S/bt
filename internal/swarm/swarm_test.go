package swarm

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunSeedVerificationAndLifecycle(t *testing.T) {
	tor, data := makeTestTorrentAndData(t, 74, 32)
	dir := t.TempDir()
	seedPath := filepath.Join(dir, "seed.bin")
	if err := os.WriteFile(seedPath, data, 0o644); err != nil {
		t.Fatal(err)
	}

	// Valid seed verifies, logs start and final stats upon context cancellation.
	logPath := filepath.Join(dir, "seed.jsonl")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := Run(ctx, Config{
		Torrent: tor,
		OutPath: seedPath,
		Seed:    true,
		Name:    "seed",
		LogPath: logPath,
	})
	if err != nil {
		t.Fatalf("Run(valid seed) error: %v", err)
	}

	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected at least start and final stats lines, got %d:\n%s", len(lines), raw)
	}
	var first, last map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &last); err != nil {
		t.Fatal(err)
	}
	if first["ev"] != "start" || last["ev"] != "stats" {
		t.Fatalf("unexpected events: first=%v last=%v", first["ev"], last["ev"])
	}

	// Corrupted seed must fail before logging start.
	badPath := filepath.Join(dir, "bad.bin")
	badData := append([]byte(nil), data...)
	badData[0] ^= 0xff
	if err := os.WriteFile(badPath, badData, 0o644); err != nil {
		t.Fatal(err)
	}
	badLog := filepath.Join(dir, "bad.jsonl")
	if err := Run(context.Background(), Config{
		Torrent: tor,
		OutPath: badPath,
		Seed:    true,
		Name:    "seed",
		LogPath: badLog,
	}); err == nil {
		t.Fatal("expected Run to fail when seed file fails SHA-1 verification")
	}
	if _, err := os.Stat(badLog); !os.IsNotExist(err) {
		t.Fatalf("expected log file not to be created on seed verification failure, stat err=%v", err)
	}
}

func TestRunLeecherPreallocatesFile(t *testing.T) {
	tor, _ := makeTestTorrentAndData(t, 74, 32)
	dir := t.TempDir()
	outPath := filepath.Join(dir, "l1.bin")
	logPath := filepath.Join(dir, "l1.jsonl")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Run(ctx, Config{
		Torrent: tor,
		OutPath: outPath,
		Seed:    false,
		Name:    "l1",
		LogPath: logPath,
	}); err != nil {
		t.Fatalf("Run(leecher) error: %v", err)
	}

	st, err := os.Stat(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if st.Size() != tor.Length {
		t.Fatalf("output size = %d, want %d", st.Size(), tor.Length)
	}
}
