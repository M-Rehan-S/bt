package wire

import (
	"encoding/binary"
	"fmt"
	"io"
)

// Peer wire message IDs defined in BEP 3.
const (
	MsgChoke         = 0
	MsgUnchoke       = 1
	MsgInterested    = 2
	MsgNotInterested = 3
	MsgHave          = 4
	MsgBitfield      = 5
	MsgRequest       = 6
	MsgPiece         = 7
)

// maxMessageLen caps incoming frame lengths before allocation to prevent
// excessive memory allocation on corrupt frames (1 << 17 = 131,072 bytes).
const maxMessageLen = 131072

// Message represents a length-prefixed BitTorrent peer wire message.
type Message struct {
	ID      byte
	Payload []byte
}

// Serialize encodes m as <4-byte big-endian length><id><payload>.
// A nil Message encodes as a 4-byte keep-alive (00 00 00 00).
func (m *Message) Serialize() []byte {
	if m == nil {
		return make([]byte, 4)
	}
	length := uint32(len(m.Payload) + 1)
	buf := make([]byte, 4+length)
	binary.BigEndian.PutUint32(buf[0:4], length)
	buf[4] = m.ID
	copy(buf[5:], m.Payload)
	return buf
}

// ReadMessage reads a single wire message from r.
// A zero-length keep-alive frame returns (nil, nil).
func ReadMessage(r io.Reader) (*Message, error) {
	var lenBuf [4]byte
	if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
		return nil, err
	}

	length := binary.BigEndian.Uint32(lenBuf[:])
	if length == 0 {
		return nil, nil
	}
	if length > maxMessageLen {
		return nil, fmt.Errorf("wire: message length %d exceeds limit %d", length, maxMessageLen)
	}

	msgBuf := make([]byte, length)
	if _, err := io.ReadFull(r, msgBuf); err != nil {
		return nil, err
	}

	var payload []byte
	if length > 1 {
		payload = msgBuf[1:]
	}

	return &Message{
		ID:      msgBuf[0],
		Payload: payload,
	}, nil
}

// NewHave builds a have message for piece index idx.
func NewHave(idx uint32) *Message {
	payload := make([]byte, 4)
	binary.BigEndian.PutUint32(payload, idx)
	return &Message{ID: MsgHave, Payload: payload}
}

// NewRequest builds a request message for a block (idx, begin, length).
func NewRequest(idx, begin, length uint32) *Message {
	payload := make([]byte, 12)
	binary.BigEndian.PutUint32(payload[0:4], idx)
	binary.BigEndian.PutUint32(payload[4:8], begin)
	binary.BigEndian.PutUint32(payload[8:12], length)
	return &Message{ID: MsgRequest, Payload: payload}
}

// NewPiece builds a piece message carrying a block of data at (idx, begin).
func NewPiece(idx, begin uint32, data []byte) *Message {
	payload := make([]byte, 8+len(data))
	binary.BigEndian.PutUint32(payload[0:4], idx)
	binary.BigEndian.PutUint32(payload[4:8], begin)
	copy(payload[8:], data)
	return &Message{ID: MsgPiece, Payload: payload}
}

// ParseHave extracts the piece index from a have message.
func ParseHave(m *Message) (uint32, error) {
	if err := validateMessageID(m, MsgHave); err != nil {
		return 0, err
	}
	if len(m.Payload) != 4 {
		return 0, fmt.Errorf("wire: have payload length %d (expected 4)", len(m.Payload))
	}
	return binary.BigEndian.Uint32(m.Payload), nil
}

// ParseRequest extracts (idx, begin, length) from a request message.
func ParseRequest(m *Message) (idx, begin, length uint32, err error) {
	if err := validateMessageID(m, MsgRequest); err != nil {
		return 0, 0, 0, err
	}
	if len(m.Payload) != 12 {
		return 0, 0, 0, fmt.Errorf("wire: request payload length %d (expected 12)", len(m.Payload))
	}
	idx = binary.BigEndian.Uint32(m.Payload[0:4])
	begin = binary.BigEndian.Uint32(m.Payload[4:8])
	length = binary.BigEndian.Uint32(m.Payload[8:12])
	return idx, begin, length, nil
}

// ParsePiece extracts (idx, begin, data) from a piece message.
func ParsePiece(m *Message) (idx, begin uint32, data []byte, err error) {
	if err := validateMessageID(m, MsgPiece); err != nil {
		return 0, 0, nil, err
	}
	if len(m.Payload) < 8 {
		return 0, 0, nil, fmt.Errorf("wire: piece payload length %d (expected at least 8)", len(m.Payload))
	}
	idx = binary.BigEndian.Uint32(m.Payload[0:4])
	begin = binary.BigEndian.Uint32(m.Payload[4:8])
	return idx, begin, m.Payload[8:], nil
}

func validateMessageID(m *Message, wantID byte) error {
	if m == nil {
		return fmt.Errorf("wire: nil message (expected id %d)", wantID)
	}
	if m.ID != wantID {
		return fmt.Errorf("wire: unexpected message id %d (expected %d)", m.ID, wantID)
	}
	return nil
}
