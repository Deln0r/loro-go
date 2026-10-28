package loro

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"math"
	"math/bits"
	"slices"
	"strconv"
	"strings"

	"github.com/Deln0r/loro-go/encoding/change"
)

// TreeID names a tree node: the id of the op that created it.
type TreeID struct {
	Peer    uint64
	Counter int64
}

// TreeRoot, given as a parent, stands for the top level of a tree. It is
// not a node.
var TreeRoot = TreeID{Peer: math.MaxUint64, Counter: -1}

// String renders the id as loro does, "counter@peer".
func (id TreeID) String() string {
	if id == TreeRoot {
		return "root"
	}
	return strconv.FormatInt(id.Counter, 10) + "@" + strconv.FormatUint(id.Peer, 10)
}

func treeIDOf(s string) (TreeID, bool) {
	id, err := parseStoredID(s)
	if err != nil {
		return TreeID{}, false
	}
	return TreeID(id), true
}

// The fractional indexes of loro's fractional_index crate
// (crates/fractional_index/src/lib.rs): byte strings ordered as bytes,
// each ending in the terminator 0x80, which alone is the default. The
// crate's functions assume indexes it made; on others, which an imported
// change can carry (an empty one, one of zero bytes only), they reach
// unreachable!() or index out of bounds and panic. These return false
// there instead, and the edit that needed the index fails.
const fiTerminator = 0x80

func fiBefore(b []byte) ([]byte, bool) {
	for i, c := range b {
		if c > fiTerminator {
			return slices.Clone(b[:i]), true
		}
		if c > 0 {
			out := slices.Clone(b[:i+1])
			out[i]--
			return out, true
		}
	}
	return nil, false
}

func fiAfter(b []byte) ([]byte, bool) {
	for i, c := range b {
		if c < fiTerminator {
			return slices.Clone(b[:i]), true
		}
		if c < 0xff {
			out := slices.Clone(b[:i+1])
			out[i]++
			return out, true
		}
	}
	return nil, false
}

func fiBetween(left, right []byte) ([]byte, bool) {
	if len(left) == 0 || len(right) == 0 {
		return nil, false
	}
	shorter := min(len(left), len(right)) - 1
	for i := 0; i < shorter; i++ {
		l, r := int(left[i]), int(right[i])
		switch diff := r - l; {
		case diff > 1:
			out := slices.Clone(left[:i+1])
			out[i] += byte(diff / 2)
			return out, true
		case diff == 1:
			suffix, ok := fiAfter(left[i+1:])
			return fiJoin(left[:i+1], suffix, ok)
		case diff < 0:
			return nil, false
		}
	}
	switch {
	case len(left) < len(right):
		prefix := right[:shorter+1]
		if prefix[len(prefix)-1] < fiTerminator {
			return nil, false
		}
		suffix, ok := fiBefore(right[shorter+1:])
		return fiJoin(prefix, suffix, ok)
	case len(left) > len(right):
		prefix := left[:shorter+1]
		if prefix[len(prefix)-1] >= fiTerminator {
			return nil, false
		}
		suffix, ok := fiAfter(left[shorter+1:])
		return fiJoin(prefix, suffix, ok)
	}
	return nil, false
}

func fiJoin(prefix, suffix []byte, ok bool) ([]byte, bool) {
	if !ok {
		return nil, false
	}
	return append(slices.Clone(prefix), suffix...), true
}

// fiNew is FractionalIndex::new: an index between lower and upper, either
// of which may be absent.
func fiNew(lower, upper []byte) ([]byte, bool) {
	var out []byte
	switch {
	case lower != nil && upper != nil:
		b, ok := fiBetween(lower, upper)
		if !ok {
			return nil, false
		}
		out = b
	case lower != nil:
		b, ok := fiAfter(lower)
		if !ok {
			return nil, false
		}
		out = b
	case upper != nil:
		b, ok := fiBefore(upper)
		if !ok {
			return nil, false
		}
		out = b
	default:
		return []byte{fiTerminator}, true
	}
	return append(out, fiTerminator), true
}

// fiEvenly is FractionalIndex::generate_n_evenly: n indexes between lower
// and upper, the middle one first, then each half the same way.
func fiEvenly(lower, upper []byte, n int) ([][]byte, bool) {
	if lower != nil && upper != nil && bytes.Compare(lower, upper) >= 0 {
		return nil, false
	}
	var out [][]byte
	var gen func(lower, upper []byte, n int) bool
	gen = func(lower, upper []byte, n int) bool {
		if n == 0 {
			return true
		}
		mid, ok := fiNew(lower, upper)
		if !ok {
			return false
		}
		if n == 1 {
			out = append(out, mid)
			return true
		}
		if !gen(lower, mid, n/2) {
			return false
		}
		out = append(out, mid)
		return gen(mid, upper, n-n/2-1)
	}
	return out, gen(lower, upper, n)
}

