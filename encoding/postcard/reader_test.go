package postcard

import (
	"errors"
	"math"
	"testing"
)

func TestReaderSequential(t *testing.T) {
	var buf []byte
	buf = append(buf, 0x2A)       // raw byte 42
	buf = AppendUvarint(buf, 300) // multi-byte unsigned
	buf = AppendVarint(buf, -7)   // zigzag signed
	buf = AppendUvarint(buf, 3)   // string length
	buf = append(buf, "abc"...)   // string payload
	buf = append(buf, 0xDE, 0xAD) // raw tail
	r := NewReader(buf)

	if b, err := r.Byte(); err != nil || b != 0x2A {
		t.Fatalf("Byte = %#x, %v; want 0x2a, nil", b, err)
	}
	if v, err := r.Uvarint(); err != nil || v != 300 {
		t.Fatalf("Uvarint = %d, %v; want 300, nil", v, err)
	}
	if v, err := r.Varint(); err != nil || v != -7 {
		t.Fatalf("Varint = %d, %v; want -7, nil", v, err)
	}
	if s, err := r.String(); err != nil || s != "abc" {
		t.Fatalf("String = %q, %v; want \"abc\", nil", s, err)
	}
	if r.Remaining() != 2 {
		t.Fatalf("Remaining = %d, want 2", r.Remaining())
	}
	tail, err := r.Bytes(2)
	if err != nil || tail[0] != 0xDE || tail[1] != 0xAD {
		t.Fatalf("Bytes = % x, %v; want de ad, nil", tail, err)
	}
	if !r.Empty() {
		t.Errorf("Empty = false after consuming everything")
	}
	if r.Remaining() != 0 || len(r.Rest()) != 0 {
		t.Errorf("Remaining = %d, Rest = % x; want 0, empty", r.Remaining(), r.Rest())
	}
}

func TestReaderEmptyBuffer(t *testing.T) {
	r := NewReader(nil)
	if !r.Empty() {
		t.Error("Empty = false on a nil buffer")
	}
	if _, err := r.Byte(); !errors.Is(err, ErrTruncated) {
		t.Errorf("Byte err = %v, want ErrTruncated", err)
	}
	if _, err := r.Uvarint(); !errors.Is(err, ErrTruncated) {
		t.Errorf("Uvarint err = %v, want ErrTruncated", err)
	}
}

// TestReaderBytesBounds covers the length guard in Bytes: it is written against
// the unread remainder rather than as r.I+n, so a hostile length near MaxInt
// cannot overflow the addition and slip past the check.
func TestReaderBytesBounds(t *testing.T) {
	r := NewReader([]byte{1, 2, 3, 4})
	if _, err := r.Bytes(2); err != nil {
		t.Fatalf("Bytes(2): %v", err)
	}
	for _, n := range []int{-1, 3, math.MaxInt32, math.MaxInt} {
		if _, err := r.Bytes(n); !errors.Is(err, ErrTruncated) {
			t.Errorf("Bytes(%d) err = %v, want ErrTruncated", n, err)
		}
	}
	if r.Remaining() != 2 {
		t.Errorf("Remaining = %d after failed reads, want 2 (cursor must not move)", r.Remaining())
	}
	if b, err := r.Bytes(2); err != nil || b[0] != 3 || b[1] != 4 {
		t.Errorf("Bytes(2) = % x, %v; want 03 04, nil", b, err)
	}
}

// TestReaderStringOversizedLength asserts a declared string length beyond the
// buffer is rejected on the raw value, before any narrowing conversion.
func TestReaderStringOversizedLength(t *testing.T) {
	buf := AppendUvarint(nil, math.MaxUint64)
	buf = append(buf, "abc"...)
	r := NewReader(buf)
	if s, err := r.String(); !errors.Is(err, ErrTruncated) {
		t.Errorf("String = %q, err = %v; want ErrTruncated", s, err)
	}
}

func TestVarintRoundTripBoundaries(t *testing.T) {
	unsigned := []uint64{0, 1, 127, 128, 16383, 16384, 1 << 32, math.MaxUint64}
	for _, v := range unsigned {
		r := NewReader(AppendUvarint(nil, v))
		got, err := r.Uvarint()
		if err != nil || got != v {
			t.Errorf("uvarint %d round-trip = %d, %v", v, got, err)
		}
		if !r.Empty() {
			t.Errorf("uvarint %d: %d bytes left after decode", v, r.Remaining())
		}
	}

	signed := []int64{0, -1, 1, 63, -64, math.MaxInt64, math.MinInt64}
	for _, v := range signed {
		r := NewReader(AppendVarint(nil, v))
		got, err := r.Varint()
		if err != nil || got != v {
			t.Errorf("varint %d round-trip = %d, %v", v, got, err)
		}
		if !r.Empty() {
			t.Errorf("varint %d: %d bytes left after decode", v, r.Remaining())
		}
	}
}
