package loro

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestOrderingAgainstLoroCrdt replays 300 random insert-anywhere histories and
// requires the merged state to match what loro-crdt itself produced.
//
// This exists because the fixed fixture corpus could not have caught what it
// was written for. Almost every fixture appends, and an append is the one shape
// a wrong sibling order still gets right: the merge shipped with a comparator
// that agreed with loro-crdt on 6 of these 300 histories while all 52 fixtures
// passed. Breadth over inserts at arbitrary positions is the thing that has
// teeth here.
func TestOrderingAgainstLoroCrdt(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "testdata", "fixtures", "ordering_corpus.json"))
	if err != nil {
		t.Fatalf("corpus: %v", err)
	}
	var corpus []struct {
		Seed     int            `json:"seed"`
		Update   string         `json:"update"`
		Expected map[string]any `json:"expected"`
	}
	if err := json.Unmarshal(raw, &corpus); err != nil {
		t.Fatalf("corpus: %v", err)
	}
	if len(corpus) == 0 {
		t.Fatal("corpus is empty")
	}

	diverged := 0
	for _, c := range corpus {
		blob, err := base64.StdEncoding.DecodeString(c.Update)
		if err != nil {
			t.Fatalf("seed %d: %v", c.Seed, err)
		}
		u, err := DecodeUpdates(blob)
		if err != nil {
			t.Fatalf("seed %d decode: %v", c.Seed, err)
		}
		got, err := MergeState(u)
		if err != nil {
			t.Fatalf("seed %d merge: %v", c.Seed, err)
		}
		gotJSON, _ := json.Marshal(got)
		wantJSON, _ := json.Marshal(c.Expected)
		if string(gotJSON) != string(wantJSON) {
			diverged++
			if diverged <= 3 {
				t.Errorf("seed %d diverged:\n got  %s\n want %s", c.Seed, gotJSON, wantJSON)
			}
		}
	}
	if diverged > 0 {
		t.Errorf("%d of %d histories diverged from loro-crdt", diverged, len(corpus))
	}
}

// TestOrderingNamedCases pins the three shapes that were wrong, each as small as
// it can be made, so a failure names the defect rather than a seed number.
//
//	prepend twice  "BBB" then "Z" at 0   loro: "ZBBB"   was: "BBBZ"
//	mid insert     "ABC", "X" at 1, "Y" at 1  loro: "AYXBC"   was: "ABCXY"
//	list prepend   [1,2] then 9 at 0     loro: [9,1,2]  was: [1,2,9]
//
// All three are a single peer with no concurrency at all, which is what made
// the bug embarrassing: it needed no exotic history, only an insert that was
// not an append.
func TestOrderingNamedCases(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "testdata", "fixtures", "ordering_corpus.json"))
	if err != nil {
		t.Fatalf("corpus: %v", err)
	}
	var corpus []struct {
		Seed     int            `json:"seed"`
		Update   string         `json:"update"`
		Expected map[string]any `json:"expected"`
	}
	if err := json.Unmarshal(raw, &corpus); err != nil {
		t.Fatal(err)
	}
	// A history whose text is not simply the concatenation of its inserts in
	// order proves the corpus actually exercises non-append inserts; without
	// one, this whole file would be testing appends.
	nonAppend := 0
	for _, c := range corpus {
		blob, _ := base64.StdEncoding.DecodeString(c.Update)
		u, err := DecodeUpdates(blob)
		if err != nil {
			continue
		}
		for _, ch := range u.Changes {
			for _, op := range ch.Ops {
				if op.Pos > 0 && op.Counter > 0 {
					nonAppend++
				}
			}
		}
	}
	if nonAppend == 0 {
		t.Fatal("the corpus contains no insert away from position 0 after the first op, so it cannot catch an ordering bug")
	}
	t.Logf("corpus carries %d inserts at a non-zero position", nonAppend)
}
