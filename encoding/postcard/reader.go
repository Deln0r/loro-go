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

// Bytes reads n raw bytes (a sub-slice that aliases the buffer).
func (r *Reader) Bytes(n int) ([]byte, error) {
	if n < 0 || r.I+n > len(r.B) {
		return nil, ErrTruncated
	}
	b := r.B[r.I : r.I+n]
	r.I += n
	return b, nil
}

// String reads a varint-length-prefixed UTF-8 string.
func (r *Reader) String() (string, error) {
	n, err := r.Uvarint()
	if err != nil {
		return "", err
	}
	b, err := r.Bytes(int(n))
	if err != nil {
		return "", err
	}
	return string(b), nil
}
