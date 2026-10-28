package loro

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// versionsEmitted is one history for testdata/gen/validate_versions.mjs:
// the whole history, and the Go exports from versions of it.
type versionsEmitted struct {
	Name    string            `json:"name"`
	Update  []byte            `json:"update"`
	Exports []versionsExactly `json:"exports"`
}

type versionsExactly struct {
	From []byte `json:"from"`
	Go   []byte `json:"go"`
}

// TestEmitVersionExportsForJS writes, for every history of
// versions_corpus.json and every version it is exported from, and every
// export from a version of the named scripts, the Document's
// ExportUpdatesFrom to testdata/go_out/versions.json, with the history and
// the version. testdata/gen/validate_versions.mjs builds a
// loro-crdt document holding the history up to that version, imports the
// Go export into it, and requires it to reach the whole history's version
// and state.
func TestEmitVersionExportsForJS(t *testing.T) {
	var out []versionsEmitted
	n := 0
	for _, h := range loadVersionsCorpus(t) {
		d := NewDocument(math.MaxUint64)
		update := unb64(h.Update)
		if err := d.Import(update); err != nil {
			t.Fatal(err)
		}
		e := versionsEmitted{Name: fmt.Sprintf("seed %d", h.Seed), Update: update}
		for _, v := range h.Versions {
			from := unb64(v.Bytes)
			vv, err := DecodeVersionVector(from)
			if err != nil {
				t.Fatal(err)
			}
			got, err := d.ExportUpdatesFrom(vv)
			if err != nil {
				t.Fatal(err)
			}
			e.Exports = append(e.Exports, versionsExactly{From: from, Go: got})
			n++
		}
		out = append(out, e)
	}
	// The named scripts' exports, each with the exporter's whole history
	// at the time.
	for _, s := range loadScripts(t, "versions_named.json") {
		r := runVersionsScript(&s, 0, func(d *Document, from, got []byte) {
			update, err := d.ExportUpdates()
			if err != nil {
				t.Fatal(err)
			}
			name := fmt.Sprintf("%s peer %d export %d", s.Name, d.PeerID(), n)
			out = append(out, versionsEmitted{Name: name, Update: update, Exports: []versionsExactly{{From: from, Go: got}}})
			n++
		})
		if len(r.failures) > 0 {
			t.Fatal(r.failures[0])
		}
	}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join("..", "testdata", "go_out")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "versions.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %d exports of %d histories", n, len(out))
}
