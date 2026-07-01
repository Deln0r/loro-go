package loro

import (
	"math/rand"
	"reflect"
	"testing"
)

// mergeSeqRef is the original per-op re-flatten merge, kept as a reference oracle
// for the optimized mergeSeq. It must produce identical output.
func mergeSeqRef(ops []Op, isText bool) []elem {
	var all []elem
	for _, op := range sortedOps(ops) {
		items := expandItems(op, isText)
		pos := int(op.Pos)
		var lp uint64
		var lc int64
		hasLeft := false
		if pos > 0 {
			proj := flatten(causalProjectionRef(all, op))
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

func causalProjectionRef(all []elem, op Op) []elem {
	var out []elem
	for _, e := range all {
		if e.peer == op.Peer && e.counter < op.Counter {
			out = append(out, e)
		}
	}
	return out
}

// genTextOps builds a realistic single-peer text insert stream (sequential
// counters, monotonic lamport, positions within the current length).
func genTextOps(r *rand.Rand, nops int) []Op {
	var ops []Op
	var counter, clock int64
	vis := 0
	for i := 0; i < nops; i++ {
		l := 1 + r.Intn(3)
		s := make([]rune, l)
		for k := range s {
			s[k] = rune('a' + r.Intn(26))
		}
		ops = append(ops, Op{Peer: 1, Counter: counter, Lamport: clock, Pos: int64(r.Intn(vis + 1)), Value: string(s)})
		counter += int64(l)
		clock += int64(l)
		vis += l
	}
	return ops
}

// genTwoPeerListOps builds a realistic two-peer list insert stream. Each peer has
// its own sequential counters and a shared increasing lamport clock.
func genTwoPeerListOps(r *rand.Rand, nops int) []Op {
	counter := map[uint64]int64{1: 0, 2: 0}
	vis := map[uint64]int{1: 0, 2: 0}
	var clock int64
	var ops []Op
	for i := 0; i < nops; i++ {
		peer := uint64(1 + r.Intn(2))
		l := 1 + r.Intn(3)
		items := make([]any, l)
		for k := range items {
			items[k] = int64(r.Intn(1000))
		}
		ops = append(ops, Op{Peer: peer, Counter: counter[peer], Lamport: clock, Pos: int64(r.Intn(vis[peer] + 1)), Value: items})
		counter[peer] += int64(l)
		vis[peer] += l
		clock += int64(l)
	}
	return ops
}

// TestMergeSeqMatchesReference asserts the optimized mergeSeq is byte-for-byte
// equivalent to the reference algorithm across many random single-peer text and
// two-peer list histories, including the concurrent-insert orderings that the
// fixed fixture set does not exhaust.
func TestMergeSeqMatchesReference(t *testing.T) {
	for seed := 0; seed < 400; seed++ {
		r := rand.New(rand.NewSource(int64(seed)))
		text := genTextOps(r, 1+r.Intn(60))
		if got, want := mergeSeq(text, true), mergeSeqRef(text, true); !reflect.DeepEqual(got, want) {
			t.Fatalf("text seed %d: mergeSeq diverged from reference\n got  %+v\n want %+v", seed, got, want)
		}
		list := genTwoPeerListOps(r, 1+r.Intn(60))
		if got, want := mergeSeq(list, false), mergeSeqRef(list, false); !reflect.DeepEqual(got, want) {
			t.Fatalf("list seed %d: mergeSeq diverged from reference\n got  %+v\n want %+v", seed, got, want)
		}
	}
}
