package change

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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
		"rt_struct",
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

		vr := NewValueReader(blk.Values, keys)
		vw := ValueWriter{Key: keyIn(keys)}
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
		keys, err := DecodeKeys(blk.Keys)
		if err != nil {
			t.Fatal(err)
		}
		vr := NewValueReader(blk.Values, keys)
		vw := ValueWriter{Key: keyIn(keys)}
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

// TestCorpusRoundTrip decodes and re-encodes every block of every history in
// the random corpora, which hold the op kinds the named fixtures barely touch:
// MovableList moves and sets, tree moves and deletes, marks, backwards
// deletes, and style values that are objects of one key. Each blob and each
// file must come back byte-identical.
func TestCorpusRoundTrip(t *testing.T) {
	for _, name := range []string{
		"seq_single_corpus.json", "seq_concurrent_corpus.json", "ordering_corpus.json",
		"movable_single_corpus.json", "movable_concurrent_corpus.json", "tree_corpus.json",
		"rich_corpus.json",
	} {
		raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "fixtures", name))
		if err != nil {
			t.Fatal(err)
		}
		var corpus []struct {
			Seed   int    `json:"seed"`
			Update string `json:"update"`
		}
		if err := json.Unmarshal(raw, &corpus); err != nil {
			t.Fatal(err)
		}
		for _, c := range corpus {
			orig, err := base64.StdEncoding.DecodeString(c.Update)
			if err != nil {
				t.Fatal(err)
			}
			h, err := fast.ParseHeader(orig)
			if err != nil {
				t.Fatalf("%s seed %d: %v", name, c.Seed, err)
			}
			blocks, err := SplitBlocks(h.Body)
			if err != nil {
				t.Fatalf("%s seed %d: %v", name, c.Seed, err)
			}
			label := fmt.Sprintf("%s seed %d", name, c.Seed)
			reBlocks := make([][]byte, len(blocks))
			for bi := range blocks {
				reBlocks[bi] = roundTripBlock(t, label, blocks[bi])
			}
			if file := fast.Encode(fast.ModeFastUpdates, EncodeFastUpdates(reBlocks)); !bytes.Equal(file, orig) {
				t.Errorf("%s: file round-trip mismatch", label)
			}
		}
	}
}

// keyIn is the key register of a re-encoder of a decoded block: every key a
// value names is already in the block's arena, at its first index.
func keyIn(keys []string) func(string) uint64 {
	idx := make(map[string]uint64, len(keys))
	for i := len(keys) - 1; i >= 0; i-- {
		idx[keys[i]] = uint64(i)
	}
	return func(s string) uint64 { return idx[s] }
}

// TestNestedMapsRoundTripByValue re-encodes the values of a block holding maps
// nested in values, one of them with three keys. loro writes a map's keys in
// the order of its hash map and ValueWriter in sorted order, so the bytes of a
// map of two or more keys can differ; what must hold is that every value reads
// back the same, keys resolved through the block's own arena.
func TestNestedMapsRoundTripByValue(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "fixtures", "nested_values.update.bin"))
	if err != nil {
		t.Fatal(err)
	}
	h, err := fast.ParseHeader(raw)
	if err != nil {
		t.Fatal(err)
	}
	blocks, err := SplitBlocks(h.Body)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range blocks {
		blk, err := ParseBlock(b)
		if err != nil {
			t.Fatal(err)
		}
		ops, err := DecodeOps(blk.Ops)
		if err != nil {
			t.Fatal(err)
		}
		keys, err := DecodeKeys(blk.Keys)
		if err != nil {
			t.Fatal(err)
		}
		var vals []any
		vr := NewValueReader(blk.Values, keys)
		vw := ValueWriter{Key: keyIn(keys)}
		for i := 0; i < ops.N(); i++ {
			v, err := vr.OpContent(ops.ValueKind[i])
			if err != nil {
				t.Fatal(err)
			}
			vals = append(vals, v)
			if err := vw.OpContent(ops.ValueKind[i], v); err != nil {
				t.Fatal(err)
			}
		}
		back := NewValueReader(vw.Bytes(), keys)
		for i := 0; i < ops.N(); i++ {
			v, err := back.OpContent(ops.ValueKind[i])
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(v, vals[i]) {
				t.Errorf("op %d: re-encoded value reads %#v, want %#v", i, v, vals[i])
			}
		}
	}
}
