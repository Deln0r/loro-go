package loro

import (
	"slices"

	"github.com/Deln0r/loro-go/encoding/change"
)

// docState is a Document's current state: what MergeState builds from the
// applied history, kept so local ops can go in one at a time. Text and List
// containers are merged sequences; Map and Counter containers keep their ops,
// which the resolver reads.
type docState struct {
	conts map[contKey]*cinfo
	seqs  map[contKey]*seqState
	// anchored says a Text holds a style anchor no delete removed, so its
	// Unicode positions and its positions counted with anchors differ.
	anchored map[contKey]bool
	// maps is each Map's winning op per key, built when first asked for.
	maps map[contKey]map[string]Op
	work *seqWork
}

func newDocState() *docState {
	return &docState{
		conts:    map[contKey]*cinfo{},
		seqs:     map[contKey]*seqState{},
		anchored: map[contKey]bool{},
		maps:     map[contKey]map[string]Op{},
		work:     &seqWork{limit: seqWorkLimit, strict: true},
	}
}

// buildState merges changes as MergeState does and keeps the result. The
// merge is strict: the changes are causally complete, so a position past
// its author's view is an error, not something to clamp.
func buildState(changes []Change) (*docState, error) {
	st := newDocState()
	c := collect(&Updates{Changes: changes})
	seqs, err := mergeSequences(c.copies, c.units, st.work)
	if err != nil {
		return nil, err
	}
	st.conts, st.seqs = c.conts, seqs
	for k, s := range seqs {
		if k.kind != change.CText {
			continue
		}
		for _, n := range s.nodes {
			if n.anchor && len(n.deleters) == 0 {
				st.anchored[k] = true
				break
			}
		}
	}
	return st, nil
}

// rebuild replaces the state with the merge of the applied history, and of
// the open transaction's ops when one is still open. When the merge refuses
// the history the state is left as it was and the error returned.
func (d *Document) rebuild() error {
	stored := d.log.store.changes()
	changes := make([]Change, 0, len(stored)+1)
	for _, c := range stored {
		changes = append(changes, c.ch)
	}
	if t := d.tx; t != nil && len(t.ops) > 0 {
		changes = append(changes, Change{ID: ID{Peer: d.peer, Counter: t.startCounter}, Lamport: t.startLamport, Deps: t.deps, Ops: t.ops})
	}
	st, err := buildState(changes)
	if err != nil {
		return err
	}
	d.st = st
	return nil
}

// applyLocal puts one local op into the state. Its author has everything
// merged so far in view, so a sequence op takes the merge's fast path, and a
// map write has the highest lamport there is and wins its key.
func (s *docState) applyLocal(op Op) error {
	k := keyOf(op)
	s.work.steps = 0
	if isSeqOp(op) {
		q := s.seqs[k]
		if q == nil {
			q = newSeqState(s.work)
			s.seqs[k] = q
		}
		if op.VKind == change.VKMarkStart {
			s.anchored[k] = true
		}
		return q.apply(op, true, pastView{}, true)
	}
	ci := s.conts[k]
	if ci == nil {
		ci = &cinfo{}
		s.conts[k] = ci
	}
	ci.ops = append(ci.ops, op)
	if m := s.maps[k]; m != nil && op.Kind == change.CMap {
		m[op.MapKey] = op
	}
	return nil
}

// has says the container holds state: an op applied or made locally.
func (s *docState) has(k contKey) bool { return s.seqs[k] != nil || s.conts[k] != nil }

// mapEntry returns the op that decides key in Map k, and false when the key
// is absent or deleted.
func (s *docState) mapEntry(k contKey, key string) (Op, bool) {
	m := s.maps[k]
	if m == nil {
		m = map[string]Op{}
		if ci := s.conts[k]; ci != nil {
			for _, op := range sortedOps(ci.ops) {
				m[op.MapKey] = op
			}
		}
		s.maps[k] = m
	}
	op, ok := m[key]
	if !ok || op.VKind == change.VKDeleteOnce {
		return Op{}, false
	}
	return op, true
}

