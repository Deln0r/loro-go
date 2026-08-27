package change

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/Deln0r/loro-go/encoding/fast"
)

// TestFileRoundTrip decodes every block's blobs to their semantic forms,
// re-encodes them, reassembles the blocks + FastUpdates body + header (with a
// recomputed xxh32 checksum), and asserts the whole file is byte-identical to
// the original loro-crdt blob. All eight blobs go through their semantic
// encoders; nothing is copied through raw.
func TestFileRoundTrip(t *testing.T) {
	for _, name := range []string{
		"text_hi", "map_kv", "list_abc", "map_float",
		"text_del", "list_del", "map_del",
		"two_changes", "cross_del",
		"unicode_text", "map_mixed", "text_cjk_del", "counter",
		"tree_simple", "tree_meta", "tree_wide", "mlist",
		"richtext", "rt_one", "rt_two", "rt_overlap",
	} {
		orig, err := os.ReadFile(filepath.Join("..", "..", "testdata", "fixtures", name+".update.bin"))
		if err != nil {
			t.Skipf("fixture %s missing: %v", name, err)
		}
		h, err := fast.ParseHeader(orig)
		if err != nil {
			t.Fatal(err)
		}
		blocks, err := SplitBlocks(h.Body)
		if err != nil {
			t.Fatal(err)
		}
		reBlocks := make([][]byte, len(blocks))
		for bi := range blocks {
			reBlocks[bi] = roundTripBlock(t, name, blocks[bi])
		}
		body := EncodeFastUpdates(reBlocks)
		file := fast.Encode(fast.ModeFastUpdates, body)
		if !bytes.Equal(file, orig) {
			t.Errorf("%s file round-trip mismatch:\n got  % x\n want % x", name, file, orig)
		}
	}
}

// roundTripBlock decodes one block's blobs semantically, re-encodes them, and
// returns the reassembled block, asserting each blob is byte-identical.
func roundTripBlock(t *testing.T, name string, raw []byte) []byte {
	t.Helper()
	{
		blk, err := ParseBlock(raw)
		if err != nil {
			t.Fatal(err)
		}

		ops, err := DecodeOps(blk.Ops)
		if err != nil {
			t.Fatal(err)
		}
		reOps := EncodeOps(ops)
		if !bytes.Equal(reOps, blk.Ops) {
			t.Errorf("%s ops: % x != % x", name, reOps, blk.Ops)
		}

		conts, err := DecodeContainers(blk.CIDs)
		if err != nil {
			t.Fatal(err)
		}
		if reCids := EncodeContainers(conts); !bytes.Equal(reCids, blk.CIDs) {
			t.Errorf("%s cids: % x != % x", name, reCids, blk.CIDs)
		}

		keys, err := DecodeKeys(blk.Keys)
		if err != nil {
			t.Fatal(err)
		}
		if reKeys := EncodeKeys(keys); !bytes.Equal(reKeys, blk.Keys) {
			t.Errorf("%s keys: % x != % x", name, reKeys, blk.Keys)
		}

		ch, err := DecodeHeader(blk.Header, int(blk.NChanges))
		if err != nil {
			t.Fatal(err)
		}
		if reHeader := EncodeHeader(ch); !bytes.Equal(reHeader, blk.Header) {
			t.Errorf("%s header: % x != % x", name, reHeader, blk.Header)
		}

		cm, err := DecodeChangeMeta(blk.ChangeMeta, int(blk.NChanges))
		if err != nil {
			t.Fatal(err)
		}
		if reCM := EncodeChangeMeta(cm); !bytes.Equal(reCM, blk.ChangeMeta) {
			t.Errorf("%s change_meta: % x != % x", name, reCM, blk.ChangeMeta)
		}

		vr := NewValueReader(blk.Values)
		var vw ValueWriter
		for i := 0; i < ops.N(); i++ {
			val, err := vr.OpContent(ops.ValueKind[i])
			if err != nil {
				t.Fatal(err)
			}
			if err := vw.OpContent(ops.ValueKind[i], val); err != nil {
				t.Fatal(err)
			}
		}

		pos, err := DecodePositions(blk.Positions)
		if err != nil {
			t.Fatal(err)
		}
		if rePos := EncodePositions(pos); !bytes.Equal(rePos, blk.Positions) {
			t.Errorf("%s positions: % x != % x", name, rePos, blk.Positions)
		}

		dels, err := DecodeDeleteIDs(blk.DeleteIDs)
		if err != nil {
			t.Fatal(err)
		}
		if reDels := EncodeDeleteIDs(dels); !bytes.Equal(reDels, blk.DeleteIDs) {
			t.Errorf("%s delete ids: % x != % x", name, reDels, blk.DeleteIDs)
		}

		blk2 := *blk
		blk2.Positions = EncodePositions(pos)
		blk2.DeleteIDs = EncodeDeleteIDs(dels)
		blk2.Header = EncodeHeader(ch)
		blk2.ChangeMeta = EncodeChangeMeta(cm)
		blk2.Ops = reOps
		blk2.CIDs = EncodeContainers(conts)
		blk2.Keys = EncodeKeys(keys)
		blk2.Values = vw.Bytes()
		return EncodeBlock(&blk2)
	}
}

// TestValuesRoundTrip decodes each fixture's VALUES blob to op values and
// re-encodes it, asserting byte-identity against the original (covers the f64
// BIG-endian path via map_float).
func TestValuesRoundTrip(t *testing.T) {
	for _, name := range []string{"text_hi", "map_kv", "list_abc", "map_float", "text_del", "list_del", "map_del"} {
		b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "fixtures", name+".update.bin"))
		if err != nil {
			t.Skipf("fixture %s missing: %v", name, err)
		}
		h, err := fast.ParseHeader(b)
		if err != nil {
			t.Fatal(err)
		}
		blocks, err := SplitBlocks(h.Body)
		if err != nil {
			t.Fatal(err)
		}
		blk, err := ParseBlock(blocks[0])
		if err != nil {
			t.Fatal(err)
		}
		ops, err := DecodeOps(blk.Ops)
		if err != nil {
			t.Fatal(err)
		}
		vr := NewValueReader(blk.Values)
		var vw ValueWriter
		for i := 0; i < ops.N(); i++ {
			val, err := vr.OpContent(ops.ValueKind[i])
			if err != nil {
				t.Fatalf("%s op %d decode: %v", name, i, err)
			}
			if err := vw.OpContent(ops.ValueKind[i], val); err != nil {
				t.Fatalf("%s op %d encode: %v", name, i, err)
			}
		}
		if !bytes.Equal(vw.Bytes(), blk.Values) {
			t.Errorf("%s: re-encoded values % x, want % x", name, vw.Bytes(), blk.Values)
		}
	}
}
