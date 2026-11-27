package swarm

import (
	"bytes"
	"crypto/sha1"
	"os"
	"path/filepath"
	"testing"

	"github.com/M-Rehan-S/bt/internal/metainfo"
)

func makeTestTorrentAndData(t *testing.T, totalLen, pieceLen int) (*metainfo.Torrent, []byte) {
	t.Helper()
	data := make([]byte, totalLen)
	for i := range data {
		data[i] = byte((i*31 + 7) % 251)
	}

	var hashes [][20]byte
	for off := 0; off < totalLen; off += pieceLen {
		end := off + pieceLen
		if end > totalLen {
			end = totalLen
		}
		hashes = append(hashes, sha1.Sum(data[off:end]))
	}

	tor := &metainfo.Torrent{
		Name:        "test.bin",
		Length:      int64(totalLen),
		PieceLength: pieceLen,
		PieceHashes: hashes,
	}
	return tor, data
}

func TestStorageLeecherWriteVerifyRead(t *testing.T) {
	// 2 full pieces of 32 bytes + 1 shorter last piece of 10 bytes = 74 bytes.
	tor, data := makeTestTorrentAndData(t, 74, 32)
	outPath := filepath.Join(t.TempDir(), "out.bin")

	store, err := OpenStorage(outPath, tor, false)
	if err != nil {
		t.Fatalf("OpenStorage(leecher) error: %v", err)
	}
	defer store.Close()

	st, err := os.Stat(outPath)
	if err != nil {
		t.Fatalf("stat output: %v", err)
	}
	if st.Size() != tor.Length {
		t.Fatalf("preallocated size = %d, want %d", st.Size(), tor.Length)
	}

	if got := store.PieceSize(0); got != 32 {
		t.Fatalf("PieceSize(0) = %d, want 32", got)
	}
	if got := store.PieceSize(2); got != 10 {
		t.Fatalf("PieceSize(2) = %d, want 10", got)
	}
	if got := store.PieceSize(3); got != 0 {
		t.Fatalf("PieceSize(3) = %d, want 0", got)
	}

	for idx := 0; idx < store.NumPieces(); idx++ {
		off := idx * tor.PieceLength
		piece := data[off : off+store.PieceSize(idx)]
		if !store.VerifyPiece(idx, piece) {
			t.Fatalf("VerifyPiece(%d) returned false for valid piece", idx)
		}
		if err := store.WritePiece(idx, piece); err != nil {
			t.Fatalf("WritePiece(%d) error: %v", idx, err)
		}
	}

	// Verify ReadBlock on both a full piece and the shorter last piece.
	blk, err := store.ReadBlock(0, 16, 16)
	if err != nil {
		t.Fatalf("ReadBlock(0) error: %v", err)
	}
	if !bytes.Equal(blk, data[16:32]) {
		t.Fatalf("ReadBlock(0) mismatch")
	}

	lastBlk, err := store.ReadBlock(2, 4, 6)
	if err != nil {
		t.Fatalf("ReadBlock(2) error: %v", err)
	}
	if !bytes.Equal(lastBlk, data[68:74]) {
		t.Fatalf("ReadBlock(2) mismatch: got %x want %x", lastBlk, data[68:74])
	}

	// Out-of-range reads and mismatched length writes must fail.
	if _, err := store.ReadBlock(2, 4, 7); err == nil {
		t.Fatal("expected error for ReadBlock past end of shorter last piece")
	}
	if err := store.WritePiece(0, []byte{1, 2, 3}); err == nil {
		t.Fatal("expected error for WritePiece with wrong length")
	}
	if store.VerifyPiece(0, make([]byte, 32)) {
		t.Fatal("expected VerifyPiece to return false on bad data")
	}
}

func TestStorageSeedVerification(t *testing.T) {
	tor, data := makeTestTorrentAndData(t, 74, 32)
	dir := t.TempDir()
	seedPath := filepath.Join(dir, "seed.bin")
	if err := os.WriteFile(seedPath, data, 0o644); err != nil {
		t.Fatal(err)
	}

	store, err := OpenStorage(seedPath, tor, true)
	if err != nil {
		t.Fatalf("OpenStorage(seed valid) error: %v", err)
	}
	store.Close()

	// Corrupted byte in last piece must fail verification.
	badData := append([]byte(nil), data...)
	badData[70] ^= 0xff
	badPath := filepath.Join(dir, "bad.bin")
	if err := os.WriteFile(badPath, badData, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenStorage(badPath, tor, true); err == nil {
		t.Fatal("expected OpenStorage(seed) to fail on corrupted piece")
	}

	// Wrong file size must fail.
	shortPath := filepath.Join(dir, "short.bin")
	if err := os.WriteFile(shortPath, data[:73], 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenStorage(shortPath, tor, true); err == nil {
		t.Fatal("expected OpenStorage(seed) to fail on truncated seed file")
	}
}
