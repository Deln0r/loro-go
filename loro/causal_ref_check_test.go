package loro

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/Deln0r/loro-go/encoding/change"
)

// refState renders every root Text and List container of u with the brute-force
// reference, keyed by container name. A mergeable child is a root by name only
// and toJSON shows it under its parent, and a List that holds containers shows
// their contents where the reference has the bare values, so both are left
// out: the reference orders sequences, it does not place children.
func refState(u *Updates) map[string]any {
	kinds := map[string]change.ContainerType{}
	holds := map[string]bool{}
	for _, ch := range u.Changes {
		for _, op := range ch.Ops {
			if op.IsRoot && (op.Kind == change.CText || op.Kind == change.CList) && !isMergeableName(op.Container) {
				kinds[op.Container] = op.Kind
				if items, ok := op.Value.([]any); ok && slices.ContainsFunc(items, holdsContainer) {
					holds[op.Container] = true
				}
			}
		}
	}
	out := map[string]any{}
	for name, kind := range kinds {
		if holds[name] {
			continue
		}
		isText := kind == change.CText
		out[name] = normalize(refRender(causalRefSeq(u.Changes, name, isText), isText))
	}
	return out
}

// TestCausalRefMatchesLoro holds the reference itself to loro-crdt: on every
// fixture and on every history of the three random corpora, its Text and List
// containers must equal loro's own toJSON. An oracle that disagreed
// with loro would only make the merge agree with the oracle.
func TestCausalRefMatchesLoro(t *testing.T) {
	dir := filepath.Join("..", "testdata", "fixtures")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if name, ok := strings.CutSuffix(e.Name(), ".update.bin"); ok {
			names = append(names, name)
		}
	}
	sort.Strings(names)

	compared := 0
	check := func(label string, u *Updates, want map[string]any) {
		for name, got := range refState(u) {
			compared++
			if w := want[name]; !reflect.DeepEqual(got, w) {
				t.Errorf("%s/%s: reference gives %#v, loro gives %#v", label, name, got, w)
			}
		}
	}
	for _, name := range names {
		blob, err := os.ReadFile(filepath.Join(dir, name+".update.bin"))
		if err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(filepath.Join(dir, name+".json"))
		if err != nil {
			continue // a blob without a recorded state, e.g. foreign_delete parts
		}
		var want map[string]any
		if err := json.Unmarshal(raw, &want); err != nil {
			continue
		}
		u, err := DecodeUpdates(blob)
		if err != nil {
			t.Fatalf("%s decode: %v", name, err)
		}
		check(name, u, want)
	}

	for _, name := range []string{"ordering_corpus.json", "seq_single_corpus.json", "seq_concurrent_corpus.json"} {
		for _, c := range loadCorpus(t, name) {
			blob, err := base64.StdEncoding.DecodeString(c.Update)
			if err != nil {
				t.Fatal(err)
			}
			u, err := DecodeUpdates(blob)
			if err != nil {
				t.Fatalf("%s seed %d decode: %v", name, c.Seed, err)
			}
			check(fmt.Sprintf("%s seed %d", name, c.Seed), u, c.Expected)
		}
	}
	t.Logf("%d containers compared", compared)
}
