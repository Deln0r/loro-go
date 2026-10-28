package loro

import (
	"fmt"

	"github.com/Deln0r/loro-go/encoding/change"
)

// MovableList is a handle on a MovableList container: a list whose items
// can be moved and set in place. loro keeps it as positions and elements
// (state/movable_list_state.rs): an op counts positions, the ones that
// lost a concurrent move to another position of their element included,
// while users count only the positions their elements are at. Indexes
// given to and returned by these methods are the users'.
type MovableList struct{ h handle }

// MovableList returns a handle on the root MovableList container called
// name.
func (d *Document) MovableList(name string) *MovableList {
	return &MovableList{d.handle(name, KindMovableList)}
}

// Kind returns KindMovableList.
func (l *MovableList) Kind() ContainerKind { return l.h.kind() }

// ID returns the container's id.
func (l *MovableList) ID() string { return l.h.id() }

// IsDeleted reports whether the container is gone from the document.
func (l *MovableList) IsDeleted() bool { return l.h.isDeleted() }

// JSON returns the list's items, the containers in it included.
func (l *MovableList) JSON() (any, error) { return l.h.json() }

// movSlots lists, in order, the merged sequence's live positions that hold
// their element, each with its index among all live positions: users
// count the first, ops the second (convert_index, movable_list_state.rs:857).
type movSlot struct {
	node *seqNode
	op   int
}

func movSlots(s *seqState) []movSlot {
	if s == nil {
		return nil
	}
	var out []movSlot
	op := 0
	for _, n := range s.nodes {
		if len(n.deleters) != 0 {
			continue
		}
		if n.elem != nil && n.elem.pos == n {
			out = append(out, movSlot{n, op})
		}
		op++
	}
	return out
}

// opIndex converts a user index to the op index loro records: the index
// among all live positions of the position at user index i, and for the
// end of the list the number of live positions.
func opIndex(s *seqState, slots []movSlot, i int, d *Document) int64 {
	if i == len(slots) {
		if s == nil {
			return 0
		}
		return int64(s.live)
	}
	if d.knocked(ruleMovableOpIndex) {
		return int64(i)
	}
	return int64(slots[i].op)
}

// Len returns the number of items.
func (l *MovableList) Len() int {
	l.h.d.mu.Lock()
	defer l.h.d.mu.Unlock()
	return len(movSlots(l.h.state().seqs[l.h.k]))
}

// Get returns the item at index i. A container stored there is returned as
// its id, "cid:counter@peer:Kind".
func (l *MovableList) Get(i int) (any, error) {
	l.h.d.mu.Lock()
	defer l.h.d.mu.Unlock()
	slots := movSlots(l.h.state().seqs[l.h.k])
	if i < 0 || i >= len(slots) {
		return nil, outOfRange(i, 1, len(slots))
	}
	e := slots[i].node.elem
	return copyValue(plainValue(e.value, e.valueID, e.valueSet)), nil
}

// GetContainer returns the container stored at index i, false when the
// item there is no container.
func (l *MovableList) GetContainer(i int) (Container, bool) {
	d := l.h.d
	d.mu.Lock()
	defer d.mu.Unlock()
	slots := movSlots(l.h.state().seqs[l.h.k])
	if i < 0 || i >= len(slots) {
		return nil, false
	}
	e := slots[i].node.elem
	ct, ok := e.value.(change.ContainerType)
	if !ok || !editableKind(ct) {
		return nil, false
	}
	return d.handleOf(childKey(e.valueID, ct)), true
}

// Insert inserts items at index pos, one op per item as loro's insert makes
// them, at the op index of the user index (handler.rs:3730).
func (l *MovableList) Insert(pos int, items ...any) error {
	d := l.h.d
	d.mu.Lock()
	defer d.mu.Unlock()
	vals := make([]any, len(items))
	for i, it := range items {
		v, err := storeValue(it)
		if err != nil {
			return err
		}
		vals[i] = v
	}
	for i, v := range vals {
		if _, err := l.insert(pos+i, v); err != nil {
			return err
		}
	}
	return nil
}

// InsertContainer inserts a new container of kind at index pos and returns
// a handle on it. The container is named by the insert op's id.
func (l *MovableList) InsertContainer(pos int, kind ContainerKind) (Container, error) {
	d := l.h.d
	d.mu.Lock()
	defer d.mu.Unlock()
	if !editableKind(kind) {
		return nil, fmt.Errorf("loro: no container kind %d", kind)
	}
	id, err := l.insert(pos, kind)
	if err != nil {
		return nil, err
	}
	return d.placed(id, kind)
}

