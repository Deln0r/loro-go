package loro

import (
	"cmp"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/Deln0r/loro-go/encoding/change"
)

// seqWorkLimit bounds the steps the sequence merge may take over one call:
// positions looked up, siblings skipped, ancestry walked, deletes applied. An
// ordinary document needs a small fraction of it. Past it MergeState returns
// an error, so a hostile blob cannot keep it busy indefinitely.
const seqWorkLimit = 1 << 28

// seqVersionCacheLimit bounds the version-vector entries kept for reuse across
// concurrent changes. Past it, versions are recomputed by walking the changes.
const seqVersionCacheLimit = 1 << 22

// seqWork counts the steps of one merge or one local op against limit.
// strict, which only a Document sets, makes a position past what its
// author saw an error rather than something to clamp: a Document holds
// every change's whole causal past, so such a position is one loro refuses
// too ("list diff retains 1000 items but state only has 0",
// state/list_state.rs:585).
type seqWork struct {
	steps, limit int
	strict       bool
	// total counts every step spent, never reset, and movSteps the work
	// of MovableList indexes, for tests that bound them.
	total, movSteps int64
}

// beyondView is the error a strict merge returns for a position past its
// author's view.
func beyondView(op Op, pos, length int64) error {
	return fmt.Errorf("loro: op %d@%d in %s reaches position %d of the %d elements its author saw", op.Counter, op.Peer, keyOf(op), pos, length)
}

func (w *seqWork) spend(n int) error {
	w.steps += n
	w.total += int64(n)
	if w.steps > w.limit {
		return fmt.Errorf("loro: sequence merge exceeded %d steps", w.limit)
	}
	return nil
}

// seqChange is one change as it arrived, with the ops that were new when it
// did: a copy of a change already merged from an overlapping export has none.
type seqChange struct {
	ch         *Change
	start, end int64 // the copy's id range on its author
	fresh      []Op
}

// causalIndex answers what a change's author had seen: the closure of its
// deps, as a version vector (peer -> end of the counters seen, exclusive).
type causalIndex struct {
	byPeer map[uint64][]*seqChange // sorted by start
	maxEnd map[uint64][]int64      // running maximum of end over byPeer
	cache  map[*seqChange]map[uint64]int64
	cached int
	work   *seqWork
	// complete says every change's deps, and its author's own earlier ops,
	// are present in the input, so each author's view can be rebuilt exactly.
	complete bool
}

func newCausalIndex(all []*seqChange, work *seqWork) (*causalIndex, error) {
	ix := &causalIndex{
		byPeer: map[uint64][]*seqChange{},
		maxEnd: map[uint64][]int64{},
		cache:  map[*seqChange]map[uint64]int64{},
		work:   work,
	}
	for _, sc := range all {
		ix.byPeer[sc.ch.ID.Peer] = append(ix.byPeer[sc.ch.ID.Peer], sc)
	}
	for p, cs := range ix.byPeer {
		sort.SliceStable(cs, func(i, j int) bool { return cs[i].start < cs[j].start })
		ends := make([]int64, len(cs))
		var m int64
		for i, c := range cs {
			m = max(m, c.end)
			ends[i] = m
		}
		ix.maxEnd[p] = ends
	}
	// A change's deps must all come before it. loro gives a change a lamport
	// above every op it depends on, so a dep whose own lamport is not below
	// the change's is forward or circular, and nothing honest produces one.
	ix.complete = true
	for _, sc := range all {
		if sc.start > 0 {
			z, err := ix.find(ID{Peer: sc.ch.ID.Peer, Counter: sc.start - 1})
			if err != nil {
				return nil, err
			}
			if z == nil {
				ix.complete = false
			}
		}
		for _, d := range sc.ch.Deps {
			z, err := ix.find(d)
			if err != nil {
				return nil, err
			}
			if z == nil {
				ix.complete = false
				continue
			}
			if l := z.ch.Lamport + (d.Counter - z.start); l >= sc.ch.Lamport {
				return nil, fmt.Errorf("loro: change %d@%d (lamport %d) depends on %d@%d (lamport %d)",
					sc.ch.ID.Counter, sc.ch.ID.Peer, sc.ch.Lamport, d.Counter, d.Peer, l)
			}
		}
	}
	return ix, nil
}

