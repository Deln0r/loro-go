package loro

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestTextDelta validates rich-text toDelta reconstruction (styled runs) against
// loro's text.toDelta() output.
func TestTextDelta(t *testing.T) {
	dir := filepath.Join("..", "testdata", "fixtures")
	for _, name := range []string{"rt_one", "rt_two", "rt_overlap"} {
		blob, err := os.ReadFile(filepath.Join(dir, name+".update.bin"))
		if err != nil {
			t.Skipf("fixture %s missing: %v", name, err)
		}
		u, err := DecodeUpdates(blob)
		if err != nil {
			t.Fatalf("%s decode: %v", name, err)
		}
		delta, err := TextDelta(u, "rt")
		if err != nil {
			t.Fatalf("%s delta: %v", name, err)
		}
		got := make([]any, len(delta))
		for i, d := range delta {
			m := map[string]any{"insert": d.Insert}
			if len(d.Attributes) > 0 {
				m["attributes"] = d.Attributes
			}
			got[i] = m
		}
		wantBytes, err := os.ReadFile(filepath.Join(dir, name+".delta.json"))
		if err != nil {
			t.Fatal(err)
		}
		var want any
		if err := json.Unmarshal(wantBytes, &want); err != nil {
			t.Fatal(err)
		}
		if g := normalize(got); !reflect.DeepEqual(g, want) {
			t.Errorf("%s delta mismatch:\n got  %#v\n want %#v", name, g, want)
		}
	}
}