// liveNodes returns the sequence's elements no delete removed, anchors
// included.
func (s *seqState) liveNodes() []*seqNode {
	out := make([]*seqNode, 0, s.live)
	for _, n := range s.nodes {
		if len(n.deleters) == 0 {
			out = append(out, n)
		}
	}
	return out
}

// textLen is the number of characters a Text shows.
func (st *docState) textLen(k contKey) int64 {
	s := st.seqs[k]
	if s == nil {
		return 0
	}
	if !st.anchored[k] {
		return int64(s.live)
	}
	var n int64
	for _, x := range s.nodes {
		if len(x.deleters) == 0 && !x.anchor {
			n++
		}
	}
	return n
}

// The expand flags of a mark (container/richtext.rs:158).
const (
	expandBeforeFlag = 0b0000_0010
	expandAfterFlag  = 0b0000_0100
)

// anchorStyle returns the mark an anchor belongs to and whether it is the
// start anchor. An end anchor belongs to the mark whose start anchor has the
// id just before its own.
func (s *seqState) anchorStyle(n *seqNode) (MarkInfo, bool) {
	if m, ok := n.value.(markStart); ok {
		return m.info, true
	}
	if st := s.byID[ID{Peer: n.peer, Counter: n.counter - 1}]; st != nil {
		if m, ok := st.value.(markStart); ok {
			return m.info, false
		}
	}
	return MarkInfo{}, false
}

// preferInsertBefore is TextStyleInfoFlag::prefer_insert_before
// (container/richtext.rs:252): new text goes before a start anchor unless the
// mark expands before, and before an end anchor when it expands after.
func preferInsertBefore(info MarkInfo, start bool) bool {
	if start {
		return info.Info&expandBeforeFlag == 0
	}
	return info.Info&expandAfterFlag != 0
}

// staysBeforeAnchor is insert_stays_before_anchor
// (container/richtext/richtext_state.rs:1416): new text never goes past the
// start anchor of an unmark (a null or false value), nor past an anchor it
// prefers to stay in front of.
func staysBeforeAnchor(info MarkInfo, start bool) bool {
	if start && (info.Value == nil || info.Value == false) {
		return true
	}
	return preferInsertBefore(info, start)
}

// textInsertIndex returns the position, anchors counted, at which loro puts
// text inserted at Unicode position pos: get_entity_index_for_text_insert
// and find_best_insert_pos (richtext_state.rs:1430, :1846). Where anchors sit
// between two characters, the expand rules of their marks pick the place
// among them. Without anchors the two positions are one.
func (st *docState) textInsertIndex(k contKey, pos int64) int64 {
	s := st.seqs[k]
	if s == nil || !st.anchored[k] {
		return pos
	}
	live := s.liveNodes()
	if len(live) == 0 {
		return 0
	}
	var left int
	var entity int64
	if pos == 0 {
		if !live[0].anchor {
			return 0
		}
	} else {
		chars := int64(0)
		at := -1
		for i, x := range live {
			if !x.anchor {
				chars++
				if chars == pos {
					at = i
					break
				}
			}
		}
		if at < 0 {
			return int64(len(live))
		}
		entity = int64(at + 1)
		left = at + 1
		if left == len(live) || !live[left].anchor {
			return entity
		}
	}
	right := left
	for right < len(live) && live[right].anchor {
		right++
	}
	type visit struct {
		info  MarkInfo
		start bool
		at    int64
	}
	var visited []visit
	at := entity
	for i := left; i < right; i++ {
		info, start := s.anchorStyle(live[i])
		visited = append(visited, visit{info, start, at})
		if staysBeforeAnchor(info, start) {
			break
		}
		at++
	}
	for len(visited) > 0 {
		top := visited[len(visited)-1]
		visited = visited[:len(visited)-1]
		if !preferInsertBefore(top.info, top.start) {
			break
		}
		at = top.at
	}
	return at
}

// textRange is a run of characters one delete op removes: they sit next to
// each other, anchors counted, and their ids follow one another.
type textRange struct {
	entity int64 // position of the first, anchors counted
	n      int64
	peer   uint64
	ctr    int64 // id counter of the first
}

