package loro

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/Deln0r/loro-go/encoding/change"
)

// counterIncrement extracts the numeric increment from a Counter op. Loro encodes
// counter increments in the VALUES stream as I64 for whole numbers and F64 for
// fractional ones, so an op value is either int64 or float64 here. Returns false
// for any other op kind so non-increment ops are skipped.
func counterIncrement(op Op) (float64, bool) {
	switch v := op.Value.(type) {
	case int64:
		return float64(v), true
	case float64:
		return v, true
	default:
		return 0, false
	}
}

// counterValue sums a Counter container's increments. Counter is a commutative
// CRDT (the value is the order-independent sum of all increments), so this is
// exact regardless of merge order. The result is int64 when integral (matching
// loro's toJSON, which prints whole counters without a fractional part) and
// float64 otherwise.
func counterValue(ops []Op) any {
	var sum float64
	for _, op := range ops {
		if inc, ok := counterIncrement(op); ok {
			sum += inc
		}
	}
	return numFromF64(sum)
}

// numFromF64 narrows an accumulated counter sum back to int64 when it has no
// fractional part, keeping float64 only for genuinely fractional counters.
func numFromF64(f float64) any {
	if !math.IsInf(f, 0) && !math.IsNaN(f) && f == math.Trunc(f) {
		return int64(f)
	}
	return f
}

// MergeState reconstructs document state with CRDT semantics, so it is correct
// for CONCURRENT / multi-peer histories (unlike BuildState which applies in
// order). Map containers resolve by last-writer-wins on (lamport, peer); Text
// and List containers replay inserts into a tree parented by left origin and
// walk it pre-order.
//
// Siblings under one origin order NEWEST FIRST: descending lamport, with an
// equal lamport meaning the two inserts were concurrent and broken by ascending
// peer. That asymmetry is the whole rule. An earlier version ordered siblings
// ascending throughout, which is right for concurrent inserts and wrong for
// sequential ones, so every insert that was not an append came out reversed:
// "BBB" then "Z" at position 0 produced "BBBZ" where loro gives "ZBBB".
//
// Measured against loro-crdt on the 300 random insert-anywhere histories in
// testdata/fixtures/ordering_corpus.json, counting how many matched:
//
//	ascending siblings, splice at subtree end (as shipped)      8
//	ascending siblings, splice after the origin                 6
//	descending siblings, splice at subtree end                107
//	descending siblings, splice after the origin (this code)  300
//
// All 52 fixtures passed in every one of those four states, because a fixture
// that only appends cannot tell the rules apart.
//
// Simplifications (honest limits): left-origin is resolved as the element at
// position-1 in the peer's current view. Fugue's right origin is not recorded,
// so the non-interleaving guarantee for concurrent multi-element inserts at the
// same position is not fully general. Deletes are applied as id-addressed
// tombstones; move ops are handled only for MovableList.
func MergeState(u *Updates) (map[string]any, error) {
	type cinfo struct {
		kind   change.ContainerType
		isRoot bool
		ops    []Op
	}
	conts := map[string]*cinfo{}
	var order []string

	// An op is identified by the id RANGE it occupies: the peer that made it and
	// the counters [Counter, Counter+Len) that peer assigned to its atoms.
	// Re-applying an atom must be a no-op, or the document ends up corrupted
	// rather than merged, and duplicates are ordinary once updates travel over a
	// real transport that retries and hands back overlapping pagination windows.
	//
	// The range, not just its first counter, is what matters. loro coalesces
	// adjacent atoms from one peer into a single run, so two exports of the same
	// document taken at different moments share a first counter while covering
	// different spans: "ab" is counter 0 len 2 and "abcd" is counter 0 len 4.
	// Keying on the first counter alone made whichever arrived first win, so
	// "abcd" merged after "ab" silently lost "cd". The complement was as bad: a
	// tail delta re-sent at a later start counter missed the key entirely and its
	// atoms were applied a second time, which is the "hello" -> "hellooloo"
	// corruption this dedup exists to prevent.
	//
	// So each incoming op is clipped to the sub-ranges not already consumed, and
	// dropped only when its whole range is covered.
	type spanKey struct {
		peer      uint64
		container string
		kind      change.ValueKind
	}
	consumed := map[spanKey][]idRange{}

	for _, ch := range u.Changes {
		for _, op := range ch.Ops {
			key := spanKey{peer: op.Peer, container: op.Container, kind: op.VKind}
			span := idRange{start: op.Counter, end: op.Counter + atomCount(op)}

			fresh := uncovered(consumed[key], span)
			consumed[key] = addRange(consumed[key], span)
			if len(fresh) == 0 {
				continue
			}

			ci := conts[op.Container]
			if ci == nil {
				ci = &cinfo{kind: op.Kind, isRoot: op.IsRoot}
				conts[op.Container] = ci
				order = append(order, op.Container)
			}
			for _, r := range fresh {
				clipped, ok := clipOp(op, span, r)
				if !ok {
					// The value cannot be sliced, so the op is all-or-nothing.
					// It reaches here only when some part of it is new.
					ci.ops = append(ci.ops, op)
					break
				}
				ci.ops = append(ci.ops, clipped)
			}
		}
	}

	// buildOne reconstructs a single non-tree container's value.
	buildOne := func(ci *cinfo) (any, error) {
		switch ci.kind {
		case change.CMap:
			ops := sortedOps(ci.ops)
			m := map[string]any{}
			for _, op := range ops { // ascending order => last write wins = max(lamport,peer)
				if op.VKind == change.VKDeleteOnce {
					delete(m, op.MapKey)
					continue
				}
				m[op.MapKey] = op.Value
			}
			return m, nil
		case change.CText:
			seq := tombstone(mergeSeq(opsOfKind(ci.ops, change.VKStr), true), deleteSpans(ci.ops))
			var sb strings.Builder
			for _, e := range seq {
				sb.WriteString(e.value.(string))
			}
			return sb.String(), nil
		case change.CList:
			seq := tombstone(mergeSeq(opsOfKind(ci.ops, change.VKLoroValue), false), deleteSpans(ci.ops))
			return seqValues(seq), nil
		case change.CMovableList:
			seq := tombstone(mergeSeq(opsOfKind(ci.ops, change.VKLoroValue), false), deleteSpans(ci.ops))
			lst := seqValues(seq)
			for _, m := range sortedOps(opsOfKind(ci.ops, change.VKListMove)) {
				lst = applyMove(lst, int(m.MoveFrom), int(m.Pos))
			}
			return lst, nil
		case change.CCounter:
			return counterValue(ci.ops), nil
		default:
			return nil, fmt.Errorf("loro: merge unsupported container kind %v", ci.kind)
		}
	}

	built := map[string]any{}
	// Non-root Map containers are tree-node meta maps, keyed by node id "counter@peer".
	metaMaps := map[string]map[string]any{}
	// Pass 1: every container except trees, which need the meta maps built first.
	for _, name := range order {
		ci := conts[name]
		if ci.kind == change.CTree {
			continue
		}
		val, err := buildOne(ci)
		if err != nil {
			return nil, err
		}
		built[name] = val
		if !ci.isRoot {
			if m, ok := val.(map[string]any); ok {
				metaMaps[name] = m
			}
		}
	}
	// Pass 2: trees, inlining each node's meta map by node id.
	for _, name := range order {
		if conts[name].kind == change.CTree {
			built[name] = buildTree(conts[name].ops, metaMaps)
		}
	}

	// Only root containers appear at the top level; nested ones are inlined above.
	state := map[string]any{}
	for _, name := range order {
		if conts[name].isRoot {
			state[name] = built[name]
		}
	}
	return state, nil
}

