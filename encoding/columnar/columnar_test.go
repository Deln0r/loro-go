package columnar

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

type goldenRow struct {
	strategy, ty, csv, full, col string
}

func loadGolden(t *testing.T) []goldenRow {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "columnar_golden.txt")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("golden missing (run: cd testdata/rustgen && cargo run > ../columnar_golden.txt): %v", err)
	}
	var rows []goldenRow
	for _, ln := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		f := strings.Split(ln, "|")
		if len(f) != 7 {
			t.Fatalf("bad golden line: %q", ln)
		}
		rows = append(rows, goldenRow{strategy: f[0], ty: f[1], csv: f[2], full: f[3], col: f[6]})
	}
	return rows
}

func wantInts(t *testing.T, csv string) []int64 {
	t.Helper()
	if csv == "" {
		return nil
	}
	var out []int64
	for _, s := range strings.Split(csv, ",") {
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			t.Fatalf("bad int %q: %v", s, err)
		}
		out = append(out, v)
	}
	return out
}

func toI64u(in []uint64) []int64 {
	out := make([]int64, len(in))
	for i, v := range in {
		out[i] = int64(v)
	}
	return out
}
func toI64u8(in []uint8) []int64 {
	out := make([]int64, len(in))
	for i, v := range in {
		out[i] = int64(v)
	}
	return out
}

func eqI64(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestStrategiesAgainstGolden(t *testing.T) {
	for _, row := range loadGolden(t) {
		col, err := hex.DecodeString(row.col)
		if err != nil {
			t.Fatalf("hex %q: %v", row.col, err)
		}
		name := row.strategy + "/" + row.ty + "/" + row.csv
		t.Run(name, func(t *testing.T) {
			if row.strategy == "BoolRle" {
				got, err := BoolRle(col)
				if err != nil {
					t.Fatal(err)
				}
				var gi []int64
				for _, v := range got {
					if v {
						gi = append(gi, 1)
					} else {
						gi = append(gi, 0)
					}
				}
				var wi []int64
				for _, s := range strings.Split(row.csv, ",") {
					switch s {
					case "true":
						wi = append(wi, 1)
					case "false":
						wi = append(wi, 0)
					}
				}
				if !eqI64(gi, wi) {
					t.Fatalf("BoolRle got %v want %v", got, wi)
				}
				return
			}

			want := wantInts(t, row.csv)
			var got []int64
			switch {
			case row.strategy == "Rle" && row.ty == "u8":
				v, e := AnyRleU8(col)
				if e != nil {
					t.Fatal(e)
				}
				got = toI64u8(v)
			case row.strategy == "Rle" && (row.ty == "u64" || row.ty == "u32"):
				v, e := AnyRleU64(col)
				if e != nil {
					t.Fatal(e)
				}
				got = toI64u(v)
			case row.strategy == "Rle" && row.ty == "i32":
				v, e := AnyRleI64(col)
				if e != nil {
					t.Fatal(e)
				}
				got = v
			case row.strategy == "DeltaRle":
				v, e := DeltaRleI64(col)
				if e != nil {
					t.Fatal(e)
				}
				got = v
			case row.strategy == "DeltaOfDelta":
				v, e := DeltaOfDelta(col)
				if e != nil {
					t.Fatal(e)
				}
				got = v
			default:
				t.Fatalf("unhandled %s/%s", row.strategy, row.ty)
			}
			if !eqI64(got, want) {
				t.Fatalf("%s got %v want %v (col % x)", name, got, want, col)
			}
		})
	}
}

func TestEncodeRoundTrip(t *testing.T) {
	for _, row := range loadGolden(t) {
		col, _ := hex.DecodeString(row.col)
		name := row.strategy + "/" + row.ty + "/" + row.csv
		t.Run(name, func(t *testing.T) {
			var got []byte
			switch {
			case row.strategy == "BoolRle":
				var vals []bool
				for _, s := range strings.Split(row.csv, ",") {
					switch s {
					case "true":
						vals = append(vals, true)
					case "false":
						vals = append(vals, false)
					}
				}
				got = EncodeBoolRle(vals)
			case row.strategy == "Rle" && row.ty == "u8":
				v, _ := AnyRleU8(col)
				got = EncodeAnyRleU8(v)
			case row.strategy == "Rle" && (row.ty == "u64" || row.ty == "u32"):
				v, _ := AnyRleU64(col)
				got = EncodeAnyRleU64(v)
			case row.strategy == "Rle" && row.ty == "i32":
				v, _ := AnyRleI64(col)
				got = EncodeAnyRleI64(v)
			case row.strategy == "DeltaRle":
				got = EncodeDeltaRleI64(wantInts(t, row.csv))
			case row.strategy == "DeltaOfDelta":
				got = EncodeDeltaOfDelta(wantInts(t, row.csv))
			default:
				t.Fatalf("unhandled %s/%s", row.strategy, row.ty)
			}
			if hex.EncodeToString(got) != row.col {
				t.Fatalf("%s: encoded % x, want %s", name, got, row.col)
			}
		})
	}
}

func TestEncodeColumnsFraming(t *testing.T) {
	for _, row := range loadGolden(t) {
		col, _ := hex.DecodeString(row.col)
		got := EncodeColumns([][]byte{col})
		if hex.EncodeToString(got) != row.full {
			t.Fatalf("%s: framed % x, want %s", row.csv, got, row.full)
		}
	}
}

func TestColumnsFraming(t *testing.T) {
	for _, row := range loadGolden(t) {
		full, _ := hex.DecodeString(row.full)
		cols, err := Columns(full)
		if err != nil {
			t.Fatalf("%s: Columns: %v", row.csv, err)
		}
		if len(cols) != 1 {
			t.Fatalf("%s: got %d columns, want 1", row.csv, len(cols))
		}
		if hex.EncodeToString(cols[0]) != row.col {
			t.Fatalf("%s: column % x, want %s", row.csv, cols[0], row.col)
		}
	}
}
