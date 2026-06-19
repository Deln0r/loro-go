package columnar

import "testing"

// FuzzStrategies feeds arbitrary bytes to every column-strategy decoder. A
// positive RLE run repeats one value an arbitrary number of times, so these must
// stay bounded (maxColRows) and never panic, OOM, or hang on hostile input.
func FuzzStrategies(f *testing.F) {
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, b []byte) {
		_, _ = Columns(b)
		_, _ = AnyRleU64(b)
		_, _ = AnyRleI64(b)
		_, _ = AnyRleU8(b)
		_, _ = BoolRle(b)
		_, _ = DeltaRleI64(b)
		_, _ = DeltaOfDelta(b)
	})
}
