package change

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"

	"github.com/Deln0r/loro-go/encoding/postcard"
)

// ValueKind is the op-content kind stored in the ops value_type column
// (crates/loro-internal/src/encoding/value.rs). Future kinds set bit 0x80.
type ValueKind uint8

const (
	VKNull        ValueKind = 0
	VKTrue        ValueKind = 1
	VKFalse       ValueKind = 2
	VKI64         ValueKind = 3
	VKF64         ValueKind = 4
	VKStr         ValueKind = 5
	VKBinary      ValueKind = 6
	VKContainer   ValueKind = 7
	VKDeleteOnce  ValueKind = 8
	VKDeleteSeq   ValueKind = 9
	VKDeltaInt    ValueKind = 10
	VKLoroValue   ValueKind = 11
	VKMarkStart   ValueKind = 12
	VKTreeMove    ValueKind = 13
	VKListMove    ValueKind = 14
	VKListSet     ValueKind = 15
	VKRawTreeMove ValueKind = 16
)

// LoroValueKind is the nested self-describing value kind used by VKLoroValue.
type loroValueKind uint8

const (
	lvNull      loroValueKind = 0
	lvTrue      loroValueKind = 1
	lvFalse     loroValueKind = 2
	lvI64       loroValueKind = 3
	lvF64       loroValueKind = 4
	lvStr       loroValueKind = 5
	lvBinary    loroValueKind = 6
	lvList      loroValueKind = 7
	lvMap       loroValueKind = 8
	lvContainer loroValueKind = 9
)

// ValueReader reads the change-block VALUES stream. Integers are LEB128 (signed
// for i64, unsigned for lengths), f64 is 8-byte BIG-endian, strings/bytes are
// length-prefixed. Note: this is NOT postcard (postcard uses zigzag for signed
// and little-endian for f64); the value stream is hand-rolled in loro.
type ValueReader struct {
	r *postcard.Reader
}

// NewValueReader returns a ValueReader over the raw values blob.
func NewValueReader(b []byte) *ValueReader { return &ValueReader{r: postcard.NewReader(b)} }

// Empty reports whether the whole values blob has been consumed.
func (v *ValueReader) Empty() bool { return v.r.Empty() }

// U64 reads a LEB128 unsigned int (same wire as postcard uvarint).
func (v *ValueReader) U64() (uint64, error) { return v.r.Uvarint() }

// I64 reads a LEB128 *signed* int (sign-extended), matching leb128::read::signed.
func (v *ValueReader) I64() (int64, error) {
	var result int64
	var shift uint
	for {
		b, err := v.r.Byte()
		if err != nil {
			return 0, err
		}
		result |= int64(b&0x7f) << shift
		shift += 7
		if b&0x80 == 0 {
			if shift < 64 && b&0x40 != 0 {
				result |= -1 << shift
			}
			return result, nil
		}
		if shift >= 64 {
			return 0, errors.New("loro/change: i64 leb128 overflow")
		}
	}
}

// F64 reads an 8-byte big-endian IEEE-754 double.
func (v *ValueReader) F64() (float64, error) {
	b, err := v.r.Bytes(8)
	if err != nil {
		return 0, err
	}
	return math.Float64frombits(binary.BigEndian.Uint64(b)), nil
}

// Str reads a LEB128-length-prefixed UTF-8 string.
func (v *ValueReader) Str() (string, error) { return v.r.String() }

// Binary reads a LEB128-length-prefixed byte slice. The length is checked
// against the unread remainder before the int conversion so a hostile value
// cannot truncate on a 32-bit int or drive an over-large read.
func (v *ValueReader) Binary() ([]byte, error) {
	n, err := v.r.Uvarint()
	if err != nil {
		return nil, err
	}
	if n > uint64(v.r.Remaining()) {
		return nil, fmt.Errorf("loro/change: binary length %d exceeds remaining %d", n, v.r.Remaining())
	}
	return v.r.Bytes(int(n))
}