// treeIndex is a Tree's current shape, as loro's TreeState keeps it: each
// node's parent, fractional index and the (lamport, peer) of the move that
// put it there, and each parent's children in order. It is built once from
// the container's ops, as buildTree applies them, and then kept current by
// each local op (tree_state.rs:673 mov), so an edit costs the depth it
// walks and the siblings it touches, not the tree's history.
type treeIndex struct {
	nodes map[string]treePlace
	// children holds each live parent's children in order: "" for the top
	// level. Nodes under deletedTreeRoot keep no list: nothing asks for
	// their order, and a local op never moves a deleted node.
	children map[string][]string
	// childCount is how many nodes have each node as their parent, the
	// deleted ones included: a node with none is nobody's ancestor.
	childCount map[string]int
	// err is set when building the index walked past buildTree's budget.
	err error
	// steps counts the work done on the index: ops replayed, ancestors
	// walked, children compared or shifted.
	steps *int64
}

type treePlace struct {
	parent  string // "" for the top level
	fi      string // uppercase hex
	lamport int64
	peer    uint64
}

func (a treePlace) before(b treePlace) bool {
	if a.fi != b.fi {
		return a.fi < b.fi
	}
	if a.lamport != b.lamport {
		return a.lamport < b.lamport
	}
	return a.peer < b.peer
}

func (a treePlace) compare(b treePlace) int {
	switch {
	case a.before(b):
		return -1
	case b.before(a):
		return 1
	}
	return 0
}

// tree returns Tree k's index, building it when the state has none: its
// ops in (lamport, peer) order, each move that would close a cycle
// skipped, then each parent's children sorted once. Only a node that has
// children can be an ancestor, so creating a node never walks the tree,
// and a chain imported one level at a time builds in O(ops log ops).
func (st *docState) tree(k contKey) *treeIndex {
	if t := st.trees[k]; t != nil {
		return t
	}
	t := &treeIndex{nodes: map[string]treePlace{}, children: map[string][]string{}, childCount: map[string]int{}, steps: &st.treeSteps}
	var ops []Op
	if ci := st.conts[k]; ci != nil {
		ops = ci.ops
	}
	full := st.knockTree
	budget := int64(treeWalkFloor + treeWalkPerMove*len(ops))
	walked := int64(0)
	for _, op := range sortedOps(ops) {
		n, ok := op.Value.(TreeNode)
		if op.VKind != change.VKRawTreeMove || !ok {
			continue
		}
		*t.steps++
		parent := treeParentOf(n)
		if parent != "" && parent != deletedTreeRoot && (parent == n.ID || full || t.childCount[n.ID] > 0) {
			before := *t.steps
			cyclic := t.isAncestor(n.ID, parent)
			if walked += *t.steps - before; walked > budget {
				t.err = fmt.Errorf("loro: tree ancestry checks exceeded %d steps for %d ops", budget, len(ops))
				break
			}
			if cyclic {
				continue
			}
		}
		if old, ok := t.nodes[n.ID]; ok {
			t.childCount[old.parent]--
		}
		t.nodes[n.ID] = treePlace{parent: parent, fi: n.FI, lamport: op.Lamport, peer: op.Peer}
		t.childCount[parent]++
	}
	for id, p := range t.nodes {
		if p.parent != deletedTreeRoot {
			t.children[p.parent] = append(t.children[p.parent], id)
		}
	}
	for _, kids := range t.children {
		*t.steps += int64(len(kids)) * int64(bits.Len(uint(len(kids))))
		slices.SortFunc(kids, func(a, b string) int { return t.nodes[a].compare(t.nodes[b]) })
	}
	st.trees[k] = t
	return t
}

func treeParentOf(n TreeNode) string {
	if n.HasParent {
		return n.Parent
	}
	return ""
}

