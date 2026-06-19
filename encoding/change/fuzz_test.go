package change

import "testing"

// FuzzBlobDecoders feeds arbitrary bytes to each single-blob change decoder. None
// may panic, allocate without bound, or hang: every blob in a change block is
// attacker-controlled once a malicious peer recomputes the outer checksum.
func FuzzBlobDecoders(f *testing.F) {
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, b []byte) {
		_, _ = DecodeOps(b)
		_, _ = DecodeContainers(b)
		_, _ = DecodeKeys(b)
		_, _ = DecodeDeleteIDs(b)
		_, _ = DecodePositions(b)
	})
}

// FuzzDecodeHeader exercises the count-driven header/change_meta decoders. The
// change count n is taken from the input so the peer/atom/dep allocation caps are
// hit with both small and large declared counts.
func FuzzDecodeHeader(f *testing.F) {
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, b []byte) {
		n := 1
		if len(b) >= 2 {
			n = int(b[0]) | int(b[1])<<8
			b = b[2:]
		}
		_, _ = DecodeHeader(b, n)
		_, _ = DecodeChangeMeta(b, n)
	})
}

// FuzzLoroValue exercises the nested self-describing value reader (recursion depth
// cap and length-prefixed list/map/binary/string bounds).
func FuzzLoroValue(f *testing.F) {
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, b []byte) {
		vr := NewValueReader(b)
		_, _ = vr.LoroValue()
	})
}