// find returns a change holding id, or nil when the input has none. Copies of
// one change from overlapping exports overlap each other, so it may have to
// step back over several; each step is counted.
func (ix *causalIndex) find(id ID) (*seqChange, error) {
	cs, ends := ix.byPeer[id.Peer], ix.maxEnd[id.Peer]
	i := sort.Search(len(cs), func(i int) bool { return cs[i].start > id.Counter })
	for j := i - 1; j >= 0 && ends[j] > id.Counter; j-- {
		if err := ix.work.spend(1); err != nil {
			return nil, err
		}
		if id.Counter < cs[j].end {
			return cs[j], nil
		}
	}
	return nil, nil
}

// versionOf returns what sc's author had seen when it started sc: the closure
// of its deps and of its own earlier changes. A version computed for one
// change of a concurrent branch is kept, so the next change of that branch
// stops its walk there instead of walking the whole history again.
func (ix *causalIndex) versionOf(sc *seqChange) (map[uint64]int64, error) {
	if v, ok := ix.cache[sc]; ok {
		return v, nil
	}
	v := map[uint64]int64{}
	seen := map[*seqChange]bool{}
	stack := append([]ID(nil), sc.ch.Deps...)
	if sc.start > 0 {
		stack = append(stack, ID{Peer: sc.ch.ID.Peer, Counter: sc.start - 1})
	}
	for len(stack) > 0 {
		if err := ix.work.spend(1); err != nil {
			return nil, err
		}
		id := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if id.Counter+1 > v[id.Peer] {
			v[id.Peer] = id.Counter + 1
		}
		z, err := ix.find(id)
		if err != nil {
			return nil, err
		}
		if z == nil || seen[z] {
			continue
		}
		seen[z] = true
		if zv, ok := ix.cache[z]; ok {
			if err := ix.work.spend(len(zv)); err != nil {
				return nil, err
			}
			for p, end := range zv {
				if end > v[p] {
					v[p] = end
				}
			}
			continue
		}
		stack = append(stack, z.ch.Deps...)
		if z.start > 0 {
			stack = append(stack, ID{Peer: z.ch.ID.Peer, Counter: z.start - 1})
		}
	}
	if ix.cached+len(v) <= seqVersionCacheLimit {
		ix.cache[sc] = v
		ix.cached += len(v)
	}
	return v, nil
}

// pastView is what one op's author had seen: its change's version plus the
// author's own earlier atoms.
type pastView struct {
	vv   map[uint64]int64
	peer uint64
	own  int64
}

func (p pastView) has(peer uint64, counter int64) bool {
	return (peer == p.peer && counter < p.own) || counter < p.vv[peer]
}

// seqNode is one element of a Text, List or MovableList sequence: a character,
// a list item, or one of the two anchors a rich-text mark adds. Its origins are
// the elements its author saw on either side of it: left, the element just
// before the insertion point in the author's view, and right, the next element
// after that point the author had seen, deleted or not. Concurrent inserts are
// ordered from these, as loro orders them.
type seqNode struct {
	peer     uint64
	counter  int64
	left     *seqNode
	right    *seqNode
	value    any
	anchor   bool
	deleters []ID     // the delete atoms that removed it
	elem     *movElem // MovableList: the element this position holds
}

// movElem is a MovableList element. loro keeps a MovableList as two layers: a
// sequence of positions, merged like any list, and elements, each created by
// an insert. A move adds a new position for its element and deletes the one it
// moved from; the element sits at whichever of its positions has the highest
// writer, and shows the value with the highest writer among its insert and
// sets. A writer is (lamport, peer), compared in that order.
//
// valueID is the id of the atom that wrote the value shown: the insert's atom
// for the element, or the set, which valueSet records. A container placed in
// the list takes that id as its own, so a set that puts a new container in an
// element names it by the set's id, not by the element's.
type movElem struct {
	key         ListElem // the element's name: its creator and lamport
	value       any
	valueWriter writer
	valueID     ID
	valueSet    bool
	pos         *seqNode
	posWriter   writer
}

