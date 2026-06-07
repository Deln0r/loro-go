package change

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/Deln0r/loro-go/encoding/fast"
)

// TestFileRoundTrip decodes ops/cids/keys/values to their semantic forms,
// re-encodes them, reassembles the block + FastUpdates body + header (with a
// recomputed xxh32 checksum), and asserts the whole file is byte-identical to
// the original loro-crdt blob. header/change_meta/positions/delete_ids pass
// through raw (their semantic encoders are the remaining gap).
func TestFileRoundTrip(t *testing.T) {
	for _, name := range []string{"text_hi", "map_kv", "list_abc", "map_float"} {
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
		blk, err := ParseBlock(blocks[0])
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

		blk2 := *blk
		blk2.Header = EncodeHeader(ch)
		blk2.ChangeMeta = EncodeChangeMeta(cm)
		blk2.Ops = reOps
		blk2.CIDs = EncodeContainers(conts)
		blk2.Keys = EncodeKeys(keys)
		blk2.Values = vw.Bytes()

		body := EncodeFastUpdates([][]byte{EncodeBlock(&blk2)})
		file := fast.Encode(fast.ModeFastUpdates, body)
		if !bytes.Equal(file, orig) {
			t.Errorf("%s file round-trip mismatch:\n got  % x\n want % x", name, file, orig)
		}
	}
}

// TestValuesRoundTrip decodes each fixture's VALUES blob to op values and
// re-encodes it, asserting byte-identity against the original (covers the f64
// BIG-endian path via map_float).
func TestValuesRoundTrip(t *testing.T) {
	for _, name := range []string{"text_hi", "map_kv", "list_abc", "map_float"} {
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
