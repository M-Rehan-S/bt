package wire

// Bitfield stores piece availability with one bit per piece, MSB-first.
// Piece 0 is the highest bit (0x80) of byte 0.
type Bitfield []byte

// NewBitfield allocates a zeroed Bitfield for numPieces pieces.
func NewBitfield(numPieces int) Bitfield {
	if numPieces <= 0 {
		return make(Bitfield, 0)
	}
	return make(Bitfield, (numPieces+7)/8)
}

// Has reports whether piece i is set in the bitfield.
// Out-of-range indices return false.
func (b Bitfield) Has(i int) bool {
	if i < 0 {
		return false
	}
	byteIdx := i / 8
	if byteIdx >= len(b) {
		return false
	}
	offset := i % 8
	return b[byteIdx]&(0x80>>offset) != 0
}

// Set marks piece i as present in the bitfield.
// Out-of-range indices are ignored.
func (b Bitfield) Set(i int) {
	if i < 0 {
		return
	}
	byteIdx := i / 8
	if byteIdx >= len(b) {
		return
	}
	offset := i % 8
	b[byteIdx] |= 0x80 >> offset
}
