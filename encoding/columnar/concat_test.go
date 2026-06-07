package columnar

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/Deln0r/loro-go/encoding/postcard"
)

// TestConcatReadersAdvance verifies the count-limited readers decode the golden
// columns AND advance the reader exactly to the column end (a sentinel byte
// appended after the column must remain unread).
func TestConcatReadersAdvance(t *testing.T) {
	for _, row := range loadGolden(t) {
		col, _ := hex.DecodeString(row.col)
		count := 0
		if row.csv != "" {
			count = len(strings.Split(row.csv, ","))
		}
		buf := append(append([]byte{}, col...), 0xAA) // sentinel
		r := postcard.NewReader(buf)
		name := row.strat + "/" + row.ty + "/" + row.csv

		var got []int64
		switch {
		case row.strat == "BoolRle":
			v, err := BoolRleN(r, count)
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			for _, b := range v {
				if b {
					got = append(got, 1)
				} else {
					got = append(got, 0)
				}
			}
		case row.strat == "Rle" && (row.ty == "u64" || row.ty == "u32" || row.ty == "u8"):
			v, err := AnyRleNU64(r, count)
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			got = toI64u(v)
		case row.strat == "DeltaOfDelta":
			v, err := DeltaOfDeltaN(r, count)
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			got = v
		default:
			continue // Rle/i32 and DeltaRle not used raw-concatenated
		}

		if r.I != len(col) {
			t.Errorf("%s: reader at %d, want %d (must stop at column end)", name, r.I, len(col))
		}
		if row.strat == "BoolRle" {
			var want []int64
			for _, s := range strings.Split(row.csv, ",") {
				if s == "true" {
					want = append(want, 1)
				} else if s == "false" {
					want = append(want, 0)
				}
			}
			if !eqI64(got, want) {
				t.Errorf("%s: got %v want %v", name, got, want)
			}
			continue
		}
		if !eqI64(got, wantInts(t, row.csv)) {
			t.Errorf("%s: got %v want %v", name, got, wantInts(t, row.csv))
		}
	}
}
