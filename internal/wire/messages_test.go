package wire

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"io"
	"reflect"
	"testing"
	"testing/iotest"
)

func decodeHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("hex decode %q: %v", s, err)
	}
	return b
}

func TestGoldenMessages(t *testing.T) {
	bf := NewBitfield(200)
	bf.Set(0)
	bf.Set(9)
	bfGolden := make([]byte, 30)
	copy(bfGolden, decodeHex(t, "0000001a058040"))

	cases := []struct {
		name string
		msg  *Message
		wire []byte
	}{
		{
			name: "keep-alive",
			msg:  nil,
			wire: decodeHex(t, "00000000"),
		},
		{
			name: "choke",
			msg:  &Message{ID: MsgChoke},
			wire: decodeHex(t, "0000000100"),
		},
		{
			name: "unchoke",
			msg:  &Message{ID: MsgUnchoke},
			wire: decodeHex(t, "0000000101"),
		},
		{
			name: "interested",
			msg:  &Message{ID: MsgInterested},
			wire: decodeHex(t, "0000000102"),
		},
		{
			name: "not interested",
			msg:  &Message{ID: MsgNotInterested},
			wire: decodeHex(t, "0000000103"),
		},
		{
			name: "have piece 17",
			msg:  NewHave(17),
			wire: decodeHex(t, "000000050400000011"),
		},
		{
			name: "request idx=1 begin=16384 length=16384",
			msg:  NewRequest(1, 16384, 16384),
			wire: decodeHex(t, "0000000d06000000010000400000004000"),
		},
		{
			name: "piece idx=1 begin=0 data=aabbcc",
			msg:  NewPiece(1, 0, []byte{0xaa, 0xbb, 0xcc}),
			wire: decodeHex(t, "0000000c070000000100000000aabbcc"),
		},
		{
			name: "bitfield 200 pieces with 0 and 9 set",
			msg:  &Message{ID: MsgBitfield, Payload: bf},
			wire: bfGolden,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotBytes := tc.msg.Serialize()
			if !bytes.Equal(gotBytes, tc.wire) {
				t.Fatalf("Serialize() = %x, want %x", gotBytes, tc.wire)
			}

			gotMsg, err := ReadMessage(bytes.NewReader(tc.wire))
			if err != nil {
				t.Fatalf("ReadMessage() error: %v", err)
			}
			if !reflect.DeepEqual(gotMsg, tc.msg) {
				t.Fatalf("ReadMessage() = %#v, want %#v", gotMsg, tc.msg)
			}
		})
	}
}

func TestMessageRoundTripAllTypes(t *testing.T) {
	bf := NewBitfield(16)
	bf.Set(3)
	bf.Set(15)

	cases := []struct {
		name string
		msg  *Message
	}{
		{name: "keep-alive", msg: nil},
		{name: "choke", msg: &Message{ID: MsgChoke}},
		{name: "unchoke", msg: &Message{ID: MsgUnchoke}},
		{name: "interested", msg: &Message{ID: MsgInterested}},
		{name: "not interested", msg: &Message{ID: MsgNotInterested}},
		{name: "have", msg: NewHave(42)},
		{name: "bitfield", msg: &Message{ID: MsgBitfield, Payload: bf}},
		{name: "request", msg: NewRequest(7, 32768, 16384)},
		{name: "piece", msg: NewPiece(7, 32768, []byte("hello block"))},
		{name: "unknown cancel id=8", msg: &Message{ID: 8, Payload: make([]byte, 12)}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := tc.msg.Serialize()
			got, err := ReadMessage(bytes.NewReader(raw))
			if err != nil {
				t.Fatalf("ReadMessage error: %v", err)
			}
			if !reflect.DeepEqual(got, tc.msg) {
				t.Fatalf("round-trip mismatch: got %#v, want %#v", got, tc.msg)
			}
		})
	}
}

