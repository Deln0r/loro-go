package loro

import (
	"fmt"
	"slices"
	"sort"

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

// movIndex is a MovableList's user view of its merged sequence: the live
// positions that hold their element, in order (slots), and, for each live
// position whose element went elsewhere in a concurrent move (one loro
// keeps, state/movable_list_state.rs), how many slots come before it
// (lost, ascending). A user index is an index into slots; an op index,
// which loro's ops record (convert_index, movable_list_state.rs:857),
// counts the lost positions too. Local ops never lose a position, so lost
// stays as an import left it and is usually empty. The index is built
// once per state and kept current by each local op, so a call costs a
// binary search among the lost positions and copies of the slot slice,
// not a scan of every position ever made.
type movIndex struct {
	slots []*seqNode
	lost  []int
}

// movIndexCheck makes every update of a movIndex compare itself with one
// built anew, for tests.
var movIndexCheck bool

// mov returns s's movIndex, building it when s has none.
func (s *seqState) mov() *movIndex {
	if s == nil {
		return &movIndex{}
	}
	if s.movIx == nil {
		s.movIx = buildMovIndex(s)
	}
	return s.movIx
}

func buildMovIndex(s *seqState) *movIndex {
	ix := &movIndex{}
	for _, n := range s.nodes {
		s.work.movSteps++
		if len(n.deleters) != 0 {
			continue
		}
		if n.elem != nil && n.elem.pos == n {
			ix.slots = append(ix.slots, n)
		} else {
			ix.lost = append(ix.lost, len(ix.slots))
		}
	}
	return ix
}

// lostBefore is how many lost positions lie before op index o: the t-th
// lost position, of rank r, has op index r + t.
func (ix *movIndex) lostBefore(o int64) int {
	return sort.Search(len(ix.lost), func(t int) bool { return int64(ix.lost[t]+t) >= o })
}

// opIndex is the op index of user index i, the end of the list for i past
// the last slot.
func (ix *movIndex) opIndex(i int, d *Document) int64 {
	if i >= len(ix.slots) {
		return int64(len(ix.slots) + len(ix.lost))
	}
	if d.knocked(ruleMovableOpIndex) {
		return int64(i)
	}
	return int64(i + sort.Search(len(ix.lost), func(t int) bool { return ix.lost[t] > i }))
}

// noteLocal brings the index up to date with local op op, which s has just
// applied: an insert adds slots at its op index, a delete removes the slot
// at its op index, a move does both. The lost positions after the change
// shift by the slots it added or removed.
func (s *seqState) noteLocal(op Op) {
	ix := s.movIx
	if ix == nil {
		return
	}
	insert := func(o int64, nodes []*seqNode) {
		t := ix.lostBefore(o)
		j := int(o) - t
		ix.slots = slices.Insert(ix.slots, j, nodes...)
		for ; t < len(ix.lost); t++ {
			ix.lost[t] += len(nodes)
			s.work.movSteps++
		}
	}
	// remove takes out the slot at op index o, which must satisfy is.
	remove := func(o int64, is func(*seqNode) bool) bool {
		t := ix.lostBefore(o)
		j := int(o) - t
		if t < len(ix.lost) && int64(ix.lost[t]+t) == o || j < 0 || j >= len(ix.slots) || !is(ix.slots[j]) {
			return false
		}
		ix.slots = slices.Delete(ix.slots, j, j+1)
		for ; t < len(ix.lost); t++ {
			ix.lost[t]--
			s.work.movSteps++
		}
		return true
	}
	ok := true
	switch op.VKind {
	case change.VKLoroValue:
		items, _ := op.Value.([]any)
		nodes := make([]*seqNode, len(items))
		for k := range items {
			nodes[k] = s.byID[ID{Peer: op.Peer, Counter: op.Counter + int64(k)}]
			ok = ok && nodes[k] != nil
		}
		if ok {
			insert(op.Pos, nodes)
		}
	case change.VKDeleteSeq:
		span, isSpan := op.Value.(DeleteSpan)
		ok = isSpan && span.Len == 1 && remove(op.Pos, func(n *seqNode) bool { return n.peer == span.Peer && n.counter == span.Counter })
	case change.VKListMove:
		// The old position is the slot at MoveFrom: the element's, now
		// deleted by the move.
		le, _ := op.Value.(ListElem)
		e := s.elems[le]
		ok = e != nil && e.pos != nil && remove(op.MoveFrom, func(n *seqNode) bool { return n.elem == e && len(n.deleters) != 0 })
		if ok {
			insert(op.Pos, []*seqNode{e.pos})
		}
	}
	if !ok {
		// Not a change this index follows: build it anew when next asked.
		s.movIx = nil
		return
	}
	if movIndexCheck {
		fresh := buildMovIndex(s)
		if !slices.Equal(fresh.slots, ix.slots) || !slices.Equal(fresh.lost, ix.lost) {
			panic(fmt.Sprintf("loro: MovableList index after %v@%d: kept %d slots %v lost, built %d slots %v lost",
				op.VKind, op.Counter, len(ix.slots), ix.lost, len(fresh.slots), fresh.lost))
		}
	}
}

// Len returns the number of items.
func (l *MovableList) Len() int {
	l.h.d.mu.Lock()
	defer l.h.d.mu.Unlock()
	return len(l.h.state().seqs[l.h.k].mov().slots)
}

// Get returns the item at index i. A container stored there is returned as
// its id, "cid:counter@peer:Kind".
func (l *MovableList) Get(i int) (any, error) {
	l.h.d.mu.Lock()
	defer l.h.d.mu.Unlock()
	slots := l.h.state().seqs[l.h.k].mov().slots
	if i < 0 || i >= len(slots) {
		return nil, outOfRange(i, 1, len(slots))
	}
	e := slots[i].elem
	return copyValue(plainValue(e.value, e.valueID, e.valueSet)), nil
}

// GetContainer returns the container stored at index i, false when the
// item there is no container.
func (l *MovableList) GetContainer(i int) (Container, bool) {
	d := l.h.d
	d.mu.Lock()
	defer d.mu.Unlock()
	slots := l.h.state().seqs[l.h.k].mov().slots
	if i < 0 || i >= len(slots) {
		return nil, false
	}
	e := slots[i].elem
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
	ix := s.mov()
	if pos < 0 || pos > len(ix.slots) {
		return ID{}, outOfRange(pos, 0, len(ix.slots))
	}
	op := l.h.op(change.VKLoroValue)
	op.Pos = ix.opIndex(pos, d)
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
	ix := s.mov()
	if !inRange(pos, n, len(ix.slots)) {
		return outOfRange(pos, n, len(ix.slots))
	}
	type del struct {
		id ID
		at int64
	}
	var dels []del
	for i := pos; i < pos+n; i++ {
		nd := ix.slots[i]
		dels = append(dels, del{ID{Peer: nd.peer, Counter: nd.counter}, ix.opIndex(i, d) - int64(i-pos)})
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
	ix := s.mov()
	if from < 0 || from >= len(ix.slots) {
		return outOfRange(from, 1, len(ix.slots))
	}
	if to < 0 || to >= len(ix.slots) {
		return outOfRange(to, 1, len(ix.slots))
	}
	op := l.h.op(change.VKListMove)
	op.MoveFrom = ix.opIndex(from, d)
	op.Pos = ix.opIndex(to, d)
	op.Value = ix.slots[from].elem.key
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
	slots := st.seqs[l.h.k].mov().slots
	if pos < 0 || pos >= len(slots) {
		return ID{}, outOfRange(pos, 1, len(slots))
	}
	e := slots[pos].elem
	if d.knocked(ruleMovableSetSkipEqual) && valueEqual(e.value, v) {
		return ID{}, nil
	}
	op := l.h.op(change.VKListSet)
	op.Value = ListSetValue{Elem: e.key, Value: v}
	id := ID{Peer: d.peer, Counter: d.begin().nextCounter}
	return id, d.apply(st, op, opProv{})
}
