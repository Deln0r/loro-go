package loro

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Deln0r/loro-go/encoding/change"
)

// corpusInput reads the single []byte argument out of a go fuzz corpus file.
func corpusInput(t *testing.T, target, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "fuzz", target, name))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if rest, ok := strings.CutPrefix(line, "[]byte("); ok {
			s, err := strconv.Unquote(strings.TrimSuffix(rest, ")"))
			if err != nil {
				t.Fatal(err)
			}
			return []byte(s)
		}
	}
	t.Fatalf("%s/%s: no []byte argument", target, name)
	return nil
}

// TestInsertLengthMismatchIsRejected replays the second input the resealed
// fuzzer found. It holds two text inserts of length 1 that each carry two
// characters, so the merge created elements whose ids belonged to the next op
// and one insert became its own left neighbour; the flatten then recursed until
// the process died. Such an op is malformed and must be refused at decode.
func TestInsertLengthMismatchIsRejected(t *testing.T) {
	data := reseal(corpusInput(t, "FuzzDecodeUpdates", "6708d8421908ebbe"))
	_, err := DecodeUpdates(data)
	if err == nil {
		t.Fatal("inserts whose length disagrees with their value decoded without error")
	}
	if !strings.Contains(err.Error(), "carries") {
		t.Fatalf("expected the insert-length error, got: %v", err)
	}
}

// TestSequenceMergeSurvivesCollidingIDs checks the merge beneath that
// rejection. A text insert and a mark built by hand claim the same ids, the
// shape the malformed inserts produced; the merge places elements by scanning
// what it has already merged, so a collision can only misplace text, never
// send it into a loop.
func TestSequenceMergeSurvivesCollidingIDs(t *testing.T) {
	text := func(counter, lamport, pos int64, s string) Op {
		return Op{Container: "t", IsRoot: true, Kind: change.CText, VKind: change.VKStr,
			Pos: pos, Value: s, Len: int64(len(s)), Peer: 1, Counter: counter, Lamport: lamport}
	}
	mark := Op{Container: "t", IsRoot: true, Kind: change.CText, VKind: change.VKMarkStart,
		Pos: 1, Value: MarkInfo{Start: 1, Len: 1}, Len: 1, Peer: 1, Counter: 1, Lamport: 2}
	u := &Updates{Changes: []Change{
		{ID: ID{Peer: 1, Counter: 0}, Ops: []Op{text(0, 0, 0, "ab"), mark, text(1, 3, 1, "c")}},
	}}
	if _, err := MergeState(u); err != nil {
		t.Fatalf("merge: %v", err)
	}
}

// TestSequenceMergeRejectsForwardDeps refuses a change that depends on an op
// whose lamport is not below its own. loro gives every change a lamport above
// all it depends on, so such a dep is forward or circular, and a merge that
// took it as history could resolve positions against ops not yet made.
func TestSequenceMergeRejectsForwardDeps(t *testing.T) {
	ins := func(peer uint64, lamport int64) Op {
		return Op{Container: "t", IsRoot: true, Kind: change.CText, VKind: change.VKStr,
			Value: "x", Len: 1, Peer: peer, Lamport: lamport}
	}
	u := &Updates{Changes: []Change{
		{ID: ID{Peer: 1}, Lamport: 5, Deps: []ID{{Peer: 2}}, Ops: []Op{ins(1, 5)}},
		{ID: ID{Peer: 2}, Lamport: 5, Deps: []ID{{Peer: 1}}, Ops: []Op{ins(2, 5)}},
	}}
	_, err := MergeState(u)
	if err == nil || !strings.Contains(err.Error(), "depends on") {
		t.Fatalf("circular deps merged: err = %v", err)
	}
}

// TestHugeDeleteSpanIsCheap feeds the merge a delete whose span claims two
// billion ids. Its cost has to follow the elements that exist, not the length
// the input declares.
func TestHugeDeleteSpanIsCheap(t *testing.T) {
	ops := []Op{
		{Container: "t", IsRoot: true, Kind: change.CText, VKind: change.VKStr, Value: "abc", Len: 3, Peer: 1},
		{Container: "t", IsRoot: true, Kind: change.CText, VKind: change.VKDeleteSeq,
			Value: DeleteSpan{Peer: 1, Counter: 1, Len: 1 << 31}, Len: 1 << 31, Peer: 1, Counter: 3, Lamport: 3},
	}
	u := &Updates{Changes: []Change{{ID: ID{Peer: 1}, Ops: ops}}}
	state, err := mergeState(u, 1000)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if state["t"] != "a" {
		t.Fatalf("t = %q, want %q", state["t"], "a")
	}
}

// TestSequenceMergeStopsAtItsStepLimit runs two branches that never synced
// under a small step limit. Every op is concurrent with the other branch and
// is resolved by a scan, so the limit is reached and the merge says so instead
// of running on.
func TestSequenceMergeStopsAtItsStepLimit(t *testing.T) {
	var changes []Change
	for peer := uint64(1); peer <= 2; peer++ {
		for i := int64(0); i < 200; i++ {
			ch := Change{ID: ID{Peer: peer, Counter: i}, Lamport: i}
			if i > 0 {
				ch.Deps = []ID{{Peer: peer, Counter: i - 1}}
			}
			ch.Ops = []Op{{Container: "t", IsRoot: true, Kind: change.CText, VKind: change.VKStr,
				Pos: i, Value: "x", Len: 1, Peer: peer, Counter: i, Lamport: i}}
			changes = append(changes, ch)
		}
	}
	u := &Updates{Changes: changes}
	if _, err := mergeState(u, seqWorkLimit); err != nil {
		t.Fatalf("within the real limit: %v", err)
	}
	if _, err := mergeState(u, 10000); err == nil || !strings.Contains(err.Error(), "exceeded") {
		t.Fatalf("under a 10000-step limit: err = %v", err)
	}
}

// TestManyDeletesOfOneElementStayLinear sends one element a hundred thousand
// separate deletes. Each delete records its atom on the element, so the cost
// has to stay one step per delete: checking the element's earlier deletes
// first, as an earlier draft did, made this quadratic, five billion
// comparisons for an input of a few hundred kilobytes.
func TestManyDeletesOfOneElementStayLinear(t *testing.T) {
	const n = 100000
	ops := []Op{{Container: "t", IsRoot: true, Kind: change.CText, VKind: change.VKStr, Value: "x", Len: 1, Peer: 1}}
	for i := int64(1); i <= n; i++ {
		ops = append(ops, Op{Container: "t", IsRoot: true, Kind: change.CText, VKind: change.VKDeleteSeq,
			Value: DeleteSpan{Peer: 1, Counter: 0, Len: 1}, Len: 1, Peer: 1, Counter: i, Lamport: i})
	}
	u := &Updates{Changes: []Change{{ID: ID{Peer: 1}, Ops: ops}}}
	state, err := mergeState(u, 8*n)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if state["t"] != "" {
		t.Fatalf("t = %q, want empty", state["t"])
	}
}
