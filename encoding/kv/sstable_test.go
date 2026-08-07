package kv

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// oplogOf extracts the oplog SSTable from a FastSnapshot fixture: the 22-byte
// fast header (magic + checksum + mode) is followed by a u32-LE section length
// and then the SSTable itself.
func oplogOf(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "fixtures", name))
	if err != nil {
		t.Skipf("fixture %s missing: %v", name, err)
	}
	if len(b) < 26 {
		t.Fatalf("%s: too short (%d bytes)", name, len(b))
	}
	body := b[22:]
	n := binary.LittleEndian.Uint32(body[:4])
	if int(n) > len(body)-4 {
		t.Fatalf("%s: oplog length %d exceeds body", name, n)
	}
	return body[4 : 4+int(n)]
}

// TestParseSSTableFixtures reads the oplog section of real loro-crdt snapshots.
// cross_del carries an LZ4-compressed block, so it also exercises the
// decompression branch. Change-block keys are 12 bytes; the reserved vv/fr/sv/sf
// keys are 2.
func TestParseSSTableFixtures(t *testing.T) {
	for _, name := range []string{"text_hi.snapshot.bin", "map_kv.snapshot.bin", "cross_del.snapshot.bin"} {
		t.Run(name, func(t *testing.T) {
			entries, err := ParseSSTable(oplogOf(t, name))
			if err != nil {
				t.Fatalf("ParseSSTable: %v", err)
			}
			if len(entries) == 0 {
				t.Fatal("no entries decoded")
			}
			blocks := 0
			for _, e := range entries {
				if len(e.Key) == 12 {
					blocks++
					if len(e.Value) == 0 {
						t.Errorf("change block %x has empty value", e.Key)
					}
				}
			}
			if blocks == 0 {
				t.Error("no 12-byte change-block keys found")
			}
		})
	}
}

func TestParseSSTableMalformed(t *testing.T) {
	good := oplogOf(t, "text_hi.snapshot.bin")

	corrupt := func(fn func([]byte)) []byte {
		b := append([]byte{}, good...)
		fn(b)
		return b
	}

	cases := []struct {
		name string
		in   []byte
	}{
		{"empty", nil},
		{"too short", good[:8]},
		{"bad magic", corrupt(func(b []byte) { copy(b[:4], "XXXX") })},
		{"bad version", corrupt(func(b []byte) { b[4] = 9 })},
		{"meta_offset past end", corrupt(func(b []byte) {
			binary.LittleEndian.PutUint32(b[len(b)-4:], uint32(len(b)))
		})},
		{"meta_offset before header", corrupt(func(b []byte) {
			binary.LittleEndian.PutUint32(b[len(b)-4:], 1)
		})},
		{"corrupt meta checksum", corrupt(func(b []byte) { b[len(b)-5] ^= 0xFF })},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := ParseSSTable(c.in); err == nil {
				t.Error("want error, got nil")
			}
		})
	}
}

// TestParseSSTableImplausibleBlockCount asserts a tiny table cannot declare a
// huge block count: the count is bounded by the bytes that could back it, so a
// crafted header errors instead of pre-allocating.
func TestParseSSTableImplausibleBlockCount(t *testing.T) {
	good := oplogOf(t, "text_hi.snapshot.bin")
	b := append([]byte{}, good...)
	off := binary.LittleEndian.Uint32(b[len(b)-4:])
	binary.LittleEndian.PutUint32(b[off:off+4], 1<<30) // num_blocks
	if _, err := ParseSSTable(b); err == nil {
		t.Error("want error for implausible block count, got nil")
	}
}
