package change

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/Deln0r/loro-go/encoding/fast"
)

// TestDumpBlocks is an exploratory dump (run with -v) used to pin the
// ContainerType and ValueKind enums empirically against real fixtures.
func TestDumpBlocks(t *testing.T) {
	for _, name := range []string{"two_changes", "cross_del"} {
		b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "fixtures", name+".update.bin"))
		if err != nil {
			t.Skipf("fixture %s missing: %v", name, err)
		}
		h, err := fast.ParseHeader(b)
		if err != nil {
			t.Fatalf("%s header: %v", name, err)
		}
		blocks, err := SplitBlocks(h.Body)
		if err != nil {
			t.Fatalf("%s split: %v", name, err)
		}
		t.Logf("=== %s: %d block(s) ===", name, len(blocks))
		for bi, raw := range blocks {
			blk, err := ParseBlock(raw)
			if err != nil {
				t.Fatalf("%s block %d: %v", name, bi, err)
			}
			t.Logf("  cs=%d cl=%d ls=%d ll=%d n=%d", blk.CounterStart, blk.CounterLen, blk.LamportStart, blk.LamportLen, blk.NChanges)
			t.Logf("  header     %x", blk.Header)
			t.Logf("  changeMeta %x", blk.ChangeMeta)
			t.Logf("  cids       %x", blk.CIDs)
			t.Logf("  keys       %x", blk.Keys)
			t.Logf("  positions  %x", blk.Positions)
			t.Logf("  ops        %x", blk.Ops)
			t.Logf("  deleteIDs  %x", blk.DeleteIDs)
			t.Logf("  values     %x", blk.Values)
		}
		ops, _ := os.ReadFile(filepath.Join("..", "..", "testdata", "fixtures", name+".ops.json"))
		t.Logf("  ops.json: %s", string(ops))
		_ = hex.EncodeToString
	}
}
