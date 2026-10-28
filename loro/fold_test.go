package loro

import (
	"runtime"
	"testing"
)

// TestImportFoldsInLinearWork imports one change of 200,000 one-character
// inserts at successive positions, which loro folds into one op as it reads
// the block, and then the same change with one more insert, whose known
// part is compared atom by atom. Both must allocate in proportion to the
// blob. Folding by copying the text folded so far cost 4 MB of copying
// for 10,000 inserts and 20 GB for 200,000, and comparing the known part
// by converting the op's text to runes for every atom cost as much again;
// either fails this bound.
func TestImportFoldsInLinearWork(t *testing.T) {
	const n = 200000
	blob := func(m int) []byte {
		doc := NewDoc(1)
		for i := 0; i < m; i++ {
			doc.TextInsert("t", i, "x")
		}
		return doc.ExportUpdates()
	}
	first, second := blob(n), blob(n+1)
	allocated := func(f func() error) uint64 {
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		if err := f(); err != nil {
			t.Fatal(err)
		}
		runtime.ReadMemStats(&after)
		return after.TotalAlloc - before.TotalAlloc
	}
	// Decoding a block and folding its ops measured 440 bytes per byte of
	// blob, the whole import 720 and the import of the known change 10.
	const perByte = 2048
	d := NewDocument(9)
	if got := allocated(func() error { _, err := decodeBlob(first, d); return err }); got > perByte*uint64(len(first)) {
		t.Errorf("decoding and folding %d inserts allocated %d bytes, over %d per byte of the %d-byte blob", n, got, perByte, len(first))
	}
	if got := allocated(func() error { return d.Import(first) }); got > perByte*uint64(len(first)) {
		t.Errorf("importing %d inserts allocated %d bytes, over %d per byte of the %d-byte blob", n, got, perByte, len(first))
	}
	out, err := d.ExportUpdates()
	if err != nil {
		t.Fatal(err)
	}
	if u, err := DecodeUpdates(out); err != nil || len(u.Changes) != 1 || len(u.Changes[0].Ops) != 1 {
		t.Fatalf("the inserts did not fold into one op (%v)", err)
	}
	if got := allocated(func() error { return d.Import(second) }); got > perByte*uint64(len(second)) {
		t.Errorf("importing them again with one more allocated %d bytes, over %d per byte of the %d-byte blob", got, perByte, len(second))
	}
}
