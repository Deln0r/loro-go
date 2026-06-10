package loro

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// normalize converts decoded int64 to float64 so the reconstructed state can be
// compared against encoding/json output (which represents all numbers as float64).
func normalize(v any) any {
	switch x := v.(type) {
	case int64:
		return float64(x)
	case map[string]any:
		m := make(map[string]any, len(x))
		for k, e := range x {
			m[k] = normalize(e)
		}
		return m
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = normalize(e)
		}
		return out
	default:
		return v
	}
}

func TestBuildStateMatchesToJSON(t *testing.T) {
	dir := filepath.Join("..", "testdata", "fixtures")
	for _, name := range []string{"text_hi", "map_kv", "list_abc", "map_float", "text_del", "list_del", "map_del"} {
		blob, err := os.ReadFile(filepath.Join(dir, name+".update.bin"))
		if err != nil {
			t.Skipf("fixture %s missing: %v", name, err)
		}
		u, err := DecodeUpdates(blob)
		if err != nil {
			t.Fatalf("%s decode: %v", name, err)
		}
		state, err := BuildState(u)
		if err != nil {
			t.Fatalf("%s build: %v", name, err)
		}

		wantBytes, err := os.ReadFile(filepath.Join(dir, name+".json"))
		if err != nil {
			t.Fatal(err)
		}
		var want any
		if err := json.Unmarshal(wantBytes, &want); err != nil {
			t.Fatal(err)
		}
		if got := normalize(state); !reflect.DeepEqual(got, want) {
			t.Errorf("%s state mismatch:\n got  %#v\n want %#v", name, got, want)
		}
	}
}

func TestDecodeSnapshotMatchesToJSON(t *testing.T) {
	dir := filepath.Join("..", "testdata", "fixtures")
	for _, name := range []string{"text_hi", "map_kv", "list_abc", "map_float", "text_del", "list_del", "map_del"} {
		blob, err := os.ReadFile(filepath.Join(dir, name+".snapshot.bin"))
		if err != nil {
			t.Skipf("fixture %s missing: %v", name, err)
		}
		u, err := DecodeSnapshot(blob)
		if err != nil {
			t.Fatalf("%s decode snapshot: %v", name, err)
		}
		state, err := BuildState(u)
		if err != nil {
			t.Fatalf("%s build: %v", name, err)
		}
		wantBytes, err := os.ReadFile(filepath.Join(dir, name+".json"))
		if err != nil {
			t.Fatal(err)
		}
		var want any
		if err := json.Unmarshal(wantBytes, &want); err != nil {
			t.Fatal(err)
		}
		if got := normalize(state); !reflect.DeepEqual(got, want) {
			t.Errorf("%s snapshot state mismatch:\n got  %#v\n want %#v", name, got, want)
		}
	}
}

func TestMergeStateMatchesToJSON(t *testing.T) {
	dir := filepath.Join("..", "testdata", "fixtures")
	// includes concurrent / multi-peer fixtures
	for _, name := range []string{
		"text_hi", "map_kv", "list_abc", "map_float",
		"conc_text", "conc_map", "conc_list", "conc_text2", "conc_list2", "richtext", "mlist", "tree_simple", "text_del", "list_del", "map_del",
	} {
		blob, err := os.ReadFile(filepath.Join(dir, name+".update.bin"))
		if err != nil {
			t.Skipf("fixture %s missing: %v", name, err)
		}
		u, err := DecodeUpdates(blob)
		if err != nil {
			t.Fatalf("%s decode: %v", name, err)
		}
		state, err := MergeState(u)
		if err != nil {
			t.Fatalf("%s merge: %v", name, err)
		}
		wantBytes, err := os.ReadFile(filepath.Join(dir, name+".json"))
		if err != nil {
			t.Fatal(err)
		}
		var want any
		if err := json.Unmarshal(wantBytes, &want); err != nil {
			t.Fatal(err)
		}
		if got := normalize(state); !reflect.DeepEqual(got, want) {
			t.Errorf("%s merged state mismatch:\n got  %#v\n want %#v", name, got, want)
		}
	}
}

func TestDecodeUpdatesMetadata(t *testing.T) {
	blob, err := os.ReadFile(filepath.Join("..", "testdata", "fixtures", "text_hi.update.bin"))
	if err != nil {
		t.Skip("fixture missing")
	}
	u, err := DecodeUpdates(blob)
	if err != nil {
		t.Fatal(err)
	}
	if len(u.Changes) != 1 {
		t.Fatalf("changes = %d, want 1", len(u.Changes))
	}
	c := u.Changes[0]
	if c.ID.Peer != 1 || c.ID.Counter != 0 || c.Lamport != 0 || c.Timestamp != 0 {
		t.Errorf("change meta = %+v, want peer 1 counter 0 lamport 0 ts 0", c)
	}
	if len(c.Ops) != 1 || c.Ops[0].Container != "t" || c.Ops[0].Value != "hi" {
		t.Errorf("op = %+v, want container t value hi", c.Ops)
	}
}
