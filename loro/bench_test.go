package loro

import (
	"strconv"
	"testing"
)

// buildBenchDoc constructs a representative document: a text grown from many
// single-character inserts, a map with many keys, and a list with many items.
// It exercises all three column-heavy container types in one blob.
func buildBenchDoc() *Doc {
	d := NewDoc(1)
	for i := 0; i < 1000; i++ {
		d.TextInsert("t", i, "x")
	}
	for i := 0; i < 1000; i++ {
		d.MapSet("m", "k"+strconv.Itoa(i), int64(i))
	}
	items := make([]any, 1000)
	for i := range items {
		items[i] = int64(i)
	}
	d.ListInsert("l", 0, items)
	return d
}

func BenchmarkExportUpdates(b *testing.B) {
	d := buildBenchDoc()
	b.SetBytes(int64(len(d.ExportUpdates())))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = d.ExportUpdates()
	}
}

func BenchmarkDecodeUpdates(b *testing.B) {
	blob := buildBenchDoc().ExportUpdates()
	b.SetBytes(int64(len(blob)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := DecodeUpdates(blob); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkMergeState(b *testing.B) {
	blob := buildBenchDoc().ExportUpdates()
	u, err := DecodeUpdates(blob)
	if err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(len(blob)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := MergeState(u); err != nil {
			b.Fatal(err)
		}
	}
}