// applyLocal puts a local tree op into the index: it unlinks the node from
// where it was and links it where the op puts it, each by a binary search
// among the siblings. The op has the greatest lamport the document has
// seen, so it lands where replaying the ops in order would put it. Move
// has refused a cycle already; a node with children is checked again all
// the same, as building does.
func (t *treeIndex) applyLocal(op Op) {
	n, ok := op.Value.(TreeNode)
	if op.VKind != change.VKRawTreeMove || !ok {
		return
	}
	*t.steps++
	parent := treeParentOf(n)
	if parent != "" && parent != deletedTreeRoot && (parent == n.ID || t.childCount[n.ID] > 0) && t.isAncestor(n.ID, parent) {
		return
	}
	if old, ok := t.nodes[n.ID]; ok {
		t.childCount[old.parent]--
		t.unlink(n.ID, old)
	}
	place := treePlace{parent: parent, fi: n.FI, lamport: op.Lamport, peer: op.Peer}
	t.nodes[n.ID] = place
	t.childCount[parent]++
	if parent == deletedTreeRoot {
		return
	}
	kids := t.children[parent]
	i, _ := slices.BinarySearchFunc(kids, place, func(id string, p treePlace) int { return t.nodes[id].compare(p) })
	*t.steps += int64(bits.Len(uint(len(kids)))) + int64(len(kids)-i)
	t.children[parent] = slices.Insert(kids, i, n.ID)
}

// unlink removes node, which sits at place, from its parent's children.
func (t *treeIndex) unlink(node string, place treePlace) {
	if place.parent == deletedTreeRoot {
		return
	}
	kids := t.children[place.parent]
	i, found := slices.BinarySearchFunc(kids, place, func(id string, p treePlace) int { return t.nodes[id].compare(p) })
	*t.steps += int64(bits.Len(uint(len(kids)))) + int64(len(kids)-i)
	if found && kids[i] == node {
		t.children[place.parent] = slices.Delete(kids, i, i+1)
	}
}

// isAncestor reports whether node is parent or one of parent's ancestors.
func (t *treeIndex) isAncestor(node, parent string) bool {
	for at, steps := parent, 0; at != "" && at != deletedTreeRoot && steps <= len(t.nodes); steps++ {
		*t.steps++
		if at == node {
			return true
		}
		p, ok := t.nodes[at]
		if !ok {
			return false
		}
		at = p.parent
	}
	return false
}

// alive reports whether node exists and neither it nor an ancestor is
// deleted.
func (t *treeIndex) alive(node string) bool {
	for at, steps := node, 0; steps <= len(t.nodes); steps++ {
		*t.steps++
		p, ok := t.nodes[at]
		if !ok {
			return false
		}
		switch p.parent {
		case deletedTreeRoot:
			return false
		case "":
			return true
		}
		at = p.parent
	}
	return false
}

// indexOf is node's index among its siblings, -1 when it is deleted or
// does not exist.
func (t *treeIndex) indexOf(node string) int {
	p, ok := t.nodes[node]
	if !ok || p.parent == deletedTreeRoot {
		return -1
	}
	kids := t.children[p.parent]
	*t.steps += int64(bits.Len(uint(len(kids))))
	i, found := slices.BinarySearchFunc(kids, p, func(id string, q treePlace) int { return t.nodes[id].compare(q) })
	if !found {
		return -1
	}
	return i
}

// Tree is a handle on a Tree container: a movable tree of nodes, each with
// a meta map, siblings ordered by fractional index.
type Tree struct{ h handle }

// Tree returns a handle on the root Tree container called name.
func (d *Document) Tree(name string) *Tree { return &Tree{d.handle(name, KindTree)} }

// Kind returns KindTree.
func (t *Tree) Kind() ContainerKind { return t.h.kind() }

// ID returns the container's id.
func (t *Tree) ID() string { return t.h.id() }

// IsDeleted reports whether the container is gone from the document.
func (t *Tree) IsDeleted() bool { return t.h.isDeleted() }

// JSON returns the tree as toJSON shows it, nodes nested in their parents.
func (t *Tree) JSON() (any, error) { return t.h.json() }

func parentKey(parent TreeID) string {
	if parent == TreeRoot {
		return ""
	}
	return parent.String()
}

// Create adds a node as the last child of parent (TreeRoot for the top
// level) and returns its id.
func (t *Tree) Create(parent TreeID) (TreeID, error) {
	d := t.h.d
	d.mu.Lock()
	defer d.mu.Unlock()
	st := t.h.state()
	ti := st.tree(t.h.k)
	if ti.err != nil {
		return TreeID{}, ti.err
	}
	return t.create(st, parent, len(ti.children[parentKey(parent)]))
}