// maxLoroValueDepth bounds nested LoroValue recursion so a crafted chain of
// nested lists/maps cannot overflow the stack. Loro itself uses 128.
const maxLoroValueDepth = 128

// LoroValue reads one self-describing nested value (kind byte + content).
func (v *ValueReader) LoroValue() (any, error) { return v.loroValue(0) }

func (v *ValueReader) loroValue(depth int) (any, error) {
	if depth > maxLoroValueDepth {
		return nil, fmt.Errorf("loro/change: LoroValue nested deeper than %d", maxLoroValueDepth)
	}
	kb, err := v.r.Byte()
	if err != nil {
		return nil, err
	}
	switch loroValueKind(kb) {
	case lvNull:
		return nil, nil
	case lvTrue:
		return true, nil
	case lvFalse:
		return false, nil
	case lvI64:
		return v.I64()
	case lvF64:
		return v.F64()
	case lvStr:
		return v.Str()
	case lvBinary:
		return v.Binary()
	case lvList:
		n, err := v.U64()
		if err != nil {
			return nil, err
		}
		if n > uint64(v.r.Remaining()) { // each element costs at least a kind byte
			return nil, fmt.Errorf("loro/change: list length %d exceeds remaining %d", n, v.r.Remaining())
		}
		out := make([]any, n)
		for i := range out {
			if out[i], err = v.loroValue(depth + 1); err != nil {
				return nil, err
			}
		}
		return out, nil
	case lvMap:
		n, err := v.U64()
		if err != nil {
			return nil, err
		}
		if n > uint64(v.r.Remaining()) { // each entry costs at least a key-len + kind byte
			return nil, fmt.Errorf("loro/change: map length %d exceeds remaining %d", n, v.r.Remaining())
		}
		out := make(map[string]any, n)
		for i := uint64(0); i < n; i++ {
			k, err := v.Str()
			if err != nil {
				return nil, err
			}
			val, err := v.loroValue(depth + 1)
			if err != nil {
				return nil, err
			}
			out[k] = val
		}
		return out, nil
	case lvContainer:
		ct, err := v.U64()
		if err != nil {
			return nil, err
		}
		return ContainerType(ct), nil
	default:
		return nil, fmt.Errorf("loro/change: unknown LoroValueKind %d", kb)
	}
}

// appendLEBSigned appends the LEB128 signed (sign-extended) encoding of v,
// matching leb128::write::signed (the inverse of ValueReader.I64).
func appendLEBSigned(out []byte, v int64) []byte {
	for {
		b := byte(v & 0x7f)
		v >>= 7 // arithmetic shift
		if (v == 0 && b&0x40 == 0) || (v == -1 && b&0x40 != 0) {
			return append(out, b)
		}
		out = append(out, b|0x80)
	}
}

// ValueWriter is the byte-identical inverse of ValueReader for the VALUES stream.
type ValueWriter struct {
	buf []byte
}

// Bytes returns the accumulated value stream.
func (w *ValueWriter) Bytes() []byte { return w.buf }

func (w *ValueWriter) U64(v uint64) { w.buf = postcard.AppendUvarint(w.buf, v) }
func (w *ValueWriter) I64(v int64)  { w.buf = appendLEBSigned(w.buf, v) }

func (w *ValueWriter) F64(f float64) {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], math.Float64bits(f))
	w.buf = append(w.buf, b[:]...)
}

func (w *ValueWriter) Str(s string) {
	w.buf = postcard.AppendUvarint(w.buf, uint64(len(s)))
	w.buf = append(w.buf, s...)
}

func (w *ValueWriter) Binary(b []byte) {
	w.buf = postcard.AppendUvarint(w.buf, uint64(len(b)))
	w.buf = append(w.buf, b...)
}

