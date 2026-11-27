package wire

import "testing"

func TestNewBitfieldSizeAndZeroed(t *testing.T) {
	cases := []struct {
		numPieces int
		wantBytes int
	}{
		{numPieces: 0, wantBytes: 0},
		{numPieces: 1, wantBytes: 1},
		{numPieces: 8, wantBytes: 1},
		{numPieces: 9, wantBytes: 2},
		{numPieces: 200, wantBytes: 25},
	}

	for _, tc := range cases {
		bf := NewBitfield(tc.numPieces)
		if len(bf) != tc.wantBytes {
			t.Fatalf("NewBitfield(%d) len = %d, want %d", tc.numPieces, len(bf), tc.wantBytes)
		}
		for i, b := range bf {
			if b != 0 {
				t.Fatalf("NewBitfield(%d) byte %d = %#x, want 0", tc.numPieces, i, b)
			}
		}
	}
}

func TestBitfieldMSBOrderGolden(t *testing.T) {
	// Verify Set produces the exact golden bytes.
	bf := NewBitfield(200)
	bf.Set(0)
	bf.Set(9)

	if bf[0] != 0x80 || bf[1] != 0x40 {
		t.Fatalf("got first two bytes %02x %02x, want 80 40", bf[0], bf[1])
	}
	for i := 2; i < len(bf); i++ {
		if bf[i] != 0 {
			t.Fatalf("unexpected non-zero byte at index %d: %#x", i, bf[i])
		}
	}

	// Verify Has independently against raw golden bytes (not constructed via Set).
	raw := make(Bitfield, 25)
	raw[0] = 0x80
	raw[1] = 0x40
	for i := 0; i < 200; i++ {
		want := i == 0 || i == 9
		if got := raw.Has(i); got != want {
			t.Fatalf("raw.Has(%d) = %v, want %v", i, got, want)
		}
	}
}

func TestBitfieldEveryBitInFirstTwoBytes(t *testing.T) {
	wantBytes := []byte{0x80, 0x40, 0x20, 0x10, 0x08, 0x04, 0x02, 0x01}
	for bit := 0; bit < 16; bit++ {
		bf := NewBitfield(16)
		bf.Set(bit)
		byteIdx := bit / 8
		if bf[byteIdx] != wantBytes[bit%8] {
			t.Fatalf("Set(%d): byte[%d] = %02x, want %02x", bit, byteIdx, bf[byteIdx], wantBytes[bit%8])
		}
		if !bf.Has(bit) {
			t.Fatalf("Has(%d) = false after Set(%d)", bit, bit)
		}
	}
}

func TestBitfieldOutOfRangeSafe(t *testing.T) {
	bf := NewBitfield(10) // 2 bytes
	bf.Set(-1)
	bf.Set(16)
	bf.Set(1000)

	if bf.Has(-1) || bf.Has(16) || bf.Has(1000) {
		t.Fatal("Has returned true for out-of-range index")
	}
	if bf[0] != 0 || bf[1] != 0 {
		t.Fatalf("out-of-range Set modified bitfield: %x", bf)
	}
}
