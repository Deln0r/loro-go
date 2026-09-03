package loro

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// mergedJSON merges the given blobs, in the given order, into one state.
func mergedJSON(t *testing.T, blobs ...[]byte) string {
	t.Helper()
	all := &Updates{}
	for _, blob := range blobs {
		u, err := DecodeUpdates(blob)
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		all.Changes = append(all.Changes, u.Changes...)
	}
	state, err := MergeState(all)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	out, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(out)
}

// TestMergeIsIdempotent pins the CRDT invariant that applying the same op twice
// is the same as applying it once.
//
// Nothing in the fixture corpus exercised this: each fixture merges a fixed set
// of distinct changes exactly once. The gap showed up the first time updates
// travelled over a real transport, where a peer replaying a room received an
// update it had already seen and the text came back mangled
// ("hello" -> "hellooloolloolooellooloollooloo...").
func TestMergeIsIdempotent(t *testing.T) {
	d := NewDoc(1)
	d.TextInsert("notes", 0, "hello")
	d.MapSet("meta", "k", "v")
	d.ListInsert("items", 0, []any{int64(1), int64(2)})
	blob := d.ExportUpdates()

	once := mergedJSON(t, blob)
	for _, n := range []int{2, 3, 5} {
		blobs := make([][]byte, n)
		for i := range blobs {
			blobs[i] = blob
		}
		if got := mergedJSON(t, blobs...); got != once {
			t.Errorf("applying the same update %d times changed the result:\n once = %s\n  x%d  = %s", n, once, n, got)
		}
	}
}

// TestMergeIsIdempotentAcrossPeers checks that absorbing a duplicate does not
// also swallow a different peer's genuinely distinct op. Deduplicating on
// something too coarse (a container name, a position) would pass the
// single-peer test above and lose data here.
func TestMergeIsIdempotentAcrossPeers(t *testing.T) {
	a := NewDoc(1)
	a.TextInsert("notes", 0, "alice")
	aBlob := a.ExportUpdates()

	b := NewDoc(2)
	b.TextInsert("notes", 0, "bob")
	bBlob := b.ExportUpdates()

	clean := mergedJSON(t, aBlob, bBlob)
	noisy := mergedJSON(t, aBlob, bBlob, aBlob, bBlob, bBlob)
	if clean != noisy {
		t.Errorf("duplicates changed the merged result:\n clean = %s\n noisy = %s", clean, noisy)
	}

	notes, ok := func() (string, bool) {
		var m map[string]any
		if err := json.Unmarshal([]byte(clean), &m); err != nil {
			return "", false
		}
		s, ok := m["notes"].(string)
		return s, ok
	}()
	if !ok {
		t.Fatalf("no notes container in %s", clean)
	}
	// Deduplicating too aggressively would drop one peer's insert entirely.
	if len(notes) != len("alicebob") {
		t.Errorf("merged text = %q, want both peers' inserts (8 chars)", notes)
	}
}

// readFixture loads one raw blob from the cross-implementation corpus.
func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "testdata", "fixtures", name))
	if err != nil {
		t.Fatalf("fixture %s: %v", name, err)
	}
	return b
}

// expectedState reads the toJSON state loro-crdt itself produced for a scenario.
func expectedState(t *testing.T, name string) string {
	t.Helper()
	raw := readFixture(t, name)
	var v map[string]any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("fixture %s: %v", name, err)
	}
	out, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("fixture %s: %v", name, err)
	}
	return string(out)
}

// TestOverlappingExportsKeepEveryAtom pins the harder half of deduplication.
//
// loro coalesces adjacent atoms from one peer into a single run, so two exports
// of the same document taken at different moments share a first counter while
// covering different id spans: "ab" is counter 0 len 2, "abcd" is counter 0
// len 4. Keying on the first counter alone let whichever arrived first win, so
// merging "abcd" after "ab" silently dropped "cd". Reading a Matrix room
// forwards hit exactly that order.
func TestOverlappingExportsKeepEveryAtom(t *testing.T) {
	early := readFixture(t, "span_overlap.early.bin")
	late := readFixture(t, "span_overlap.late.bin")
	want := expectedState(t, "span_overlap.json")

	for _, c := range []struct {
		name  string
		blobs [][]byte
	}{
		{"oldest first", [][]byte{early, late}},
		{"newest first", [][]byte{late, early}},
		{"late alone", [][]byte{late}},
		{"repeated", [][]byte{early, late, early, late, late}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := mergedJSON(t, c.blobs...); got != want {
				t.Errorf("merged = %s, want %s", got, want)
			}
		})
	}
}

// TestPartialTailResendIsAbsorbed pins the complementary half: a from-version
// delta re-sends a tail at a LATER start counter, so a first-counter key misses
// the overlap entirely and applies those atoms twice. That produced exactly the
// corruption this dedup exists to prevent ("hello" came back "hellooloo").
func TestPartialTailResendIsAbsorbed(t *testing.T) {
	full := readFixture(t, "span_tail.full.bin")
	tail := readFixture(t, "span_tail.tail.bin")
	want := expectedState(t, "span_tail.json")

	for _, c := range []struct {
		name  string
		blobs [][]byte
	}{
		{"full then tail", [][]byte{full, tail}},
		{"tail then full", [][]byte{tail, full}},
		{"full alone", [][]byte{full}},
		{"interleaved", [][]byte{tail, full, tail, full}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := mergedJSON(t, c.blobs...); got != want {
				t.Errorf("merged = %s, want %s", got, want)
			}
		})
	}
}
