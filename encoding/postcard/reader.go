package postcard

// Reader is a sequential cursor over a postcard byte buffer.
type Reader struct {
	B []byte
	I int
}

// NewReader returns a Reader positioned at the start of b.
func NewReader(b []byte) *Reader { return &Reader{B: b} }

// Empty reports whether the cursor is at or past the end.
func (r *Reader) Empty() bool { return r.I >= len(r.B) }

// Remaining returns the number of unread bytes.
func (r *Reader) Remaining() int { return len(r.B) - r.I }

// Rest returns the unread tail without advancing.
func (r *Reader) Rest() []byte { return r.B[r.I:] }

// Byte reads one raw byte.
func (r *Reader) Byte() (byte, error) {
	if r.I >= len(r.B) {
		return 0, ErrTruncated
	}
	c := r.B[r.I]
	r.I++
	return c, nil
}

// Uvarint reads an unsigned LEB128 varint.
func (r *Reader) Uvarint() (uint64, error) {
	v, n, err := Uvarint(r.B[r.I:])
	if err != nil {
		return 0, err
	}
	r.I += n
	return v, nil
}

// Varint reads a signed (zigzag) varint.
func (r *Reader) Varint() (int64, error) {
	v, n, err := Varint(r.B[r.I:])
	if err != nil {
		return 0, err
	}
	r.I += n
	return v, nil
}

// Bytes reads n raw bytes (a sub-slice that aliases the buffer). The bound is
// written as n > len(r.B)-r.I rather than r.I+n > len(r.B) so a hostile length
// near math.MaxInt cannot overflow the addition and slip past the check.
func (r *Reader) Bytes(n int) ([]byte, error) {
	if n < 0 || n > len(r.B)-r.I {
		return nil, ErrTruncated
	}
	b := r.B[r.I : r.I+n]
	r.I += n
	return b, nil
}

// String reads a varint-length-prefixed UTF-8 string. The declared length is
// checked against the unread remainder on the raw uint64 before the int
// conversion, so a value above the buffer size (or one that would truncate on a
// 32-bit int) is rejected rather than driving a bad allocation or read.
func (r *Reader) String() (string, error) {
	n, err := r.Uvarint()
	if err != nil {
		return "", err
	}
	if n > uint64(r.Remaining()) {
		return "", ErrTruncated
	}
	b, err := r.Bytes(int(n))
	if err != nil {
		return "", err
	}
	return string(b), nil
}
