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
	Delta    []any          `json:"delta"`
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

// TestMovableAndTreeCorporaAgainstLoro replays 300 random histories each of
// MovableList edits by one author, MovableList edits by two or three peers
// with syncs, and Tree edits (create, move, delete, meta writes) by one to
// three peers. Every history must come out as loro-crdt has it. Before
// MovableList kept positions and elements apart, as loro does, 71 and 96 of
// the two MovableList corpora matched, and a history with a set() did not
// decode at all.
func TestMovableAndTreeCorporaAgainstLoro(t *testing.T) {
	for _, name := range []string{"movable_single_corpus.json", "movable_concurrent_corpus.json", "tree_corpus.json"} {
		corpus := loadCorpus(t, name)
		seeds, got, want := divergences(t, corpus)
		for i := range seeds {
			if i < 3 {
				t.Errorf("%s seed %d diverged:\n got  %s\n want %s", name, seeds[i], got[i], want[i])
			}
		}
		if len(seeds) > 0 {
			t.Errorf("%s: %d of %d histories diverge from loro-crdt", name, len(seeds), len(corpus))
		}
	}
}

// TestRichTextCorpusAgainstLoro replays 300 random rich-text histories by one
// to three peers: marks of keys with each of loro's four expand rules, valued
// true, a string, an object or an array, unmarks, inserts at the edges of
// styled runs, and deletes of the whole text. The text must come out as
// loro-crdt has it; TestTextDeltaCorporaAgainstLoro holds the styles to it.
func TestRichTextCorpusAgainstLoro(t *testing.T) {
	corpus := loadCorpus(t, "rich_corpus.json")
	seeds, got, want := divergences(t, corpus)
	for i := range seeds {
		if i < 3 {
			t.Errorf("seed %d diverged:\n got  %s\n want %s", seeds[i], got[i], want[i])
		}
	}
	if len(seeds) > 0 {
		t.Errorf("%d of %d rich-text histories diverge from loro-crdt", len(seeds), len(corpus))
	}
}

// TestNestedCorpusAgainstLoro replays 300 random histories by one to three
// peers in which containers of all six kinds are stored as values: put under
// map keys, inserted into Lists and MovableLists, a burst at a time so that one
// insert op holds several, set over MovableList elements, and nested up to
// three levels below a root, with edits inside each, keys overwritten and
// list items deleted while they hold containers, containers put under one key
// by two peers concurrently, and mergeable children, some ensured under one
// key by two peers concurrently. Every history must come out as loro-crdt has
// it. Before child containers were resolved, a map key or list item holding
// one showed the container's bare kind code.
func TestNestedCorpusAgainstLoro(t *testing.T) {
	corpus := loadCorpus(t, "nested_corpus.json")
	seeds, got, want := divergences(t, corpus)
	for i := range seeds {
		if i < 3 {
			t.Errorf("seed %d diverged:\n got  %s\n want %s", seeds[i], got[i], want[i])
		}
	}
	if len(seeds) > 0 {
		t.Errorf("%d of %d nested-container histories diverge from loro-crdt", len(seeds), len(corpus))
	}
}
