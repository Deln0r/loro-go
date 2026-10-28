package loro

import (
	"maps"
	"testing"
)

// FuzzDecodeVersion decodes hostile bytes as a version vector and as
// frontiers, as a peer's sync message would bring them. Decoding either
// fails or gives what encodes and decodes to the same entries again; a
// decoded version vector, counters of 0 and below and peers never seen
// included, is exported from by a document with history, and another
// document imports that export.
func FuzzDecodeVersion(f *testing.F) {
	for i, h := range loadVersionsCorpus(f) {
		if i%8 != 0 {
			continue
		}
		f.Add(unb64(h.VV))
		f.Add(unb64(h.FrontiersBytes))
		for _, v := range h.Versions {
			f.Add(unb64(v.Bytes))
		}
	}
	f.Add([]byte{})
	f.Add([]byte{0xff, 0xff, 0xff, 0xff, 0x0f})
	doc := NewDocument(1)
	_ = doc.Text("t").Insert(0, "abc")
	doc.CommitWith(CommitOptions{Message: "a"})
	other := NewDocument(2)
	if blob, err := doc.ExportUpdates(); err == nil {
		_ = other.Import(blob)
	}
	_ = other.Map("m").Set("k", int64(1))
	_ = other.Text("t").Insert(1, "xy")
	blob, err := other.ExportUpdates()
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if vv, err := DecodeVersionVector(data); err == nil {
			again, err := DecodeVersionVector(vv.Encode())
			if err != nil {
				t.Fatalf("a decoded version vector does not decode once encoded: %v", err)
			}
			if !maps.Equal(again.Map(), vv.Map()) {
				t.Fatalf("version vector %v encodes and decodes as %v", vv.Map(), again.Map())
			}
			d := NewDocument(3)
			if err := d.Import(blob); err != nil {
				t.Fatal(err)
			}
			out, err := d.ExportUpdatesFrom(vv)
			if err != nil {
				t.Fatalf("export from %v: %v", vv.Map(), err)
			}
			if err := NewDocument(4).Import(out); err != nil {
				t.Fatalf("export from %v does not import: %v", vv.Map(), err)
			}
		}
		if ids, err := DecodeFrontiers(data); err == nil {
			b, err := EncodeFrontiers(ids)
			if err != nil {
				t.Fatalf("decoded frontiers %v do not encode: %v", ids, err)
			}
			again, err := DecodeFrontiers(b)
			if err != nil || len(again) != len(ids) {
				t.Fatalf("frontiers %v encode and decode as %v (%v)", ids, again, err)
			}
			fr := frontiersOf(ids)
			if !fr.equal(again) {
				t.Fatalf("frontiers %v encode and decode as %v", ids, again)
			}
		}
	})
}
