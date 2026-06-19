package kv

import "testing"

// FuzzParseSSTable feeds arbitrary bytes to the SSTable reader (block-meta count,
// offsets, prefix-compressed keys, optional LZ4 blocks). It must error rather
// than panic, allocate without bound, or hang on malformed tables.
func FuzzParseSSTable(f *testing.F) {
	f.Add([]byte("LORO\x00\x00\x00\x00\x00"))
	f.Fuzz(func(t *testing.T, b []byte) {
		_, _ = ParseSSTable(b)
	})
}
