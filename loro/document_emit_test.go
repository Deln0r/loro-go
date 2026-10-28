package loro

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// TestEmitDocumentExportsForJS writes, for every peer of every script, the
// Document's export and JSON at the script's last checkpoint, with
// loro-crdt's export of the same peer, to testdata/go_out/document.json.
// testdata/gen/validate_document.mjs imports each Go export into loro-crdt
// and holds it to the Go peer's JSON and to loro's own export. The
// histories a Document refuses (refusedBlobs) go to
// testdata/go_out/document_refused, which loro-crdt must refuse too.
func TestEmitDocumentExportsForJS(t *testing.T) {
	r := &checkResult{last: map[string]peerExport{}}
	for _, name := range []string{"script_corpus.json", "script_crowd_corpus.json", "script_named.json", "script_rich_corpus.json", "script_rich_named.json", "script_struct_corpus.json", "script_struct_crowd_corpus.json", "script_struct_named.json"} {
		scripts := loadScripts(t, name)
		for i := range scripts {
			runScript(&scripts[i], 0, r)
		}
	}
	if len(r.failures) > 0 {
		t.Fatal(r.failures[0])
	}
	keys := make([]string, 0, len(r.last))
	for k := range r.last {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	out := make([]peerExport, len(keys))
	for i, k := range keys {
		out[i] = r.last[k]
	}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join("..", "testdata", "go_out")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "document.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	refused := filepath.Join(dir, "document_refused")
	if err := os.RemoveAll(refused); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(refused, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, blob := range refusedBlobs(t) {
		if err := os.WriteFile(filepath.Join(refused, name+".bin"), blob, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("wrote %d exports, %d of %d checks byte for byte", len(out), r.bytes, r.checks)
}
