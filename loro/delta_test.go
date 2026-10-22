package loro

import (
	"encoding/base64"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// deltaJSON renders a delta the way loro's toDelta() serialises it.
func deltaJSON(delta []DeltaOp) any {
	out := make([]any, len(delta))
	for i, d := range delta {
		m := map[string]any{"insert": d.Insert}
		if len(d.Attributes) > 0 {
			m["attributes"] = d.Attributes
		}
		out[i] = m
	}
	return normalize(out)
}

// readDelta reads a delta loro-crdt recorded in testdata/fixtures.
func readDelta(t *testing.T, file string) any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "testdata", "fixtures", file))
	if err != nil {
		t.Fatal(err)
	}
	var want any
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	return want
}

// decodeFixture decodes the FastUpdates blobs of testdata/fixtures named by
// files into one Updates, in that order.
func decodeFixture(t *testing.T, files ...string) *Updates {
	t.Helper()
	all := &Updates{}
	for _, file := range files {
		blob, err := os.ReadFile(filepath.Join("..", "testdata", "fixtures", file))
		if err != nil {
			t.Fatal(err)
		}
		u, err := DecodeUpdates(blob)
		if err != nil {
			t.Fatalf("%s decode: %v", file, err)
		}
		all.Changes = append(all.Changes, u.Changes...)
	}
	return all
}

// TestTextDelta validates rich-text toDelta reconstruction (styled runs) against
// loro's text.toDelta() output: one mark, two, overlapping marks, marks of one
// key with different values, an unmark, marks of one key made concurrently by
// two peers, decided once by peer and once by lamport, object and array values,
// a mark whose end anchor was deleted, a Text sharing its name with a List, and
// an unmark over bold text, which stays a run apart from the text beside it.
func TestTextDelta(t *testing.T) {
	for _, name := range []string{
		"rt_one", "rt_two", "rt_overlap", "rt_same_key", "rt_unmark",
		"rt_conc_color", "rt_conc_color_lamport", "rt_struct", "rt_end_deleted",
		"rt_same_name", "rt_split_mark", "rt_unmark_alone",
	} {
		delta, err := TextDelta(decodeFixture(t, name+".update.bin"), "rt")
		if err != nil {
			t.Fatalf("%s delta: %v", name, err)
		}
		if got, want := deltaJSON(delta), readDelta(t, name+".delta.json"); !reflect.DeepEqual(got, want) {
			t.Errorf("%s delta mismatch:\n got  %#v\n want %#v", name, got, want)
		}
	}
}

// TestTextDeltaOfASplitMark merges a mark whose two anchors arrive in different
// exports. The first export alone stops between them, and a mark without its
// end styles nothing; with the full export, in either order, it is whole.
func TestTextDeltaOfASplitMark(t *testing.T) {
	cases := []struct {
		files []string
		want  string
	}{
		{[]string{"rt_split_mark.part.bin"}, "rt_split_mark.part.delta.json"},
		{[]string{"rt_split_mark.part.bin", "rt_split_mark.update.bin"}, "rt_split_mark.delta.json"},
		{[]string{"rt_split_mark.update.bin", "rt_split_mark.part.bin"}, "rt_split_mark.delta.json"},
	}
	for _, c := range cases {
		delta, err := TextDelta(decodeFixture(t, c.files...), "rt")
		if err != nil {
			t.Fatalf("%v: %v", c.files, err)
		}
		if got, want := deltaJSON(delta), readDelta(t, c.want); !reflect.DeepEqual(got, want) {
			t.Errorf("%v:\n got  %#v\n want %#v", c.files, got, want)
		}
	}
}