type writer struct {
	lamport int64
	peer    uint64
}

func (a writer) after(b writer) bool {
	if a.lamport != b.lamport {
		return a.lamport > b.lamport
	}
	return a.peer > b.peer
}

// seqState is one container's merged sequence in document order, including
// elements deletes removed.
type seqState struct {
	nodes      []*seqNode
	byID       map[ID]*seqNode
	byPeer     map[uint64][]*seqNode
	live       int          // nodes no merged delete removed, anchors included
	pendingEnd map[ID]int64 // mark-end op -> where its anchor goes
	work       *seqWork
	cursor     seqCursor
	elems      map[ListElem]*movElem // MovableList elements by their creating op
	movIx      *movIndex             // a MovableList's user view, once asked for
}

// seqCursor remembers where the fast path last found an element: before is
// how many elements no delete removed lie ahead of index idx. Edits cluster,
// so the next position asked for is usually near the last one, and walking
// from here crosses fewer removed elements than walking from either end. It is
// kept exact on every insert and delete, and dropped when a delete's indexes
// are not known.
type seqCursor struct {
	ok     bool
	idx    int
	before int64
}

func newSeqState(work *seqWork) *seqState {
	return &seqState{byID: map[ID]*seqNode{}, byPeer: map[uint64][]*seqNode{}, pendingEnd: map[ID]int64{}, work: work, elems: map[ListElem]*movElem{}}
}

func isSeqOp(op Op) bool {
	switch op.Kind {
	case change.CText:
		switch op.VKind {
		case change.VKStr, change.VKMarkStart, change.VKNull, change.VKDeleteSeq:
			return true
		}
	case change.CList:
		return op.VKind == change.VKLoroValue || op.VKind == change.VKDeleteSeq
	case change.CMovableList:
		switch op.VKind {
		case change.VKLoroValue, change.VKDeleteSeq, change.VKListMove, change.VKListSet:
			return true
		}
	}
	return false
}

// apply merges one op. all says the op's author had seen every op merged so
// far and nothing else; otherwise past says what it had seen. byPosition says
// the author's view can be rebuilt exactly, so a delete is applied where its
// position says, as loro applies it, rather than by the ids it recorded.
func (s *seqState) apply(op Op, all bool, past pastView, byPosition bool) error {
	switch op.VKind {
	case change.VKDeleteSeq:
		if byPosition && op.Kind != change.CMovableList {
			if done, err := s.removeAt(op, all, past); err != nil || done {
				return err
			}
		}
		return s.remove(op)
	case change.VKMarkStart:
		// A mark adds a start anchor at Start and an end anchor that its
		// mark-end op places, at Start+Len+1: the end of the span, counted
		// after the start anchor has gone in.
		mi, ok := op.Value.(MarkInfo)
		var start any
		if ok {
			start = markStart{info: mi, w: writer{lamport: op.Lamport, peer: op.Peer}}
		}
		if _, err := s.insert(op, op.Pos, []any{start}, true, all, past); err != nil {
			return err
		}
		if ok {
			s.pendingEnd[ID{Peer: op.Peer, Counter: op.Counter + 1}] = mi.Start + mi.Len + 1
		}
		return nil
	case change.VKNull:
		id := ID{Peer: op.Peer, Counter: op.Counter}
		at, ok := s.pendingEnd[id]
		if !ok {
			return nil
		}
		delete(s.pendingEnd, id)
		_, err := s.insert(op, at, []any{markEnd{}}, true, all, past)
		return err
	case change.VKStr:
		str, _ := op.Value.(string)
		var items []any
		for _, r := range str {
			items = append(items, string(r))
		}
		_, err := s.insert(op, op.Pos, items, false, all, past)
		return err
	case change.VKListMove:
		return s.move(op, all, past)
	case change.VKListSet:
		if set, ok := op.Value.(ListSetValue); ok {
			if e := s.elems[set.Elem]; e != nil {
				if w := (writer{op.Lamport, op.Peer}); w.after(e.valueWriter) {
					e.value, e.valueWriter = set.Value, w
					e.valueID, e.valueSet = ID{Peer: op.Peer, Counter: op.Counter}, true
				}
			}
		}
		return nil
	default:
		items, _ := op.Value.([]any)
		run, err := s.insert(op, op.Pos, items, false, all, past)
		if err != nil || op.Kind != change.CMovableList {
			return err
		}
		for k, n := range run {
			w := writer{op.Lamport + int64(k), op.Peer}
			key := ListElem{Peer: op.Peer, Lamport: w.lamport}
			e := &movElem{key: key, value: items[k], valueWriter: w, valueID: ID{Peer: op.Peer, Counter: n.counter}, pos: n, posWriter: w}
			n.elem, n.value = e, nil
			s.elems[key] = e
		}
		return nil
	}
}