func (l *MovableList) insert(pos int, v any) (ID, error) {
	d := l.h.d
	if err := d.editable(l.h.k); err != nil {
		return ID{}, err
	}
	st := l.h.state()
	s := st.seqs[l.h.k]
	slots := movSlots(s)
	if pos < 0 || pos > len(slots) {
		return ID{}, outOfRange(pos, 0, len(slots))
	}
	op := l.h.op(change.VKLoroValue)
	op.Pos = opIndex(s, slots, pos, d)
	op.Value = []any{v}
	id := ID{Peer: d.peer, Counter: d.begin().nextCounter}
	return id, d.apply(st, op, opProv{kind: provList})
}

// Delete removes n items from index pos: one delete per item, naming the
// position the item is at and recorded at its op index with the ones before
// it gone (handler.rs:4098); they fold where loro folds deletes.
func (l *MovableList) Delete(pos, n int) error {
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
	s := st.seqs[l.h.k]
	slots := movSlots(s)
	if !inRange(pos, n, len(slots)) {
		return outOfRange(pos, n, len(slots))
	}
	type del struct {
		id ID
		at int64
	}
	var dels []del
	for i := pos; i < pos+n; i++ {
		nd := slots[i].node
		dels = append(dels, del{ID{Peer: nd.peer, Counter: nd.counter}, opIndex(s, slots, i, d) - int64(i-pos)})
	}
	for _, x := range dels {
		op := l.h.op(change.VKDeleteSeq)
		op.Pos = x.at
		op.Value = DeleteSpan{Peer: x.id.Peer, Counter: x.id.Counter, Len: 1}
		if err := d.apply(st, op, opProv{}); err != nil {
			return err
		}
	}
	return nil
}

// Move moves the item at index from so that it ends at index to: one op,
// naming the element by its creator and lamport, from and to recorded as
// op indexes (handler.rs:3815). Moving an item to where it is makes no op.
func (l *MovableList) Move(from, to int) error {
	d := l.h.d
	d.mu.Lock()
	defer d.mu.Unlock()
	if from == to && !d.knocked(ruleMovableMoveInPlace) {
		return nil
	}
	if err := d.editable(l.h.k); err != nil {
		return err
	}
	st := l.h.state()
	s := st.seqs[l.h.k]
	slots := movSlots(s)
	if from < 0 || from >= len(slots) {
		return outOfRange(from, 1, len(slots))
	}
	if to < 0 || to >= len(slots) {
		return outOfRange(to, 1, len(slots))
	}
	op := l.h.op(change.VKListMove)
	op.MoveFrom = opIndex(s, slots, from, d)
	op.Pos = opIndex(s, slots, to, d)
	op.Value = slots[from].node.elem.key
	return d.apply(st, op, opProv{})
}

// Set sets the item at index pos to v: one op naming the element by its
// creator and lamport. Unlike Map.Set, setting the value it has makes an
// op all the same, as loro's does (handler.rs:3987).
func (l *MovableList) Set(pos int, v any) error {
	d := l.h.d
	d.mu.Lock()
	defer d.mu.Unlock()
	val, err := storeValue(v)
	if err != nil {
		return err
	}
	_, err = l.set(pos, val)
	return err
}

// SetContainer sets the item at index pos to a new container of kind and
// returns a handle on it. The container is named by the set op's id.
func (l *MovableList) SetContainer(pos int, kind ContainerKind) (Container, error) {
	d := l.h.d
	d.mu.Lock()
	defer d.mu.Unlock()
	if !editableKind(kind) {
		return nil, fmt.Errorf("loro: no container kind %d", kind)
	}
	id, err := l.set(pos, kind)
	if err != nil {
		return nil, err
	}
	return d.placed(id, kind)
}

func (l *MovableList) set(pos int, v any) (ID, error) {
	d := l.h.d
	if err := d.editable(l.h.k); err != nil {
		return ID{}, err
	}
	st := l.h.state()
	slots := movSlots(st.seqs[l.h.k])
	if pos < 0 || pos >= len(slots) {
		return ID{}, outOfRange(pos, 1, len(slots))
	}
	e := slots[pos].node.elem
	if d.knocked(ruleMovableSetSkipEqual) && valueEqual(e.value, v) {
		return ID{}, nil
	}
	op := l.h.op(change.VKListSet)
	op.Value = ListSetValue{Elem: e.key, Value: v}
	id := ID{Peer: d.peer, Counter: d.begin().nextCounter}
	return id, d.apply(st, op, opProv{})
}
