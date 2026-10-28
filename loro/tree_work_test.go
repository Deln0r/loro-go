package loro

import (
	"math/bits"
	"testing"
)

// treeWork runs three tree workloads on Documents with knock and returns
// the tree work each did (docState.treeSteps): a chain n deep built locally
// one Create at a time; that chain imported, edited at its leaf and middle
// and grown by n/10 nodes; and n top-level nodes imported, edited and
// grown by n/10 nodes.
func treeWork(t *testing.T, n int, knock ruleSet) (chain, deep, wide int64) {
	t.Helper()
	a := NewDocument(1)
	a.knock = knock
	tr := a.Tree("tr")
	p := TreeRoot
	var ids []TreeID
	for range n {
		id, err := tr.Create(p)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
		p = id
	}
	chain = a.st.treeSteps
	blob, err := a.ExportUpdates()
	if err != nil {
		t.Fatal(err)
	}
	edit := func(blob []byte, leaf, mid, under TreeID) int64 {
		b := NewDocument(2)
		b.knock = knock
		if err := b.Import(blob); err != nil {
			t.Fatal(err)
		}
		bt := b.Tree("tr")
		steps := []error{
			func() error { _, err := bt.Create(leaf); return err }(),
			func() error { _, err := bt.CreateAt(TreeRoot, 0); return err }(),
			func() error {
				m, err := bt.Meta(leaf)
				if err == nil {
					err = m.Set("k", int64(1))
				}
				return err
			}(),
			func() error { _, err := bt.Children(mid); return err }(),
			func() error { _, err := bt.Parent(leaf); return err }(),
			bt.Move(leaf, TreeRoot, 0),
			bt.Move(mid, leaf, 0),
			bt.Delete(mid),
		}
		for i, err := range steps {
			if err != nil {
				t.Fatalf("edit %d: %v", i, err)
			}
		}
		// Then n/10 nodes more, each under the last for a chain and at the
		// end of the top level otherwise.
		at := under
		for range n / 10 {
			id, err := bt.Create(at)
			if err != nil {
				t.Fatal(err)
			}
			if at != TreeRoot {
				at = id
			}
		}
		// The import built a new state; its steps are the index's build
		// and the edits.
		return b.st.treeSteps
	}
	deep = edit(blob, ids[n-1], ids[n/2], ids[n-2])
	w := NewDocument(3)
	w.knock = knock
	wt := w.Tree("tr")
	var top []TreeID
	for range n {
		id, err := wt.Create(TreeRoot)
		if err != nil {
			t.Fatal(err)
		}
		top = append(top, id)
	}
	if blob, err = w.ExportUpdates(); err != nil {
		t.Fatal(err)
	}
	wide = edit(blob, top[n-1], top[n/2], TreeRoot)
	return chain, deep, wide
}

// TestTreeEditsCostDepthAndSiblings: a local tree edit costs the depth it
// walks and the siblings it touches, not the tree's history, and an
// imported tree is indexed in O(ops log ops) (review finding F1). Each
// workload of n ops, edits included, stays within 8 n log n steps; with
// the index rebuilt on every local op and every move walked, as before,
// the same workloads at n = 300 exceed that many times over.
func TestTreeEditsCostDepthAndSiblings(t *testing.T) {
	bound := func(n int) int64 { return 8 * int64(n) * int64(bits.Len(uint(n))) }
	const n = 20000
	chain, deep, wide := treeWork(t, n, 0)
	t.Logf("n = %d: local chain %d steps, imported chain and edits %d, imported top level and edits %d; bound %d", n, chain, deep, wide, bound(n))
	for name, got := range map[string]int64{"local chain": chain, "imported chain": deep, "imported top level": wide} {
		if got > bound(n) {
			t.Errorf("%s: %d steps for %d nodes, more than %d", name, got, n, bound(n))
		}
	}
	const small = 300
	chain, deep, wide = treeWork(t, small, 1<<ruleTreeIncremental)
	t.Logf("n = %d, index rebuilt per op: local chain %d steps, imported chain and edits %d, imported top level and edits %d; bound %d", small, chain, deep, wide, bound(small))
	for name, got := range map[string]int64{"local chain": chain, "imported chain": deep, "imported top level": wide} {
		if got <= bound(small) {
			t.Errorf("rebuilt per op, %s of %d took %d steps, within %d", name, small, got, bound(small))
		}
	}
}