// move applies a MovableList move the way loro does: the position at index
// MoveFrom of its author's view is deleted by the move, a new position for the
// element goes in at index Pos of that view with the old one gone, and the
// element takes the new position if the move is its latest writer. A move of an
// element or from an index the input does not have is skipped.
func (s *seqState) move(op Op, all bool, past pastView) error {
	le, ok := op.Value.(ListElem)
	if !ok {
		return nil
	}
	e := s.elems[le]
	if e == nil {
		return nil
	}
	src, err := s.nthVisible(op.MoveFrom, all, past)
	if err != nil {
		return err
	}
	if src < 0 {
		if s.work.strict {
			return beyondView(op, op.MoveFrom+1, int64(s.live))
		}
		return nil
	}
	if n := s.nodes[src]; len(n.deleters) == 0 {
		s.live--
		if s.cursor.ok && src < s.cursor.idx {
			s.cursor.before--
		}
	}
	s.nodes[src].deleters = append(s.nodes[src].deleters, ID{Peer: op.Peer, Counter: op.Counter})
	// The destination counts the author's view after its own delete.
	after := past
	after.own = op.Counter + 1
	run, err := s.insert(op, op.Pos, []any{nil}, false, all, after)
	if err != nil || len(run) == 0 {
		return err
	}
	n := run[0]
	n.elem = e
	if w := (writer{op.Lamport, op.Peer}); w.after(e.posWriter) {
		e.pos, e.posWriter = n, w
	}
	return nil
}

// nthVisible returns the index of the element at position i (from 0) of the
// op's author's view, or -1 if the view is shorter.
func (s *seqState) nthVisible(i int64, all bool, past pastView) (int, error) {
	if i < 0 {
		return -1, nil
	}
	if all {
		if i >= int64(s.live) {
			return -1, nil
		}
		return s.locate(i+1, true, past)
	}
	seen, steps := int64(0), 0
	for j, n := range s.nodes {
		steps += 1 + len(n.deleters)
		if !past.has(n.peer, n.counter) || removedIn(n, past) {
			continue
		}
		if seen == i {
			return j, s.work.spend(steps)
		}
		seen++
	}
	return -1, s.work.spend(steps)
}

// viewError is a position past the end of its author's view, which a
// strict merge refuses.
type viewError struct{ pos, length int64 }

func (e *viewError) Error() string {
	return fmt.Sprintf("loro: position %d is past the %d elements its author saw", e.pos, e.length)
}

