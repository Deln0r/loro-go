package loro

import (
	"bytes"
	"encoding/hex"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/Deln0r/loro-go/encoding/change"
)

// TestScriptStructCorpus runs the 300 scripts of script_struct_corpus.json:
// MovableList and Tree edits, containers stored in containers at any depth,
// mergeable children and edits through deleted handles, by one to three
// peers with partial and out-of-order syncs, and holds every Document to
// loro-crdt at every checkpoint, its version and frontiers included.
func TestScriptStructCorpus(t *testing.T) {
	scripts := loadScripts(t, "script_struct_corpus.json")
	r := runScripts(t, scripts, 0)
	reportScripts(t, "script_struct_corpus.json", len(scripts), r)
	if r.versionChecks != r.checks {
		t.Errorf("%d of %d checks recorded a version", r.versionChecks, r.checks)
	}
}

// TestScriptStructCrowdCorpus is TestScriptStructCorpus for the 60 scripts
// of script_struct_crowd_corpus.json, by 4 to 12 peers.
func TestScriptStructCrowdCorpus(t *testing.T) {
	scripts := loadScripts(t, "script_struct_crowd_corpus.json")
	r := runScripts(t, scripts, 0)
	reportScripts(t, "script_struct_crowd_corpus.json", len(scripts), r)
	if r.versionChecks != r.checks {
		t.Errorf("%d of %d checks recorded a version", r.versionChecks, r.checks)
	}
}

// TestStructNamedScripts runs the scripts of script_struct_named.json, one
// or more per structure rule, and requires each to match loro-crdt at every
// check, bytes included.
func TestStructNamedScripts(t *testing.T) {
	for _, s := range loadScripts(t, "script_struct_named.json") {
		t.Run(s.Name, func(t *testing.T) {
			r := &checkResult{}
			runScript(&s, 0, r)
			for _, f := range r.failures {
				t.Error(f)
			}
			if r.checks == 0 || r.json != r.checks || r.atoms != r.checks || r.shape != r.checks || r.bytes != r.checks ||
				r.versionChecks != r.checks || r.versions != r.checks {
				t.Errorf("%d checks: JSON %d, atoms %d, shape %d, bytes %d, version and frontiers %d of %d recorded",
					r.checks, r.json, r.atoms, r.shape, r.bytes, r.versions, r.versionChecks)
			}
		})
	}
}

// TestStructNamedScriptsNeedTheirRule is the positive control of
// TestStructNamedScripts: with one structure rule switched off, the script
// written for it no longer matches loro-crdt.
func TestStructNamedScriptsNeedTheirRule(t *testing.T) {
	scripts := map[string]*script{}
	all := loadScripts(t, "script_struct_named.json")
	for i := range all {
		scripts[all[i].Name] = &all[i]
	}
	for _, c := range []struct {
		rule   rule
		script string
	}{
		{ruleMovableOpIndex, "movable_op_index_after_moves"},
		{ruleMovableOpIndex, "movable_concurrent_move_set_delete"},
		{ruleMovableSetSkipEqual, "movable_set_equal_value"},
		{ruleMovableMoveInPlace, "movable_move_in_place"},
		{ruleTreeRearrange, "tree_rearrange_equal_indexes"},
		{ruleTreeMoveInPlace, "tree_move_in_place"},
		{ruleTreeCycle, "tree_cycle_refused"},
		{ruleDeletedRefused, "deleted_container_handles"},
		{ruleDeletedRefused, "deleted_container_no_op_edits"},
		{ruleDeletedRefused, "mergeable_reactivation"},
		{ruleMergeableID, "mergeable_reactivation"},
		{ruleMergeableID, "mergeable_nested_paths"},
		{ruleCounterAttach, "nested_children"},
		{ruleKnownContainers, "get_container_by_id"},
	} {
		s := scripts[c.script]
		if s == nil {
			t.Fatalf("no script %s", c.script)
		}
		r := &checkResult{}
		runScript(s, 1<<c.rule, r)
		if len(r.failures) == 0 && r.checks > 0 && r.json == r.checks && r.atoms == r.checks && r.shape == r.checks && r.bytes == r.checks {
			t.Errorf("rule %d off: %s still matches loro-crdt", c.rule, c.script)
			continue
		}
		first := ""
		if len(r.failures) > 0 {
			first, _, _ = strings.Cut(r.failures[0], "\n")
		}
		t.Logf("rule %d off: %s: %d checks, JSON %d, atoms %d, bytes %d; %s",
			c.rule, c.script, r.checks, r.json, r.atoms, r.bytes, first)
	}
}

