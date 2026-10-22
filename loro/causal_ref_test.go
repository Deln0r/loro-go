package loro

import (
	"sort"
	"strings"

	"github.com/Deln0r/loro-go/encoding/change"
)

// refVV is a version vector: for each peer, the end (exclusive) of the counter
// range a version holds.
type refVV map[uint64]int64

func (v refVV) has(peer uint64, counter int64) bool { return counter < v[peer] }

// changeAtoms is how many ids a change's ops take: an insert of several
// elements one per element, any other op one. Counted here rather than with
// the merge's helper, so the reference shares no code with the merge.
func changeAtoms(c Change) int64 {
	var n int64
	for _, op := range c.Ops {
		n += max(op.Len, 1)
	}
	return n
}

// refVersions returns, for each change, the version its author had when making
// it: the transitive closure of its deps. Memoised recursion, test sizes only.
func refVersions(changes []Change) func(Change) refVV {
	byPeer := map[uint64][]Change{}
	for _, c := range changes {
		byPeer[c.ID.Peer] = append(byPeer[c.ID.Peer], c)
	}
	containing := func(id ID) (Change, bool) {
		for _, c := range byPeer[id.Peer] {
			if id.Counter >= c.ID.Counter && id.Counter < c.ID.Counter+changeAtoms(c) {
				return c, true
			}
		}
		return Change{}, false
	}
	memo := map[ID]refVV{}
	var vvOf func(c Change) refVV
	vvOf = func(c Change) refVV {
		if v, ok := memo[c.ID]; ok {
			return v
		}
		v := refVV{}
		for _, d := range c.Deps {
			if z, ok := containing(d); ok {
				for p, end := range vvOf(z) {
					if end > v[p] {
						v[p] = end
					}
				}
			}
			if d.Counter+1 > v[d.Peer] {
				v[d.Peer] = d.Counter + 1
			}
		}
		memo[c.ID] = v
		return v
	}
	return vvOf
}

// refDeleter is the id of the delete atom that removes e under op d, if d's
// span covers e. The span starts at the lowest id it removes and holds |Len|
// ids; atom k removes the k-th element in deletion order, and a backwards
// delete (negative Len) removed the highest id first. Written out here rather
// than taken from the decoder, so the oracle does not inherit its mistakes.
func refDeleter(d Op, e elem) (ID, bool) {
	span, ok := d.Value.(DeleteSpan)
	if !ok || e.peer != span.Peer {
		return ID{}, false
	}
	n := span.Len
	if n < 0 {
		n = -n
	}
	if e.counter < span.Counter || e.counter >= span.Counter+n {
		return ID{}, false
	}
	k := e.counter - span.Counter
	if span.Len < 0 {
		k = span.Counter + n - 1 - e.counter
	}
	return ID{Peer: d.Peer, Counter: d.Counter + k}, true
}

// refAnchor is the value of a rich-text style anchor: it takes a position in
// the text's coordinate space and is never rendered.
type refAnchor struct{}

