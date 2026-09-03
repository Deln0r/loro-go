package loro

import (
	"encoding/json"
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
