package columnar

import "github.com/Deln0r/loro-go/encoding/postcard"

// This file is the byte-identical inverse of the decoders in columnar.go.
// Verified by round-tripping the serde_columnar 0.3.14 golden vectors.

// anyRleEncode encodes an AnyRle/Rle column. Maximal runs of equal values with
// length >= 2 become repeat-runs (positive zigzag token + one content value);
// consecutive singletons are merged into one literal-run (negative token + each
// value). This matches serde_columnar's encoder choices.
func anyRleEncode[T comparable](vals []T, write func(*[]byte, T)) []byte {
	var out []byte
	n := len(vals)
	i := 0
	for i < n {
		j := i + 1
		for j < n && vals[j] == vals[i] {
			j++
		}
		if j-i >= 2 { // repeat run
			out = postcard.AppendVarint(out, int64(j-i))
			write(&out, vals[i])
			i = j
			continue
		}
		// literal run: accumulate singletons until a >=2 run begins
		start := i
		for i < n {
			k := i + 1
			for k < n && vals[k] == vals[i] {
				k++
			}
			if k-i >= 2 {
				break
			}
			i++
		}
		out = postcard.AppendVarint(out, int64(-(i - start)))
		for x := start; x < i; x++ {
			write(&out, vals[x])
		}
	}
	return out
}

func wUvarint(o *[]byte, v uint64) { *o = postcard.AppendUvarint(*o, v) }
func wVarint(o *[]byte, v int64)   { *o = postcard.AppendVarint(*o, v) }
func wByte(o *[]byte, v uint8)     { *o = append(*o, v) }

// EncodeAnyRleU64 encodes an Rle column with unsigned-varint content.
func EncodeAnyRleU64(vals []uint64) []byte { return anyRleEncode(vals, wUvarint) }

// EncodeAnyRleI64 encodes an Rle column with signed zigzag-varint content.
func EncodeAnyRleI64(vals []int64) []byte { return anyRleEncode(vals, wVarint) }

// EncodeAnyRleU8 encodes an Rle column with raw-byte content.
func EncodeAnyRleU8(vals []uint8) []byte { return anyRleEncode(vals, wByte) }

// EncodeBoolRle encodes a BoolRle column: alternating run lengths starting from
// false. A leading 0 appears when the data starts with true.
func EncodeBoolRle(vals []bool) []byte {
	var out []byte
	cur := false
	i := 0
	for i < len(vals) {
		cnt := uint64(0)
		for i < len(vals) && vals[i] == cur {
			cnt++
			i++
		}
		out = postcard.AppendUvarint(out, cnt)
		cur = !cur
	}
	return out
}

// EncodeDeltaRleI64 encodes a DeltaRle column: first-order deltas (from 0) run
// through the Rle encoder with zigzag content.
func EncodeDeltaRleI64(vals []int64) []byte {
	deltas := make([]int64, len(vals))
	var prev int64
	for i, v := range vals {
		deltas[i] = v - prev
		prev = v
	}
	return anyRleEncode(deltas, wVarint)
}

// bitWriter packs bits MSB-first.
type bitWriter struct {
	bytes []byte
	nbits int
}

func (w *bitWriter) writeBit(b int) {
	if w.nbits%8 == 0 {
		w.bytes = append(w.bytes, 0)
	}
	if b != 0 {
		w.bytes[len(w.bytes)-1] |= 1 << uint(7-(w.nbits%8))
	}
	w.nbits++
}

func (w *bitWriter) writeBits(v uint64, n int) {
	for i := n - 1; i >= 0; i-- {
		w.writeBit(int((v >> uint(i)) & 1))
	}
}

func writeDoD(w *bitWriter, dod int64) {
	if dod == 0 {
		w.writeBit(0)
		return
	}
	w.writeBit(1)
	if dod >= -63 && dod <= 64 {
		w.writeBit(0)
		w.writeBits(uint64(dod+63), 7)
		return
	}
	w.writeBit(1)
	if dod >= -255 && dod <= 256 {
		w.writeBit(0)
		w.writeBits(uint64(dod+255), 9)
		return
	}
	w.writeBit(1)
	if dod >= -2047 && dod <= 2048 {
		w.writeBit(0)
		w.writeBits(uint64(dod+2047), 12)
		return
	}
	w.writeBit(1)
	if dod >= -1048575 && dod <= 1048576 {
		w.writeBit(0)
		w.writeBits(uint64(dod+1048575), 21)
		return
	}
	w.writeBit(1) // '11111'
	w.writeBits(uint64(dod), 64)
}

// EncodeDeltaOfDelta encodes a DeltaOfDelta (Gorilla) column: Option<i64> head
// (00 + lub for empty), a last-used-bits byte, then the MSB-first dod bit stream.
func EncodeDeltaOfDelta(vals []int64) []byte {
	if len(vals) == 0 {
		return []byte{0x00, 0x00} // None head + last_used_bits 0
	}
	out := []byte{0x01}
	out = postcard.AppendVarint(out, vals[0])
	w := &bitWriter{}
	prevDelta := int64(0)
	prevVal := vals[0]
	for k := 1; k < len(vals); k++ {
		delta := vals[k] - prevVal
		writeDoD(w, delta-prevDelta)
		prevDelta = delta
		prevVal = vals[k]
	}
	lub := byte(0)
	if w.nbits > 0 {
		if r := w.nbits % 8; r == 0 {
			lub = 8
		} else {
			lub = byte(r)
		}
	}
	out = append(out, lub)
	out = append(out, w.bytes...)
	return out
}

// EncodeColumns wraps raw column blobs in the serde_columnar vec framing:
// varint(field_count=1) + varint(col_count) + per column varint(len)+bytes.
func EncodeColumns(cols [][]byte) []byte {
	out := postcard.AppendUvarint(nil, 1) // field_count
	out = postcard.AppendUvarint(out, uint64(len(cols)))
	for _, c := range cols {
		out = postcard.AppendUvarint(out, uint64(len(c)))
		out = append(out, c...)
	}
	return out
}