// CreateAt adds a node as child number index of parent and returns its id.
func (t *Tree) CreateAt(parent TreeID, index int) (TreeID, error) {
	d := t.h.d
	d.mu.Lock()
	defer d.mu.Unlock()
	st := t.h.state()
	ti := st.tree(t.h.k)
	if ti.err != nil {
		return TreeID{}, ti.err
	}
	if n := len(ti.children[parentKey(parent)]); index < 0 || index > n {
		return TreeID{}, fmt.Errorf("loro: index %d is out of bounds for %d children", index, n)
	}
	return t.create(st, parent, index)
}

// create is create_with_txn (handler/tree.rs:612): the new node's id is the
// next op's, its fractional index fits between its neighbours, and when
// those have equal indexes the node and the siblings that share them are
// spread out anew, each sibling moved by an op of its own.
func (t *Tree) create(st *docState, parent TreeID, index int) (TreeID, error) {
	d := t.h.d
	if err := d.editable(t.h.k); err != nil {
		return TreeID{}, err
	}
	ti := st.tree(t.h.k)
	pk := parentKey(parent)
	if pk != "" {
		if _, ok := ti.nodes[pk]; !ok {
			return TreeID{}, fmt.Errorf("loro: tree node %s does not exist", pk)
		}
	}
	tx := d.begin()
	target := TreeID{Peer: d.peer, Counter: tx.nextCounter}
	ids, fis, err := t.positions(ti, target.String(), pk, index)
	if err != nil {
		return TreeID{}, err
	}
	for i, id := range ids {
		if err := t.place(st, id, pk, fis[i]); err != nil {
			return TreeID{}, err
		}
	}
	return target, nil
}

// positions is generate_fi_at (state/tree_state.rs:170) with jitter 0,
// loro's default: the index for target as child number index of parent,
// target being out of the children. When the neighbours on both sides
// have one index, the siblings from index on that share it are given new
// indexes too, evenly between the left one and the next different one.
func (t *Tree) positions(ti *treeIndex, target, parent string, index int) ([]string, [][]byte, error) {
	// The children without target, read in place: skip is target's index
	// among them, or past the end when it is not one.
	all := ti.children[parent]
	skip := len(all)
	if p, ok := ti.nodes[target]; ok && p.parent == parent {
		skip = ti.indexOf(target)
	}
	n := len(all)
	if skip < len(all) {
		n--
	}
	kid := func(i int) string {
		if i >= skip {
			i++
		}
		return all[i]
	}
	if n == 0 {
		return []string{target}, [][]byte{{fiTerminator}}, nil
	}
	fiOf := func(id string) []byte {
		b, _ := hex.DecodeString(ti.nodes[id].fi)
		return b
	}
	var left, right []byte
	if index > 0 {
		left = fiOf(kid(index - 1))
	}
	if index < n {
		right = fiOf(kid(index))
	}
	reset := []string{target}
	var next []byte
	if left != nil && right != nil && bytes.Equal(left, right) && !t.h.d.knocked(ruleTreeRearrange) {
		reset = append(reset, kid(index))
		for i := index + 1; i < n; i++ {
			*ti.steps++
			if f := fiOf(kid(i)); bytes.Equal(f, left) {
				reset = append(reset, kid(i))
			} else {
				next = f
				break
			}
		}
	}
	if len(reset) == 1 {
		fi, ok := fiNew(left, right)
		if !ok {
			return nil, nil, fmt.Errorf("loro: no fractional index fits between %X and %X", left, right)
		}
		return reset, [][]byte{fi}, nil
	}
	fis, ok := fiEvenly(left, next, len(reset))
	if !ok {
		return nil, nil, fmt.Errorf("loro: no fractional indexes fit between %X and %X", left, next)
	}
	return reset, fis, nil
}

// place makes the tree op that puts node under parent at fractional index
// fi: a create when the node is new, a move otherwise.
func (t *Tree) place(st *docState, node, parent string, fi []byte) error {
	op := t.h.op(change.VKRawTreeMove)
	n := TreeNode{ID: node, FI: strings.ToUpper(hex.EncodeToString(fi))}
	if parent != "" {
		n.HasParent, n.Parent = true, parent
	}
	op.Value = n
	return t.h.d.apply(st, op, opProv{})
}

