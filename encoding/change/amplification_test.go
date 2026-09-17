package change

import (
	"runtime"
	"testing"

	"github.com/Deln0r/loro-go/encoding/postcard"
)

// nestedHeaders builds depth container headers of kind k, each declaring pad
// entries, followed by pad bytes of an unknown kind so the innermost element
// fails at once. Every header passes the per-level "count <= remaining bytes"
// check, because the tail alone is pad bytes long.
func nestedHeaders(k loroValueKind, depth, pad int) []byte {
	var b []byte
	for d := 0; d < depth; d++ {
		b = append(b, byte(k))
		b = postcard.AppendUvarint(b, uint64(pad))
		if k == lvMap {
			b = append(b, 0) // an empty key: one length byte of zero
		}
	}
	for i := 0; i < pad; i++ {
		b = append(b, 0xFF)
	}
	return b
}

// TestLoroValueAllocationIsLinear pins the decoder against allocation
// amplification through nesting.
//
// The count bound on a list or map is checked against the bytes remaining, but
// that check is local to one level, and every nested header is checked against
// the same remaining bytes. When the decoder reserved storage for the declared
// count up front, a few hundred bytes of nested headers in front of a tail made
// every level reserve room for the whole tail. Measured before the fix: a 1 MB
// value of 128 nested maps allocated 10.7 GB and took 2.2 s; nested lists
// allocated 2.1 GB. Here the input is 64 KB, which allocated 672 MB (maps) and
// 134 MB (lists) before, so a regression fails this test instead of taking the
// runner down.
func TestLoroValueAllocationIsLinear(t *testing.T) {
	const pad = 64 << 10
	for _, c := range []struct {
		name string
		kind loroValueKind
	}{
		{"nested lists", lvList},
		{"nested maps", lvMap},
	} {
		t.Run(c.name, func(t *testing.T) {
			in := nestedHeaders(c.kind, maxLoroValueDepth, pad)

			runtime.GC()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			_, err := NewValueReader(in).LoroValue()
			runtime.ReadMemStats(&after)

			if err == nil {
				t.Fatal("a value whose innermost element is garbage decoded without error")
			}
			allocated := after.TotalAlloc - before.TotalAlloc
			// 60x the input. The fixed decoder allocates about 1 MB here, bounded
			// by depth times the preallocation cap rather than by the input;
			// the unfixed one allocated hundreds of megabytes.
			if limit := uint64(60 * len(in)); allocated > limit {
				t.Errorf("decoding %d bytes allocated %d bytes, want at most %d: declared counts are being reserved up front again",
					len(in), allocated, limit)
			}
		})
	}
}