// deleteSpans collects the id spans removed by the container's DeleteSeq ops.
func deleteSpans(ops []Op) []DeleteSpan {
	var out []DeleteSpan
	for _, op := range ops {
		if op.VKind != change.VKDeleteSeq {
			continue
		}
		if d, ok := op.Value.(DeleteSpan); ok {
			out = append(out, d)
		}
	}
	return out
}

// tombstone drops sequence elements whose id falls inside any delete span.
// Deletes are id-addressed, so this is order-independent and correct under
// concurrent insert/delete (a delete never touches elements it has not seen).
func tombstone(seq []elem, spans []DeleteSpan) []elem {
	if len(spans) == 0 {
		return seq
	}
	out := seq[:0]
	for _, e := range seq {
		dead := false
		for _, d := range spans {
			start, n := d.Normalize()
			if e.peer == d.Peer && e.counter >= start && e.counter < start+n {
				dead = true
				break
			}
		}
		if !dead {
			out = append(out, e)
		}
	}
	return out
}

func opsOfKind(ops []Op, vk change.ValueKind) []Op {
	var out []Op
	for _, op := range ops {
		if op.VKind == vk {
			out = append(out, op)
		}
	}
	return out
}

func seqValues(seq []elem) []any {
	out := make([]any, len(seq))
	for i, e := range seq {
		out[i] = e.value
	}
	return out
}

func applyMove(lst []any, from, to int) []any {
	if from < 0 || from >= len(lst) {
		return lst
	}
	el := lst[from]
	lst = append(lst[:from], lst[from+1:]...)
	if to < 0 {
		to = 0
	}
	if to > len(lst) {
		to = len(lst)
	}
	return append(lst[:to], append([]any{el}, lst[to:]...)...)
}