// LoroValue writes one self-describing nested value (kind byte + content).
// Note: a map[string]any cannot round-trip byte-identically because Go map
// iteration order is unspecified; loro's fixtures use no nested LoroValue map.
func (w *ValueWriter) LoroValue(v any) error {
	switch x := v.(type) {
	case nil:
		w.buf = append(w.buf, byte(lvNull))
	case bool:
		if x {
			w.buf = append(w.buf, byte(lvTrue))
		} else {
			w.buf = append(w.buf, byte(lvFalse))
		}
	case int64:
		w.buf = append(w.buf, byte(lvI64))
		w.I64(x)
	case float64:
		w.buf = append(w.buf, byte(lvF64))
		w.F64(x)
	case string:
		w.buf = append(w.buf, byte(lvStr))
		w.Str(x)
	case []byte:
		w.buf = append(w.buf, byte(lvBinary))
		w.Binary(x)
	case []any:
		w.buf = append(w.buf, byte(lvList))
		w.U64(uint64(len(x)))
		for _, e := range x {
			if err := w.LoroValue(e); err != nil {
				return err
			}
		}
	case map[string]any:
		w.buf = append(w.buf, byte(lvMap))
		w.U64(uint64(len(x)))
		for k, val := range x {
			w.Str(k)
			if err := w.LoroValue(val); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("loro/change: cannot encode LoroValue of type %T", v)
	}
	return nil
}

// Mark writes a MarkStart value (inverse of ReadMark).
func (w *ValueWriter) Mark(m Mark) error {
	w.buf = append(w.buf, m.Info)
	w.U64(uint64(m.Len))
	w.U64(uint64(m.KeyIdx))
	return w.LoroValue(m.Value)
}

// ListMove writes a MovableList move value (inverse of ReadListMove).
func (w *ValueWriter) ListMove(m ListMove) {
	w.U64(uint64(m.From))
	w.U64(uint64(m.FromIdx))
	w.U64(uint64(m.Lamport))
}

// RawTreeMove writes a Tree create/move value (inverse of ReadRawTreeMove).
func (w *ValueWriter) RawTreeMove(m RawTreeMove) {
	w.U64(uint64(m.SubjectPeerIdx))
	w.U64(uint64(m.SubjectCounter))
	w.U64(uint64(m.PositionIdx))
	if m.ParentNull {
		w.buf = append(w.buf, 1)
		return
	}
	w.buf = append(w.buf, 0)
	w.U64(uint64(m.ParentPeerIdx))
	w.U64(uint64(m.ParentCounter))
}

// OpContent writes one op's value, dispatched by ValueKind (inverse of read).
func (w *ValueWriter) OpContent(vk ValueKind, v any) error {
	// A value of the wrong Go type is a caller error, not a reason to panic:
	// every kind below reports a mismatch as an error instead.
	bad := func() error {
		return fmt.Errorf("loro/change: value kind %d cannot encode a %T", vk, v)
	}
	switch vk {
	case VKNull, VKTrue, VKFalse, VKDeleteOnce, VKDeleteSeq:
		return nil // no inline bytes; delete targets live in delete_start_ids
	case VKI64, VKDeltaInt:
		x, ok := v.(int64)
		if !ok {
			return bad()
		}
		w.I64(x)
	case VKF64:
		x, ok := v.(float64)
		if !ok {
			return bad()
		}
		w.F64(x)
	case VKStr:
		x, ok := v.(string)
		if !ok {
			return bad()
		}
		w.Str(x)
	case VKBinary:
		x, ok := v.([]byte)
		if !ok {
			return bad()
		}
		w.Binary(x)
	case VKLoroValue:
		return w.LoroValue(v)
	case VKContainer:
		x, ok := v.(uint64)
		if !ok {
			return bad()
		}
		w.U64(x)
	case VKMarkStart:
		x, ok := v.(Mark)
		if !ok {
			return bad()
		}
		return w.Mark(x)
	case VKListMove:
		x, ok := v.(ListMove)
		if !ok {
			return bad()
		}
		w.ListMove(x)
	case VKRawTreeMove:
		x, ok := v.(RawTreeMove)
		if !ok {
			return bad()
		}
		w.RawTreeMove(x)
	default:
		return fmt.Errorf("loro/change: OpContent encode unhandled value kind %d", vk)
	}
	return nil
}

// Mark is a rich-text style mark (VKMarkStart): info flags, span length, key
// index, and the style value. Start position is the op's prop.
type Mark struct {
	Info   uint8
	Len    int64
	KeyIdx int64
	Value  any
}

// ListMove is a MovableList move op (VKListMove).
type ListMove struct {
	From    int64
	FromIdx int64
	Lamport int64
}

// RawTreeMove is a Tree create/move op (VKRawTreeMove).
type RawTreeMove struct {
	SubjectPeerIdx int64
	SubjectCounter int64
	PositionIdx    int64
	ParentNull     bool
	ParentPeerIdx  int64
	ParentCounter  int64
}

// ReadMark reads a MarkStart value: info u8, len, key_idx, then a nested value.
func (v *ValueReader) ReadMark() (Mark, error) {
	info, err := v.r.Byte()
	if err != nil {
		return Mark{}, err
	}
	ln, err := v.r.Uvarint()
	if err != nil {
		return Mark{}, err
	}
	ki, err := v.r.Uvarint()
	if err != nil {
		return Mark{}, err
	}
	val, err := v.LoroValue()
	if err != nil {
		return Mark{}, err
	}
	return Mark{Info: info, Len: int64(ln), KeyIdx: int64(ki), Value: val}, nil
}

// ReadListMove reads a ListMove value: from, from_idx, lamport (all usize).
func (v *ValueReader) ReadListMove() (ListMove, error) {
	from, err := v.r.Uvarint()
	if err != nil {
		return ListMove{}, err
	}
	fromIdx, err := v.r.Uvarint()
	if err != nil {
		return ListMove{}, err
	}
	lamport, err := v.r.Uvarint()
	if err != nil {
		return ListMove{}, err
	}
	return ListMove{From: int64(from), FromIdx: int64(fromIdx), Lamport: int64(lamport)}, nil
}

// ReadRawTreeMove reads a RawTreeMove value.
func (v *ValueReader) ReadRawTreeMove() (RawTreeMove, error) {
	var m RawTreeMove
	spi, err := v.r.Uvarint()
	if err != nil {
		return m, err
	}
	sc, err := v.r.Uvarint()
	if err != nil {
		return m, err
	}
	pi, err := v.r.Uvarint()
	if err != nil {
		return m, err
	}
	pnull, err := v.r.Byte()
	if err != nil {
		return m, err
	}
	m.SubjectPeerIdx, m.SubjectCounter, m.PositionIdx = int64(spi), int64(sc), int64(pi)
	m.ParentNull = pnull != 0
	if !m.ParentNull {
		ppi, err := v.r.Uvarint()
		if err != nil {
			return m, err
		}
		pc, err := v.r.Uvarint()
		if err != nil {
			return m, err
		}
		m.ParentPeerIdx, m.ParentCounter = int64(ppi), int64(pc)
	}
	return m, nil
}

// OpContent reads one op's value from the stream, dispatched by its ValueKind.
// Delete/move kinds carry no inline value and must not be read here.
func (v *ValueReader) OpContent(vk ValueKind) (any, error) {
	switch vk {
	case VKNull:
		return nil, nil
	case VKTrue:
		return true, nil
	case VKFalse:
		return false, nil
	case VKI64:
		return v.I64()
	case VKF64:
		return v.F64()
	case VKStr:
		return v.Str()
	case VKBinary:
		return v.Binary()
	case VKLoroValue:
		return v.LoroValue()
	case VKMarkStart:
		return v.ReadMark()
	case VKListMove:
		return v.ReadListMove()
	case VKRawTreeMove:
		return v.ReadRawTreeMove()
	case VKDeltaInt:
		return v.I64()
	case VKContainer:
		return v.U64()
	case VKDeleteOnce, VKDeleteSeq:
		return nil, nil
	default:
		return nil, fmt.Errorf("loro/change: OpContent unhandled value kind %d", vk)
	}
}
