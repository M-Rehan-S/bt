package swarm

import (
	"crypto/sha1"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/M-Rehan-S/bt/internal/metainfo"
)

// Storage reads and writes verified piece data at fixed file offsets.
type Storage struct {
	file        *os.File
	length      int64
	pieceLen    int
	pieceHashes [][20]byte
}

// OpenStorage opens an existing file read-only and verifies all piece hashes in
// seed mode, or creates and truncates the output file to full size for leeching.
func OpenStorage(path string, t *metainfo.Torrent, seed bool) (*Storage, error) {
	if t == nil {
		return nil, errors.New("storage: nil torrent")
	}
	if t.Length < 0 || t.PieceLength <= 0 {
		return nil, fmt.Errorf("storage: invalid torrent length=%d pieceLength=%d", t.Length, t.PieceLength)
	}
	expectedPieces := 0
	if t.Length > 0 {
		expectedPieces = int((t.Length + int64(t.PieceLength) - 1) / int64(t.PieceLength))
	}
	if len(t.PieceHashes) != expectedPieces {
		return nil, fmt.Errorf("storage: piece hash count %d does not match expected %d", len(t.PieceHashes), expectedPieces)
	}

	if seed {
		f, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("storage: open seed file: %w", err)
		}
		st, err := f.Stat()
		if err != nil {
			f.Close()
			return nil, fmt.Errorf("storage: stat seed file: %w", err)
		}
		if st.Size() != t.Length {
			f.Close()
			return nil, fmt.Errorf("storage: seed file size %d does not match torrent length %d", st.Size(), t.Length)
		}
		s := &Storage{
			file:        f,
			length:      t.Length,
			pieceLen:    t.PieceLength,
			pieceHashes: t.PieceHashes,
		}
		if err := s.VerifyAll(); err != nil {
			f.Close()
			return nil, err
		}
		return s, nil
	}

	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("storage: create output dir: %w", err)
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, fmt.Errorf("storage: create output file: %w", err)
	}
	if err := f.Truncate(t.Length); err != nil {
		f.Close()
		return nil, fmt.Errorf("storage: truncate output file: %w", err)
	}
	return &Storage{
		file:        f,
		length:      t.Length,
		pieceLen:    t.PieceLength,
		pieceHashes: t.PieceHashes,
	}, nil
}

// Close closes the underlying file.
func (s *Storage) Close() error {
	if s == nil || s.file == nil {
		return nil
	}
	return s.file.Close()
}

// NumPieces returns the total number of pieces in the torrent.
func (s *Storage) NumPieces() int {
	return len(s.pieceHashes)
}

// PieceSize returns the exact byte length of piece idx, handling a shorter last piece.
func (s *Storage) PieceSize(idx int) int {
	if idx < 0 || idx >= len(s.pieceHashes) {
		return 0
	}
	rem := s.length - int64(idx)*int64(s.pieceLen)
	if rem > int64(s.pieceLen) {
		return s.pieceLen
	}
	return int(rem)
}

// VerifyPiece checks whether data matches the expected length and SHA-1 hash of piece idx.
func (s *Storage) VerifyPiece(idx int, data []byte) bool {
	if idx < 0 || idx >= len(s.pieceHashes) {
		return false
	}
	if len(data) != s.PieceSize(idx) {
		return false
	}
	return sha1.Sum(data) == s.pieceHashes[idx]
}

// VerifyAll reads every piece from disk and verifies its SHA-1 digest.
func (s *Storage) VerifyAll() error {
	buf := make([]byte, s.pieceLen)
	for idx := range s.pieceHashes {
		psize := s.PieceSize(idx)
		offset := int64(idx) * int64(s.pieceLen)
		n, err := s.file.ReadAt(buf[:psize], offset)
		if err != nil {
			return fmt.Errorf("storage: read piece %d: %w", idx, err)
		}
		if n != psize {
			return fmt.Errorf("storage: short read on piece %d (%d/%d)", idx, n, psize)
		}
		if sha1.Sum(buf[:psize]) != s.pieceHashes[idx] {
			return fmt.Errorf("storage: piece %d hash mismatch", idx)
		}
	}
	return nil
}

// WritePiece writes a verified piece to its fixed offset using WriteAt.
func (s *Storage) WritePiece(idx int, data []byte) error {
	if idx < 0 || idx >= len(s.pieceHashes) {
		return fmt.Errorf("storage: piece index %d out of range", idx)
	}
	psize := s.PieceSize(idx)
	if len(data) != psize {
		return fmt.Errorf("storage: piece %d length %d does not match expected %d", idx, len(data), psize)
	}
	offset := int64(idx) * int64(s.pieceLen)
	n, err := s.file.WriteAt(data, offset)
	if err != nil {
		return fmt.Errorf("storage: write piece %d: %w", idx, err)
	}
	if n != psize {
		return fmt.Errorf("storage: short write on piece %d (%d/%d)", idx, n, psize)
	}
	return nil
}

// ReadBlock reads a slice of piece idx starting at begin using ReadAt.
func (s *Storage) ReadBlock(idx, begin, length int) ([]byte, error) {
	if idx < 0 || idx >= len(s.pieceHashes) {
		return nil, fmt.Errorf("storage: piece index %d out of range", idx)
	}
	psize := s.PieceSize(idx)
	if begin < 0 || length <= 0 || int64(begin)+int64(length) > int64(psize) {
		return nil, fmt.Errorf("storage: block out of range idx=%d begin=%d length=%d pieceSize=%d", idx, begin, length, psize)
	}
	buf := make([]byte, length)
	offset := int64(idx)*int64(s.pieceLen) + int64(begin)
	n, err := s.file.ReadAt(buf, offset)
	if err != nil {
		return nil, fmt.Errorf("storage: read block idx=%d begin=%d: %w", idx, begin, err)
	}
	if n != length {
		return nil, fmt.Errorf("storage: short read on block idx=%d begin=%d (%d/%d)", idx, begin, n, length)
	}
	return buf, nil
}
