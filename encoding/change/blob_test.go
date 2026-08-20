package change

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

// blocksOf parses the change blocks of a FastUpdates fixture. The 22-byte fast
// header is skipped inline so this package's tests stay independent of the
// header package.
func blocksOf(t *testing.T, name string) []*Block {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "fixtures", name))
	if err != nil {
		t.Skipf("fixture %s missing (run: cd testdata/gen && node gen.mjs): %v", name, err)
	}
	if len(b) < 22 {
		t.Fatalf("%s: shorter than a fast header", name)
	}
	raw, err := SplitBlocks(b[22:])
	if err != nil {
		t.Fatalf("%s: SplitBlocks: %v", name, err)
	}
	out := make([]*Block, 0, len(raw))
	for i, r := range raw {
		blk, err := ParseBlock(r)
		if err != nil {
			t.Fatalf("%s: ParseBlock(block %d): %v", name, i, err)
		}
		out = append(out, blk)
	}
	return out
}

// blockWithDeletes returns the first block carrying a delete_start_ids blob.
func blockWithDeletes(t *testing.T, name string) *Block {
	t.Helper()
	for _, blk := range blocksOf(t, name) {
		if len(blk.DeleteIDs) > 0 {
			return blk
		}
	}
	t.Fatalf("%s: no block carries delete_start_ids", name)
	return nil
}

// TestDecodeDeleteIDsSinglePeer checks the decoded id span against the delete op
// loro-crdt itself reported for the fixture (start_id 1@0, i.e. peer index 0
// counter 1, with the op's own len).
func TestDecodeDeleteIDsSinglePeer(t *testing.T) {
	cases := []struct {
		fixture string
		want    DeleteID
	}{
		{"text_del.update.bin", DeleteID{PeerIdx: 0, Counter: 1, Len: 2}},
		{"list_del.update.bin", DeleteID{PeerIdx: 0, Counter: 1, Len: 1}},
	}
	for _, c := range cases {
		t.Run(c.fixture, func(t *testing.T) {
			ids, err := DecodeDeleteIDs(blockWithDeletes(t, c.fixture).DeleteIDs)
			if err != nil {
				t.Fatalf("DecodeDeleteIDs: %v", err)
			}
			if len(ids) != 1 {
				t.Fatalf("got %d delete ids, want 1", len(ids))
			}
			if ids[0] != c.want {
				t.Errorf("got %+v, want %+v", ids[0], c.want)
			}
		})
	}
}

// TestDecodeDeleteIDsCrossPeer covers the case the span exists for: the deleting
// peer is not the peer whose elements are removed, so PeerIdx must resolve
// through the block's own peer table rather than defaulting to the author.
func TestDecodeDeleteIDsCrossPeer(t *testing.T) {
	blk := blockWithDeletes(t, "cross_del.update.bin")
	ids, err := DecodeDeleteIDs(blk.DeleteIDs)
	if err != nil {
		t.Fatalf("DecodeDeleteIDs: %v", err)
	}
	if len(ids) != 1 {
		t.Fatalf("got %d delete ids, want 1", len(ids))
	}
	hdr, err := DecodeHeader(blk.Header, int(blk.NChanges))
	if err != nil {
		t.Fatalf("DecodeHeader: %v", err)
	}
	d := ids[0]
	if d.PeerIdx < 0 || int(d.PeerIdx) >= len(hdr.Peers) {
		t.Fatalf("peer index %d out of range for peer table %v", d.PeerIdx, hdr.Peers)
	}
	if target := hdr.Peers[d.PeerIdx]; target != 1 {
		t.Errorf("delete targets peer %d, want peer 1 (the inserting peer)", target)
	}
	if hdr.Peers[0] != 2 {
		t.Errorf("block author = peer %d, want peer 2 (the deleting peer)", hdr.Peers[0])
	}
	if d.Counter != 1 || d.Len != 1 {
		t.Errorf("span = counter %d len %d, want counter 1 len 1", d.Counter, d.Len)
	}
}

func TestDecodeDeleteIDsEmpty(t *testing.T) {
	ids, err := DecodeDeleteIDs(nil)
	if err != nil || ids != nil {
		t.Errorf("DecodeDeleteIDs(nil) = %v, %v; want nil, nil", ids, err)
	}
}

// TestDecodePositionsFixtures decodes the fractional-index blob and compares it
// to the fractional_index values loro-crdt reported for the same fixtures.
func TestDecodePositionsFixtures(t *testing.T) {
	for _, name := range []string{"tree_simple.update.bin", "tree_meta.update.bin"} {
		t.Run(name, func(t *testing.T) {
			var got [][]byte
			for _, blk := range blocksOf(t, name) {
				pos, err := DecodePositions(blk.Positions)
				if err != nil {
					t.Fatalf("DecodePositions: %v", err)
				}
				got = append(got, pos...)
			}
			if len(got) == 0 {
				t.Fatal("no positions decoded")
			}
			for i, p := range got {
				if len(p) == 0 {
					t.Errorf("position %d is empty", i)
				}
			}
			if hex.EncodeToString(got[0]) != "80" {
				t.Errorf("first fractional index = %s, want 80", hex.EncodeToString(got[0]))
			}
		})
	}
}

func TestDecodePositionsEmpty(t *testing.T) {
	pos, err := DecodePositions(nil)
	if err != nil || pos != nil {
		t.Errorf("DecodePositions(nil) = %v, %v; want nil, nil", pos, err)
	}
}

func TestBlobDecodersRejectGarbage(t *testing.T) {
	garbage := []byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}
	if _, err := DecodeDeleteIDs(garbage); err == nil {
		t.Error("DecodeDeleteIDs: want error on garbage, got nil")
	}
	if _, err := DecodePositions(garbage); err == nil {
		t.Error("DecodePositions: want error on garbage, got nil")
	}
	// field_count/col_count must be exactly 1 and 2 for the positions blob.
	if _, err := DecodePositions([]byte{0x01, 0x05}); err == nil {
		t.Error("DecodePositions: want error on wrong column count, got nil")
	}
	// A truncated length-prefixed column must error rather than over-read.
	if _, err := DecodePositions([]byte{0x01, 0x02, 0x40}); err == nil {
		t.Error("DecodePositions: want error on truncated column, got nil")
	}
}

// TestMovableListCarriesNoPositions documents a format fact: a MovableList move
// references the moved element by id (from, from_idx, lamport) rather than by a
// fractional index, so its block carries no positions blob. Fractional indices
// are a Tree feature here.
func TestMovableListCarriesNoPositions(t *testing.T) {
	for _, blk := range blocksOf(t, "mlist.update.bin") {
		if len(blk.Positions) != 0 {
			t.Errorf("movable-list block has a %d-byte positions blob, want none", len(blk.Positions))
		}
	}
}
