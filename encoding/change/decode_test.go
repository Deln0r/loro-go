package change

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Deln0r/loro-go/encoding/fast"
)

// decoded is a fully-decoded op tied to its container, for assertions.
type decoded struct {
	containerKind ContainerType
	containerName string
	prop          int64
	value         any
}

func decodeFixture(t *testing.T, name string) []decoded {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "fixtures", name+".update.bin"))
	if err != nil {
		t.Skipf("fixture %s missing: %v", name, err)
	}
	h, err := fast.ParseHeader(b)
	if err != nil {
		t.Fatal(err)
	}
	if err := fast.VerifyChecksum(b); err != nil {
		t.Fatal(err)
	}
	blocks, err := SplitBlocks(h.Body)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 1 {
		t.Fatalf("%s: %d blocks, want 1", name, len(blocks))
	}
	blk, err := ParseBlock(blocks[0])
	if err != nil {
		t.Fatal(err)
	}
	containers, err := DecodeContainers(blk.CIDs)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := DecodeKeys(blk.Keys)
	if err != nil {
		t.Fatal(err)
	}
	ops, err := DecodeOps(blk.Ops)
	if err != nil {
		t.Fatal(err)
	}
	vr := NewValueReader(blk.Values)

	var out []decoded
	for i := 0; i < ops.N(); i++ {
		c := containers[ops.ContainerIdx[i]]
		cname := ""
		if c.IsRoot && int(c.KeyOrCounter) < len(keys) {
			cname = keys[c.KeyOrCounter]
		}
		val, err := vr.OpContent(ops.ValueKind[i])
		if err != nil {
			t.Fatalf("%s op %d value: %v", name, i, err)
		}
		out = append(out, decoded{c.Kind, cname, ops.Prop[i], val})
	}
	if !vr.Empty() {
		t.Errorf("%s: values stream not fully consumed (%d bytes left)", name, vr.r.Remaining())
	}
	return out
}

func TestDecodeTextHi(t *testing.T) {
	ops := decodeFixture(t, "text_hi")
	if len(ops) != 1 {
		t.Fatalf("ops = %d, want 1", len(ops))
	}
	o := ops[0]
	if o.containerKind != CText || o.containerName != "t" {
		t.Errorf("container = %v %q, want Text t", o.containerKind, o.containerName)
	}
	if o.prop != 0 || o.value != "hi" {
		t.Errorf("op = pos %d value %v, want pos 0 \"hi\"", o.prop, o.value)
	}
}

func TestDecodeMapKV(t *testing.T) {
	ops := decodeFixture(t, "map_kv")
	if len(ops) != 2 {
		t.Fatalf("ops = %d, want 2", len(ops))
	}
	// For a Map op, prop is the key index into the keys pool.
	keys := []string{"k", "n"} // keys[0], keys[1]
	want := []struct {
		key string
		val any
	}{
		{"k", "v"},
		{"n", int64(42)},
	}
	for i, o := range ops {
		if o.containerKind != CMap || o.containerName != "m" {
			t.Errorf("op %d container = %v %q, want Map m", i, o.containerKind, o.containerName)
		}
		if int(o.prop) >= len(keys) || keys[o.prop] != want[i].key {
			t.Errorf("op %d key idx %d, want key %q", i, o.prop, want[i].key)
		}
		if !reflect.DeepEqual(o.value, want[i].val) {
			t.Errorf("op %d value = %#v (%T), want %#v", i, o.value, o.value, want[i].val)
		}
	}
}

// TestDecodeMapFloat validates the f64 BIG-endian path against real loro bytes.
func TestDecodeMapFloat(t *testing.T) {
	ops := decodeFixture(t, "map_float")
	if len(ops) != 3 {
		t.Fatalf("ops = %d, want 3", len(ops))
	}
	want := []float64{3.14, 1e308, -2.5} // insertion order: pi, big, neg
	for i, o := range ops {
		if o.containerKind != CMap || o.containerName != "m" {
			t.Errorf("op %d container = %v %q, want Map m", i, o.containerKind, o.containerName)
		}
		f, ok := o.value.(float64)
		if !ok {
			t.Fatalf("op %d value type %T, want float64", i, o.value)
		}
		if f != want[i] {
			t.Errorf("op %d float = %v, want %v (BE decode)", i, f, want[i])
		}
	}
}

func TestDecodeListABC(t *testing.T) {
	ops := decodeFixture(t, "list_abc")
	if len(ops) != 1 {
		t.Fatalf("ops = %d, want 1", len(ops))
	}
	o := ops[0]
	if o.containerKind != CList || o.containerName != "l" {
		t.Errorf("container = %v %q, want List l", o.containerKind, o.containerName)
	}
	want := []any{"a", "b", "c"}
	if !reflect.DeepEqual(o.value, want) {
		t.Errorf("value = %#v, want %#v", o.value, want)
	}
}
