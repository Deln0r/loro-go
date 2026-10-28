package loro

import (
	"bytes"
	"fmt"
	"math"
	"unicode/utf8"

	"github.com/Deln0r/loro-go/encoding/change"
)

// handle is what every container handle holds: its document and the
// container's id. It holds no state of the container: each call looks the
// container up in the document as it is then, so a handle taken before an
// import edits and reads what the import made (review item 16).
type handle struct {
	d *Document
	k contKey
	// st is the state when the handle was taken. It is read only when
	// ruleHandleLive is knocked out, to show that a handle must not keep it.
	st *docState
}

func (d *Document) handle(name string, kind change.ContainerType) handle {
	d.mu.Lock()
	defer d.mu.Unlock()
	k := contKey{root: true, name: name, kind: kind}
	d.register(k)
	if !isMergeableName(name) {
		d.acquired[k] = true
	}
	return handle{d: d, k: k, st: d.st}
}

// state is the container's document state, with the step budget of the
// call about to run on it reset: a budget bounds one operation, so reads
// that walk a long text never use up what a later edit needs. The caller
// holds the lock.
func (h handle) state() *docState {
	st := h.d.st
	if h.d.knocked(ruleHandleLive) {
		st = h.st
	}
	st.work.steps = 0
	return st
}

// op is a new local op in the handle's container.
func (h handle) op(vk change.ValueKind) Op {
	return Op{Container: h.k.name, IsRoot: h.k.root, Kind: h.k.kind, VKind: vk, Len: 1}
}

func outOfRange(pos, n, length int) error {
	return fmt.Errorf("loro: %d elements from position %d are out of bounds for length %d", n, pos, length)
}

// inRange is loro's checked_range_end (handler.rs:92): n elements from pos
// lie within length, compared so that no sum can overflow.
func inRange(pos, n, length int) bool {
	return pos >= 0 && n >= 0 && pos <= length && n <= length-pos
}

// Text is a handle on a Text container: rich text, which Mark and Unmark
// style and Delta reads with its styles. Positions are counted in Unicode
// code points; loro-crdt's JavaScript API counts UTF-16 units, so a
// position from JavaScript has to be converted first.
type Text struct{ h handle }

// Text returns a handle on the root Text container called name.
func (d *Document) Text(name string) *Text { return &Text{d.handle(name, change.CText)} }

// Insert inserts s at Unicode position pos. Where style anchors sit at pos,
// the text goes before or after each as its mark's expand rule says, as in
// loro. s must be valid UTF-8.
func (t *Text) Insert(pos int, s string) error {
	d := t.h.d
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.editable(t.h.k); err != nil {
		return err
	}
	st := t.h.state()
	length := st.textLen(t.h.k)
	if pos < 0 || int64(pos) > length {
		return outOfRange(pos, 0, int(length))
	}
	if !utf8.ValidString(s) {
		return fmt.Errorf("loro: text to insert is not valid UTF-8")
	}
	if s == "" {
		return nil
	}
	op := t.h.op(change.VKStr)
	op.Pos = st.textInsertIndex(t.h.k, int64(pos))
	op.Value = s
	op.Len = int64(utf8.RuneCountInString(s))
	return d.apply(st, op, d.log.arena.alloc(len(s)))
}

// Delete removes n code points from Unicode position pos. As in loro
// (handler.rs:2349), the characters are deleted in runs that sit next to
// each other and whose ids follow one another, the rightmost run first, one
// op per run.
func (t *Text) Delete(pos, n int) error {
	d := t.h.d
	d.mu.Lock()
	defer d.mu.Unlock()
	if n == 0 {
		return nil
	}
	if err := d.editable(t.h.k); err != nil {
		return err
	}
	st := t.h.state()
	length := st.textLen(t.h.k)
	if !inRange(pos, n, int(length)) {
		return outOfRange(pos, n, int(length))
	}
	ranges, err := st.textDeleteRanges(t.h.k, int64(pos), int64(n))
	if err != nil {
		return err
	}
	if d.knocked(ruleDeleteSplit) && len(ranges) > 0 {
		ranges = []textRange{{entity: ranges[0].entity, n: int64(n), peer: ranges[0].peer, ctr: ranges[0].ctr}}
	}
	for i := len(ranges) - 1; i >= 0; i-- {
		r := ranges[i]
		op := t.h.op(change.VKDeleteSeq)
		op.Pos = r.entity
		op.Len = r.n
		op.Value = DeleteSpan{Peer: r.peer, Counter: r.ctr, Len: r.n}
		if err := d.apply(st, op, opProv{}); err != nil {
			return err
		}
	}
	return nil
}

// String returns the text.
func (t *Text) String() string {
	t.h.d.mu.Lock()
	defer t.h.d.mu.Unlock()
	if s := t.h.state().seqs[t.h.k]; s != nil {
		return s.text()
	}
	return ""
}

// Len returns the length of the text in code points.
func (t *Text) Len() int {
	t.h.d.mu.Lock()
	defer t.h.d.mu.Unlock()
	return int(t.h.state().textLen(t.h.k))
}