// TestTextDeltaCorporaAgainstLoro holds TextDelta to loro's own toDelta() on
// three corpora: Text with bold and italic marks by one author and by two or
// three peers editing concurrently, and rich text with marks of every expand
// rule, values of every shape, unmarks, inserts at run edges and deletes of the
// whole text. Every history carries loro's delta, and every one must match.
func TestTextDeltaCorporaAgainstLoro(t *testing.T) {
	for _, name := range []string{"seq_single_corpus.json", "seq_concurrent_corpus.json", "rich_corpus.json"} {
		corpus := loadCorpus(t, name)
		if len(corpus) != 300 {
			t.Fatalf("%s: %d histories, want 300", name, len(corpus))
		}
		bad := 0
		for _, c := range corpus {
			if c.Delta == nil {
				t.Fatalf("%s seed %d: no delta recorded", name, c.Seed)
			}
			blob, err := base64.StdEncoding.DecodeString(c.Update)
			if err != nil {
				t.Fatal(err)
			}
			u, err := DecodeUpdates(blob)
			if err != nil {
				t.Fatalf("%s seed %d decode: %v", name, c.Seed, err)
			}
			delta, err := TextDelta(u, "t")
			if err != nil {
				t.Fatalf("%s seed %d: %v", name, c.Seed, err)
			}
			if got, want := deltaJSON(delta), normalize(c.Delta); !reflect.DeepEqual(got, want) {
				bad++
				if bad <= 3 {
					t.Errorf("%s seed %d:\n got  %v\n want %v", name, c.Seed, got, want)
				}
			}
		}
		if bad > 0 {
			t.Errorf("%s: %d of %d deltas differ from loro-crdt", name, bad, len(corpus))
		}
	}
}

// TestStyleValuesCompareByContent: loro merges two runs whose style values are
// equal in content, NaN included (a NaN-valued mark over "a" and another over
// "b" give one run in loro-crdt 1.16.4), and keeps apart values that differ.
func TestStyleValuesCompareByContent(t *testing.T) {
	cases := []struct {
		a, b any
		same bool
	}{
		{math.NaN(), math.NaN(), true},
		{map[string]any{"k": int64(1)}, map[string]any{"k": int64(1)}, true},
		{[]any{int64(1), "x"}, []any{int64(1), "x"}, true},
		{[]byte{1, 2}, []byte{1, 2}, true},
		{map[string]any{"k": []any{math.NaN()}}, map[string]any{"k": []any{math.NaN()}}, true},
		{int64(1), float64(1), false},
		{map[string]any{"k": int64(1)}, map[string]any{"k": int64(2)}, false},
		{[]any{int64(1)}, []any{int64(1), int64(2)}, false},
		{nil, false, false},
	}
	for _, c := range cases {
		cost := 0
		if got := sameValue(c.a, c.b, &cost); got != c.same {
			t.Errorf("sameValue(%#v, %#v) = %v, want %v", c.a, c.b, got, c.same)
		}
	}
}

// TestTextDeltaOfALongInsertIsLinear: one insert of 200 000 characters with a
// style over all of it. Each character joins the current run in place; a run
// rebuilt by concatenation copies the run so far for every character, about
// 20 GB here, where this allocates a few megabytes.
func TestTextDeltaOfALongInsertIsLinear(t *testing.T) {
	const n = 200_000
	d := NewDoc(1)
	d.TextInsert("t", 0, strings.Repeat("a", n))
	u, err := DecodeUpdates(d.ExportUpdates())
	if err != nil {
		t.Fatal(err)
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	delta, err := TextDelta(u, "t")
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatal(err)
	}
	if len(delta) != 1 || len(delta[0].Insert) != n {
		t.Fatalf("got %d runs", len(delta))
	}
	if alloc := after.TotalAlloc - before.TotalAlloc; alloc > 256<<20 {
		t.Errorf("TextDelta allocated %d MB for %d characters", alloc>>20, n)
	}
}

// TestStyleValueComparisonsAreCharged: comparing two style values costs what
// is compared, a long key included, so a style with a huge value carried
// across many anchors is paid for; the same value on both sides of an anchor
// is equal at once.
func TestStyleValueComparisonsAreCharged(t *testing.T) {
	long := strings.Repeat("k", 1000)
	a := map[string]any{long: "v"}
	b := map[string]any{long: "v"}
	cost := 0
	if !sameValue(a, b, &cost) || cost < 1000 {
		t.Errorf("equal maps with a 1000-byte key: same=%v cost=%d, want true and at least 1000", sameValue(a, b, new(int)), cost)
	}
	cost = 0
	if !sameValue(a, a, &cost) || cost > 1 {
		t.Errorf("a map against itself cost %d, want 1", cost)
	}
	list := []any{long, long}
	cost = 0
	if !sameValue(list, list, &cost) || cost > 1 {
		t.Errorf("a list against itself cost %d, want 1", cost)
	}
}
