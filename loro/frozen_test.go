package loro

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Deln0r/loro-go/encoding/change"
)

// TestDeleteIdsWrittenByOldLoro replays a delete that loro-crdt's WASM build
// up to 1.16.3 wrote with the wrong ids (loro-dev/loro#1149): deleting "a😀x"
// from "a😀xb" recorded ids that name "a😀b". loro applies a delete by its
// position in the author's version and shows "b!", and so must this merge.
// Reading the recorded ids instead gives "x!".
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
	if state["t"] != want["t"] {
		t.Fatalf("t = %q, loro-crdt gives %q", state["t"], want["t"])
	}
}

// TestIncompleteHistoryDeletesById drops an op from the middle of one author's
// history: "abc" inserted, "x" typed at the front (the missing op), then the
// "b" of "xabc" deleted at position 2. Without the missing insert the view the
// merge can rebuild is "abc", where position 2 names "c". The ids the delete
// recorded name "b" whatever is missing, so with an incomplete history a delete
// is read by its ids.
func TestIncompleteHistoryDeletesById(t *testing.T) {
	u := &Updates{Changes: []Change{
		{ID: ID{Peer: 1}, Ops: []Op{{Container: "t", IsRoot: true, Kind: change.CText, VKind: change.VKStr,
			Value: "abc", Len: 3, Peer: 1}}},
		{ID: ID{Peer: 1, Counter: 4}, Lamport: 4, Deps: []ID{{Peer: 1, Counter: 3}}, Ops: []Op{{Container: "t", IsRoot: true,
			Kind: change.CText, VKind: change.VKDeleteSeq, Pos: 2, Len: 1, Peer: 1, Counter: 4, Lamport: 4,
			Value: DeleteSpan{Peer: 1, Counter: 1, Len: 1}}}},
	}}
	state, err := MergeState(u)
	if err != nil {
		t.Fatal(err)
	}
	if state["t"] != "ac" {
		t.Fatalf("t = %q, want %q: the delete's ids name the b", state["t"], "ac")
	}
}

// TestDeepGapDeletesById puts the gap further back. Peer 1's "b" (1@1) is
// missing; its "c" (2@1) depends on it; peer 2's delete depends on 2@1, which
// is present and is everything merged when the delete arrives, and names "b"
// by id at position 1. Only the gap two changes back shows that position 1 of
// the rebuilt view ("ac") is not the author's, so completeness has to cover the
// whole history, not the delete's own deps.
func TestDeepGapDeletesById(t *testing.T) {
	ins := func(counter, pos int64, s string) Op {
		return Op{Container: "t", IsRoot: true, Kind: change.CText, VKind: change.VKStr,
			Value: s, Len: 1, Pos: pos, Peer: 1, Counter: counter, Lamport: counter}
	}
	u := &Updates{Changes: []Change{
		{ID: ID{Peer: 1}, Ops: []Op{ins(0, 0, "a")}},
		{ID: ID{Peer: 1, Counter: 2}, Lamport: 2, Deps: []ID{{Peer: 1, Counter: 1}}, Ops: []Op{ins(2, 2, "c")}},
		{ID: ID{Peer: 2}, Lamport: 3, Deps: []ID{{Peer: 1, Counter: 2}}, Ops: []Op{{Container: "t", IsRoot: true,
			Kind: change.CText, VKind: change.VKDeleteSeq, Pos: 1, Len: 1, Peer: 2, Lamport: 3,
			Value: DeleteSpan{Peer: 1, Counter: 1, Len: 1}}}},
	}}
	state, err := MergeState(u)
	if err != nil {
		t.Fatal(err)
	}
	if state["t"] != "ac" {
		t.Fatalf("t = %q, want %q: the delete names the missing b", state["t"], "ac")
	}
}
