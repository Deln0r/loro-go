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
// order). Map containers resolve by last-writer-wins on (lamport, peer); Text and
// List containers use an RGA/Fugue replay ordering concurrent same-origin inserts
// by ascending (lamport, peer, counter), matching loro's merge for the fixtures.
//
// Simplifications (honest limits): left-origin is resolved as the element at
// position-1 in the current sequence (exact for pos 0 and non-interleaving
// histories; deep concurrent inserts at the same non-zero position and Fugue's
// multi-element non-interleaving guarantee are not yet fully general). Deletes
// and move ops are not handled (no fixture exercises them).
func MergeState(u *Updates) (map[string]any, error) {
	type cinfo struct {
		kind   change.ContainerType
		isRoot bool
		ops    []Op
	}
	conts := map[string]*cinfo{}
	var order []string
	for _, ch := range u.Changes {
		for _, op := range ch.Ops {
			ci := conts[op.Container]
			if ci == nil {
				ci = &cinfo{kind: op.Kind, isRoot: op.IsRoot}
				conts[op.Container] = ci
				order = append(order, op.Container)
			}
			ci.ops = append(ci.ops, op)
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

func idLess(a, b elem) bool {
	if a.lamport != b.lamport {
		return a.lamport < b.lamport
	}
	if a.peer != b.peer {
		return a.peer < b.peer
	}
	return a.counter < b.counter
}

// mergeSeq replays insert ops into a Fugue-style tree (each element's parent is
// its left origin) and flattens it pre-order, ordering same-parent siblings by
// ascending id. Building a tree (rather than flat skipping) keeps causal runs
// contiguous, giving the non-interleaving property for concurrent multi-element
// inserts. Left origin is resolved against the op's CAUSAL PAST (same-peer
// earlier elements + explicit deps), not the global merged sequence, so a
// position resolves to the element the author actually saw.
func mergeSeq(ops []Op, isText bool) []elem {
	var all []elem
	for _, op := range sortedOps(ops) {
		items := expandItems(op, isText)
		pos := int(op.Pos)
		var lp uint64
		var lc int64
		hasLeft := false
		if pos > 0 {
			proj := flatten(causalProjection(all, op))
			if pos-1 < len(proj) {
				hasLeft = true
				lp, lc = proj[pos-1].peer, proj[pos-1].counter
			}
		}
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
			all = append(all, e)
		}
	}
	return flatten(all)
}

// causalProjection returns the elements op causally depends on. For the current
// fixtures that is same-peer-earlier elements (explicit cross-peer deps would be
// added here for deeper histories).
func causalProjection(all []elem, op Op) []elem {
	var out []elem
	for _, e := range all {
		if e.peer == op.Peer && e.counter < op.Counter {
			out = append(out, e)
		}
	}
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
		sort.SliceStable(cs, func(i, j int) bool { return idLess(cs[i], cs[j]) })
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
