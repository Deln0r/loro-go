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

func changeAtoms(c Change) int64 {
	var n int64
	for _, op := range c.Ops {
		n += atomCount(op)
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
// minus those a delete in that past had removed. Rich-text style anchors take
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
	deleted := func(e elem, v refVV) bool {
		for _, d := range deletes {
			if id, ok := refDeleter(d.op, e); ok && (v == nil || v.has(id.Peer, id.Counter)) {
				return true
			}
		}
		return false
	}

	var all []elem
	// A mark start op adds its start anchor at Start and leaves the end anchor
	// to the mark-end op that follows it, which carries no position of its own:
	// the end goes at Start+Len+1, the +1 being the start anchor itself.
	pendingEnd := map[ID]int{}
	for _, in := range inserts {
		var known []elem
		for _, e := range all {
			if in.past.has(e.peer, e.counter) {
				known = append(known, e)
			}
		}
		var view []elem
		for _, e := range flatten(known) {
			if !deleted(e, in.past) {
				view = append(view, e)
			}
		}
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
		for k := range items {
			e := elem{peer: op.Peer, counter: op.Counter + int64(k), lamport: op.Lamport + int64(k), value: items[k]}
			if k > 0 {
				e.hasLeft, e.leftPeer, e.leftCounter = true, op.Peer, op.Counter+int64(k-1)
			} else if pos > 0 && pos <= len(view) {
				e.hasLeft, e.leftPeer, e.leftCounter = true, view[pos-1].peer, view[pos-1].counter
			}
			all = append(all, e)
		}
	}
	var out []elem
	for _, e := range flatten(all) {
		if !deleted(e, nil) {
			out = append(out, e)
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
