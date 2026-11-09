package metainfo

import (
	"crypto/sha1"
	"errors"
	"os"

	"github.com/M-Rehan-S/bt/internal/bencode"
)

type Torrent struct {
	Announce    string
	Name        string
	Length      int64
	PieceLength int
	PieceHashes [][20]byte
	InfoHash    [20]byte
}

func Load(path string) (*Torrent, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	v, err := bencode.Decode(raw)
	if err != nil {
		return nil, err
	}
	top, ok := v.(map[string]interface{})
	if !ok {
		return nil, errors.New("metainfo: top level is not a dict")
	}
	info, ok := top["info"].(map[string]interface{})
	if !ok {
		return nil, errors.New("metainfo: missing info dict")
	}
	infoRaw, err := bencode.DictValueSpan(raw, "info")
	if err != nil {
		return nil, err
	}

	t := &Torrent{InfoHash: sha1.Sum(infoRaw)}
	t.Announce, _ = top["announce"].(string)
	t.Name, _ = info["name"].(string)
	length, ok1 := info["length"].(int64)
	plen, ok2 := info["piece length"].(int64)
	pieces, ok3 := info["pieces"].(string)
	if !ok1 || !ok2 || !ok3 {
		return nil, errors.New("metainfo: missing length/piece length/pieces (single-file only)")
	}
	if len(pieces)%20 != 0 {
		return nil, errors.New("metainfo: pieces length not a multiple of 20")
	}
	t.Length, t.PieceLength = length, int(plen)
	for i := 0; i < len(pieces); i += 20 {
		var h [20]byte
		copy(h[:], pieces[i:i+20])
		t.PieceHashes = append(t.PieceHashes, h)
	}
	return t, nil
}
