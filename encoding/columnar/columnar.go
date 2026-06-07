// Package columnar decodes the serde_columnar 0.3.14 column strategies that
// Loro's change-block format layers on top of postcard: AnyRle, BoolRle,
// DeltaRle, DeltaOfDelta. Verified byte-for-byte against vectors emitted by the
// real serde_columnar crate (see testdata/columnar_golden.txt + rustgen).
//
// Row count is not stored in a column; each decoder consumes its whole buffer
// (the caller knows how many rows it expects from surrounding context).
package columnar

import (
	"errors"

	"github.com/Deln0r/loro-go/encoding/postcard"
)

// ErrColumnar marks a malformed column.
var ErrColumnar = errors.New("loro/columnar: malformed column")

// anyRle decodes an AnyRle/Rle column. The length token is a zigzag varint:
// positive n repeats the next content value n times; negative n is a literal run
// of |n| distinct content values.
func anyRle[T any](col []byte, content func(*postcard.Reader) (T, error)) ([]T, error) {
	r := postcard.NewReader(col)
	var out []T
	for !r.Empty() {
		tok, err := r.Varint()
		if err != nil {
			return nil, err
		}
		switch {
		case tok > 0:
			v, err := content(r)
			if err != nil {
				return nil, err
			}
			for i := int64(0); i < tok; i++ {
				out = append(out, v)
			}
		case tok < 0:
			for i := int64(0); i < -tok; i++ {
				v, err := content(r)
				if err != nil {
					return nil, err
				}
				out = append(out, v)
			}
		default:
			return nil, ErrColumnar
		}
	}
	return out, nil
}

func contentUvarint(r *postcard.Reader) (uint64, error) { return r.Uvarint() }
func contentVarint(r *postcard.Reader) (int64, error)   { return r.Varint() }
func contentByte(r *postcard.Reader) (uint8, error)     { return r.Byte() }

// AnyRleU64 decodes an Rle column whose content is unsigned varint (u16/u32/u64/usize).
func AnyRleU64(col []byte) ([]uint64, error) { return anyRle(col, contentUvarint) }

// AnyRleI64 decodes an Rle column whose content is signed zigzag varint (i32/i64/isize).
func AnyRleI64(col []byte) ([]int64, error) { return anyRle(col, contentVarint) }

// AnyRleU8 decodes an Rle column whose content is a raw byte (u8).
func AnyRleU8(col []byte) ([]uint8, error) { return anyRle(col, contentByte) }

// BoolRle decodes a BoolRle column: alternating run lengths (unsigned varints)
// starting from false, emitting count copies then flipping.
func BoolRle(col []byte) ([]bool, error) {
	r := postcard.NewReader(col)
	var out []bool
	cur := false
	for !r.Empty() {
		n, err := r.Uvarint()
		if err != nil {
			return nil, err
		}
		for i := uint64(0); i < n; i++ {
			out = append(out, cur)
		}
		cur = !cur
	}
	return out, nil
}

// DeltaRleI64 decodes a DeltaRle column: an Rle of zigzag first-order deltas,
// prefix-summed from 0. Works for DeltaRle over u32/i32/usize/isize; the caller
// casts the int64 results to the target type.
func DeltaRleI64(col []byte) ([]int64, error) {
	deltas, err := anyRle(col, contentVarint)
	if err != nil {
		return nil, err
	}
	out := make([]int64, len(deltas))
	var acc int64
	for i, d := range deltas {
		acc += d
		out[i] = acc
	}
	return out, nil
}

// bitReader reads bits MSB-first. Reading past the end yields zero bits, which
// is harmless because callers drive iteration by the exact valid-bit count.
type bitReader struct {
	b        []byte
	consumed int
}

func (br *bitReader) bit() int {
	idx := br.consumed / 8
	off := 7 - (br.consumed % 8)
	br.consumed++
	if idx >= len(br.b) {
		return 0
	}
	return int((br.b[idx] >> uint(off)) & 1)
}

func (br *bitReader) bits(n int) uint64 {
	var v uint64
	for i := 0; i < n; i++ {
		v = (v << 1) | uint64(br.bit())
	}
	return v
}

// readDoD reads one delta-of-delta value (Gorilla-style variable bit length).
func readDoD(br *bitReader) int64 {
	if br.bit() == 0 {
		return 0
	}
	if br.bit() == 0 {
		return int64(br.bits(7)) - 63
	}
	if br.bit() == 0 {
		return int64(br.bits(9)) - 255
	}
	if br.bit() == 0 {
		return int64(br.bits(12)) - 2047
	}
	if br.bit() == 0 {
		return int64(br.bits(21)) - 1048575
	}
	return int64(br.bits(64))
}

// DeltaOfDelta decodes a DeltaOfDelta (Gorilla) column: an Option<i64> head
// (00 = None/empty; 01 + zigzag varint = first value), a u8 last-used-bits, then
// an MSB-first bit stream of second-order deltas. Decoded values are reconstructed
// by double prefix-sum. Used for timestamps and dep counters.
func DeltaOfDelta(col []byte) ([]int64, error) {
	r := postcard.NewReader(col)
	tag, err := r.Byte()
	if err != nil {
		return nil, err
	}
	if tag == 0 {
		// None head: still followed by a last-used-bits byte (00). Consume it so
		// the cursor is correct when DoD columns are raw-concatenated (header blob).
		if _, err := r.Byte(); err != nil {
			return nil, err
		}
		return nil, nil
	}
	if tag != 1 {
		return nil, ErrColumnar
	}
	first, err := r.Varint()
	if err != nil {
		return nil, err
	}
	lastUsedBits, err := r.Byte()
	if err != nil {
		return nil, err
	}
	stream := r.Rest()
	totalBits := 0
	if len(stream) > 0 {
		totalBits = (len(stream)-1)*8 + int(lastUsedBits)
	}
	out := []int64{first}
	br := &bitReader{b: stream}
	prevDelta := int64(0)
	prevVal := first
	for br.consumed < totalBits {
		prevDelta += readDoD(br)
		prevVal += prevDelta
		out = append(out, prevVal)
	}
	return out, nil
}

// Columns splits a serde_columnar-wrapped vec value into its raw per-column byte
// slices. Layout: varint(field_count) ++ varint(col_count) ++ (varint(len) ++
// bytes) per column. Loro's wrapped column blobs have field_count == 1 (a single
// class="vec" field).
func Columns(b []byte) ([][]byte, error) {
	r := postcard.NewReader(b)
	fc, err := r.Uvarint()
	if err != nil {
		return nil, err
	}
	if fc != 1 {
		return nil, ErrColumnar
	}
	cc, err := r.Uvarint()
	if err != nil {
		return nil, err
	}
	cols := make([][]byte, 0, cc)
	for i := uint64(0); i < cc; i++ {
		n, err := r.Uvarint()
		if err != nil {
			return nil, err
		}
		col, err := r.Bytes(int(n))
		if err != nil {
			return nil, err
		}
		cols = append(cols, col)
	}
	return cols, nil
}