// locate returns the index of the pos-th element (counting from 1) the op's
// author could see. A position past the end of that view, which only an input
// missing part of its history produces, takes the last element, as loro.js
// does, or is a *viewError in a strict merge; -1 means the view is empty.
func (s *seqState) locate(pos int64, all bool, past pastView) (int, error) {
	if all {
		// Everything merged so far is in view, minus what deletes removed.
		// Start from the front, the back or the cursor, whichever is nearest
		// in such elements: typing at the end, or trimming the front of a
		// text that is all removed elements there, costs nothing.
		if s.work.strict && pos > int64(s.live) {
			return -1, &viewError{pos: pos, length: int64(s.live)}
		}
		if s.live == 0 {
			return -1, nil
		}
		want := min(pos, int64(s.live)) - 1
		idx, before, dist := 0, int64(0), want
		if back := int64(s.live) - 1 - want; back < dist {
			idx, before, dist = len(s.nodes), int64(s.live), back
		}
		if c := s.cursor; c.ok && abs64(want-c.before) <= dist {
			idx, before = c.idx, c.before
		}
		i, steps := s.walkLive(idx, before, want)
		s.cursor = seqCursor{ok: true, idx: i, before: want}
		return i, s.work.spend(steps)
	}
	seen, steps, last := int64(0), 0, -1
	for i, n := range s.nodes {
		steps += 1 + len(n.deleters)
		if !past.has(n.peer, n.counter) || removedIn(n, past) {
			continue
		}
		last = i
		if seen++; seen == pos {
			return i, s.work.spend(steps)
		}
	}
	if s.work.strict {
		return -1, &viewError{pos: pos, length: seen}
	}
	return last, s.work.spend(steps)
}

// walkLive returns the index of the element no delete removed at position
// want (from 0), walking from index idx, ahead of which lie before such
// elements, and the steps taken. want must be a position that exists.
func (s *seqState) walkLive(idx int, before, want int64) (int, int) {
	steps := 0
	if want >= before {
		for i := idx; i < len(s.nodes); i++ {
			steps++
			if len(s.nodes[i].deleters) == 0 {
				if before == want {
					return i, steps
				}
				before++
			}
		}
		return len(s.nodes) - 1, steps
	}
	for i := idx - 1; i >= 0; i-- {
		steps++
		if len(s.nodes[i].deleters) == 0 {
			before--
			if before == want {
				return i, steps
			}
		}
	}
	return 0, steps
}

func abs64(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}

func removedIn(n *seqNode, past pastView) bool {
	for _, d := range n.deleters {
		if past.has(d.Peer, d.Counter) {
			return true
		}
	}
	return false
}

func (s *seqState) insert(op Op, pos int64, items []any, anchor, all bool, past pastView) ([]*seqNode, error) {
	if len(items) == 0 {
		return nil, nil
	}
	left := -1
	if pos > 0 {
		i, err := s.locate(pos, all, past)
		if ve, ok := err.(*viewError); ok {
			return nil, beyondView(op, ve.pos, ve.length)
		}
		if err != nil {
			return nil, err
		}
		left = i
	}
	// The right origin is the first element after the left origin that the
	// author had seen, deleted or not. Everything between the two is
	// concurrent with this insert; an author who saw everything merged has
	// nothing there.
	start, end := left+1, left+1
	if !all {
		for end < len(s.nodes) && !past.has(s.nodes[end].peer, s.nodes[end].counter) {
			end++
		}
	}
	var leftNode, rightNode *seqNode
	if left >= 0 {
		leftNode = s.nodes[left]
	}
	if end < len(s.nodes) {
		rightNode = s.nodes[end]
	}
	at, steps := s.integrate(start, end, leftNode, rightNode, op.Peer)
	// slices.Insert shifts every node after at. It is a memmove, so it costs a
	// step per 64 nodes moved rather than per node, but it is counted: a
	// history of moves to the front of a long list moves the whole history on
	// every op.
	if err := s.work.spend(steps + end - start + len(items) + (len(s.nodes)-at)/64); err != nil {
		return nil, err
	}
	run := make([]*seqNode, len(items))
	for k, v := range items {
		n := &seqNode{peer: op.Peer, counter: op.Counter + int64(k), left: leftNode, right: rightNode, value: v, anchor: anchor}
		if k > 0 {
			n.left = run[k-1] // each atom's left origin is the one before it
		}
		run[k] = n
	}
	s.nodes = slices.Insert(s.nodes, at, run...)
	if s.cursor.ok && at <= s.cursor.idx {
		s.cursor.idx += len(run)
		s.cursor.before += int64(len(run))
	}
	for _, n := range run {
		id := ID{Peer: n.peer, Counter: n.counter}
		if _, dup := s.byID[id]; !dup {
			s.byID[id] = n
			s.byPeer[n.peer] = append(s.byPeer[n.peer], n)
		}
	}
	s.live += len(run)
	return run, nil
}

