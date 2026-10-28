package loro

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime/debug"
	"strings"
	"testing"
	"time"

	"github.com/Deln0r/loro-go/encoding/change"
)

// nestedFixtures are the named histories with containers stored as values,
// listed in TestMergeStateMatchesToJSON too.
var nestedFixtures = []string{
	"nested_note", "nested_in_list", "nested_in_movable", "nested_tree_meta",
	"nested_deep", "nested_empty_children", "nested_overwritten", "nested_concurrent_set",
	"mergeable_text", "mergeable_concurrent", "mergeable_nested", "mergeable_empty", "mergeable_marker",
	"nested_text_delta",
}

// TestNestedFromSnapshot reads the same histories from loro's FastSnapshot
// export, whose oplog names nested containers as an update does.
func TestNestedFromSnapshot(t *testing.T) {
	dir := filepath.Join("..", "testdata", "fixtures")
	for _, name := range nestedFixtures {
		blob, err := os.ReadFile(filepath.Join(dir, name+".snapshot.bin"))
		if err != nil {
			t.Fatal(err)
		}
		u, err := DecodeSnapshot(blob)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		state, err := MergeState(u)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		raw, err := os.ReadFile(filepath.Join(dir, name+".json"))
		if err != nil {
			t.Fatal(err)
		}
		var want any
		if err := json.Unmarshal(raw, &want); err != nil {
			t.Fatal(err)
		}
		if got := normalize(state); !reflect.DeepEqual(got, want) {
			t.Errorf("%s snapshot state:\n got  %#v\n want %#v", name, got, want)
		}
	}
}

// TestTextDeltaNested reads the delta of a Text stored under a map key, by its
// id "counter@peer", and of a mergeable Text, by its root name.
func TestTextDeltaNested(t *testing.T) {
	u := decodeFixture(t, "nested_text_delta.update.bin")
	for container, file := range map[string]string{
		"0@1":     "nested_text_delta.nested.delta.json",
		"🤝:$m>mt": "nested_text_delta.mergeable.delta.json",
	} {
		delta, err := TextDelta(u, container)
		if err != nil {
			t.Fatalf("%s: %v", container, err)
		}
		if got, want := deltaJSON(delta), readDelta(t, file); !reflect.DeepEqual(got, want) {
			t.Errorf("%s delta:\n got  %#v\n want %#v", container, got, want)
		}
	}
}

// TestBuildStateRefusesNested holds BuildState to an error on every history
// with a container stored as a value, where it used to show the child as its
// bare kind code, and on a mergeable child, which it would have shown at the
// top level under its path name.
func TestBuildStateRefusesNested(t *testing.T) {
	for _, name := range []string{"nested_note", "nested_in_list", "nested_empty_children", "mergeable_text", "mergeable_empty"} {
		u := decodeFixture(t, name+".update.bin")
		state, err := BuildState(u)
		if err == nil || !strings.Contains(err.Error(), "use MergeState") {
			t.Errorf("%s: BuildState = %v, %v; want an error pointing at MergeState", name, state, err)
		}
	}
}

// TestMergeableMarker pins the marker against the bytes loro-crdt 1.17.1
// wrote: for key "body" of root map "m" (fixture mergeable_text), and for key
// "mt" of the meta map of tree node 7@1 (fixture mergeable_nested). The same
// marker under another key, of another map, or with another kind byte is an
// ordinary binary value, as loro reads it.
func TestMergeableMarker(t *testing.T) {
	root := newMergeableParent(contKey{root: true, name: "m", kind: change.CMap})
	body := []byte{0, 76, 77, 1, 2, 10, 86, 65}
	if k, ok := root.child("body", body); !ok || k != (contKey{root: true, name: "🤝:$m>body", kind: change.CText}) {
		t.Errorf("root map marker resolves to %v, %v", k, ok)
	}
	meta := newMergeableParent(contKey{name: "7@1", kind: change.CMap})
	if k, ok := meta.child("mt", []byte{0, 76, 77, 1, 2, 180, 126, 86}); !ok || k != (contKey{root: true, name: "🤝:@1:7>mt", kind: change.CText}) {
		t.Errorf("tree-node meta marker resolves to %v, %v", k, ok)
	}
	for _, c := range []struct {
		p   *mergeableParent
		key string
		v   []byte
	}{
		{root, "other", body},
		{newMergeableParent(contKey{root: true, name: "n", kind: change.CMap}), "body", body},
		{root, "body", []byte{0, 76, 77, 1, 1, 10, 86, 65}},
		{root, "body", []byte{0, 76, 77, 1, 9, 10, 86, 65}},
		{root, "body", body[:7]},
	} {
		if k, ok := c.p.child(c.key, c.v); ok {
			t.Errorf("marker % x under %q resolves to %v; want a plain value", c.v, c.key, k)
		}
	}
}

