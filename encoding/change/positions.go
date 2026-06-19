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
