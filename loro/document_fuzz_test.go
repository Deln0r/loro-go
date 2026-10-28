package loro

import (
	"encoding/base64"
	"testing"
)

// FuzzDocumentImport imports hostile blobs, resealed past the checksum, into
// a Document: once into an empty one, which takes a snapshot whole, and once
// after an edit, so that a blob's changes may wait for each other, be
// trimmed and be merged into the change store. An import either fails or
// leaves a document that reads, edits, marks, moves, nests containers and
// exports without panicking, and whose export another Document imports.
func FuzzDocumentImport(f *testing.F) {
	seedFuzz(f, ".update.bin")
	seedFuzz(f, ".snapshot.bin")
	for _, s := range append(loadScripts(f, "script_named.json"), loadScripts(f, "script_struct_named.json")...) {
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
			fuzzStructures(t, d)
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

// fuzzStructures edits the MovableList, Tree and Map roots of an imported
// document through every structure method, and through the containers they
// hold, deleted ones included; errors are fine, panics are not.
func fuzzStructures(t *testing.T, d *Document) {
	ml := d.MovableList("ml")
	_ = ml.Insert(ml.Len(), int64(3))
	if n := ml.Len(); n > 1 {
		_ = ml.Move(0, n-1)
		_ = ml.Set(n/2, "s")
		_ = ml.Delete(0, 1)
	}
	_, _ = ml.InsertContainer(0, KindText)
	_, _ = ml.SetContainer(0, KindCounter)
	for i := range ml.Len() {
		_, _ = ml.Get(i)
		if c, ok := ml.GetContainer(i); ok {
			fuzzEdit(c)
		}
	}
	tr := d.Tree("tr")
	top, _ := tr.Children(TreeRoot)
	n, _ := tr.Create(TreeRoot)
	_, _ = tr.CreateAt(TreeRoot, 0)
	_, _ = tr.CreateAt(n, 0)
	for i, id := range top {
		_ = tr.Move(id, n, 0)
		_ = tr.Move(n, id, 0)
		_ = tr.Move(id, TreeRoot, i)
		_, _ = tr.Parent(id)
		if m, err := tr.Meta(id); err == nil {
			_ = m.Set("k", int64(i))
		}
		if i%2 == 1 {
			_ = tr.Delete(id)
		}
	}
	m := d.Map("m")
	for _, k := range []string{"a", "b", "c", "k"} {
		if c, ok := m.GetContainer(k); ok {
			fuzzEdit(c)
		}
		_, _ = m.EnsureMergeable(k, KindText)
	}
	c, err := m.SetContainer("a", KindTree)
	if err == nil {
		fuzzEdit(c)
		_ = m.Delete("a")
		fuzzEdit(c)
		if !c.IsDeleted() {
			t.Fatalf("container %s is not deleted after its key is", c.ID())
		}
	}
	if _, err := d.JSON(); err != nil {
		t.Fatalf("JSON after structure edits: %v", err)
	}
}

// fuzzEdit makes one edit through container c, whatever its kind, and
// reads it back.
func fuzzEdit(c Container) {
	switch h := c.(type) {
	case *Text:
		_ = h.Insert(0, "f")
	case *Map:
		_ = h.Set("f", int64(1))
	case *List:
		_ = h.Insert(0, int64(1))
	case *MovableList:
		_ = h.Insert(0, int64(1))
		_ = h.Move(0, h.Len()-1)
	case *Tree:
		_, _ = h.Create(TreeRoot)
	case *Counter:
		_ = h.Increment(1)
	}
	_, _ = c.JSON()
	_ = c.IsDeleted()
}