// TestMergeableNames holds the top-level filter to loro's parse of a
// mergeable name: a name that merely starts with the prefix is an ordinary
// root and shows at the top level.
func TestMergeableNames(t *testing.T) {
	for name, want := range map[string]bool{
		"🤝:$m>body":               true,
		"🤝:$m>sub>deep\\>\\s\\\\": true,
		"🤝:@1:7>li":               true,
		"🤝:@1:-7>li":              true,
		"🤝:$we\\>i\\\\rd>k\\0":    true,
		"🤝:$m":                    false, // no key
		"🤝:m>body":                false, // no base parent marker
		"🤝:$>body":                false, // empty root name
		"🤝:$a\\sb>k":              false, // a root name cannot hold '/'
		"🤝:$m>a/b":                false, // raw '/'
		"🤝:$m>a\\x":               false, // unknown escape
		"🤝:@01:7>k":               false, // leading zero
		"🤝:@1:-0>k":               false, // negative zero
		"🤝:@1:zzzzzz>k":           false, // counter past i32
		"🤝:@1:7":                  false,
		"m":                       false,
	} {
		if got := isMergeableName(name); got != want {
			t.Errorf("isMergeableName(%q) = %v, want %v", name, got, want)
		}
	}
}

// nestedMapOp is a map write into container c (a root when root is set, else
// "counter@peer") whose value is v, made by op id counter@1.
func nestedMapOp(c string, root bool, key string, counter int64, v any) Op {
	return Op{Container: c, IsRoot: root, Kind: change.CMap, VKind: change.VKLoroValue, MapKey: key, Value: v,
		Peer: 1, Counter: counter, Lamport: counter, Len: 1}
}

// oneOpChanges wraps each op in a change of its own, each depending on the
// one before.
func oneOpChanges(ops []Op) *Updates {
	u := &Updates{}
	for i, op := range ops {
		ch := Change{ID: ID{Peer: op.Peer, Counter: op.Counter}, Lamport: op.Lamport, Ops: []Op{op}}
		if i > 0 {
			ch.Deps = []ID{{Peer: ops[i-1].Peer, Counter: ops[i-1].Counter}}
		}
		u.Changes = append(u.Changes, ch)
	}
	return u
}

