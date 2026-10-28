package loro

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Deln0r/loro-go/encoding/change"
	"github.com/Deln0r/loro-go/encoding/fast"
)

// TestDecodeRefusesIndexesPastArenas: an op's container, map key or tree
// position is an index into its block's arenas, and loro-crdt refuses an
// import whose index lands past one (DecodeDataCorruptionError in
// EncodedContainer::as_container_id and decode_op; measured with
// loro-crdt 1.17.1 on the blobs below). Each case rewrites one index of a
// Document's export; the unchanged rewrite still decodes. A mark's key
// goes through the same check as a map key (value.rs:948 read_mark).
func TestDecodeRefusesIndexesPastArenas(t *testing.T) {
	d := NewDocument(1)
	if err := d.Map("m").Set("k", int64(1)); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Tree("tr").Create(TreeRoot); err != nil {
		t.Fatal(err)
	}
	child, err := d.List("l").InsertContainer(0, KindMap)
	if err != nil {
		t.Fatal(err)
	}
	if err := child.(*Map).Set("x", int64(1)); err != nil {
		t.Fatal(err)
	}
	blob, err := d.ExportUpdates()
	if err != nil {
		t.Fatal(err)
	}
	h, err := fast.ParseHeader(blob)
	if err != nil {
		t.Fatal(err)
	}
	raws, err := change.SplitBlocks(h.Body)
	if err != nil || len(raws) != 1 {
		t.Fatalf("%d blocks, %v", len(raws), err)
	}
	rewrite := func(edit func(b *change.Block, conts []change.Container, keys []string, ops *change.Ops)) []byte {
		b, err := change.ParseBlock(raws[0])
		if err != nil {
			t.Fatal(err)
		}
		conts, err := change.DecodeContainers(b.CIDs)
		if err != nil {
			t.Fatal(err)
		}
		keys, err := change.DecodeKeys(b.Keys)
		if err != nil {
			t.Fatal(err)
		}
		ops, err := change.DecodeOps(b.Ops)
		if err != nil {
			t.Fatal(err)
		}
		edit(b, conts, keys, ops)
		return fast.Encode(fast.ModeFastUpdates, change.EncodeFastUpdates([][]byte{change.EncodeBlock(b)}))
	}
	same := rewrite(func(*change.Block, []change.Container, []string, *change.Ops) {})
	if _, err := DecodeUpdates(same); err != nil {
		t.Fatalf("the unchanged rewrite: %v", err)
	}
	cases := map[string][]byte{
		"root container key": rewrite(func(b *change.Block, conts []change.Container, keys []string, _ *change.Ops) {
			for i := range conts {
				if conts[i].IsRoot {
					conts[i].KeyOrCounter = int64(len(keys))
					break
				}
			}
			b.CIDs = change.EncodeContainers(conts)
		}),
		"stored container peer": rewrite(func(b *change.Block, conts []change.Container, _ []string, _ *change.Ops) {
			for i := range conts {
				if !conts[i].IsRoot {
					conts[i].PeerIdx = 9
				}
			}
			b.CIDs = change.EncodeContainers(conts)
		}),
		"map key": rewrite(func(b *change.Block, conts []change.Container, keys []string, ops *change.Ops) {
			for i, ci := range ops.ContainerIdx {
				if conts[ci].Kind == change.CMap {
					ops.Prop[i] = int64(len(keys))
					break
				}
			}
			b.Ops = change.EncodeOps(ops)
		}),
		"tree position": rewrite(func(b *change.Block, _ []change.Container, _ []string, _ *change.Ops) {
			b.Positions = nil
		}),
	}
	// LORO_BOUNDS_OUT names a directory to write the blobs to, for
	// loro-crdt to import.
	dir := os.Getenv("LORO_BOUNDS_OUT")
	if dir != "" {
		_ = os.WriteFile(filepath.Join(dir, "unchanged.bin"), same, 0o644)
	}
	for name, bad := range cases {
		if _, err := DecodeUpdates(bad); err == nil {
			t.Errorf("%s past its arena: decoded", name)
		}
		if err := NewDocument(2).Import(bad); err == nil {
			t.Errorf("%s past its arena: imported", name)
		}
		if dir != "" {
			_ = os.WriteFile(filepath.Join(dir, name+".bin"), bad, 0o644)
		}
	}
}
