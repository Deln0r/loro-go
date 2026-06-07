package postcard

import (
	"bytes"
	"testing"
)

// Golden vectors from the postcard wire-format spec (LEB128 varint).
func TestUvarintGolden(t *testing.T) {
	cases := []struct {
		v uint64
		b []byte
	}{
		{0, []byte{0x00}},
		{1, []byte{0x01}},
		{127, []byte{0x7f}},
		{128, []byte{0x80, 0x01}},
		{300, []byte{0xac, 0x02}},
		{16384, []byte{0x80, 0x80, 0x01}},
		{^uint64(0), []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x01}},
	}
	for _, c := range cases {
		if got := AppendUvarint(nil, c.v); !bytes.Equal(got, c.b) {
			t.Errorf("encode %d = % x, want % x", c.v, got, c.b)
		}
		dv, n, err := Uvarint(c.b)
		if err != nil || dv != c.v || n != len(c.b) {
			t.Errorf("decode % x = (%d,%d,%v), want (%d,%d,nil)", c.b, dv, n, err, c.v, len(c.b))
		}
	}
}

func TestUvarintErrors(t *testing.T) {
	if _, _, err := Uvarint([]byte{0x80}); err != ErrTruncated {
		t.Errorf("truncated: got %v", err)
	}
	if _, _, err := Uvarint([]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x01}); err != ErrOverflow {
		t.Errorf("overflow: got %v", err)
	}
}

func TestZigzagRoundTrip(t *testing.T) {
	for _, v := range []int64{0, -1, 1, -2, 2, 63, -64, 1 << 40, -(1 << 40), 1<<62 - 1} {
		if Unzigzag(Zigzag(v)) != v {
			t.Errorf("zigzag round-trip failed for %d", v)
		}
	}
}