// textDeleteRanges is get_text_entity_ranges (richtext_state.rs:1997): the
// characters at Unicode positions [pos, pos+n), split wherever an anchor
// sits between two of them or their ids do not follow one another.
func (st *docState) textDeleteRanges(k contKey, pos, n int64) ([]textRange, error) {
	s := st.seqs[k]
	if s == nil || n == 0 {
		return nil, nil
	}
	// Start at the first character to delete. Without anchors its index
	// among live elements is pos, which the merge's cursor finds without
	// walking from the front.
	from, entity, chars := 0, int64(0), int64(0)
	if !st.anchored[k] {
		i, err := s.locate(pos+1, true, pastView{})
		if err != nil {
			return nil, err
		}
		from, entity, chars = i, pos, pos
	}
	var out []textRange
	for i := from; i < len(s.nodes) && chars < pos+n; i++ {
		x := s.nodes[i]
		if len(x.deleters) != 0 {
			continue
		}
		if x.anchor {
			entity++
			continue
		}
		if chars >= pos {
			last := len(out) - 1
			if last >= 0 && out[last].entity+out[last].n == entity &&
				out[last].peer == x.peer && out[last].ctr+out[last].n == x.counter {
				out[last].n++
			} else {
				out = append(out, textRange{entity: entity, n: 1, peer: x.peer, ctr: x.counter})
			}
		}
		chars++
		entity++
	}
	return out, s.work.spend(len(out))
}

// nthLive returns the element at position i of a List, or nil.
func (st *docState) nthLive(k contKey, i int64) (*seqNode, error) {
	s := st.seqs[k]
	if s == nil || i < 0 || i >= int64(s.live) {
		return nil, nil
	}
	j, err := s.locate(i+1, true, pastView{})
	if err != nil || j < 0 {
		return nil, err
	}
	return s.nodes[j], nil
}

// visibleRoots returns the root containers toJSON shows, one per name, as
// loro picks them (state.rs:1616 preferred_root_containers): among roots in
// the order they were registered, which have state, the last one wins its
// name, except that an empty one never hides one that is not. A root has
// state once a handle to it was taken or an op of it applied.
func (d *Document) visibleRoots() ([]contKey, map[contKey]any, error) {
	var cands []contKey
	for _, k := range d.rootOrder {
		if (d.acquired[k] && !d.knocked(ruleRootsVisible)) || d.st.has(k) {
			cands = append(cands, k)
		}
	}
	r := &resolver{conts: d.st.conts, seqs: d.st.seqs, vals: map[contKey]any{}, state: map[contKey]uint8{}}
	if err := r.resolve(cands); err != nil {
		return nil, nil, err
	}
	chosen := map[string]int{}
	var picked []contKey
	for _, k := range cands {
		i, ok := chosen[k.name]
		if !ok {
			chosen[k.name] = len(picked)
			picked = append(picked, k)
			continue
		}
		if d.knocked(ruleTypedRoots) {
			continue
		}
		if emptyRootValue(picked[i].kind, r.vals[picked[i]]) || !emptyRootValue(k.kind, r.vals[k]) {
			picked[i] = k
		}
	}
	return picked, r.vals, nil
}

// emptyRootValue is visible_container_value_is_empty (state.rs:75). A
// counter is never empty.
func emptyRootValue(kind change.ContainerType, v any) bool {
	switch kind {
	case change.CText:
		s, ok := v.(string)
		return ok && s == ""
	case change.CMap:
		m, ok := v.(map[string]any)
		return ok && len(m) == 0
	case change.CList, change.CMovableList, change.CTree:
		l, ok := v.([]any)
		return ok && len(l) == 0
	}
	return false
}

// copyValue copies maps, lists and byte slices, so a value handed out does
// not share memory with the document.
func copyValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = copyValue(e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = copyValue(e)
		}
		return out
	case []byte:
		return slices.Clone(x)
	}
	return v
}
