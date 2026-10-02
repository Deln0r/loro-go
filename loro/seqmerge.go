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

type seqWork struct{ steps, limit int }

func (w *seqWork) spend(n int) error {
	w.steps += n
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
	for _, sc := range all {
		for _, d := range sc.ch.Deps {
			z, err := ix.find(d)
			if err != nil {
				return nil, err
			}
			if z == nil {
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
// a list item, or one of the two anchors a rich-text mark adds.
type seqNode struct {
	peer     uint64
	counter  int64
	lamport  int64
	depth    int // in the left-origin tree; children of the root are 1
	value    any
	anchor   bool
	deleters []ID // the delete atoms that removed it
}

// before orders two inserts that share a left origin. RGA puts the causally
// later one first, so lamport descends; equal lamports mean the two were
// concurrent, and those go by ascending peer.
func (a *seqNode) before(b *seqNode) bool {
	if a.lamport != b.lamport {
		return a.lamport > b.lamport
	}
	if a.peer != b.peer {
		return a.peer < b.peer
	}
	return a.counter > b.counter
}

// seqState is one container's merged sequence, held in pre-order of the
// left-origin tree, which is the document order.
type seqState struct {
	nodes      []*seqNode
	byID       map[ID]*seqNode
	byPeer     map[uint64][]*seqNode
	live       int          // nodes no merged delete removed, anchors included
	pendingEnd map[ID]int64 // mark-end op -> where its anchor goes
	work       *seqWork
}

func newSeqState(work *seqWork) *seqState {
	return &seqState{byID: map[ID]*seqNode{}, byPeer: map[uint64][]*seqNode{}, pendingEnd: map[ID]int64{}, work: work}
}

func isSeqOp(op Op) bool {
	switch op.Kind {
	case change.CText:
		switch op.VKind {
		case change.VKStr, change.VKMarkStart, change.VKNull, change.VKDeleteSeq:
			return true
		}
	case change.CList, change.CMovableList:
		return op.VKind == change.VKLoroValue || op.VKind == change.VKDeleteSeq
	}
	return false
}

// apply merges one op. all says the op's author had seen every op merged so
// far and nothing else; otherwise past says what it had seen.
func (s *seqState) apply(op Op, all bool, past pastView) error {
	switch op.VKind {
	case change.VKDeleteSeq:
		return s.remove(op)
	case change.VKMarkStart:
		// A mark adds a start anchor at Start and an end anchor that its
		// mark-end op places, at Start+Len+1: the end of the span, counted
		// after the start anchor has gone in.
		if err := s.insert(op, op.Pos, []any{nil}, true, all, past); err != nil {
			return err
		}
		if mi, ok := op.Value.(MarkInfo); ok {
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
		return s.insert(op, at, []any{nil}, true, all, past)
	case change.VKStr:
		str, _ := op.Value.(string)
		var items []any
		for _, r := range str {
			items = append(items, string(r))
		}
		return s.insert(op, op.Pos, items, false, all, past)
	default:
		items, _ := op.Value.([]any)
		return s.insert(op, op.Pos, items, false, all, past)
	}
}

// locate returns the index of the pos-th element (counting from 1) the op's
// author could see, or -1 if it saw fewer, which only an input missing part of
// its history produces.
func (s *seqState) locate(pos int64, all bool, past pastView) (int, error) {
	if all {
		// Everything merged so far is in view, minus what deletes removed.
		// Walk from whichever end is nearer: typing at the end costs nothing.
		if pos > int64(s.live) {
			return -1, nil
		}
		if pos <= int64(s.live)/2 {
			seen := int64(0)
			for i, n := range s.nodes {
				if len(n.deleters) == 0 {
					if seen++; seen == pos {
						return i, s.work.spend(i + 1)
					}
				}
			}
		} else {
			after := int64(s.live) - pos
			for i := len(s.nodes) - 1; i >= 0; i-- {
				if len(s.nodes[i].deleters) == 0 {
					if after == 0 {
						return i, s.work.spend(len(s.nodes) - i)
					}
					after--
				}
			}
		}
		return -1, nil
	}
	seen, steps := int64(0), 0
	for i, n := range s.nodes {
		steps += 1 + len(n.deleters)
		if !past.has(n.peer, n.counter) || removedIn(n, past) {
			continue
		}
		if seen++; seen == pos {
			return i, s.work.spend(steps)
		}
	}
	return -1, s.work.spend(steps)
}

func removedIn(n *seqNode, past pastView) bool {
	for _, d := range n.deleters {
		if past.has(d.Peer, d.Counter) {
			return true
		}
	}
	return false
}

func (s *seqState) insert(op Op, pos int64, items []any, anchor, all bool, past pastView) error {
	if len(items) == 0 {
		return nil
	}
	left := -1
	if pos > 0 {
		i, err := s.locate(pos, all, past)
		if err != nil {
			return err
		}
		left = i
	}
	depth := 1
	if left >= 0 {
		depth = s.nodes[left].depth + 1
	}
	run := make([]*seqNode, len(items))
	for k, v := range items {
		run[k] = &seqNode{
			peer:    op.Peer,
			counter: op.Counter + int64(k),
			lamport: op.Lamport + int64(k),
			depth:   depth + k, // each atom's left origin is the one before it
			value:   v,
			anchor:  anchor,
		}
	}
	// The run becomes a child of its left origin. That origin's existing
	// children follow it, each with its subtree; the run goes in front of the
	// first child it sorts before.
	at := left + 1
	skipped := 0
	for at < len(s.nodes) && s.nodes[at].depth >= depth {
		c := s.nodes[at]
		if !c.before(run[0]) {
			break
		}
		for at++; at < len(s.nodes) && s.nodes[at].depth > c.depth; at++ {
			skipped++
		}
		skipped++
	}
	if err := s.work.spend(skipped + len(run)); err != nil {
		return err
	}
	s.nodes = slices.Insert(s.nodes, at, run...)
	for _, n := range run {
		id := ID{Peer: n.peer, Counter: n.counter}
		if _, dup := s.byID[id]; !dup {
			s.byID[id] = n
			s.byPeer[n.peer] = append(s.byPeer[n.peer], n)
		}
	}
	s.live += len(run)
	return nil
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

// text renders a Text container, values a List or MovableList: every element
// no delete removed, anchors left out.
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

func (s *seqState) values() []any {
	out := []any{}
	for _, n := range s.nodes {
		if len(n.deleters) == 0 && !n.anchor {
			out = append(out, n.value)
		}
	}
	return out
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
func mergeSequences(all, units []*seqChange, work *seqWork) (map[string]*seqState, error) {
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
	seqs := map[string]*seqState{}
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
				s := seqs[op.Container]
				if s == nil {
					s = newSeqState(work)
					seqs[op.Container] = s
				}
				if err := s.apply(op, inView, past); err != nil {
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
