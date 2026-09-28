package loro

import (
	"strconv"
	"strings"
	"testing"

	"github.com/Deln0r/loro-go/encoding/change"
)

// treeMove builds one tree op: node placed under parent, or at the root when
// parent is empty.
func treeMove(lamport int64, node, parent string) Op {
	n := TreeNode{ID: node, FI: "80"}
	if parent != "" {
		n.HasParent = true
		n.Parent = parent
	}
	return Op{
		Container: "tr",
		IsRoot:    true,
		Kind:      change.CTree,
		VKind:     change.VKRawTreeMove,
		Value:     n,
		Peer:      1,
		Counter:   lamport,
		Lamport:   lamport,
		Len:       1,
	}
}

func nodeID(i int) string { return strconv.Itoa(i) + "@1" }

// chain returns depth ops that build a single path: each node the only child of
// the one before it.
func chain(depth int) []Op {
	ops := make([]Op, 0, depth)
	for i := 0; i < depth; i++ {
		parent := ""
		if i > 0 {
			parent = nodeID(i - 1)
		}
		ops = append(ops, treeMove(int64(i), nodeID(i), parent))
	}
	return ops
}

// TestTreeDeepChainIsLinear pins the shortcut that keeps building a deep tree
// linear. Every creation is a move under the previous node, and walking up from
// each new parent to check for a cycle costs the current depth, so a chain of n
// nodes used to cost about n*n/2 steps: 65 s for an honest 60 000-level tree
// from loro-crdt. A new node has no children and cannot close a cycle, so it
// needs no walk. No wall-clock bound here: 10 000 levels needs about 5e7 walk
// steps without the shortcut, which is past the ancestry budget, so losing the
// shortcut turns this into an error rather than a slow pass.
func TestTreeDeepChainIsLinear(t *testing.T) {
	const depth = 10000
	u := &Updates{Changes: []Change{{Ops: chain(depth)}}}
	state, err := MergeState(u)
	if err != nil {
		t.Fatalf("a %d-level chain of creations did not merge: %v", depth, err)
	}
	level, _ := state["tr"].([]any)
	got := 0
	for len(level) == 1 {
		got++
		node, _ := level[0].(map[string]any)
		level, _ = node["children"].([]any)
	}
	if got != depth {
		t.Fatalf("walked %d levels, want %d", got, depth)
	}
}

// TestTreeHostileMovesAreBounded pins the budget on the walks that remain.
// Moving the top of a deep chain under its own leaf is always a cycle, but
// finding that out walks the whole chain, and a crafted history can repeat it
// on every op. Without a bound that is O(depth * moves) of CPU per merge.
func TestTreeHostileMovesAreBounded(t *testing.T) {
	const depth, moves = 3000, 3000
	ops := chain(depth)
	for k := 0; k < moves; k++ {
		ops = append(ops, treeMove(int64(depth+k), nodeID(0), nodeID(depth-1)))
	}
	_, err := MergeState(&Updates{Changes: []Change{{Ops: ops}}})
	if err == nil {
		t.Fatalf("%d cycle-closing moves over a %d-level chain merged without hitting the ancestry budget", moves, depth)
	}
	if !strings.Contains(err.Error(), "ancestry") {
		t.Fatalf("expected the ancestry budget error, got: %v", err)
	}
}
