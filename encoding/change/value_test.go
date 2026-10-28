package change

import (
	"bytes"
	"maps"
	"math"
	"reflect"
	"slices"
	"testing"

	"github.com/Deln0r/loro-go/encoding/postcard"
)

// TestOpContentRoundTrip pins ValueWriter as the exact inverse of ValueReader for
// every op value kind that carries inline bytes in the VALUES stream.
func TestOpContentRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		vk   ValueKind
		val  any
	}{
		{"null", VKNull, nil},
		{"true", VKTrue, true},
		{"false", VKFalse, false},
		{"i64 zero", VKI64, int64(0)},
		{"i64 positive", VKI64, int64(1) << 40},
		{"i64 negative", VKI64, int64(-1234567890123)},
		{"i64 min", VKI64, int64(math.MinInt64)},
		{"i64 max", VKI64, int64(math.MaxInt64)},
		{"f64", VKF64, 3.14159},
		{"f64 huge", VKF64, 1e308},
		{"f64 tiny negative", VKF64, -2.5e-300},
		{"str", VKStr, "hello 世界"},
		{"str empty", VKStr, ""},
		{"binary", VKBinary, []byte{0x00, 0xFF, 0x7F}},
		{"binary empty", VKBinary, []byte{}},
		{"lorovalue int", VKLoroValue, int64(42)},
		{"lorovalue string", VKLoroValue, "nested"},
		{"lorovalue bool", VKLoroValue, true},
		{"lorovalue nil", VKLoroValue, nil},
		{"lorovalue float", VKLoroValue, -0.125},
		{"lorovalue binary", VKLoroValue, []byte{1, 2, 3}},
		{"lorovalue map", VKLoroValue, map[string]any{"b": int64(1), "a": "x"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var a testArena
			w := ValueWriter{Key: a.key}
			if err := w.OpContent(c.vk, c.val); err != nil {
				t.Fatalf("write: %v", err)
			}
			got, err := NewValueReader(w.Bytes(), a.keys).OpContent(c.vk)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if !reflect.DeepEqual(got, c.val) {
				t.Errorf("round-trip = %#v, want %#v", got, c.val)
			}
		})
	}
}

// TestF64WireIsBigEndian guards the single most likely silent-corruption bug in
// this format: f64 in the VALUES stream is big-endian, unlike plain postcard.
func TestF64WireIsBigEndian(t *testing.T) {
	var w ValueWriter
	w.F64(1.0)
	want := []byte{0x3F, 0xF0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
	if got := w.Bytes(); !bytes.Equal(got, want) {
		t.Fatalf("f64(1.0) encoded % x, want % x (big-endian)", got, want)
	}

	// Sign of negative zero must survive the round-trip bit-for-bit.
	var w2 ValueWriter
	negZero := math.Copysign(0, -1)
	w2.F64(negZero)
	got, err := NewValueReader(w2.Bytes(), nil).F64()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if math.Float64bits(got) != math.Float64bits(negZero) {
		t.Errorf("negative zero round-trip lost the sign bit: %x", math.Float64bits(got))
	}
}

func TestLoroValueNestedRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		val  any
	}{
		{"list of scalars", []any{int64(1), "two", 3.5, true, nil}},
		{"empty list", []any{}},
		{"nested list", []any{[]any{int64(1)}, []any{[]any{"deep"}}}},
		{"single-key map", map[string]any{"k": int64(7)}},
		{"map with list", map[string]any{"xs": []any{int64(1), int64(2)}}},
		{"map of several keys", map[string]any{"z": int64(1), "a": "two", "m": nil}},
		{"maps inside maps and lists", map[string]any{"k": []any{map[string]any{"q": "s", "k": int64(2)}}}},
		{"child containers", map[string]any{"t": CText, "xs": []any{CMovableList, CTree}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var a testArena
			w := ValueWriter{Key: a.key}
			if err := w.LoroValue(c.val); err != nil {
				t.Fatalf("write: %v", err)
			}
			got, err := NewValueReader(w.Bytes(), a.keys).LoroValue()
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if !reflect.DeepEqual(got, c.val) {
				t.Errorf("round-trip = %#v, want %#v", got, c.val)
			}
		})
	}
}

func TestReadMark(t *testing.T) {
	var inner ValueWriter
	if err := inner.LoroValue("bold"); err != nil {
		t.Fatal(err)
	}
	buf := []byte{0x08} // info flags
	buf = postcard.AppendUvarint(buf, 5)
	buf = postcard.AppendUvarint(buf, 2)
	buf = append(buf, inner.Bytes()...)

	got, err := NewValueReader(buf, nil).ReadMark()
	if err != nil {
		t.Fatalf("ReadMark: %v", err)
	}
	want := Mark{Info: 0x08, Len: 5, KeyIdx: 2, Value: "bold"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v, want %#v", got, want)
	}
}