// Move puts node under parent (TreeRoot for the top level) as child number
// index, counted without the node. Moving a node where it already is makes
// no op; moving it under itself or one of its descendants is an error and
// records nothing.
func (t *Tree) Move(node, parent TreeID, index int) error {
	d := t.h.d
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.editable(t.h.k); err != nil {
		return err
	}
	st := t.h.state()
	ti := st.tree(t.h.k)
	if ti.err != nil {
		return ti.err
	}
	nk, pk := node.String(), parentKey(parent)
	cur, exists := ti.nodes[nk]
	n := len(ti.children[pk])
	if exists && cur.parent == pk {
		// mov_with_txn (handler/tree.rs:710): already there is no op.
		if i := ti.indexOf(nk); i == index && !d.knocked(ruleTreeMoveInPlace) {
			return nil
		}
		n--
	}
	if index < 0 || index > n {
		return fmt.Errorf("loro: index %d is out of bounds for %d children", index, n)
	}
	if !exists || cur.parent == deletedTreeRoot {
		return fmt.Errorf("loro: tree node %s is deleted or does not exist", nk)
	}
	if pk != "" {
		if _, ok := ti.nodes[pk]; !ok {
			return fmt.Errorf("loro: tree node %s does not exist", pk)
		}
		if (pk == nk || ti.childCount[nk] > 0) && ti.isAncestor(nk, pk) && !d.knocked(ruleTreeCycle) {
			return fmt.Errorf("loro: moving tree node %s under %s would make it its own ancestor", nk, pk)
		}
	}
	ids, fis, err := t.positions(ti, nk, pk, index)
	if err != nil {
		return err
	}
	for i, id := range ids {
		if err := t.place(st, id, pk, fis[i]); err != nil {
			return err
		}
	}
	return nil
}

// Delete deletes node and with it its descendants: a move under loro's
// deleted root. A node that is deleted already, or does not exist, is an
// error.
func (t *Tree) Delete(node TreeID) error {
	d := t.h.d
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.editable(t.h.k); err != nil {
		return err
	}
	st := t.h.state()
	nk := node.String()
	ti := st.tree(t.h.k)
	if ti.err != nil {
		return ti.err
	}
	if ti.indexOf(nk) < 0 {
		return fmt.Errorf("loro: tree node %s is deleted or does not exist", nk)
	}
	op := t.h.op(change.VKRawTreeMove)
	op.Value = TreeNode{ID: nk, HasParent: true, Parent: deletedTreeRoot}
	return d.apply(st, op, opProv{})
}

// Children returns parent's children in order (TreeRoot for the top level).
func (t *Tree) Children(parent TreeID) ([]TreeID, error) {
	d := t.h.d
	d.mu.Lock()
	defer d.mu.Unlock()
	ti := t.h.state().tree(t.h.k)
	if ti.err != nil {
		return nil, ti.err
	}
	pk := parentKey(parent)
	if pk != "" {
		if _, ok := ti.nodes[pk]; !ok {
			return nil, fmt.Errorf("loro: tree node %s does not exist", pk)
		}
	}
	var out []TreeID
	for _, id := range ti.children[pk] {
		if tid, ok := treeIDOf(id); ok {
			out = append(out, tid)
		}
	}
	return out, nil
}

// Parent returns node's parent, TreeRoot for a top-level node. A deleted
// node, or one that does not exist, is an error.
func (t *Tree) Parent(node TreeID) (TreeID, error) {
	d := t.h.d
	d.mu.Lock()
	defer d.mu.Unlock()
	ti := t.h.state().tree(t.h.k)
	if ti.err != nil {
		return TreeID{}, ti.err
	}
	p, ok := ti.nodes[node.String()]
	switch {
	case !ok:
		return TreeID{}, fmt.Errorf("loro: tree node %s does not exist", node)
	case p.parent == deletedTreeRoot:
		return TreeID{}, fmt.Errorf("loro: tree node %s is deleted", node)
	case p.parent == "":
		return TreeRoot, nil
	}
	id, _ := treeIDOf(p.parent)
	return id, nil
}

// Meta returns the map that holds node's data: the Map container named by
// the node's id (TreeID::associated_meta_container). Once the node is
// deleted, the map is too: its contents stay readable and edits are
// refused.
func (t *Tree) Meta(node TreeID) (*Map, error) {
	d := t.h.d
	d.mu.Lock()
	defer d.mu.Unlock()
	ti := t.h.state().tree(t.h.k)
	if ti.err != nil {
		return nil, ti.err
	}
	if _, ok := ti.nodes[node.String()]; !ok {
		return nil, fmt.Errorf("loro: tree node %s does not exist", node)
	}
	return &Map{handle{d: d, k: contKey{name: node.String(), kind: KindMap}, st: d.st}}, nil
}