// integrate places a run whose origins are left and right among the elements
// between them, nodes[start:end], all concurrent with it, and returns the
// index it goes at with the steps spent. The rule is loro's (Fugue, by
// YATA-style scanning; the same branches as loro.js's scannedFugueInsertion):
// walk the elements that share the run's left origin, together with their
// descendants; between two with the same right origin the lower peer goes
// first; otherwise the one whose right origin is a nearer direct right child of
// the shared left origin goes first.
func (s *seqState) integrate(start, end int, left, right *seqNode, peer uint64) (int, int) {
	if start == end {
		return start, 0
	}
	steps := 0
	// rightParent is the index of r when r is a direct right child of the
	// run's left origin, that is when r's own left origin is that same
	// element. ok is false otherwise, and false sorts after every index. A
	// right origin always lies after the element naming it, so the search
	// starts at start, and it goes only as far as the furthest origin asked
	// for, so a large document behind the insertion point costs nothing.
	index := map[*seqNode]int{}
	indexed := start
	rightParent := func(r *seqNode) (int, bool) {
		if r == nil || r.left != left {
			return 0, false
		}
		if i, ok := index[r]; ok {
			return i, true
		}
		for ; indexed < len(s.nodes); indexed++ {
			steps++
			index[s.nodes[indexed]] = indexed
			if s.nodes[indexed] == r {
				indexed++
				return indexed - 1, true
			}
		}
		return 0, false
	}
	ours, oursOK := rightParent(right)
	at, scanning := start, false
	visited := map[*seqNode]bool{}
scan:
	for i := start; i < end; i++ {
		steps++
		other := s.nodes[i]
		if other.left != left && (other.left == nil || !visited[other.left]) {
			break
		}
		visited[other] = true
		if other.left == left {
			if other.right == right {
				if other.peer > peer {
					break
				}
				scanning = false
			} else {
				theirs, theirsOK := rightParent(other.right)
				switch c := comparePositions(theirs, theirsOK, ours, oursOK); {
				case c < 0:
					scanning = true
				case c == 0 && other.peer > peer:
					break scan
				default:
					scanning = false
				}
			}
		}
		if !scanning {
			at = i + 1
		}
	}
	return at, steps
}

// comparePositions orders two optional indexes, an absent one after any index.
func comparePositions(a int, aok bool, b int, bok bool) int {
	switch {
	case aok && bok:
		return a - b
	case aok:
		return -1
	case bok:
		return 1
	}
	return 0
}

// removeAt applies a Text or List delete by position, the way loro's import
// does: the |Len| elements of the author's view starting at the delete's lowest
// position. A backwards delete is recorded at the last element it removed and
// removed the highest first, so its atom k takes the k-th from the top. Atom
// ids are recorded as remove records them. This is what makes a delete that
// loro-crdt's WASM build up to 1.16.3 wrote with wrong ids
// (loro-dev/loro#1149) remove the characters loro removes. It reports false,
// changing nothing, when the view is too short for the position, which only an
// input missing part of its history produces; the caller then reads the ids.
// A strict merge refuses such a delete instead.
func (s *seqState) removeAt(op Op, all bool, past pastView) (bool, error) {
	span, ok := op.Value.(DeleteSpan)
	if !ok {
		return false, nil
	}
	_, n := span.Normalize()
	low := op.Pos
	if span.Len < 0 {
		low = op.Pos - n + 1
	}
	if n == 0 || low < 0 || (all && low+n > int64(s.live)) {
		if s.work.strict {
			return false, beyondView(op, low+n, int64(s.live))
		}
		return false, nil
	}
	// Walk the view from its low-th element, collecting n.
	var targets []*seqNode
	var at []int // index of each target
	seen, steps := int64(0), 0
	visible := func(nd *seqNode) bool {
		if all {
			return len(nd.deleters) == 0
		}
		return past.has(nd.peer, nd.counter) && !removedIn(nd, past)
	}
	from := 0
	if all {
		// Find the first target through locate, which can start from the
		// cursor, rather than walking from the front across every element
		// removed there.
		i, err := s.locate(low+1, true, past)
		if err != nil {
			return false, err
		}
		from, seen = i, low
	}
	for i := from; i < len(s.nodes) && int64(len(targets)) < n; i++ {
		nd := s.nodes[i]
		steps += 1 + len(nd.deleters)
		if !visible(nd) {
			continue
		}
		if seen >= low {
			targets = append(targets, nd)
			at = append(at, i)
		}
		seen++
	}
	if err := s.work.spend(steps); err != nil {
		return false, err
	}
	if int64(len(targets)) < n {
		if s.work.strict {
			return false, beyondView(op, low+n, low+int64(len(targets)))
		}
		return false, nil
	}
	for k, nd := range targets {
		atom := int64(k)
		if span.Len < 0 {
			atom = n - 1 - int64(k)
		}
		if len(nd.deleters) == 0 {
			s.live--
			if s.cursor.ok && at[k] < s.cursor.idx {
				s.cursor.before--
			}
		}
		nd.deleters = append(nd.deleters, ID{Peer: op.Peer, Counter: op.Counter + atom})
	}
	return true, nil
}