func TestReadListMove(t *testing.T) {
	buf := postcard.AppendUvarint(nil, 3)
	buf = postcard.AppendUvarint(buf, 9)
	buf = postcard.AppendUvarint(buf, 41)

	got, err := NewValueReader(buf, nil).ReadListMove()
	if err != nil {
		t.Fatalf("ReadListMove: %v", err)
	}
	if want := (ListMove{From: 3, FromIdx: 9, Lamport: 41}); got != want {
		t.Errorf("got %#v, want %#v", got, want)
	}
}

func TestReadRawTreeMove(t *testing.T) {
	build := func(parentNull bool) []byte {
		buf := postcard.AppendUvarint(nil, 1) // subject peer idx
		buf = postcard.AppendUvarint(buf, 12) // subject counter
		buf = postcard.AppendUvarint(buf, 4)  // position idx
		if parentNull {
			return append(buf, 1)
		}
		buf = append(buf, 0)
		buf = postcard.AppendUvarint(buf, 2)  // parent peer idx
		return postcard.AppendUvarint(buf, 7) // parent counter
	}

	t.Run("root node", func(t *testing.T) {
		got, err := NewValueReader(build(true), nil).ReadRawTreeMove()
		if err != nil {
			t.Fatalf("ReadRawTreeMove: %v", err)
		}
		want := RawTreeMove{SubjectPeerIdx: 1, SubjectCounter: 12, PositionIdx: 4, ParentNull: true}
		if got != want {
			t.Errorf("got %#v, want %#v", got, want)
		}
	})

	t.Run("child node", func(t *testing.T) {
		got, err := NewValueReader(build(false), nil).ReadRawTreeMove()
		if err != nil {
			t.Fatalf("ReadRawTreeMove: %v", err)
		}
		want := RawTreeMove{SubjectPeerIdx: 1, SubjectCounter: 12, PositionIdx: 4, ParentPeerIdx: 2, ParentCounter: 7}
		if got != want {
			t.Errorf("got %#v, want %#v", got, want)
		}
	})
}

func TestValueReaderRejects(t *testing.T) {
	t.Run("unknown loro value kind", func(t *testing.T) {
		if _, err := NewValueReader([]byte{0x7E}, nil).LoroValue(); err == nil {
			t.Error("want error for unknown kind, got nil")
		}
	})
	t.Run("unhandled op kind", func(t *testing.T) {
		if _, err := NewValueReader(nil, nil).OpContent(ValueKind(0x7F)); err == nil {
			t.Error("want error for unhandled op kind, got nil")
		}
	})
	t.Run("truncated f64", func(t *testing.T) {
		if _, err := NewValueReader([]byte{0x3F, 0xF0}, nil).F64(); err == nil {
			t.Error("want error for truncated f64, got nil")
		}
	})
	t.Run("truncated mark", func(t *testing.T) {
		if _, err := NewValueReader([]byte{0x08}, nil).ReadMark(); err == nil {
			t.Error("want error for truncated mark, got nil")
		}
	})
	t.Run("write unhandled kind", func(t *testing.T) {
		var w ValueWriter
		if err := w.OpContent(VKMarkStart, nil); err == nil {
			t.Error("want error encoding an unhandled kind, got nil")
		}
	})
}

func TestContainerTypeString(t *testing.T) {
	cases := map[ContainerType]string{
		CMap: "Map", CList: "List", CText: "Text",
		CTree: "Tree", CMovableList: "MovableList", CCounter: "Counter",
		ContainerType(200): "Unknown",
	}
	for ct, want := range cases {
		if got := ct.String(); got != want {
			t.Errorf("ContainerType(%d).String() = %q, want %q", ct, got, want)
		}
	}
}

// testArena is a block's key arena: key registers a key and returns its index.
type testArena struct{ keys []string }

func (a *testArena) key(s string) uint64 {
	if i := slices.Index(a.keys, s); i >= 0 {
		return uint64(i)
	}
	a.keys = append(a.keys, s)
	return uint64(len(a.keys) - 1)
}

// TestNestedMapKeysAreArenaIndexes pins the wire form loro writes for a map
// nested in a value: each key is its index in the block's key arena, not the
// key's text. {"k": 1} with "k" at index 1 is map, one entry, key 1, i64 1.
// Reading the key as a string turned it into "\x03" and the value into true,
// so every object stored in a list or a map came back as garbage.
func TestNestedMapKeysAreArenaIndexes(t *testing.T) {
	wire := []byte{byte(lvMap), 0x01, 0x01, byte(lvI64), 0x01}
	got, err := NewValueReader(wire, []string{"x", "k"}).LoroValue()
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]any{"k": int64(1)}; !reflect.DeepEqual(got, want) {
		t.Errorf("read %#v, want %#v", got, want)
	}
	a := testArena{keys: []string{"x", "k"}}
	w := ValueWriter{Key: a.key}
	if err := w.LoroValue(map[string]any{"k": int64(1)}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(w.Bytes(), wire) {
		t.Errorf("wrote % x, want % x", w.Bytes(), wire)
	}
	if _, err := NewValueReader(wire, []string{"x"}).LoroValue(); err == nil {
		t.Error("a key index past the arena decoded without error")
	}
	if err := new(ValueWriter).LoroValue(map[string]any{"k": int64(1)}); err == nil {
		t.Error("a map written without a key arena did not fail")
	}
}

