package loro

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestEncodeFromScratchByteIdentical builds documents in Go from scratch and
// asserts the exported FastUpdates bytes are byte-identical to what loro-crdt
// produced for the same edits. Byte-identity also proves the blobs are
// importable by loro (they are exactly loro's own bytes).
func TestEncodeFromScratchByteIdentical(t *testing.T) {
	cases := []struct {
		name  string
		build func(*Doc)
	}{
		{"text_hi", func(d *Doc) { d.TextInsert("t", 0, "hi") }},
		{"map_kv", func(d *Doc) { d.MapSet("m", "k", "v"); d.MapSet("m", "n", int64(42)) }},
		{"list_abc", func(d *Doc) { d.ListInsert("l", 0, []any{"a", "b", "c"}) }},
	}
	for _, c := range cases {
		want, err := os.ReadFile(filepath.Join("..", "testdata", "fixtures", c.name+".update.bin"))
		if err != nil {
			t.Skipf("fixture %s missing: %v", c.name, err)
		}
		d := NewDoc(1)
		c.build(d)
		got := d.ExportUpdates()
		if !bytes.Equal(got, want) {
			t.Errorf("%s:\n got  % x\n want % x", c.name, got, want)
		}
	}
}

// TestEncodeRoundTripThroughDecode builds a novel doc, exports, and decodes it
// back, asserting the state round-trips (covers values not in any fixture).
func TestEncodeRoundTripThroughDecode(t *testing.T) {
	d := NewDoc(7)
	d.TextInsert("greeting", 0, "world")
	d.MapSet("cfg", "enabled", true)
	d.MapSet("cfg", "ratio", 0.5)
	d.ListInsert("items", 0, []any{int64(10), "x"})
	blob := d.ExportUpdates()

	u, err := DecodeUpdates(blob)
	if err != nil {
		t.Fatalf("decode of self-encoded blob: %v", err)
	}
	state, err := MergeState(u)
	if err != nil {
		t.Fatal(err)
	}
	if state["greeting"] != "world" {
		t.Errorf("greeting = %v", state["greeting"])
	}
	cfg := state["cfg"].(map[string]any)
	if cfg["enabled"] != true || cfg["ratio"] != 0.5 {
		t.Errorf("cfg = %v", cfg)
	}
	items := state["items"].([]any)
	if len(items) != 2 || items[0] != int64(10) || items[1] != "x" {
		t.Errorf("items = %v", items)
	}
}
