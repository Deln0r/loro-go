package change

import (
	"github.com/Deln0r/loro-go/encoding/columnar"
	"github.com/Deln0r/loro-go/encoding/postcard"
)

// DecodePositions decodes the positions blob (blob 5, PositionArena.encode_v2):
// varint(field_count=1) + varint(col_count=2) + col0 [len]+AnyRle<usize> of
// common-prefix lengths + col1 [len]+(varint(n) + per entry varint(rest_len)+rest).
// Positions are fractional-index byte strings, prefix-diffed against the previous
// in sorted order. Returns nil for an empty blob.
func DecodePositions(blob []byte) ([][]byte, error) {
	if len(blob) == 0 {
		return nil, nil
	}
	r := postcard.NewReader(blob)
	fc, err := r.Uvarint()
	if err != nil {
		return nil, err
	}
	cc, err := r.Uvarint()
	if err != nil {
		return nil, err
	}
	if fc != 1 || cc != 2 {
		return nil, ErrBlock
	}
	col0, err := readLenPrefixed(r)
	if err != nil {
		return nil, err
	}
	col1, err := readLenPrefixed(r)
	if err != nil {
		return nil, err
	}
	prefixes, err := columnar.AnyRleU64(col0)
	if err != nil {
		return nil, err
	}
	cr := postcard.NewReader(col1)
	n, err := cr.Uvarint()
	if err != nil {
		return nil, err
	}
	if n > uint64(cr.Remaining()) { // each entry consumes at least its restLen varint
		return nil, ErrBlock
	}
	out := make([][]byte, n)
	var prev []byte
	for i := uint64(0); i < n; i++ {
		restLen, err := cr.Uvarint()
		if err != nil {
			return nil, err
		}
		rest, err := cr.Bytes(int(restLen))
		if err != nil {
			return nil, err
		}
		prefix := 0
		if int(i) < len(prefixes) {
			prefix = int(prefixes[i])
		}
		if prefix > len(prev) {
			return nil, ErrBlock
		}
		pos := append(append([]byte{}, prev[:prefix]...), rest...)
		out[i] = pos
		prev = pos
	}
	return out, nil
}

func readLenPrefixed(r *postcard.Reader) ([]byte, error) {
	n, err := r.Uvarint()
	if err != nil {
		return nil, err
	}
	return r.Bytes(int(n))
}

// EncodePositions re-emits the positions blob (inverse of DecodePositions). Each
// entry is stored as the length of its common prefix with the previous entry plus
// the differing tail, so the prefix lengths ride in an AnyRle column and the tails
// in a length-prefixed stream. An empty set encodes to an empty blob.
func EncodePositions(positions [][]byte) []byte {
	if len(positions) == 0 {
		return nil
	}
	prefixes := make([]uint64, len(positions))
	col1 := postcard.AppendUvarint(nil, uint64(len(positions)))
	var prev []byte
	for i, p := range positions {
		n := commonPrefixLen(prev, p)
		prefixes[i] = uint64(n)
		rest := p[n:]
		col1 = postcard.AppendUvarint(col1, uint64(len(rest)))
		col1 = append(col1, rest...)
		prev = p
	}
	out := postcard.AppendUvarint(nil, 1) // field count
	out = postcard.AppendUvarint(out, 2)  // column count
	out = appendLenPrefixed(out, columnar.EncodeAnyRleU64(prefixes))
	return appendLenPrefixed(out, col1)
}

// commonPrefixLen returns how many leading bytes a and b share.
func commonPrefixLen(a, b []byte) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}

func appendLenPrefixed(dst, col []byte) []byte {
	dst = postcard.AppendUvarint(dst, uint64(len(col)))
	return append(dst, col...)
}
