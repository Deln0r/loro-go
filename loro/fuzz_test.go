package loro

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Deln0r/loro-go/encoding/change"
	"github.com/Deln0r/loro-go/encoding/fast"
	"github.com/Deln0r/loro-go/encoding/xxh32"
)

// seedFuzz adds every fixture file with the given suffix to the fuzz corpus so
// the fuzzer starts from valid blobs and mutates outward from real structure.
func seedFuzz(f *testing.F, suffix string) {
	dir := filepath.Join("..", "testdata", "fixtures")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), suffix) {
			continue
		}
		if b, err := os.ReadFile(filepath.Join(dir, e.Name())); err == nil {
			f.Add(b)
		}
	}
}

// reseal recomputes the header checksum over a mutated blob, which is exactly
// what a hostile peer does before sending one. Without it these two targets
// could only test the checksum wall: any mutation of the body breaks the
// xxh32, DecodeUpdates and DecodeSnapshot reject it on the spot, and nothing
// below the checksum is ever reached. Measured before this existed: after a
// week of nightly runs the FuzzDecodeSnapshot corpus had grown by nothing
// since the first night, and of 14 cached inputs one got past the checksum.
// The rejection itself is unit-tested in encoding/fast, so the fuzzer loses
// nothing by skipping it.
func reseal(data []byte) []byte {
	if len(data) < fast.HeaderSize {
		return data
	}
	out := append([]byte(nil), data...)
	binary.LittleEndian.PutUint32(out[16:20], xxh32.Checksum(out[20:], xxh32.Seed))
	return out
}

// FuzzDecodeUpdates asserts the FastUpdates decoder never panics on arbitrary
// bytes: malformed input must return an error, not crash, allocate without
// bound, or hang. When decode succeeds, state reconstruction must not panic
// either, so both BuildState and MergeState are exercised on the result.
func FuzzDecodeUpdates(f *testing.F) {
	seedFuzz(f, ".update.bin")
	f.Fuzz(func(t *testing.T, data []byte) {
		u, err := DecodeUpdates(reseal(data))
		if err != nil {
			return
		}
		_, _ = BuildState(u)
		_, _ = MergeState(u)
	})
}

// FuzzDecodeSnapshot is the same robustness contract for the FastSnapshot
// (oplog SSTable) decode path.
func FuzzDecodeSnapshot(f *testing.F) {
	seedFuzz(f, ".snapshot.bin")
	f.Fuzz(func(t *testing.T, data []byte) {
		u, err := DecodeSnapshot(reseal(data))
		if err != nil {
			return
		}
		_, _ = BuildState(u)
		_, _ = MergeState(u)
	})
}

// FuzzDecodeBlock exercises the change-block decoder on arbitrary bytes, below
// the header checksum that DecodeUpdates would otherwise gate. A malicious peer
// controls the body and can recompute a valid checksum, so the columnar, value,
// header, positions and delete decoders must themselves be robust: a malformed
// block must error, never panic, allocate without bound, or hang.
func FuzzDecodeBlock(f *testing.F) {
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, data []byte) {
		blk, err := change.ParseBlock(data)
		if err != nil {
			return
		}
		chs, err := decodeBlock(blk)
		if err != nil {
			return
		}
		u := &Updates{Changes: chs}
		_, _ = BuildState(u)
		_, _ = MergeState(u)
	})
}