// TestNestedCycleIsAnError crafts a map that holds itself: root "m" puts map
// 1@1 under "a", and a second op with the same id, written into 1@1, puts 1@1
// under "self". loro's own import refuses a container that is its own
// ancestor; MergeState returns an error instead of looping.
func TestNestedCycleIsAnError(t *testing.T) {
	u := oneOpChanges([]Op{
		nestedMapOp("m", true, "a", 1, change.CMap),
	})
	u.Changes = append(u.Changes, Change{ID: ID{Peer: 1, Counter: 1}, Lamport: 1,
		Ops: []Op{nestedMapOp("1@1", false, "self", 1, change.CMap)}})
	done := make(chan error, 1)
	go func() {
		_, err := MergeState(u)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "its own ancestor") {
			t.Fatalf("MergeState = %v; want an error naming the cycle", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("MergeState did not return on a container that holds itself")
	}
}

// TestNestedDeepChain nests 200 000 maps, each the only value of the one
// before, under a 16 MiB stack limit. The walk keeps its path on the heap, so
// the depth costs no goroutine stack; a walk that recursed once per level
// would need well over the limit, and overflowing it is a fatal error, not a
// panic a caller could recover from.
func TestNestedDeepChain(t *testing.T) {
	defer debug.SetMaxStack(debug.SetMaxStack(16 << 20))
	const depth = 200_000
	ops := []Op{nestedMapOp("m", true, "c", 0, change.CMap)}
	for i := int64(1); i < depth; i++ {
		ops = append(ops, nestedMapOp(childKey(ID{Peer: 1, Counter: i - 1}, change.CMap).name, false, "c", i, change.CMap))
	}
	state, err := MergeState(oneOpChanges(ops))
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for v := state["m"]; v != nil; n++ {
		m, ok := v.(map[string]any)
		if !ok {
			t.Fatalf("level %d is %T", n, v)
		}
		v = m["c"]
	}
	if n != depth+1 {
		t.Errorf("chain is %d maps deep, want %d", n, depth+1)
	}
}

// TestNestedSharedChildrenBuiltOnce crafts a lattice in which every map of a
// level holds both maps of the next: the same op ids written into two
// containers, which only crafted input does. There are 2^60 paths to the
// bottom, so building a child once per path would never finish; built once
// per container, the 120 maps take no time.
func TestNestedSharedChildrenBuiltOnce(t *testing.T) {
	const levels = 60
	id := func(level, side int) int64 { return int64(1 + 2*level + side) }
	ops := []Op{
		nestedMapOp("m", true, "x", id(0, 0), change.CMap),
		nestedMapOp("m", true, "y", id(0, 1), change.CMap),
	}
	for l := 0; l < levels-1; l++ {
		for side := range 2 {
			c := childKey(ID{Peer: 1, Counter: id(l, side)}, change.CMap).name
			ops = append(ops,
				nestedMapOp(c, false, "x", id(l+1, 0), change.CMap),
				nestedMapOp(c, false, "y", id(l+1, 1), change.CMap))
		}
	}
	u := &Updates{}
	for _, op := range ops {
		u.Changes = append(u.Changes, Change{ID: ID{Peer: 1, Counter: op.Counter}, Lamport: op.Lamport, Ops: []Op{op}})
	}
	done := make(chan error, 1)
	go func() {
		_, err := MergeState(u)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("MergeState rebuilt shared children once per path")
	}
}

// TestContainerInsidePlainValue holds a container kind nested inside a plain
// value, which only a crafted blob holds, to what loro-crdt 1.17.1's toJSON
// printed for the same blob: the container id loro's value reader gives it,
// not a bare kind code. In a map value that is a list, element i is read at
// the op's id plus i, and everything inside an element at the element's id;
// a list insert's items are such elements already.
func TestContainerInsidePlainValue(t *testing.T) {
	d := NewDoc(1)
	d.MapSet("m", "v", []any{int64(1), []any{change.CText}, map[string]any{"a": change.CMap}})
	d.ListInsert("l", 0, []any{"x", []any{change.CCounter}})
	u, err := DecodeUpdates(d.ExportUpdates())
	if err != nil {
		t.Fatal(err)
	}
	state, err := MergeState(u)
	if err != nil {
		t.Fatal(err)
	}
	// {"l":["x",["cid:2@1:Counter"]],"m":{"v":[1,["cid:1@1:Text"],{"a":"cid:2@1:Map"}]}}
	want := map[string]any{
		"m": map[string]any{"v": []any{int64(1), []any{"cid:1@1:Text"}, map[string]any{"a": "cid:2@1:Map"}}},
		"l": []any{"x", []any{"cid:2@1:Counter"}},
	}
	if !reflect.DeepEqual(state, want) {
		t.Errorf("state = %#v, want %#v", state, want)
	}
}

// TestUnreachableNestedNotShown: a nested container whose ops arrived but
// which no root reaches shows nowhere, and a mergeable container whose parent
// never wrote its marker is not shown at the top level.
func TestUnreachableNestedNotShown(t *testing.T) {
	u := oneOpChanges([]Op{
		nestedMapOp("m", true, "k", 0, int64(1)),
		nestedMapOp("0@1", false, "orphan", 1, int64(2)),
		nestedMapOp("🤝:$m>sub", true, "x", 2, int64(3)),
	})
	state, err := MergeState(u)
	if err != nil {
		t.Fatal(err)
	}
	if want := (map[string]any{"m": map[string]any{"k": int64(1)}}); !reflect.DeepEqual(state, want) {
		t.Errorf("state = %#v, want %#v", state, want)
	}
	if b, _ := json.Marshal(state); bytes.Contains(b, []byte("orphan")) {
		t.Errorf("unreachable container shown: %s", b)
	}
}

// TestNestedUnreachableCycleIsIgnored: a Map that holds itself but that no
// root reaches cannot change the state, so MergeState shows the rest of the
// document. loro-crdt refuses such a document at import; loro-go only checks
// the cycles a root reaches.
func TestNestedUnreachableCycleIsIgnored(t *testing.T) {
	u := &Updates{Changes: []Change{{
		ID: ID{Peer: 1, Counter: 0},
		Ops: []Op{
			{Container: "m", IsRoot: true, Kind: change.CMap, VKind: change.VKLoroValue, MapKey: "x", Value: int64(1), Peer: 1, Counter: 0, Len: 1},
			{Container: "1@1", Kind: change.CMap, VKind: change.VKLoroValue, MapKey: "self", Value: change.CMap, Peer: 1, Counter: 1, Lamport: 1, Len: 1},
		},
	}}}
	state, err := MergeState(u)
	if err != nil {
		t.Fatalf("MergeState: %v", err)
	}
	if got := normalize(state); !reflect.DeepEqual(got, map[string]any{"m": map[string]any{"x": 1.0}}) {
		t.Errorf("state = %#v", got)
	}
}
