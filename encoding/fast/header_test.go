package fast

import (
	"os"
	"path/filepath"
	"testing"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "fixtures", name))
	if err != nil {
		t.Skipf("fixture %s missing (run: cd testdata/gen && node gen.mjs): %v", name, err)
	}
	return b
}

func TestParseHeaderFastUpdates(t *testing.T) {
	b := fixture(t, "text_hi.update.bin")
	h, err := ParseHeader(b)
	if err != nil {
		t.Fatalf("ParseHeader: %v", err)
	}
	if h.Mode != ModeFastUpdates {
		t.Errorf("mode = %d, want %d (FastUpdates)", h.Mode, ModeFastUpdates)
	}
	if len(h.Body) != len(b)-HeaderSize {
		t.Errorf("body len = %d, want %d", len(h.Body), len(b)-HeaderSize)
	}
	if err := VerifyChecksum(b); err != nil {
		t.Errorf("VerifyChecksum on real loro-crdt blob: %v", err)
	}
}

// TestChecksumAllFixtures proves the xxh32 impl + checksum scheme byte-for-byte
// against every real loro-crdt 1.12.5 fixture (closes WIRE_REFERENCE GAP #6).
func TestChecksumAllFixtures(t *testing.T) {
	for _, name := range []string{
		"text_hi.update.bin", "text_hi.snapshot.bin",
		"map_kv.update.bin", "map_kv.snapshot.bin",
		"list_abc.update.bin", "list_abc.snapshot.bin",
	} {
		b := fixture(t, name)
		if err := VerifyChecksum(b); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestParseHeaderFastSnapshot(t *testing.T) {
	b := fixture(t, "text_hi.snapshot.bin")
	h, err := ParseHeader(b)
	if err != nil {
		t.Fatalf("ParseHeader: %v", err)
	}
	if h.Mode != ModeFastSnapshot {
		t.Errorf("mode = %d, want %d (FastSnapshot)", h.Mode, ModeFastSnapshot)
	}
}

func TestParseHeaderRejects(t *testing.T) {
	if _, err := ParseHeader([]byte("lor")); err != ErrShort {
		t.Errorf("short: got %v", err)
	}
	bad := append([]byte("xxxx"), make([]byte, HeaderSize)...)
	if _, err := ParseHeader(bad); err != ErrMagic {
		t.Errorf("magic: got %v", err)
	}
	out := append([]byte("loro"), make([]byte, HeaderSize-4)...)
	out[20], out[21] = 0x00, 0x01 // ModeOutdatedRle, big-endian
	if _, err := ParseHeader(out); err != ErrOutdated {
		t.Errorf("outdated: got %v", err)
	}
}
