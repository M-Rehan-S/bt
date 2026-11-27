package wire

import (
	"bytes"
	"net"
	"testing"
	"testing/iotest"
)

func TestWriteHandshakeGoldenLayout(t *testing.T) {
	var infoHash, peerID [20]byte
	for i := range infoHash {
		infoHash[i] = byte(i + 1)
		peerID[i] = byte(0xa0 + i)
	}

	var buf bytes.Buffer
	if err := WriteHandshake(&buf, infoHash, peerID); err != nil {
		t.Fatalf("WriteHandshake error: %v", err)
	}

	got := buf.Bytes()
	if len(got) != 68 {
		t.Fatalf("handshake len = %d, want 68", len(got))
	}
	if got[0] != 19 {
		t.Fatalf("pstrlen = %d, want 19", got[0])
	}
	if string(got[1:20]) != "BitTorrent protocol" {
		t.Fatalf("pstr = %q", string(got[1:20]))
	}
	for i := 20; i < 28; i++ {
		if got[i] != 0 {
			t.Fatalf("reserved byte %d = %#x, want 0", i, got[i])
		}
	}
	if !bytes.Equal(got[28:48], infoHash[:]) {
		t.Fatalf("infoHash mismatch: %x vs %x", got[28:48], infoHash)
	}
	if !bytes.Equal(got[48:68], peerID[:]) {
		t.Fatalf("peerID mismatch: %x vs %x", got[48:68], peerID)
	}
}

func TestHandshakeNetPipeRoundTrip(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	var wantHash, wantPeerID [20]byte
	copy(wantHash[:], "01234567890123456789")
	copy(wantPeerID[:], "-RZ0001-123456789012")

	errCh := make(chan error, 1)
	go func() {
		errCh <- WriteHandshake(c1, wantHash, wantPeerID)
	}()

	gotHash, gotPeerID, err := ReadHandshake(c2)
	if err != nil {
		t.Fatalf("ReadHandshake over net.Pipe error: %v", err)
	}
	if writeErr := <-errCh; writeErr != nil {
		t.Fatalf("WriteHandshake over net.Pipe error: %v", writeErr)
	}
	if gotHash != wantHash {
		t.Fatalf("infoHash = %x, want %x", gotHash, wantHash)
	}
	if gotPeerID != wantPeerID {
		t.Fatalf("peerID = %x, want %x", gotPeerID, wantPeerID)
	}
}

func TestReadHandshakeOneByteReader(t *testing.T) {
	var wantHash, wantPeerID [20]byte
	copy(wantHash[:], "abcdefghijklmnopqrst")
	copy(wantPeerID[:], "uvwxyz0123456789ABCD")

	var buf bytes.Buffer
	if err := WriteHandshake(&buf, wantHash, wantPeerID); err != nil {
		t.Fatalf("WriteHandshake error: %v", err)
	}

	gotHash, gotPeerID, err := ReadHandshake(iotest.OneByteReader(&buf))
	if err != nil {
		t.Fatalf("ReadHandshake(OneByteReader) error: %v", err)
	}
	if gotHash != wantHash || gotPeerID != wantPeerID {
		t.Fatalf("mismatch: got (%x, %x), want (%x, %x)", gotHash, gotPeerID, wantHash, wantPeerID)
	}
}

func TestReadHandshakeRejectsInvalidInputs(t *testing.T) {
	var h, p [20]byte
	var validBuf bytes.Buffer
	if err := WriteHandshake(&validBuf, h, p); err != nil {
		t.Fatal(err)
	}
	valid := validBuf.Bytes()

	wrongPstr := append([]byte(nil), valid...)
	copy(wrongPstr[1:20], "BitTorrent Xrotocol")

	wrongLen := append([]byte(nil), valid...)
	wrongLen[0] = 18

	cases := []struct {
		name string
		data []byte
	}{
		{name: "wrong protocol string", data: wrongPstr},
		{name: "wrong protocol length", data: wrongLen},
		{name: "empty EOF", data: []byte{}},
		{name: "truncated 67 bytes", data: valid[:67]},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := ReadHandshake(bytes.NewReader(tc.data)); err == nil {
				t.Fatalf("expected error for %s", tc.name)
			}
		})
	}
}