// remove applies a delete: each of its atoms removes one element of its span.
// The atom is recorded on the element, so a later insert whose author had
// seen only some of a delete's atoms sees exactly those elements gone.
func (s *seqState) remove(op Op) error {
	span, ok := op.Value.(DeleteSpan)
	if !ok {
		return nil
	}
	mark := func(n *seqNode) {
		k, ok := span.atomFor(n.counter)
		if !ok {
			return
		}
		// No check for an atom already recorded: one recorded twice changes
		// neither visibility nor what any author saw, and scanning for it
		// would make many deletes of one element quadratic.
		if len(n.deleters) == 0 {
			s.live--
			s.cursor.ok = false // the element's index is not known here
		}
		n.deleters = append(n.deleters, ID{Peer: op.Peer, Counter: op.Counter + k})
	}
	// Visit whichever is smaller: the span's ids or the author's elements.
	// A span's declared length is attacker-controlled; the elements are not.
	start, n := span.Normalize()
	peerNodes := s.byPeer[span.Peer]
	if n <= int64(len(peerNodes)) {
		for c := start; c < start+n; c++ {
			if nd := s.byID[ID{Peer: span.Peer, Counter: c}]; nd != nil {
				mark(nd)
			}
		}
		return s.work.spend(int(n) + 1)
	}
	for _, nd := range peerNodes {
		mark(nd)
	}
	return s.work.spend(len(peerNodes) + 1)
}

// text renders a Text container, each a List or MovableList: every element no
// delete removed, anchors left out.
func (s *seqState) text() string {
	var sb strings.Builder
	for _, n := range s.nodes {
		if len(n.deleters) == 0 && !n.anchor {
			str, _ := n.value.(string)
			sb.WriteString(str)
		}
	}
	return sb.String()
}

// each calls f with the value of every list item no delete removed, in order,
// and the id of the atom that wrote it: the item's own id in a List, and in a
// MovableList the insert or set whose value the element shows. A container
// placed in the list is named by that id. whole says the value is a set's
// whole value rather than one item of an insert.
func (s *seqState) each(f func(v any, id ID, whole bool)) {
	for _, n := range s.nodes {
		if len(n.deleters) != 0 || n.anchor {
			continue
		}
		if n.elem == nil {
			f(n.value, ID{Peer: n.peer, Counter: n.counter}, false)
			continue
		}
		// A MovableList element shows at its current position only; its other
		// positions are moved-from or lost a concurrent move.
		if n.elem.pos == n {
			f(n.elem.value, n.elem.valueID, n.elem.valueSet)
		}
	}
}

