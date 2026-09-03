package loromatrix

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Deln0r/loro-go/loro"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "fixtures", name))
	if err != nil {
		t.Fatalf("fixture %s: %v", name, err)
	}
	return b
}

// TestValidationIsNotAuthorisation states, in runnable form, exactly what the
// checks in this package are worth.
//
// Encode and Decode verify that a blob is well-formed: right header, matching
// checksum, parseable change blocks. That buys availability of reading, because
// a malformed or hostile blob errors out instead of taking the reader down. It
// buys nothing at all about whether its author was entitled to send it.
//
// Deletes in this CRDT are id-addressed and carry no authorisation: any peer
// holding the ids may tombstone them. So in a Matrix room the right to write is
// the right to delete everyone else's content, and this test is the proof: a
// peer that authored nothing wipes the document, and every check here passes on
// the blob that does it. Anyone deploying this needs to read room membership as
// full authority over the document, not as a reader role.
func TestValidationIsNotAuthorisation(t *testing.T) {
	authored := fixture(t, "foreign_delete.authored.bin")
	wipe := fixture(t, "foreign_delete.wipe.bin")

	for name, blob := range map[string][]byte{"authored": authored, "wipe": wipe} {
		content, err := Encode(blob)
		if err != nil {
			t.Fatalf("%s: Encode rejected a well-formed blob: %v", name, err)
		}
		if _, err := Decode(content); err != nil {
			t.Fatalf("%s: Decode rejected a well-formed blob: %v", name, err)
		}
	}

	// Publish both through the transport, as two room members would.
	f := &fakeHomeserver{}
	cli := newTestClient(t, f)
	ctx := context.Background()
	for _, blob := range [][]byte{authored, wipe} {
		if _, err := Publish(ctx, cli, testRoomID, blob); err != nil {
			t.Fatalf("publish: %v", err)
		}
	}
	u, errs, err := Replay(ctx, cli, testRoomID, 0)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if len(errs) != 0 {
		t.Fatalf("the hostile blob was reported as malformed, which it is not: %v", errs)
	}
	state, err := State(u)
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	got, _ := json.Marshal(state)

	want := fixture(t, "foreign_delete.json")
	var w map[string]any
	if err := json.Unmarshal(want, &w); err != nil {
		t.Fatal(err)
	}
	wantJSON, _ := json.Marshal(w)
	if string(got) != string(wantJSON) {
		t.Fatalf("merged = %s, want %s", got, wantJSON)
	}

	// The document is empty, and nothing anywhere flagged it. That is the point.
	text, _ := state["t"].(string)
	if text != "" {
		t.Fatalf("expected the document to have been wiped, got %q", text)
	}
	t.Log("a peer that authored nothing emptied the document; every validation passed")
}

// TestAuthoredContentSurvivesAlone is the control: without the second peer's
// delete, the same authored blob reads back intact. Without this, the test
// above would pass even if the transport were dropping everything.
func TestAuthoredContentSurvivesAlone(t *testing.T) {
	u, err := loro.DecodeUpdates(fixture(t, "foreign_delete.authored.bin"))
	if err != nil {
		t.Fatal(err)
	}
	state, err := State(u)
	if err != nil {
		t.Fatal(err)
	}
	if text, _ := state["t"].(string); text == "" {
		t.Fatal("the authored blob alone reads back empty, so the wipe test proves nothing")
	}
}
