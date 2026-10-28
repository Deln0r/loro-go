package loro

import "testing"

// movableWork inserts n items into a MovableList that already holds some
// positions lost to concurrent moves, then moves the first item to the end
// n times, reading Len and an item on every round, and returns the work its
// index did (seqWork.movSteps) and how many positions the list then holds.
func movableWork(t *testing.T, n int, knock ruleSet) (steps int64, positions int) {
	t.Helper()
	a, b := NewDocument(1), NewDocument(2)
	a.knock, b.knock = knock, knock
	if err := a.MovableList("ml").Insert(0, "x", "y", "z"); err != nil {
		t.Fatal(err)
	}
	sync := func(from, to *Document) {
		blob, err := from.ExportUpdates()
		if err == nil {
			err = to.Import(blob)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	sync(a, b)
	// Both move "x": one of the two new positions loses, and stays live.
	if err := a.MovableList("ml").Move(0, 2); err != nil {
		t.Fatal(err)
	}
	if err := b.MovableList("ml").Move(0, 1); err != nil {
		t.Fatal(err)
	}
	sync(b, a)
	ml := a.MovableList("ml")
	k := contKey{root: true, name: "ml", kind: KindMovableList}
	if lost := len(a.st.seqs[k].mov().lost); lost == 0 {
		t.Fatal("no lost position to count around")
	}
	before := a.st.work.movSteps
	for i := range n {
		if err := ml.Insert(ml.Len(), int64(i)); err != nil {
			t.Fatal(err)
		}
	}
	for i := range n {
		if err := ml.Move(0, ml.Len()-1); err != nil {
			t.Fatal(err)
		}
		if _, err := ml.Get(i % ml.Len()); err != nil {
			t.Fatal(err)
		}
	}
	return a.st.work.movSteps - before, len(a.st.seqs[k].nodes)
}

// TestMovableListCallsDoNotScanPositions: a MovableList call costs its
// index a binary search among the positions lost to concurrent moves, not
// a scan of every position the list ever had (review finding F4). The
// workload of 2n calls over a list that ends with 3n positions does at most
// 4 steps of index work per call; with the index rebuilt on every call, as
// before, it does about one per position per call.
func TestMovableListCallsDoNotScanPositions(t *testing.T) {
	const n = 16000
	steps, positions := movableWork(t, n, 0)
	t.Logf("n = %d: %d index steps for %d calls, %d positions", n, steps, 4*n, positions)
	if steps > 4*2*n {
		t.Errorf("%d index steps for %d calls", steps, 2*n)
	}
	const small = 1000
	steps, positions = movableWork(t, small, 1<<ruleMovableIndex)
	t.Logf("n = %d, index rebuilt per call: %d index steps, %d positions", small, steps, positions)
	if steps <= 4*2*small {
		t.Errorf("rebuilt per call: %d index steps for %d calls", steps, 2*small)
	}
}