// buildTree reconstructs loro's Tree toJSON: a nested list of nodes ordered by
// (fractional_index, id) among siblings. metaMaps holds each node's meta map
// (the node's data sub-container) keyed by node id; absent nodes get an empty meta.
func buildTree(ops []Op, metaMaps map[string]map[string]any) []any {
	type tn struct {
		id, parent string
		hasParent  bool
		fi         string
	}
	var nodes []tn
	for _, op := range ops {
		if op.VKind != change.VKRawTreeMove {
			continue
		}
		n, ok := op.Value.(TreeNode)
		if !ok {
			continue
		}
		nodes = append(nodes, tn{n.ID, n.Parent, n.HasParent, n.FI})
	}
	childrenOf := map[string][]tn{}
	for _, n := range nodes {
		p := ""
		if n.hasParent {
			p = n.parent
		}
		childrenOf[p] = append(childrenOf[p], n)
	}
	var build func(parent string) []any
	build = func(parent string) []any {
		kids := append([]tn{}, childrenOf[parent]...)
		sort.SliceStable(kids, func(i, j int) bool {
			if kids[i].fi != kids[j].fi {
				return kids[i].fi < kids[j].fi
			}
			return kids[i].id < kids[j].id
		})
		out := make([]any, len(kids))
		for idx, k := range kids {
			var parentVal any
			if k.hasParent {
				parentVal = k.parent
			}
			meta := metaMaps[k.id]
			if meta == nil {
				meta = map[string]any{}
			}
			out[idx] = map[string]any{
				"parent":           parentVal,
				"index":            float64(idx),
				"meta":             meta,
				"id":               k.id,
				"fractional_index": k.fi,
				"children":         build(k.id),
			}
		}
		return out
	}
	return build("")
}

func sortedOps(ops []Op) []Op {
	out := append([]Op{}, ops...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Lamport != b.Lamport {
			return a.Lamport < b.Lamport
		}
		if a.Peer != b.Peer {
			return a.Peer < b.Peer
		}
		return a.Counter < b.Counter
	})
	return out
}

// elem is one sequence element with its id, lamport, value, and left origin.
type elem struct {
	peer, leftPeer       uint64
	counter, leftCounter int64
	lamport              int64
	value                any
	hasLeft              bool
}

// siblingLess orders elements that share a left origin. RGA puts the causally
// LATER insert first among siblings, so lamport descends; a tie means the two
// were concurrent, and those are broken by ascending peer.
func siblingLess(a, b elem) bool {
	if a.lamport != b.lamport {
		return a.lamport > b.lamport
	}
	if a.peer != b.peer {
		return a.peer < b.peer
	}
	return a.counter > b.counter
}

// mergeSeq replays insert ops into a Fugue-style tree (each element's parent is
// its left origin) and flattens it pre-order, ordering same-parent siblings by
// ascending id. Building a tree (rather than flat skipping) keeps causal runs
// contiguous, giving the non-interleaving property for concurrent multi-element
// inserts. Left origin is resolved against the op's CAUSAL PAST (same-peer
// earlier elements + explicit deps), not the global merged sequence, so a
// position resolves to the element the author actually saw.
// The left origin of an op is the element at the op's visible position-1 in the
// author's causal view, i.e. flatten(same-peer earlier elements)[pos-1]. Rather
// than re-flattening that projection per op (the old O(n^2) path), we keep each
// peer's causal view in flatten order incrementally. Ops replay in counter order,
// so every new element carries the highest id its peer has emitted; in Fugue it
// therefore becomes the LAST child of its left origin and lands at the end of the
// origin's subtree. That makes the insert an index lookup plus a splice, and a
// plain append in the common growing-at-the-end case. The final flatten(all) and
// the resulting element set are unchanged, so the merged output is identical to
// the reference algorithm (asserted in TestMergeSeqMatchesReference).
func mergeSeq(ops []Op, isText bool) []elem {
	var all []elem
	views := map[uint64][]elem{} // peer -> its causal view in flatten order
	for _, op := range sortedOps(ops) {
		items := expandItems(op, isText)
		seq := views[op.Peer]
		pos := int(op.Pos)
		var lp uint64
		var lc int64
		hasLeft := false
		if pos > 0 && pos-1 < len(seq) {
			hasLeft = true
			lp, lc = seq[pos-1].peer, seq[pos-1].counter
		}
		run := make([]elem, len(items))
		for k := range items {
			e := elem{
				peer:    op.Peer,
				counter: op.Counter + int64(k),
				lamport: op.Lamport + int64(k),
				value:   items[k],
			}
			if k == 0 {
				e.hasLeft, e.leftPeer, e.leftCounter = hasLeft, lp, lc
			} else {
				e.hasLeft = true
				e.leftPeer = op.Peer
				e.leftCounter = op.Counter + int64(k-1)
			}
			run[k] = e
		}
		all = append(all, run...)
		li := -1
		if hasLeft {
			li = pos - 1 // left origin sits at pos-1 in the peer's view
		}
		// The new run is causally the latest sibling of its left origin, and
		// siblings order newest-first, so it goes IMMEDIATELY after the origin,
		// in front of that origin's existing children. With no origin it is the
		// newest root child and goes at the very front.
		views[op.Peer] = spliceElems(seq, li+1, run)
	}
	return flatten(all)
}

