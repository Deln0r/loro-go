package loro

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// corpusEntry is one random history and the state loro-crdt gave for it.
type corpusEntry struct {
	Seed     int            `json:"seed"`
	Update   string         `json:"update"`
	Expected map[string]any `json:"expected"`
}

func loadCorpus(t *testing.T, name string) []corpusEntry {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "testdata", "fixtures", name))
	if err != nil {
		t.Fatalf("corpus: %v", err)
	}
	var corpus []corpusEntry
	if err := json.Unmarshal(raw, &corpus); err != nil {
		t.Fatalf("corpus: %v", err)
	}
	if len(corpus) == 0 {
		t.Fatal("corpus is empty")
	}
	return corpus
}

// divergences merges every history in the corpus and returns the seeds whose
// state differs from loro's, with what each side gave.
func divergences(t *testing.T, corpus []corpusEntry) (seeds []int, got, want []string) {
	t.Helper()
	for _, c := range corpus {
		blob, err := base64.StdEncoding.DecodeString(c.Update)
		if err != nil {
			t.Fatalf("seed %d: %v", c.Seed, err)
		}
		u, err := DecodeUpdates(blob)
		if err != nil {
			t.Fatalf("seed %d decode: %v", c.Seed, err)
		}
		state, err := MergeState(u)
		if err != nil {
			t.Fatalf("seed %d merge: %v", c.Seed, err)
		}
		g, _ := json.Marshal(normalize(state))
		w, _ := json.Marshal(c.Expected)
		if string(g) != string(w) {
			seeds = append(seeds, c.Seed)
			got = append(got, string(g))
			want = append(want, string(w))
		}
	}
	return seeds, got, want
}

// TestSingleCorpusAgainstLoro replays 300 random one-author histories: inserts
// anywhere, astral characters included, deletes forwards and in backspace runs,
// marks over text, and list edits. With one author nothing is concurrent, so
// every history must come out exactly as loro-crdt has it.
func TestSingleCorpusAgainstLoro(t *testing.T) {
	corpus := loadCorpus(t, "seq_single_corpus.json")
	seeds, got, want := divergences(t, corpus)
	for i := range seeds {
		if i < 3 {
			t.Errorf("seed %d diverged:\n got  %s\n want %s", seeds[i], got[i], want[i])
		}
	}
	if len(seeds) > 0 {
		t.Errorf("%d of %d one-author histories diverge from loro-crdt", len(seeds), len(corpus))
	}
}

// TestConcurrentCorpusAgainstLoro replays 300 random histories in which two or
// three peers edit their own replicas and now and then import one another's,
// so inserts land concurrently at the same place. All 300 must match loro-crdt.
// Under the earlier rule, which put the newer of two concurrent inserts first,
// 185 did: three peers that each put one item into an empty list came out
// [29, 56, 83] where loro, ordering by Fugue's rule, gives [56, 29, 83].
func TestConcurrentCorpusAgainstLoro(t *testing.T) {
	corpus := loadCorpus(t, "seq_concurrent_corpus.json")
	seeds, got, want := divergences(t, corpus)
	for i := range seeds {
		if i < 3 {
			t.Errorf("seed %d diverged:\n got  %s\n want %s", seeds[i], got[i], want[i])
		}
	}
	if len(seeds) > 0 {
		t.Errorf("%d of %d concurrent histories diverge from loro-crdt", len(seeds), len(corpus))
	}
}
