package loro

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/Deln0r/loro-go/encoding/change"
)

// refState renders every root Text and List container of u with the brute-force
// reference, keyed by container name.
func refState(u *Updates) map[string]any {
	kinds := map[string]change.ContainerType{}
	for _, ch := range u.Changes {
		for _, op := range ch.Ops {
			if op.IsRoot && (op.Kind == change.CText || op.Kind == change.CList) {
				kinds[op.Container] = op.Kind
			}
		}
	}
	out := map[string]any{}
	for name, kind := range kinds {
		isText := kind == change.CText
		out[name] = normalize(refRender(causalRefSeq(u.Changes, name, isText), isText))
	}
	return out
}

// TestCausalRefMatchesLoro holds the reference itself to loro-crdt: on every
// fixture, every ordering-corpus history and the post-sync history, its Text
// and List containers must equal loro's own toJSON. An oracle that disagreed
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

	raw, err := os.ReadFile(filepath.Join(dir, "ordering_corpus.json"))
	if err != nil {
		t.Fatal(err)
	}
	var corpus []struct {
		Seed     int            `json:"seed"`
		Update   string         `json:"update"`
		Expected map[string]any `json:"expected"`
	}
	if err := json.Unmarshal(raw, &corpus); err != nil {
		t.Fatal(err)
	}
	for _, c := range corpus {
		blob, err := base64.StdEncoding.DecodeString(c.Update)
		if err != nil {
			t.Fatal(err)
		}
		u, err := DecodeUpdates(blob)
		if err != nil {
			t.Fatalf("seed %d decode: %v", c.Seed, err)
		}
		check("ordering seed", u, c.Expected)
	}
	t.Logf("%d containers compared", compared)
}