// spliceElems inserts run into seq at index at, returning the new slice.
func spliceElems(seq []elem, at int, run []elem) []elem {
	if at >= len(seq) {
		return append(seq, run...)
	}
	out := make([]elem, 0, len(seq)+len(run))
	out = append(out, seq[:at]...)
	out = append(out, run...)
	out = append(out, seq[at:]...)
	return out
}

type parentKey struct {
	has     bool
	peer    uint64
	counter int64
}

// flatten orders elements as a pre-order DFS of the left-origin tree, with
// same-parent siblings sorted by ascending id.
func flatten(all []elem) []elem {
	children := map[parentKey][]elem{}
	for _, e := range all {
		k := parentKey{e.hasLeft, e.leftPeer, e.leftCounter}
		children[k] = append(children[k], e)
	}
	for k := range children {
		cs := children[k]
		sort.SliceStable(cs, func(i, j int) bool { return siblingLess(cs[i], cs[j]) })
		children[k] = cs
	}
	var out []elem
	var dfs func(p parentKey)
	dfs = func(p parentKey) {
		for _, e := range children[p] {
			out = append(out, e)
			dfs(parentKey{true, e.peer, e.counter})
		}
	}
	dfs(parentKey{has: false})
	return out
}

func expandItems(op Op, isText bool) []any {
	if isText {
		s, _ := op.Value.(string)
		r := []rune(s)
		out := make([]any, len(r))
		for i, c := range r {
			out[i] = string(c)
		}
		return out
	}
	if lst, ok := op.Value.([]any); ok {
		return lst
	}
	return nil
}

// idRange is a half-open counter range [start, end) belonging to one peer.
type idRange struct{ start, end int64 }

// atomCount is how many ids an op consumes. Multi-element inserts carry Len;
// everything else (a map write, a delete op, a move) occupies exactly one id,
// and a zero or negative Len would otherwise make the range empty and defeat
// deduplication entirely.
func atomCount(op Op) int64 {
	if op.Len > 1 {
		return op.Len
	}
	return 1
}

// uncovered returns the parts of want that none of the ranges in have covers,
// in ascending order. have is kept sorted and non-overlapping by addRange.
func uncovered(have []idRange, want idRange) []idRange {
	var out []idRange
	cur := want.start
	for _, h := range have {
		if h.end <= cur {
			continue
		}
		if h.start >= want.end {
			break
		}
		if h.start > cur {
			out = append(out, idRange{cur, min64(h.start, want.end)})
		}
		if h.end > cur {
			cur = h.end
		}
		if cur >= want.end {
			return out
		}
	}
	if cur < want.end {
		out = append(out, idRange{cur, want.end})
	}
	return out
}

// addRange merges r into have, keeping it sorted and non-overlapping.
func addRange(have []idRange, r idRange) []idRange {
	out := make([]idRange, 0, len(have)+1)
	placed := false
	for _, h := range have {
		switch {
		case h.end < r.start:
			out = append(out, h)
		case h.start > r.end:
			if !placed {
				out = append(out, r)
				placed = true
			}
			out = append(out, h)
		default: // overlaps or touches: absorb
			r.start = min64(r.start, h.start)
			r.end = max64(r.end, h.end)
		}
	}
	if !placed {
		out = append(out, r)
	}
	return out
}

// clipOp narrows op, whose atoms occupy span, to the sub-range keep. It reports
// false when the op's value cannot be sliced, in which case the caller must
// treat the op as all-or-nothing rather than corrupt it.
func clipOp(op Op, span, keep idRange) (Op, bool) {
	if keep == span {
		return op, true
	}
	off := keep.start - span.start
	n := keep.end - keep.start

	switch v := op.Value.(type) {
	case string:
		r := []rune(v)
		if off < 0 || off+n > int64(len(r)) {
			return op, false
		}
		op.Value = string(r[off : off+n])
	case []any:
		if off < 0 || off+n > int64(len(v)) {
			return op, false
		}
		op.Value = append([]any(nil), v[off:off+n]...)
	default:
		return op, false
	}

	// Trimming k atoms off the front moves the run's id, its lamport and the
	// position it inserts at by the same k.
	op.Counter += off
	op.Lamport += off
	op.Pos += off
	op.Len = n
	return op, true
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
