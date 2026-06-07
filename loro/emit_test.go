package loro

import (
	"os"
	"path/filepath"
	"testing"
)

// TestEmitForJSImport writes a Go-built blob to testdata/go_out so the JS
// cross-validator (testdata/gen/validate_go.mjs) can import it into loro-crdt.
func TestEmitForJSImport(t *testing.T) {
	d := NewDoc(42)
	d.TextInsert("title", 0, "from-go")
	d.MapSet("meta", "n", int64(7))
	d.MapSet("meta", "ok", true)
	d.ListInsert("xs", 0, []any{"a", int64(2)})

	dir := filepath.Join("..", "testdata", "go_out")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "from_go.update.bin"), d.ExportUpdates(), 0o644); err != nil {
		t.Fatal(err)
	}
}
