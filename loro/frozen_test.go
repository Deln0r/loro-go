package loro

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestDeleteIdsWrittenByOldLoro pins a gap, not a fix. loro-crdt's WASM build
// up to 1.16.3 could record a text delete with the wrong ids when an astral
// character ended one of the inserts it spans (loro-dev/loro#1149): deleting
// "a😀x" from "a😀xb" records ids that name "a😀b". loro applies a delete by
// its position in the author's version and shows "b!"; this merge reads the
// ids and shows "x!". Reading by position needs the order of concurrent inserts
// to match loro's first, since a position names a different element in a
// differently ordered view. When this test fails because the merge gives
// "b!", move the fixture into TestMergeStateMatchesToJSON and drop the README
// and COMPAT notes.
func TestDeleteIdsWrittenByOldLoro(t *testing.T) {
	dir := filepath.Join("..", "testdata", "fixtures", "frozen")
	blob, err := os.ReadFile(filepath.Join(dir, "delete_astral_ids_1163.update.bin"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "delete_astral_ids_1163.json"))
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
	switch state["t"] {
	case want["t"]:
		t.Errorf("t = %q matches loro-crdt: the gap is closed; move the fixture into TestMergeStateMatchesToJSON and drop the README and COMPAT notes", state["t"])
	case "x!":
		t.Logf("t = %q where loro-crdt gives %q: the delete is read by its recorded ids", state["t"], want["t"])
	default:
		t.Errorf("t = %q, neither the id reading %q nor loro's %q", state["t"], "x!", want["t"])
	}
}