// Delta returns the text split into runs by the styles over it, as
// TextDelta does. The attribute values are copies.
func (t *Text) Delta() ([]DeltaOp, error) {
	t.h.d.mu.Lock()
	defer t.h.d.mu.Unlock()
	s := t.h.state().seqs[t.h.k]
	if s == nil {
		return nil, nil
	}
	delta, err := s.delta()
	if err != nil {
		return nil, err
	}
	// The attribute values are the marks' own; hand out copies.
	for i := range delta {
		if delta[i].Attributes != nil {
			delta[i].Attributes = copyValue(delta[i].Attributes).(map[string]any)
		}
	}
	return delta, nil
}

// Map is a handle on a Map container.
type Map struct{ h handle }

// Map returns a handle on the root Map container called name.
func (d *Document) Map(name string) *Map { return &Map{d.handle(name, change.CMap)} }

// Set sets key to v. Setting a key to the value it has already makes no op,
// as in loro (handler.rs:4486). v must be a value storeValue accepts.
func (m *Map) Set(key string, v any) error {
	d := m.h.d
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.editable(m.h.k); err != nil {
		return err
	}
	val, err := storeValue(v)
	if err != nil {
		return err
	}
	st := m.h.state()
	if cur, ok := st.mapEntry(m.h.k, key); ok && !d.knocked(ruleMapSkipEqual) && valueEqual(cur.Value, val) {
		return nil
	}
	op := m.h.op(change.VKLoroValue)
	op.MapKey = key
	op.Value = val
	return d.apply(st, op, opProv{})
}

// Delete deletes key. It makes an op even when the key is absent, as loro
// does (handler.rs:4558).
func (m *Map) Delete(key string) error {
	d := m.h.d
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.editable(m.h.k); err != nil {
		return err
	}
	st := m.h.state()
	if _, ok := st.mapEntry(m.h.k, key); !ok && d.knocked(ruleMapDeleteAbsent) {
		return nil
	}
	op := m.h.op(change.VKDeleteOnce)
	op.MapKey = key
	return d.apply(st, op, opProv{})
}

// Get returns the value at key and whether the key is set. A container
// stored at the key is returned as its id, "cid:counter@peer:Kind".
func (m *Map) Get(key string) (any, bool) {
	m.h.d.mu.Lock()
	defer m.h.d.mu.Unlock()
	op, ok := m.h.state().mapEntry(m.h.k, key)
	if !ok {
		return nil, false
	}
	return copyValue(plainValue(op.Value, ID{Peer: op.Peer, Counter: op.Counter}, true)), true
}

// List is a handle on a List container.
type List struct{ h handle }

// List returns a handle on the root List container called name.
func (d *Document) List(name string) *List { return &List{d.handle(name, change.CList)} }

// Insert inserts items at index pos, one op per item as loro's insert makes
// them; consecutive ones fold into one op. Each item must be a value
// storeValue accepts.
func (l *List) Insert(pos int, items ...any) error {
	d := l.h.d
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.editable(l.h.k); err != nil {
		return err
	}
	vals := make([]any, len(items))
	for i, it := range items {
		v, err := storeValue(it)
		if err != nil {
			return err
		}
		vals[i] = v
	}
	st := l.h.state()
	length := listLen(st, l.h.k)
	if pos < 0 || pos > length {
		return outOfRange(pos, 0, length)
	}
	for i, v := range vals {
		op := l.h.op(change.VKLoroValue)
		op.Pos = int64(pos + i)
		op.Value = []any{v}
		if err := d.apply(st, op, opProv{kind: provList}); err != nil {
			return err
		}
	}
	return nil
}

// Delete removes n items from index pos: one delete per item, each naming
// the item's id, which fold into one op where the ids follow one another
// (handler.rs:3403).
func (l *List) Delete(pos, n int) error {
	d := l.h.d
	d.mu.Lock()
	defer d.mu.Unlock()
	if n == 0 {
		return nil
	}
	if err := d.editable(l.h.k); err != nil {
		return err
	}
	st := l.h.state()
	length := listLen(st, l.h.k)
	if !inRange(pos, n, length) {
		return outOfRange(pos, n, length)
	}
	s := st.seqs[l.h.k]
	first, err := s.locate(int64(pos)+1, true, pastView{})
	if err != nil {
		return err
	}
	var ids []ID
	for i := first; i < len(s.nodes) && len(ids) < n; i++ {
		if x := s.nodes[i]; len(x.deleters) == 0 {
			ids = append(ids, ID{Peer: x.peer, Counter: x.counter})
		}
	}
	for _, id := range ids {
		op := l.h.op(change.VKDeleteSeq)
		op.Pos = int64(pos)
		op.Value = DeleteSpan{Peer: id.Peer, Counter: id.Counter, Len: 1}
		if err := d.apply(st, op, opProv{}); err != nil {
			return err
		}
	}
	return nil
}

