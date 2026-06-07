package columnar

import "github.com/Deln0r/loro-go/encoding/postcard"

// Count-limited strategy decoders for RAW-CONCATENATED columns (the header and
// change_meta blobs pack several strategy columns back-to-back with no length
// prefixes; each is delimited by its known value count). These advance the
// shared reader exactly past the bytes they consume.

// AnyRleNU64 reads exactly count unsigned-varint values from r.
func AnyRleNU64(r *postcard.Reader, count int) ([]uint64, error) {
	out := make([]uint64, 0, count)
	for len(out) < count {
		tok, err := r.Varint()
		if err != nil {
			return nil, err
		}
		switch {
		case tok > 0:
			v, err := r.Uvarint()
			if err != nil {
				return nil, err
			}
			for i := int64(0); i < tok; i++ {
				out = append(out, v)
			}
		case tok < 0:
			for i := int64(0); i < -tok; i++ {
				v, err := r.Uvarint()
				if err != nil {
					return nil, err
				}
				out = append(out, v)
			}
		default:
			return nil, ErrColumnar
		}
	}
	if len(out) != count {
		return nil, ErrColumnar
	}
	return out, nil
}

// BoolRleN reads exactly count bools from r.
func BoolRleN(r *postcard.Reader, count int) ([]bool, error) {
	out := make([]bool, 0, count)
	cur := false
	for len(out) < count {
		n, err := r.Uvarint()
		if err != nil {
			return nil, err
		}
		for i := uint64(0); i < n; i++ {
			out = append(out, cur)
		}
		cur = !cur
	}
	if len(out) != count {
		return nil, ErrColumnar
	}
	return out, nil
}

// DeltaOfDeltaN reads exactly count values from a DoD column inside a raw buffer,
// advancing r past the head, the last-used-bits byte, and the bit stream.
func DeltaOfDeltaN(r *postcard.Reader, count int) ([]int64, error) {
	tag, err := r.Byte()
	if err != nil {
		return nil, err
	}
	if count == 0 {
		if tag != 0 {
			return nil, ErrColumnar
		}
		if _, err := r.Byte(); err != nil { // last-used-bits (0)
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
	lub, err := r.Byte()
	if err != nil {
		return nil, err
	}
	out := make([]int64, 0, count)
	out = append(out, first)
	if count == 1 {
		return out, nil // no dods, empty stream
	}
	br := &bitReader{b: r.Rest()}
	prevDelta := int64(0)
	prevVal := first
	for k := 1; k < count; k++ {
		prevDelta += readDoD(br)
		prevVal += prevDelta
		out = append(out, prevVal)
	}
	numBytes := (br.consumed + 7) / 8
	if numBytes > 0 && int(lub) != br.consumed-(numBytes-1)*8 {
		return nil, ErrColumnar // last-used-bits inconsistent with consumed bits
	}
	r.I += numBytes
	if r.I > len(r.B) {
		return nil, ErrColumnar
	}
	return out, nil
}