// causalRefSeq is a brute-force reference for Text and List merge, kept as an
// oracle for MergeState. Every insert resolves its position against a view
// rebuilt from scratch: the elements in the op's causal past, in merged order,
// minus those a delete in that past had removed. Concurrent inserts between the
// same origins are placed by Fugue's rule as loro applies it, written here over
// a plain list with linear searches and no shortcut for the common case.
// Deletes are read by the ids they recorded. On a history whose ids are right
// that names the same elements as the positions the merge reads, so the random
// test also checks the merge's positional deletes against the ids. Rich-text style anchors take
// positions like characters do. It returns the elements no delete removed,
// anchors included; refRender gives the value a document shows. Quadratic or
// worse; tests only. It assumes a clean, causally closed history with no
// duplicated ops.
func causalRefSeq(changes []Change, container string, isText bool) []elem {
	vvOf := refVersions(changes)
	type refOp struct {
		op   Op
		past refVV
	}
	var inserts, deletes []refOp
	for _, ch := range changes {
		base := vvOf(ch)
		for _, op := range ch.Ops {
			if op.Container != container {
				continue
			}
			past := refVV{}
			for p, end := range base {
				past[p] = end
			}
			if op.Counter > past[op.Peer] {
				past[op.Peer] = op.Counter
			}
			switch {
			case op.VKind == change.VKStr, op.VKind == change.VKLoroValue:
				inserts = append(inserts, refOp{op, past})
			case isText && (op.VKind == change.VKMarkStart || op.VKind == change.VKNull):
				inserts = append(inserts, refOp{op, past})
			case op.VKind == change.VKDeleteSeq:
				deletes = append(deletes, refOp{op, past})
			}
		}
	}
	sort.SliceStable(inserts, func(i, j int) bool {
		a, b := inserts[i].op, inserts[j].op
		if a.Lamport != b.Lamport {
			return a.Lamport < b.Lamport
		}
		if a.Peer != b.Peer {
			return a.Peer < b.Peer
		}
		return a.Counter < b.Counter
	})
	// deleted reports whether a delete atom inside version v (nil: every
	// delete) removes e.
	deleted := func(e *elem, v refVV) bool {
		for _, d := range deletes {
			if id, ok := refDeleter(d.op, *e); ok && (v == nil || v.has(id.Peer, id.Counter)) {
				return true
			}
		}
		return false
	}

	// list is the merged sequence in document order, elements that deletes
	// removed included, each with the origins its author saw.
	var list []*elem
	indexOf := func(e *elem) int {
		for i, x := range list {
			if x == e {
				return i
			}
		}
		return -1
	}
	// A mark start op adds its start anchor at Start and leaves the end anchor
	// to the mark-end op that follows it, which carries no position of its own:
	// the end goes at Start+Len+1, the +1 being the start anchor itself.
	pendingEnd := map[ID]int{}
	for _, in := range inserts {
		op := in.op
		pos := int(op.Pos)
		var items []any
		switch op.VKind {
		case change.VKMarkStart:
			items = []any{refAnchor{}}
			if mi, ok := op.Value.(MarkInfo); ok {
				pendingEnd[ID{Peer: op.Peer, Counter: op.Counter + 1}] = int(mi.Start + mi.Len + 1)
			}
		case change.VKNull:
			end, ok := pendingEnd[ID{Peer: op.Peer, Counter: op.Counter}]
			if !ok {
				continue
			}
			items, pos = []any{refAnchor{}}, end
		default:
			items = expandItems(op, isText)
		}
		if len(items) == 0 {
			continue
		}
		// The author's view, rebuilt from scratch: what it had seen and no
		// delete it had seen removed.
		var view []*elem
		for _, e := range list {
			if in.past.has(e.peer, e.counter) && !deleted(e, in.past) {
				view = append(view, e)
			}
		}
		var left *elem
		if p := min(pos, len(view)); p > 0 {
			left = view[p-1]
		}
		start := indexOf(left) + 1
		end := start
		for end < len(list) && !in.past.has(list[end].peer, list[end].counter) {
			end++
		}
		var right *elem
		if end < len(list) {
			right = list[end]
		}
		// Fugue's placement among the concurrent elements list[start:end], as
		// loro does it. rightParent is r's index when r's own left origin is the
		// run's left origin, else -1, which sorts after any index.
		rightParent := func(r *elem) int {
			if r == nil || r.left != left {
				return -1
			}
			return indexOf(r)
		}
		order := func(a, b int) int {
			switch {
			case a >= 0 && b >= 0:
				return a - b
			case a >= 0:
				return -1
			case b >= 0:
				return 1
			}
			return 0
		}
		at, scanning := start, false
		visited := map[*elem]bool{}
		for i := start; i < end; i++ {
			other := list[i]
			if other.left != left && (other.left == nil || !visited[other.left]) {
				break
			}
			visited[other] = true
			if other.left == left {
				if other.right == right {
					if other.peer > op.Peer {
						break
					}
					scanning = false
				} else if c := order(rightParent(other.right), rightParent(right)); c < 0 {
					scanning = true
				} else if c == 0 && other.peer > op.Peer {
					break
				} else {
					scanning = false
				}
			}
			if !scanning {
				at = i + 1
			}
		}
		run := make([]*elem, len(items))
		for k, v := range items {
			run[k] = &elem{peer: op.Peer, counter: op.Counter + int64(k), value: v, left: left, right: right}
			if k > 0 {
				run[k].left = run[k-1]
			}
		}
		list = append(list[:at], append(run, list[at:]...)...)
	}
	var out []elem
	for _, e := range list {
		if !deleted(e, nil) {
			out = append(out, *e)
		}
	}
	return out
}

// refRender is what a document shows for a reference sequence: a string for
// Text, a list for List, with style anchors left out.
func refRender(seq []elem, isText bool) any {
	if isText {
		var sb strings.Builder
		for _, e := range seq {
			if s, ok := e.value.(string); ok {
				sb.WriteString(s)
			}
		}
		return sb.String()
	}
	out := []any{}
	for _, e := range seq {
		if _, anchor := e.value.(refAnchor); !anchor {
			out = append(out, e.value)
		}
	}
	return out
}

// elem is one element of the reference's sequence, with the origins its
// author saw on either side of it.
type elem struct {
	peer        uint64
	counter     int64
	value       any
	left, right *elem
}

// expandItems splits an insert's value into its atoms: runes for Text, items
// for List.
func expandItems(op Op, isText bool) []any {
	if isText {
		s, _ := op.Value.(string)
		var out []any
		for _, c := range s {
			out = append(out, string(c))
		}
		return out
	}
	lst, _ := op.Value.([]any)
	return lst
}
