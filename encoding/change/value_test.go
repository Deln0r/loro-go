package change

import (
	"bytes"
	"math"
	"reflect"
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
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var w ValueWriter
			if err := w.OpContent(c.vk, c.val); err != nil {
				t.Fatalf("write: %v", err)
			}
			got, err := NewValueReader(w.Bytes()).OpContent(c.vk)
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
	got, err := NewValueReader(w2.Bytes()).F64()
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
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var w ValueWriter
			if err := w.LoroValue(c.val); err != nil {
				t.Fatalf("write: %v", err)
			}
			got, err := NewValueReader(w.Bytes()).LoroValue()
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

	got, err := NewValueReader(buf).ReadMark()
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

	got, err := NewValueReader(buf).ReadListMove()
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
		got, err := NewValueReader(build(true)).ReadRawTreeMove()
		if err != nil {
			t.Fatalf("ReadRawTreeMove: %v", err)
		}
		want := RawTreeMove{SubjectPeerIdx: 1, SubjectCounter: 12, PositionIdx: 4, ParentNull: true}
		if got != want {
			t.Errorf("got %#v, want %#v", got, want)
		}
	})

	t.Run("child node", func(t *testing.T) {
		got, err := NewValueReader(build(false)).ReadRawTreeMove()
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
		if _, err := NewValueReader([]byte{0x7E}).LoroValue(); err == nil {
			t.Error("want error for unknown kind, got nil")
		}
	})
	t.Run("unhandled op kind", func(t *testing.T) {
		if _, err := NewValueReader(nil).OpContent(ValueKind(0x7F)); err == nil {
			t.Error("want error for unhandled op kind, got nil")
		}
	})
	t.Run("truncated f64", func(t *testing.T) {
		if _, err := NewValueReader([]byte{0x3F, 0xF0}).F64(); err == nil {
			t.Error("want error for truncated f64, got nil")
		}
	})
	t.Run("truncated mark", func(t *testing.T) {
		if _, err := NewValueReader([]byte{0x08}).ReadMark(); err == nil {
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
