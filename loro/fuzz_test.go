package loro

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
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
// bound, or hang. When decode succeeds, MergeState must not panic on the
// result either.
func FuzzDecodeUpdates(f *testing.F) {
	seedFuzz(f, ".update.bin")
	f.Fuzz(func(t *testing.T, data []byte) {
		u, err := DecodeUpdates(reseal(data))
		if err != nil {
			return
		}
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
		chs, err := decodeBlock(blk, &nameBudget{left: nameBudgetBase})
		if err != nil {
			return
		}
		u := &Updates{Changes: chs}
		_, _ = MergeState(u)
	})
}

// FuzzEncodeUpdates holds EncodeUpdates to DecodeUpdates on whatever decodes.
// Encoding the decoded changes may fail, with an error, when they break a rule
// loro's format assumes (a change without ops, ops that leave a gap, a
// dependency on the author's own peer that is not its previous op, a counter
// past 32 bits), which a block can say and loro never writes. Otherwise the
// blob it gives must decode to the same changes in the same blocks, and
// encoding those again must give the same bytes. It starts from every update
// fixture and every history of the encoder corpus, so the mutations begin from
// every op kind, child containers, commit messages and blocks of many changes.
func FuzzEncodeUpdates(f *testing.F) {
	seedFuzz(f, ".update.bin")
	if raw, err := os.ReadFile(filepath.Join("..", "testdata", "fixtures", "encode_corpus.json")); err == nil {
		var corpus []struct {
			Update string `json:"update"`
		}
		if json.Unmarshal(raw, &corpus) == nil {
			for _, c := range corpus {
				if b, err := base64.StdEncoding.DecodeString(c.Update); err == nil {
					f.Add(b)
				}
			}
		}
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		u, err := DecodeUpdates(reseal(data))
		if err != nil {
			return
		}
		blob, err := EncodeUpdates(u)
		if err != nil {
			return
		}
		back, err := DecodeUpdates(blob)
		if err != nil {
			t.Fatalf("the encoded blob does not decode: %v", err)
		}
		if d := diffUpdates(u, back); d != "" {
			t.Fatalf("decoding the encoded blob gave other changes: %s", d)
		}
		again, err := EncodeUpdates(back)
		if err != nil {
			t.Fatalf("the decoded changes do not encode again: %v", err)
		}
		if !bytes.Equal(again, blob) {
			t.Fatalf("encoding the decoded changes again gave other bytes:\n got  % x\n want % x", again, blob)
		}
	})
}
