package loro

import (
	"math/rand"
	"testing"

	"github.com/Deln0r/loro-go/encoding/change"
)

// benchMerge decodes blob once and times MergeState alone.
func benchMerge(b *testing.B, blob []byte) {
	u, err := DecodeUpdates(blob)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := MergeState(u); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkMergeTypingAtEnd is the common editing pattern: one character at a
// time, always at the end of the text.
func BenchmarkMergeTypingAtEnd(b *testing.B) {
	d := NewDoc(1)
	for i := 0; i < 20000; i++ {
		d.TextInsert("t", i, "x")
	}
	benchMerge(b, d.ExportUpdates())
}

// BenchmarkMergeTypingAnywhere inserts single characters at random positions.
func BenchmarkMergeTypingAnywhere(b *testing.B) {
	r := rand.New(rand.NewSource(1))
	d := NewDoc(1)
	for i := 0; i < 5000; i++ {
		d.TextInsert("t", r.Intn(i+1), "x")
	}
	benchMerge(b, d.ExportUpdates())
}

// BenchmarkMergeOfflineBranches merges two peers that each typed 2000 changes
// at the end of their own copy without ever syncing: every change is
// concurrent with half the merged history.
func BenchmarkMergeOfflineBranches(b *testing.B) {
	var changes []Change
	for peer := uint64(1); peer <= 2; peer++ {
		for i := int64(0); i < 2000; i++ {
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
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := MergeState(u); err != nil {
			b.Fatal(err)
		}
	}
}