// TestContainerValueIsOneByte pins the wire form of a child container stored
// as a value: the container kind byte, then the kind as one byte, which loro
// reads with read_u8 and writes with write_u8. The child's id is not on the
// wire. A list holding a Text and then the number 5 is list, two items, kind
// 9 kind 2, i64 5. Reading the kind as a LEB128 varint, as this reader once
// did, agrees for the six kinds loro has and takes the next byte along for a
// kind of 0x80 or above.
func TestContainerValueIsOneByte(t *testing.T) {
	for _, c := range []struct {
		wire []byte
		want []any
	}{
		{[]byte{byte(lvList), 0x02, byte(lvContainer), 0x02, byte(lvI64), 0x05}, []any{CText, int64(5)}},
		{[]byte{byte(lvList), 0x02, byte(lvContainer), 0x85, byte(lvI64), 0x05}, []any{ContainerType(0x85), int64(5)}},
	} {
		got, err := NewValueReader(c.wire, nil).LoroValue()
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("read % x as %#v, want %#v", c.wire, got, c.want)
		}
		var w ValueWriter
		if err := w.LoroValue(c.want); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(w.Bytes(), c.wire) {
			t.Errorf("wrote % x, want % x", w.Bytes(), c.wire)
		}
	}
}

// TestKeyOrderReproducesTheStream reads maps whose keys the stream lists out
// of sorted order, as loro's hash map lists them, and writes them back with
// the order the reader recorded: the bytes come back exactly. Without the
// recorded order they come back sorted, and an order that does not name every
// key of its map once is refused.
func TestKeyOrderReproducesTheStream(t *testing.T) {
	keys := []string{"b", "a", "c"}
	// {"b": {"c": 1, "a": 2}, "a": 3}: the outer map lists b before a, the
	// inner one c before a, and the inner map begins before the outer key a.
	wire := []byte{
		byte(lvMap), 0x02,
		0x00, byte(lvMap), 0x02, 0x02, byte(lvI64), 0x01, 0x01, byte(lvI64), 0x02,
		0x01, byte(lvI64), 0x03,
	}
	vr := NewValueReader(wire, keys)
	vr.RecordKeyOrder()
	val, err := vr.LoroValue()
	if err != nil {
		t.Fatal(err)
	}
	orders := vr.KeyOrders()
	if want := [][]string{{"b", "a"}, {"c", "a"}}; !reflect.DeepEqual(orders, want) {
		t.Fatalf("recorded %q, want %q", orders, want)
	}

	a := testArena{keys: slices.Clone(keys)}
	next := 0
	w := ValueWriter{Key: a.key, KeyOrder: func(map[string]any) []string { next++; return orders[next-1] }}
	if err := w.LoroValue(val); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(w.Bytes(), wire) {
		t.Errorf("with the recorded order wrote % x, want % x", w.Bytes(), wire)
	}

	sorted := ValueWriter{Key: a.key}
	if err := sorted.LoroValue(val); err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(sorted.Bytes(), wire) {
		t.Error("sorted keys wrote the stream's own order")
	}

	for _, bad := range [][]string{{"b"}, {"b", "b"}, {"b", "x"}} {
		w := ValueWriter{Key: a.key, KeyOrder: func(map[string]any) []string { return bad }}
		if err := w.LoroValue(map[string]any{"a": nil, "b": nil}); err == nil {
			t.Errorf("key order %q was accepted for keys a, b", bad)
		}
	}
}

// TestKeyOrderMayReuseItsBuffer writes {"a": {"x": 1, "y": 2}, "b": 3} with a
// KeyOrder that hands out one buffer, refilled on every call. The writer
// asks for the inner map's order while it is still walking the outer one, so
// unless it keeps its own copy, the refill turns the outer order [a b] into
// [x y] halfway through and "b": 3 comes out as "y": null.
func TestKeyOrderMayReuseItsBuffer(t *testing.T) {
	val := map[string]any{"a": map[string]any{"x": int64(1), "y": int64(2)}, "b": int64(3)}
	var a testArena
	buf := make([]string, 0, 4)
	w := ValueWriter{Key: a.key, KeyOrder: func(m map[string]any) []string {
		buf = append(buf[:0], slices.Sorted(maps.Keys(m))...)
		return buf
	}}
	if err := w.LoroValue(val); err != nil {
		t.Fatal(err)
	}
	sorted := ValueWriter{Key: a.key}
	if err := sorted.LoroValue(val); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(w.Bytes(), sorted.Bytes()) {
		got, _ := NewValueReader(w.Bytes(), a.keys).LoroValue()
		t.Errorf("wrote %#v, want %#v", got, val)
	}
}