// Get returns the item at index i. A container stored there is returned as
// its id, "cid:counter@peer:Kind".
func (l *List) Get(i int) (any, error) {
	l.h.d.mu.Lock()
	defer l.h.d.mu.Unlock()
	st := l.h.state()
	if length := listLen(st, l.h.k); i < 0 || i >= length {
		return nil, outOfRange(i, 1, length)
	}
	n, err := st.nthLive(l.h.k, int64(i))
	if err != nil || n == nil {
		return nil, err
	}
	return copyValue(plainValue(n.value, ID{Peer: n.peer, Counter: n.counter}, false)), nil
}

// Len returns the number of items.
func (l *List) Len() int {
	l.h.d.mu.Lock()
	defer l.h.d.mu.Unlock()
	return listLen(l.h.state(), l.h.k)
}

func listLen(st *docState, k contKey) int {
	if s := st.seqs[k]; s != nil {
		return s.live
	}
	return 0
}

// Counter is a handle on a Counter container.
type Counter struct{ h handle }

// Counter returns a handle on the root Counter container called name.
func (d *Document) Counter(name string) *Counter { return &Counter{d.handle(name, change.CCounter)} }

// counterI64Limit is 2^27: loro writes an increment as an I64 when it is a
// whole number below this in magnitude, and as an F64 otherwise
// (encoding/outdated_encode_reordered.rs:198).
const counterI64Limit = 1 << 27

// Increment adds x to the counter. loro's own peers add local increments one
// at a time and imported ones as one sum, so with fractions or magnitudes
// past 2^53 two loro peers with the same history can show different values
// (review item 1); with whole numbers below 2^53 every order gives one sum.
func (c *Counter) Increment(x float64) error {
	d := c.h.d
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.editable(c.h.k); err != nil {
		return err
	}
	op := c.h.op(change.VKF64)
	op.Value = x
	if a := math.Abs(x); a == math.Trunc(a) && a < counterI64Limit && !d.knocked(ruleCounterInt) {
		op.VKind, op.Value = change.VKI64, int64(x)
	}
	return d.apply(c.h.state(), op, opProv{})
}

// Value returns the counter's value: the sum of its increments.
func (c *Counter) Value() float64 {
	c.h.d.mu.Lock()
	defer c.h.d.mu.Unlock()
	var ops []Op
	if ci := c.h.state().conts[c.h.k]; ci != nil {
		ops = ci.ops
	}
	switch v := counterValue(ops).(type) {
	case int64:
		return float64(v)
	case float64:
		return v
	}
	return 0
}

// storeValue converts a Go value to what a map or list op stores: nil, bool,
// int64, float64, string, []byte, []any and map[string]any, the last two
// holding the same. Other integer types become int64 and float32 becomes
// float64. A float64 stays a float even when whole: loro-crdt's JavaScript
// binding turns a whole number within ±2^53 into an integer, which a caller
// converting JavaScript values has to do itself. A container cannot be
// stored as a value.
func storeValue(v any) (any, error) {
	switch x := v.(type) {
	case nil, bool, string, int64, float64:
		return x, nil
	case int:
		return int64(x), nil
	case int8:
		return int64(x), nil
	case int16:
		return int64(x), nil
	case int32:
		return int64(x), nil
	case uint8:
		return int64(x), nil
	case uint16:
		return int64(x), nil
	case uint32:
		return int64(x), nil
	case uint:
		if uint64(x) > math.MaxInt64 {
			return nil, fmt.Errorf("loro: integer %d does not fit an int64", x)
		}
		return int64(x), nil
	case uint64:
		if x > math.MaxInt64 {
			return nil, fmt.Errorf("loro: integer %d does not fit an int64", x)
		}
		return int64(x), nil
	case float32:
		return float64(x), nil
	case []byte:
		return bytes.Clone(x), nil
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			c, err := storeValue(e)
			if err != nil {
				return nil, err
			}
			out[i] = c
		}
		return out, nil
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			c, err := storeValue(e)
			if err != nil {
				return nil, err
			}
			out[k] = c
		}
		return out, nil
	}
	return nil, fmt.Errorf("loro: cannot store a value of type %T", v)
}

// valueEqual is LoroValue equality: same kind and same contents, floats
// compared as numbers (so -0 equals 0 and NaN equals nothing), a byte
// string by its bytes.
func valueEqual(a, b any) bool {
	switch x := a.(type) {
	case nil:
		return b == nil
	case bool:
		y, ok := b.(bool)
		return ok && x == y
	case int64:
		y, ok := b.(int64)
		return ok && x == y
	case float64:
		y, ok := b.(float64)
		return ok && x == y
	case string:
		y, ok := b.(string)
		return ok && x == y
	case []byte:
		y, ok := b.([]byte)
		return ok && bytes.Equal(x, y)
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !valueEqual(x[i], y[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for k, e := range x {
			f, ok := y[k]
			if !ok || !valueEqual(e, f) {
				return false
			}
		}
		return true
	}
	return false
}
