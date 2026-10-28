package loro

import (
	"encoding/base64"
	"testing"
)

// FuzzDocumentImport imports hostile blobs, resealed past the checksum, into
// a Document: once into an empty one, which takes a snapshot whole, and once
// after an edit, so that a blob's changes may wait for each other, be
// trimmed and be merged into the change store. An import either fails or
// leaves a document that reads, edits, marks and exports without panicking,
// and whose export another Document imports.
func FuzzDocumentImport(f *testing.F) {
	seedFuzz(f, ".update.bin")
	seedFuzz(f, ".snapshot.bin")
	for _, s := range loadScripts(f, "script_named.json") {
		for _, b := range s.Blobs {
			if blob, err := base64.StdEncoding.DecodeString(b); err == nil && len(blob) < 1<<14 {
				f.Add(blob)
			}
		}
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		blob := reseal(data)
		for _, edit := range []bool{false, true} {
			d := NewDocument(9)
			if edit {
				_ = d.Text("t").Insert(0, "x")
				_ = d.Map("m").Set("k", int64(1))
			}
			if err := d.Import(blob); err != nil {
				continue
			}
			_ = d.Import(blob)
			if _, err := d.JSON(); err != nil {
				continue
			}
			t1 := d.Text("t")
			_ = t1.Insert(t1.Len(), "y")
			if n := t1.Len(); n > 1 {
				_ = t1.Mark(0, n, "bold", true)
				_ = t1.Unmark(n/2, n, "link")
				_ = t1.Insert(n/2, "z")
				_ = t1.Delete(0, 1)
			}
			if _, err := t1.Delta(); err != nil {
				t.Fatalf("delta after a successful import: %v", err)
			}
			l := d.List("l")
			_ = l.Insert(l.Len(), int64(2))
			out, err := d.ExportUpdates()
			if err != nil {
				t.Fatalf("export after a successful import: %v", err)
			}
			if err := NewDocument(10).Import(out); err != nil && !lamportsTie(out) {
				t.Fatalf("a Document refuses another's export: %v", err)
			}
		}
	})
}

// lamportsTie reports whether a blob holds a change that starts at or below
// the last lamport of the change of its peer before it. A hostile history
// can make one, with changes that do not depend on the op before them of
// their own peer; the import that reads it back sorts its changes by
// lamport with Rust's unstable sort, which can put the later one first and
// then refuses it as skipping counters. loro-crdt refuses its own export of
// such a history the same way (testdata/fuzz/FuzzDocumentImport/efd0bbfe5db39d04).
func lamportsTie(blob []byte) bool {
	u, err := DecodeUpdates(blob)
	if err != nil {
		return false
	}
	last := map[uint64]int64{}
	for _, c := range u.Changes {
		if end, ok := last[c.ID.Peer]; ok && c.Lamport < end {
			return true
		}
		last[c.ID.Peer] = c.Lamport + changeEnd(c) - c.ID.Counter
	}
	return false
}
