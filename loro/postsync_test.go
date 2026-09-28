package loro

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestPostSyncEdits runs a history where both peers keep editing after they have
// synced, which is ordinary collaboration and which no other fixture contains.
//
// Map and Tree must match loro-crdt. They depend on each change's lamport, and
// the block header's lamport column used to be read one change off; this
// fixture is where that decided the result, since map key "k" is written three
// times and only the true lamports make "a2" win.
//
// Text and List still diverge, and the test says so rather than skipping them.
// Their merge resolves an insert's left neighbour against the inserting peer's
// own earlier edits only. An insert made after importing another peer's text
// can therefore lose its neighbour: here peer 2 types "B" right after peer 1's
// "A", but "A" never appears in peer 2's own history, so "B" lands before it.
// Fixing that needs each op's causal past from the change DAG. When it is fixed
// this test fails on purpose: move the fixture into TestMergeStateMatchesToJSON
// and drop the "Not yet" line in the README.
func TestPostSyncEdits(t *testing.T) {
	dir := filepath.Join("..", "testdata", "fixtures")
	blob, err := os.ReadFile(filepath.Join(dir, "post_merge_edits.update.bin"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "post_merge_edits.json"))
	if err != nil {
		t.Fatal(err)
	}
	var want map[string]any
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	u, err := DecodeUpdates(blob)
	if err != nil {
		t.Fatal(err)
	}
	state, err := MergeState(u)
	if err != nil {
		t.Fatal(err)
	}
	same := func(key string) (bool, string, string) {
		g, _ := json.Marshal(normalize(state[key]))
		w, _ := json.Marshal(want[key])
		return string(g) == string(w), string(g), string(w)
	}

	for _, key := range []string{"m", "tr"} {
		if ok, got, w := same(key); !ok {
			t.Errorf("%s diverged from loro-crdt:\n got  %s\n want %s", key, got, w)
		}
	}
	for _, key := range []string{"t", "l"} {
		if ok, got, _ := same(key); ok {
			t.Errorf("%s now matches loro-crdt (%s): the post-sync sequence gap is closed; move post_merge_edits into TestMergeStateMatchesToJSON and update the README", key, got)
		}
	}
}