// mergeSequences replays every Text, List and MovableList op in (lamport,
// peer, counter) order, a contiguous run of a change's fresh ops at a time,
// and returns each container's merged sequence.
//
// An insert records a position in the sequence as its author saw it, so each
// one is resolved against that view: the elements in its causal past, minus
// those a delete in that past removed, with mark anchors taking positions.
// Most ops are made with everything merged so far in view (one peer typing, or
// a peer editing after a sync), and the frontier check below recognises that
// in constant time; then the view is the whole merged sequence and typing at
// the end is a lookup at the end. Only an op made concurrently with something
// already merged needs its causal past from the change DAG and a scan.
func mergeSequences(all, units []*seqChange, work *seqWork) (map[contKey]*seqState, error) {
	ix, err := newCausalIndex(all, work)
	if err != nil {
		return nil, err
	}
	// Replay contiguous runs of fresh ops, not whole changes. A change that
	// arrived in two overlapping exports can keep fresh ops on both sides of a
	// stretch another copy supplied: a full export's "ab" and "ef" around a
	// delta's "cd". "ef" was typed after "cd" and must wait for it, so each
	// run takes its own place in (lamport, peer, counter) order.
	type run struct {
		src *seqChange
		ops []Op
	}
	var runs []run
	for _, u := range units {
		from := 0
		for i := 1; i <= len(u.fresh); i++ {
			if i == len(u.fresh) || u.fresh[i].Counter != u.fresh[i-1].Counter+atomCount(u.fresh[i-1]) {
				runs = append(runs, run{src: u, ops: u.fresh[from:i]})
				from = i
			}
		}
	}
	sort.SliceStable(runs, func(i, j int) bool {
		a, b := runs[i].ops[0], runs[j].ops[0]
		if a.Lamport != b.Lamport {
			return a.Lamport < b.Lamport
		}
		if a.Peer != b.Peer {
			return a.Peer < b.Peer
		}
		return a.Counter < b.Counter
	})
	seqs := map[contKey]*seqState{}
	// frontier is the set of maximal ops merged so far. An op whose deps are
	// exactly this set was made with everything merged in view.
	var frontier []ID
	for _, r := range runs {
		u := r.src
		var vv map[uint64]int64
		for _, op := range r.ops {
			deps := u.ch.Deps
			if op.Counter != u.start {
				deps = []ID{{Peer: op.Peer, Counter: op.Counter - 1}}
			}
			if err := work.spend(len(deps) + len(frontier)); err != nil {
				return nil, err
			}
			inView := sameIDs(deps, frontier)
			var past pastView
			if !inView {
				if vv == nil {
					if vv, err = ix.versionOf(u); err != nil {
						return nil, err
					}
				}
				past = pastView{vv: vv, peer: op.Peer, own: op.Counter}
			}
			if isSeqOp(op) {
				key := keyOf(op)
				s := seqs[key]
				if s == nil {
					s = newSeqState(work)
					seqs[key] = s
				}
				if err := s.apply(op, inView, past, ix.complete); err != nil {
					return nil, err
				}
			}
			last := ID{Peer: op.Peer, Counter: op.Counter + atomCount(op) - 1}
			if inView {
				frontier = append(frontier[:0], last)
				continue
			}
			keep := frontier[:0]
			for _, f := range frontier {
				if !past.has(f.Peer, f.Counter) && (f.Peer != op.Peer || f.Counter > last.Counter) {
					keep = append(keep, f)
				}
			}
			frontier = append(keep, last)
		}
	}
	return seqs, nil
}

// sameIDs reports whether a and b hold the same ids, ignoring order and
// repeats. Both are usually one or two ids long.
func sameIDs(a, b []ID) bool {
	if len(a)+len(b) <= 16 {
		for _, x := range a {
			if !slices.Contains(b, x) {
				return false
			}
		}
		for _, x := range b {
			if !slices.Contains(a, x) {
				return false
			}
		}
		return true
	}
	set := func(s []ID) []ID {
		out := slices.Clone(s)
		slices.SortFunc(out, func(x, y ID) int {
			if c := cmp.Compare(x.Peer, y.Peer); c != 0 {
				return c
			}
			return cmp.Compare(x.Counter, y.Counter)
		})
		return slices.Compact(out)
	}
	return slices.Equal(set(a), set(b))
}
