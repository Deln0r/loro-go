package loro

import (
	"fmt"
	"testing"
)

// TestFrontiersOrder pins the order loro-crdt 1.17.1 lists dependencies
// in, as read from its exports: for every pair of peers in
// script_corpus.json, the same order every time (the pair 3 and 7 lands in
// one bucket, so it keeps the order its ids went in), and for a peer that
// imported one change of each of 9 peers in ascending order, which fills a
// table of 16 buckets, probed 8 at a time.
func TestFrontiersOrder(t *testing.T) {
	for _, c := range []struct{ in, want []uint64 }{
		{[]uint64{1, 2}, []uint64{2, 1}},
		{[]uint64{2, 1}, []uint64{2, 1}},
		{[]uint64{1, 3}, []uint64{3, 1}},
		{[]uint64{2, 3}, []uint64{3, 2}},
		{[]uint64{3, 7}, []uint64{3, 7}},
		{[]uint64{7, 1000}, []uint64{1000, 7}},
		{[]uint64{3, 1000}, []uint64{1000, 3}},
		{[]uint64{4, 9}, []uint64{4, 9}},
		{[]uint64{9, 1<<40 + 5}, []uint64{1<<40 + 5, 9}},
		{[]uint64{4, 1<<40 + 5}, []uint64{4, 1<<40 + 5}},
		{[]uint64{12, 29, 46, 47, 64, 81, 98, 115, 132}, []uint64{64, 81, 98, 12, 29, 46, 47, 115, 132}},
	} {
		var ids []ID
		for i, p := range c.in {
			ids = append(ids, ID{Peer: p, Counter: int64(i)})
		}
		var got []uint64
		for _, id := range depsOrder(ids) {
			got = append(got, id.Peer)
		}
		if fmt.Sprint(got) != fmt.Sprint(c.want) {
			t.Errorf("%v: got %v, want %v", c.in, got, c.want)
		}
	}
}

// TestFrontiersTable checks the hash table's bookkeeping: a later id of a
// listed peer replaces it, a removed id leaves its bucket free, a fourth
// peer grows the table to 8 buckets, and the map turns back into a single id.
func TestFrontiersTable(t *testing.T) {
	var f frontiers
	for _, id := range []ID{{1, 0}, {2, 5}, {1, 3}, {1, 2}} {
		f.push(id)
	}
	if got := fmt.Sprint(f.ids()); got != "[{2 5} {1 3}]" {
		t.Fatalf("ids %s", got)
	}
	f.remove(ID{Peer: 2, Counter: 4})
	if f.len() != 2 {
		t.Fatal("removed an id that is not listed")
	}
	f.push(ID{Peer: 3, Counter: 0})
	f.push(ID{Peer: 4, Counter: 0})
	if b := f.tab.buckets(); b != 8 || f.len() != 4 {
		t.Fatalf("%d buckets for %d ids", b, f.len())
	}
	for _, id := range []ID{{1, 3}, {3, 0}, {4, 0}} {
		f.remove(id)
	}
	if f.tab != nil || f.n != 1 || f.one != (ID{Peer: 2, Counter: 5}) {
		t.Fatalf("one id left, got %+v", f)
	}
	f.updateOnNewChange(ID{Peer: 9, Counter: 0}, []ID{{Peer: 2, Counter: 5}})
	if got := fmt.Sprint(f.ids()); got != "[{9 0}]" {
		t.Fatalf("after a change on the frontiers: %s", got)
	}
	// Many peers in and out: the table keeps every id it holds, once.
	var g frontiers
	for p := uint64(1); p <= 40; p++ {
		g.push(ID{Peer: p, Counter: 1})
		if p%3 == 0 {
			g.remove(ID{Peer: p - 1, Counter: 1})
		}
	}
	seen := map[uint64]bool{}
	for _, id := range g.ids() {
		if seen[id.Peer] {
			t.Fatalf("peer %d listed twice", id.Peer)
		}
		seen[id.Peer] = true
	}
	if len(seen) != g.len() || g.len() != 27 {
		t.Fatalf("%d ids listed, %d held", len(seen), g.len())
	}
}
