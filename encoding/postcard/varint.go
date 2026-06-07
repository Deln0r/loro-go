// Package postcard implements the subset of the postcard wire format that
// Loro's Fast encoding (serde_columnar over postcard) relies on. Postcard encodes
// unsigned integers as LEB128 varints (little-endian 7-bit groups, high bit =
// continuation) and signed integers as zigzag-then-varint.
package postcard

import "errors"

var (
	// ErrOverflow means the varint does not fit in 64 bits.
	ErrOverflow = errors.New("postcard: varint overflows 64 bits")
	// ErrTruncated means the buffer ended mid-varint.
	ErrTruncated = errors.New("postcard: truncated varint")
)

// Uvarint decodes a postcard unsigned varint (LEB128). It returns the value and
// the number of bytes consumed.
func Uvarint(b []byte) (uint64, int, error) {
	var x uint64
	var s uint
	for i, c := range b {
		if i == 10 { // a u64 varint is at most 10 bytes
			return 0, 0, ErrOverflow
		}
		if c < 0x80 {
			if i == 9 && c > 1 {
				return 0, 0, ErrOverflow
			}
			return x | uint64(c)<<s, i + 1, nil
		}
		x |= uint64(c&0x7f) << s
		s += 7
	}
	return 0, 0, ErrTruncated
}

// AppendUvarint appends the postcard varint encoding of v to dst.
func AppendUvarint(dst []byte, v uint64) []byte {
	for v >= 0x80 {
		dst = append(dst, byte(v)|0x80)
		v >>= 7
	}
	return append(dst, byte(v))
}

// Zigzag maps a signed integer to the unsigned value postcard varint-encodes.
func Zigzag(v int64) uint64 { return uint64((v << 1) ^ (v >> 63)) }

// Unzigzag reverses Zigzag.
func Unzigzag(v uint64) int64 { return int64(v>>1) ^ -int64(v&1) }

// AppendVarint appends the postcard signed-varint encoding (zigzag then varint).
func AppendVarint(dst []byte, v int64) []byte { return AppendUvarint(dst, Zigzag(v)) }

// Varint decodes a postcard signed varint. Returns value and bytes consumed.
func Varint(b []byte) (int64, int, error) {
	u, n, err := Uvarint(b)
	if err != nil {
		return 0, 0, err
	}
	return Unzigzag(u), n, nil
}
