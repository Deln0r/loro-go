package lz4

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/Deln0r/loro-go/encoding/xxh32"
)

// frame assembles a minimal LZ4 frame around one block: magic, a descriptor with
// no optional fields (version 01), the header checksum byte, the block, and the
// end mark. uncompressed marks the block as stored rather than LZ4-compressed.
func frame(block []byte, uncompressed bool) []byte {
	out := make([]byte, 4)
	binary.LittleEndian.PutUint32(out, frameMagic)
	const flg, bd = 0x40, 0x70 // version 01, no block/content checksum, no dict
	out = append(out, flg, bd)
	out = append(out, byte(xxh32.Checksum(out[4:6], 0)>>8)) // HC
	size := uint32(len(block))
	if uncompressed {
		size |= 1 << 31
	}
	var sz [4]byte
	binary.LittleEndian.PutUint32(sz[:], size)
	out = append(out, sz[:]...)
	out = append(out, block...)
	return append(out, 0, 0, 0, 0) // end mark
}

func TestDecompressFrameStoredBlock(t *testing.T) {
	want := []byte("hello loro")
	got, err := DecompressFrame(frame(want, true))
	if err != nil {
		t.Fatalf("DecompressFrame: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestDecompressFrameMatchCopy covers the compressed path including an
// overlapping match: literals "abc" then a 4-byte match at offset 3, which
// replicates into the bytes it is still producing ("abc" + "abca").
func TestDecompressFrameMatchCopy(t *testing.T) {
	block := []byte{0x30, 'a', 'b', 'c', 0x03, 0x00} // token litLen=3 matchLen=4, offset 3
	got, err := DecompressFrame(frame(block, false))
	if err != nil {
		t.Fatalf("DecompressFrame: %v", err)
	}
	if want := []byte("abcabca"); !bytes.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestDecompressFrameMalformed(t *testing.T) {
	good := frame([]byte("payload"), true)

	cases := []struct {
		name string
		in   []byte
	}{
		{"empty", nil},
		{"too short", good[:5]},
		{"bad magic", append([]byte{0, 0, 0, 0}, good[4:]...)},
		{"truncated block", good[:len(good)-6]},
		{"no end mark", good[:len(good)-4]},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := DecompressFrame(c.in); err == nil {
				t.Error("want error, got nil")
			}
		})
	}

	t.Run("bad header checksum", func(t *testing.T) {
		bad := append([]byte{}, good...)
		bad[6] ^= 0xFF // flip the HC byte
		if _, err := DecompressFrame(bad); err == nil {
			t.Error("want error, got nil")
		}
	})

	t.Run("unsupported version", func(t *testing.T) {
		bad := append([]byte{}, good...)
		bad[4] = 0x80 // version bits 10
		if _, err := DecompressFrame(bad); err == nil {
			t.Error("want error, got nil")
		}
	})
}

// TestDecompressFrameDecompressionBomb asserts a small block cannot expand past
// the output ceiling: a long run of match extensions must be rejected rather
// than allocating without bound.
func TestDecompressFrameDecompressionBomb(t *testing.T) {
	// literals "a", then a match at offset 1 with a very long extended length.
	block := []byte{0x1F, 'a', 0x01, 0x00}
	for i := 0; i < 300000; i++ {
		block = append(block, 255) // each 255 adds 255 to matchLen and continues
	}
	block = append(block, 0)
	if _, err := DecompressFrame(frame(block, false)); err == nil {
		t.Error("want error for oversized match, got nil")
	}
}
