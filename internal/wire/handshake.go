package wire

import (
	"fmt"
	"io"
)

const (
	protocolStr  = "BitTorrent protocol"
	handshakeLen = 68 // 1 (pstrlen) + 19 (pstr) + 8 (reserved) + 20 (infoHash) + 20 (peerID)
)

// WriteHandshake sends the 68-byte BitTorrent handshake to w.
func WriteHandshake(w io.Writer, infoHash, peerID [20]byte) error {
	var buf [handshakeLen]byte
	buf[0] = byte(len(protocolStr))
	copy(buf[1:20], protocolStr)
	// buf[20:28] remains zero for the 8 reserved bytes.
	copy(buf[28:48], infoHash[:])
	copy(buf[48:68], peerID[:])

	n, err := w.Write(buf[:])
	if err != nil {
		return err
	}
	if n != len(buf) {
		return io.ErrShortWrite
	}
	return nil
}

// ReadHandshake reads and validates the 68-byte BitTorrent handshake from r.
func ReadHandshake(r io.Reader) (infoHash, peerID [20]byte, err error) {
	var buf [handshakeLen]byte
	if _, err := io.ReadFull(r, buf[:]); err != nil {
		return infoHash, peerID, err
	}
	if buf[0] != byte(len(protocolStr)) {
		return infoHash, peerID, fmt.Errorf("wire: invalid protocol length %d", buf[0])
	}
	if string(buf[1:20]) != protocolStr {
		return infoHash, peerID, fmt.Errorf("wire: invalid protocol string %q", string(buf[1:20]))
	}
	copy(infoHash[:], buf[28:48])
	copy(peerID[:], buf[48:68])
	return infoHash, peerID, nil
}
