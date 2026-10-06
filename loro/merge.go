package loro

import (
	"fmt"
	"math"
	"sort"

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
// order). Map containers resolve by last-writer-wins on (lamport, peer); Text,
// List and MovableList inserts go into a tree parented by left origin, walked
// pre-order; Tree replays moves with cycle-closing ones skipped.
//
// An insert records only a position: the index in the sequence as its author
// saw it. So each one is resolved against that view, the elements in the op's
// causal past (the closure of its change's deps, plus the author's own earlier
// atoms) minus those removed by a delete atom inside that past, with the two
// anchors a rich-text mark adds taking positions like characters. The left
// origin is the element just before the position in that view.
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
// MergeState assumes the changes it is given are causally closed: every dep
// is either among them or absent from all of them. A missing change is not an
// error, but positions that counted its elements cannot be resolved, and such
// an insert lands at the front.
//
// Cost: an op made with everything already merged in view (one peer typing,
// or a peer editing after a sync) resolves at the nearer end of the merged
// sequence. An op made concurrently with edits already merged is resolved by
// scanning it, so long concurrent branches cost time proportional to the
// product of their sizes; past seqWorkLimit steps MergeState returns an error.
//
// Honest limits: concurrent inserts at the same place are ordered newest
// first, where loro uses Fugue's rule (peer ids and the element to the right of
// the insertion point); 185 of the 300 random concurrent histories in
// testdata/fixtures/seq_concurrent_corpus.json come out as loro has them. A
// delete is applied by its recorded ids where loro applies it by position, so
// a delete that loro-crdt's WASM build up to 1.16.3 wrote with wrong ids
// (loro-dev/loro#1149) removes the wrong characters here. MovableList positions are resolved without its moves, and
// moves are then applied by index, which is wrong for an insert made after a
// move.
func MergeState(u *Updates) (map[string]any, error) {
	return mergeState(u, seqWorkLimit)
}

// mergeState is MergeState with the sequence merge's step limit as a
// parameter, so a test can reach it without a huge input.
func mergeState(u *Updates, seqLimit int) (map[string]any, error) {
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

	// Every change as it arrived, with the ops that were new in it: the
	// sequence merge needs to know which change an op came from, since that
	// change's deps say what its author had seen.
	copies := make([]*seqChange, len(u.Changes))
	var units []*seqChange
	for i := range u.Changes {
		ch := &u.Changes[i]
		sc := &seqChange{ch: ch, start: ch.ID.Counter, end: ch.ID.Counter}
		for _, op := range ch.Ops {
			sc.end += atomCount(op)
		}
		copies[i] = sc
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
					sc.fresh = append(sc.fresh, op)
					break
				}
				ci.ops = append(ci.ops, clipped)
				sc.fresh = append(sc.fresh, clipped)
			}
		}
		if len(sc.fresh) > 0 {
			units = append(units, sc)
		}
	}
	seqs, err := mergeSequences(copies, units, &seqWork{limit: seqLimit})
	if err != nil {
		return nil, err
	}

	// buildOne reconstructs a single non-tree container's value.
	buildOne := func(name string, ci *cinfo) (any, error) {
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
			if s := seqs[name]; s != nil {
				return s.text(), nil
			}
			return "", nil
		case change.CList:
			if s := seqs[name]; s != nil {
				return s.values(), nil
			}
			return []any{}, nil
		case change.CMovableList:
			lst := []any{}
			if s := seqs[name]; s != nil {
				lst = s.values()
			}
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
		val, err := buildOne(name, ci)
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
			tree, err := buildTree(conts[name].ops, metaMaps)
			if err != nil {
				return nil, err
			}
			built[name] = tree
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

func opsOfKind(ops []Op, vk change.ValueKind) []Op {
	var out []Op
	for _, op := range ops {
		if op.VKind == vk {
			out = append(out, op)
		}
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

// deletedTreeRoot is loro's TreeID::delete_root(), counter i32::MAX on peer
// u64::MAX. A tree delete is encoded as a move under this parent.
const deletedTreeRoot = "2147483647@18446744073709551615"

// The cycle check walks from a move's new parent up towards the root. Only a
// node that already has children can be an ancestor of anything, so most moves,
// and every creation, skip the walk. The walks that remain are bounded in total:
// treeWalkPerMove steps per tree op on average, plus treeWalkFloor. A document
// that needs more is refused with an error instead of taking the CPU. That
// allows an average ancestor walk of 64 levels on every single move, far past
// any real tree, while a crafted history of deep subtrees being moved under
// their own leaves is cut off after a few million steps.
const (
	treeWalkPerMove = 64
	treeWalkFloor   = 1 << 22
)

// buildTree reconstructs loro's Tree toJSON: a nested list of nodes ordered by
// (fractional_index, lamport, peer) among siblings. metaMaps holds each node's meta map
// (the node's data sub-container) keyed by node id; absent nodes get an empty meta.
//
// Every tree op is a move. Creating a node moves a new node in; deleting one
// moves it under deletedTreeRoot. So the state is not the set of ops but the
// result of applying them: each node has exactly one current parent, set by the
// last move applied to it.
//
// Moves are applied in (lamport, peer, counter) order, and a move that would
// make a node its own ancestor is skipped. That is the movable-tree rule loro
// follows, and it is what keeps two honest peers who concurrently move A under
// B and B under A from producing a cycle: the move that sorts first stands and
// the other is dropped.
//
// An earlier version skipped the application entirely and listed every op as a
// child of its parent. A node moved once appeared under both parents, a
// deleted node stayed visible, and the crossing moves above made the tree
// cyclic, so the recursive build overflowed the stack and killed the process:
// a fatal error Go cannot recover from, reachable by honest concurrent edits.
// None of the fixtures moved or deleted a node; the fuzzer found the cycle once
// it was allowed below the checksum.
func buildTree(ops []Op, metaMaps map[string]map[string]any) ([]any, error) {
	type placement struct {
		parent    string // meaningful only when hasParent
		hasParent bool
		fi        string
		// lamport and peer of the move that put the node where it is. Siblings
		// with equal fractional indices are ordered by these, as loro orders
		// them by the idlp of the last effective move.
		lamport int64
		peer    uint64
	}
	current := map[string]placement{}
	// childCount[x] is how many nodes currently have x as their parent.
	childCount := map[string]int{}
	budget := treeWalkFloor + treeWalkPerMove*len(ops)
	steps := 0

	// wouldCycle reports whether moving node under parent would make node its
	// own ancestor: true when parent is node, or node sits somewhere above
	// parent in the current state.
	//
	// An earlier version of this check walked up from every new parent, and a
	// chain built one child at a time costs a walk as long as the chain on each
	// step: an honest tree 60 000 levels deep, which loro-crdt builds in 110 ms,
	// took 65 s to merge. A node with no children cannot be anyone's ancestor, so
	// the walk only runs for nodes that have some; that covers every creation.
	wouldCycle := func(node, parent string) (bool, error) {
		if parent == node {
			return true, nil
		}
		if childCount[node] == 0 {
			return false, nil
		}
		for at := parent; ; {
			if at == node {
				return true, nil
			}
			p, ok := current[at]
			if !ok || !p.hasParent {
				return false, nil
			}
			steps++
			if steps > budget {
				return false, fmt.Errorf("loro: tree ancestry checks exceeded %d steps for %d ops", budget, len(ops))
			}
			at = p.parent
		}
	}

	for _, op := range sortedOps(ops) {
		if op.VKind != change.VKRawTreeMove {
			continue
		}
		n, ok := op.Value.(TreeNode)
		if !ok {
			continue
		}
		if n.HasParent && n.Parent != deletedTreeRoot {
			cyclic, err := wouldCycle(n.ID, n.Parent)
			if err != nil {
				return nil, err
			}
			if cyclic {
				continue
			}
		}
		if prev, ok := current[n.ID]; ok && prev.hasParent {
			childCount[prev.parent]--
		}
		if n.HasParent {
			childCount[n.Parent]++
		}
		current[n.ID] = placement{parent: n.Parent, hasParent: n.HasParent, fi: n.FI, lamport: op.Lamport, peer: op.Peer}
	}

	type child struct {
		id string
		placement
	}
	childrenOf := map[string][]child{}
	for id, p := range current {
		key := ""
		if p.hasParent {
			key = p.parent
		}
		childrenOf[key] = append(childrenOf[key], child{id, p})
	}

	// The walk starts at the root. A deleted node hangs under deletedTreeRoot,
	// which is never reached from the root, so it disappears together with its
	// subtree, as it does in loro's toJSON. visited is defence in depth: with one
	// parent per node and cycle-closing moves skipped, no node can be reached
	// twice, and if that ever stops being true this ends the walk rather than
	// the process.
	visited := map[string]bool{}
	var build func(parent string) []any
	build = func(parent string) []any {
		kids := append([]child{}, childrenOf[parent]...)
		// Fractional index first; fiHex renders two uppercase hex digits per
		// byte, so comparing the strings compares the bytes. Ties, which happen
		// whenever two peers insert into the same gap without seeing each other,
		// go to the numeric (lamport, peer) of the placing move. The id string is
		// not a substitute: "0@10" sorts before "0@2".
		sort.SliceStable(kids, func(i, j int) bool {
			a, b := kids[i], kids[j]
			if a.fi != b.fi {
				return a.fi < b.fi
			}
			if a.lamport != b.lamport {
				return a.lamport < b.lamport
			}
			return a.peer < b.peer
		})
		out := make([]any, 0, len(kids))
		for _, k := range kids {
			if visited[k.id] {
				continue
			}
			visited[k.id] = true
			var parentVal any
			if k.hasParent {
				parentVal = k.parent
			}
			meta := metaMaps[k.id]
			if meta == nil {
				meta = map[string]any{}
			}
			out = append(out, map[string]any{
				"parent":           parentVal,
				"index":            float64(len(out)),
				"meta":             meta,
				"id":               k.id,
				"fractional_index": k.fi,
				"children":         build(k.id),
			})
		}
		return out
	}
	return build(""), nil
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
	shift := off // how far the kept part's position moves

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
	case DeleteSpan:
		// Atom k of a delete removes the k-th element of its span in deletion
		// order. Kept atoms of a forward delete remove the span from off on,
		// all at the op's position. A backwards delete removed its highest id
		// first, one position further left each time, so its kept atoms remove
		// the ids below the first off, starting off positions to the left.
		start, total := v.Normalize()
		if off < 0 || off+n > total {
			return op, false
		}
		if v.Len >= 0 {
			v.Counter, v.Len = start+off, n
			shift = 0
		} else {
			v.Counter, v.Len = start+total-(off+n), -n
			shift = -off
		}
		op.Value = v
	default:
		return op, false
	}

	// Trimming k atoms off the front moves the op's id and its lamport by k.
	// An insert's position moves by k too; a delete's as set above.
	op.Counter += off
	op.Lamport += off
	op.Pos += shift
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