// TestFractionalIndex checks the fractional index port against loro: the
// crate's own case (crates/fractional_index/src/lib.rs:216) and the
// indexes loro-crdt 1.17.1 gave tree nodes, measured: a first child 80,
// one after it 8180, one before it 7F80, one between 80 and 8180 817F80.
// Malformed indexes, which an imported change can carry, give none.
func TestFractionalIndex(t *testing.T) {
	h := func(s string) []byte {
		b, err := hex.DecodeString(s)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	got := func(b []byte, ok bool) string {
		if !ok {
			return "none"
		}
		return strings.ToUpper(hex.EncodeToString(b))
	}
	for _, c := range []struct {
		name string
		got  string
		want string
	}{
		{"crate case", got(fiNew(h("0080"), h("008180"))), "00817F80"},
		{"first", got(fiNew(nil, nil)), "80"},
		{"after", got(fiNew(h("80"), nil)), "8180"},
		{"before", got(fiNew(nil, h("80"))), "7F80"},
		{"between", got(fiNew(h("80"), h("8180"))), "817F80"},
		{"equal", got(fiNew(h("80"), h("80"))), "none"},
		{"reversed", got(fiNew(h("8180"), h("80"))), "none"},
		// Indexes the crate never makes, which it panics on.
		{"empty lower", got(fiNew([]byte{}, h("80"))), "none"},
		{"empty upper", got(fiNew(h("80"), []byte{})), "none"},
		{"after empty", got(fiNew([]byte{}, nil)), "none"},
		{"before empty", got(fiNew(nil, []byte{})), "none"},
		{"before zeros", got(fiNew(nil, h("0000"))), "none"},
		{"after FFs", got(fiNew(h("FFFF"), nil)), "none"},
		{"between, no room after", got(fiNew(h("01FF"), h("0280"))), "none"},
	} {
		if c.got != c.want {
			t.Errorf("%s: %s, want %s", c.name, c.got, c.want)
		}
	}
	if _, ok := fiEvenly(h("8180"), h("80"), 2); ok {
		t.Error("fiEvenly gave indexes between a greater lower and a smaller upper")
	}
	for _, c := range []struct {
		lower, upper []byte
		n            int
	}{{nil, nil, 7}, {h("80"), nil, 3}, {h("80"), h("8180"), 5}, {nil, h("7F80"), 4}} {
		fis, ok := fiEvenly(c.lower, c.upper, c.n)
		if !ok || len(fis) != c.n {
			t.Fatalf("fiEvenly(%X, %X, %d): %d indexes, ok %v", c.lower, c.upper, c.n, len(fis), ok)
		}
		prev := c.lower
		for _, fi := range fis {
			if prev != nil && bytes.Compare(prev, fi) >= 0 || c.upper != nil && bytes.Compare(fi, c.upper) >= 0 {
				t.Errorf("fiEvenly(%X, %X, %d) = %X: not in order between the bounds", c.lower, c.upper, c.n, fis)
				break
			}
			prev = fi
		}
	}
}

// TestStructureErrors checks that misuse of MovableList, Tree and child
// containers is an error that records nothing.
func TestStructureErrors(t *testing.T) {
	d := NewDocument(1)
	ml := d.MovableList("ml")
	tr := d.Tree("tr")
	m := d.Map("m")
	if err := ml.Insert(0, 1, 2); err != nil {
		t.Fatal(err)
	}
	a, err := tr.Create(TreeRoot)
	if err != nil {
		t.Fatal(err)
	}
	b, err := tr.Create(a)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Set("v", 1); err != nil {
		t.Fatal(err)
	}
	before, err := d.ExportUpdates()
	if err != nil {
		t.Fatal(err)
	}
	ghost := TreeID{Peer: 9, Counter: 9}
	errOf := func(_ any, err error) error { return err }
	for name, err := range map[string]error{
		"insert past the end":         ml.Insert(3, 0),
		"insert negative":             ml.Insert(-1, 0),
		"insert unstorable":           ml.Insert(0, struct{}{}),
		"delete past the end":         ml.Delete(1, 2),
		"move from past the end":      ml.Move(2, 0),
		"move to past the end":        ml.Move(0, 2),
		"set past the end":            ml.Set(2, 0),
		"get past the end":            errOf(ml.Get(2)),
		"insert container of no kind": errOf(ml.InsertContainer(0, ContainerKind(9))),
		"set container of no kind":    errOf(ml.SetContainer(0, ContainerKind(9))),
		"map container of no kind":    errOf(m.SetContainer("k", ContainerKind(9))),
		"list container of no kind":   errOf(d.List("l").InsertContainer(0, ContainerKind(9))),
		"mergeable of no kind":        errOf(m.EnsureMergeable("k", ContainerKind(9))),
		"mergeable over a value":      errOf(m.EnsureMergeable("v", KindText)),
		"container kind as a value":   m.Set("x", KindText),
		"container kind as an item":   ml.Insert(0, KindMap),
		"create at past the end":      errOf(tr.CreateAt(TreeRoot, 2)),
		"create under no node":        errOf(tr.Create(ghost)),
		"move no node":                tr.Move(ghost, TreeRoot, 0),
		"move under no node":          tr.Move(b, ghost, 0),
		"move under itself":           tr.Move(a, a, 0),
		"move under its descendant":   tr.Move(a, b, 0),
		"move past the end":           tr.Move(b, TreeRoot, 2),
		"delete no node":              tr.Delete(ghost),
		"children of no node":         errOf(tr.Children(ghost)),
		"parent of no node":           errOf(tr.Parent(ghost)),
		"meta of no node":             errOf(tr.Meta(ghost)),
		"container id without cid:":   errOf(d.GetContainer("root-m:Map")),
		"container id of no kind":     errOf(d.GetContainer("cid:root-m:Mop")),
		"container id of no op":       errOf(d.GetContainer("cid:5@1:Text")),
		"container id not an op id":   errOf(d.GetContainer("cid:x@1:Text")),
	} {
		if err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	after, err := d.ExportUpdates()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Error("a refused edit was recorded")
	}
}

// TestDeletedContainer follows one child through deletion: its handle
// reports it deleted, reads its old contents, refuses edits, and an edit
// that makes no op is still no error, as in loro.
func TestDeletedContainer(t *testing.T) {
	d := NewDocument(1)
	m := d.Map("m")
	c, err := m.SetContainer("t", KindText)
	if err != nil {
		t.Fatal(err)
	}
	text := c.(*Text)
	if err := text.Insert(0, "hi"); err != nil {
		t.Fatal(err)
	}
	inner, err := m.SetContainer("m", KindMap)
	if err != nil {
		t.Fatal(err)
	}
	im := inner.(*Map)
	if err := im.Set("k", 1); err != nil {
		t.Fatal(err)
	}
	if text.IsDeleted() || im.IsDeleted() {
		t.Fatal("a live child reported deleted")
	}
	if err := m.Set("t", 0); err != nil {
		t.Fatal(err)
	}
	if err := m.Delete("m"); err != nil {
		t.Fatal(err)
	}
	if !text.IsDeleted() || !im.IsDeleted() {
		t.Fatal("an overwritten child reported live")
	}
	if v, err := text.JSON(); err != nil || v != "hi" {
		t.Errorf("deleted text reads %v, %v; want hi", v, err)
	}
	if err := text.Insert(0, "x"); err == nil || !strings.Contains(err.Error(), "deleted") {
		t.Errorf("insert into deleted text: %v", err)
	}
	if err := im.Set("k", 1); err != nil {
		t.Errorf("writing the value already there into a deleted map: %v", err)
	}
	if err := im.Set("k", 2); err == nil {
		t.Error("writing a new value into a deleted map: no error")
	}
	c2, err := m.EnsureMergeable("t2", KindText)
	if err != nil || c2.IsDeleted() {
		t.Fatalf("ensure mergeable: %v", err)
	}
	if err := c2.(*Text).Insert(0, "x"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.EnsureMergeable("e", KindMap); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"t2", "e"} {
		if err := m.Delete(k); err != nil {
			t.Fatal(err)
		}
	}
	// A mergeable child with no state of its own is gone with its marker,
	// as loro-crdt's getContainerById shows (measured).
	if _, err := d.GetContainer(`cid:root-🤝:$m>e:Map`); err == nil {
		t.Error("an empty mergeable child whose key is deleted is still known")
	}
	g, err := d.GetContainer(`cid:root-🤝:$m>t2:Text`)
	if err != nil || !g.IsDeleted() {
		t.Fatalf("mergeable child after its key is deleted: %v, deleted %v", err, g != nil && g.IsDeleted())
	}
	if c, err := m.EnsureMergeable("t2", KindText); err != nil || c.ID() != g.ID() || c.IsDeleted() {
		t.Errorf("ensure again: %v; want %s alive", err, g.ID())
	}
}

// TestTreeAccessors reads a tree's shape back through Children and Parent.
func TestTreeAccessors(t *testing.T) {
	d := NewDocument(1)
	tr := d.Tree("tr")
	a, _ := tr.Create(TreeRoot)
	b, _ := tr.Create(TreeRoot)
	c, _ := tr.CreateAt(a, 0)
	e, _ := tr.CreateAt(TreeRoot, 1)
	if got, _ := tr.Children(TreeRoot); !slices.Equal(got, []TreeID{a, e, b}) {
		t.Errorf("top level %v, want %v", got, []TreeID{a, e, b})
	}
	if p, err := tr.Parent(c); err != nil || p != a {
		t.Errorf("parent of %v: %v, %v; want %v", c, p, err, a)
	}
	if p, err := tr.Parent(a); err != nil || p != TreeRoot {
		t.Errorf("parent of %v: %v, %v; want the top level", a, p, err)
	}
	if err := tr.Move(c, b, 0); err != nil {
		t.Fatal(err)
	}
	if err := tr.Delete(a); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Parent(a); err == nil {
		t.Error("parent of a deleted node: no error")
	}
	if got, _ := tr.Children(b); !slices.Equal(got, []TreeID{c}) {
		t.Errorf("children of %v: %v, want %v", b, got, []TreeID{c})
	}
	meta, err := tr.Meta(a)
	if err != nil {
		t.Fatal(err)
	}
	if !meta.IsDeleted() || meta.ID() != "cid:0@1:Map" {
		t.Errorf("meta of a deleted node: %s, deleted %v", meta.ID(), meta.IsDeleted())
	}
	if TreeRoot.String() != "root" || a.String() != "0@1" {
		t.Errorf("ids render as %s and %s", TreeRoot, a)
	}
}

// TestAtomsCompareWholeMapValues: the harness splits a text insert into
// characters and a list insert into items, one atom each, but a map value
// is one atom whatever it holds. Splitting it compared a string by its
// first rune, a list by its first item, and panicked on "" and [].
func TestAtomsCompareWholeMapValues(t *testing.T) {
	write := func(v any) Change {
		return Change{ID: ID{Peer: 1}, Ops: []Op{{Container: "m", IsRoot: true, Kind: KindMap, VKind: change.VKLoroValue,
			MapKey: "k", Value: v, Peer: 1, Len: 1}}}
	}
	atomOf := func(v any) atom {
		a, err := atomsOf([]Change{write(v)})
		if err != nil || len(a) != 1 {
			t.Fatalf("%v: %d atoms, %v", v, len(a), err)
		}
		return a[0]
	}
	for _, pair := range [][2]any{{"abc", "abd"}, {[]any{int64(1), int64(2)}, []any{int64(1), int64(3)}}, {"", "x"}, {[]any{}, []any{int64(1)}}} {
		if reflect.DeepEqual(atomOf(pair[0]), atomOf(pair[1])) {
			t.Errorf("map values %v and %v give the same atom", pair[0], pair[1])
		}
	}
}

// TestGetContainerConcurrentImport: GetContainer reads the document's
// state only under its mutex, while an import replaces that state. Run
// with -race.
func TestGetContainerConcurrentImport(t *testing.T) {
	src := NewDocument(1)
	d := NewDocument(2)
	var wg sync.WaitGroup
	wg.Add(2)
	errs := make(chan error, 2)
	go func() {
		defer wg.Done()
		for i := range 200 {
			if err := src.Map("m").Set("a", int64(i)); err != nil {
				errs <- err
				return
			}
			blob, err := src.ExportUpdates()
			if err == nil {
				err = d.Import(blob)
			}
			if err != nil {
				errs <- err
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for range 200 {
			c, err := d.GetContainer("cid:root-m:Map")
			if err != nil {
				errs <- err
				return
			}
			if _, err := c.JSON(); err != nil {
				errs <- err
				return
			}
		}
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}
