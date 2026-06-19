package lz4

import (
	"encoding/binary"
	"testing"
)

// FuzzDecompressFrame feeds arbitrary bytes to the LZ4 frame decompressor. Match
// copies let a small block expand many-fold, so the decoder must bound its output
// (maxDecompressed) and never panic, OOM, or hang on a hostile frame.
func FuzzDecompressFrame(f *testing.F) {
	var magic [4]byte
	binary.LittleEndian.PutUint32(magic[:], frameMagic)
	f.Add(magic[:])
	f.Fuzz(func(t *testing.T, b []byte) {
		_, _ = DecompressFrame(b)
	})
}