func TestReadMessageOneByteReader(t *testing.T) {
	msgs := []*Message{
		NewHave(17),
		nil,
		NewRequest(1, 16384, 16384),
		NewPiece(1, 0, []byte{0xaa, 0xbb, 0xcc}),
	}

	var stream bytes.Buffer
	for _, m := range msgs {
		stream.Write(m.Serialize())
	}

	r := iotest.OneByteReader(&stream)
	for i, want := range msgs {
		got, err := ReadMessage(r)
		if err != nil {
			t.Fatalf("message %d ReadMessage error: %v", i, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("message %d = %#v, want %#v", i, got, want)
		}
	}

	if _, err := ReadMessage(r); err != io.EOF {
		t.Fatalf("expected io.EOF at end of stream, got %v", err)
	}
}

func TestReadMessageMaxAndOversizedLength(t *testing.T) {
	// Exactly maxMessageLen (131072) must succeed.
	exactFrame := make([]byte, 4+maxMessageLen)
	binary.BigEndian.PutUint32(exactFrame[0:4], maxMessageLen)
	exactFrame[4] = MsgPiece
	msg, err := ReadMessage(bytes.NewReader(exactFrame))
	if err != nil {
		t.Fatalf("length %d should be accepted, got error: %v", maxMessageLen, err)
	}
	if msg == nil || msg.ID != MsgPiece || len(msg.Payload) != maxMessageLen-1 {
		t.Fatalf("unexpected decoded max-size message: %#v", msg)
	}

	// Larger than maxMessageLen must fail immediately without reading a full body.
	for _, badLen := range []uint32{maxMessageLen + 1, 0x7fffffff, 0xffffffff} {
		var hdr [4]byte
		binary.BigEndian.PutUint32(hdr[:], badLen)
		if _, err := ReadMessage(bytes.NewReader(hdr[:])); err == nil {
			t.Fatalf("expected error for oversized length %d", badLen)
		}
	}
}

func TestReadMessageTruncated(t *testing.T) {
	cases := []struct {
		name string
		data []byte
	}{
		{name: "empty EOF", data: []byte{}},
		{name: "partial length header", data: []byte{0x00, 0x00, 0x01}},
		{name: "missing body", data: []byte{0x00, 0x00, 0x00, 0x05}},
		{name: "truncated body", data: []byte{0x00, 0x00, 0x00, 0x05, 0x04, 0x00, 0x01}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ReadMessage(bytes.NewReader(tc.data)); err == nil {
				t.Fatalf("expected error for %s", tc.name)
			}
		})
	}
}

func TestParseHelpersValidAndInvalid(t *testing.T) {
	// Valid round trips
	idx, err := ParseHave(NewHave(99))
	if err != nil || idx != 99 {
		t.Fatalf("ParseHave = (%d, %v), want (99, nil)", idx, err)
	}

	idx, begin, length, err := ParseRequest(NewRequest(2, 16384, 8192))
	if err != nil || idx != 2 || begin != 16384 || length != 8192 {
		t.Fatalf("ParseRequest = (%d, %d, %d, %v)", idx, begin, length, err)
	}

	idx, begin, data, err := ParsePiece(NewPiece(3, 4096, []byte{1, 2, 3, 4}))
	if err != nil || idx != 3 || begin != 4096 || !bytes.Equal(data, []byte{1, 2, 3, 4}) {
		t.Fatalf("ParsePiece = (%d, %d, %x, %v)", idx, begin, data, err)
	}

	// Invalid cases
	invalidHave := []*Message{
		nil,
		{ID: MsgChoke, Payload: make([]byte, 4)},
		{ID: MsgHave, Payload: make([]byte, 3)},
		{ID: MsgHave, Payload: make([]byte, 5)},
	}
	for i, m := range invalidHave {
		if _, err := ParseHave(m); err == nil {
			t.Errorf("ParseHave invalid case %d unexpectedly succeeded", i)
		}
	}

	invalidRequest := []*Message{
		nil,
		{ID: MsgHave, Payload: make([]byte, 12)},
		{ID: MsgRequest, Payload: make([]byte, 11)},
		{ID: MsgRequest, Payload: make([]byte, 13)},
	}
	for i, m := range invalidRequest {
		if _, _, _, err := ParseRequest(m); err == nil {
			t.Errorf("ParseRequest invalid case %d unexpectedly succeeded", i)
		}
	}

	invalidPiece := []*Message{
		nil,
		{ID: MsgRequest, Payload: make([]byte, 8)},
		{ID: MsgPiece, Payload: make([]byte, 0)},
		{ID: MsgPiece, Payload: make([]byte, 7)},
	}
	for i, m := range invalidPiece {
		if _, _, _, err := ParsePiece(m); err == nil {
			t.Errorf("ParsePiece invalid case %d unexpectedly succeeded", i)
		}
	}
}
