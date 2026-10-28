package loro

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"

	"github.com/Deln0r/loro-go/encoding/change"
	"github.com/Deln0r/loro-go/encoding/fast"
)

// TestDocumentConcurrentUse edits one Document from several goroutines, each
// through its own handles, while others read, commit, export and import. The
// document-wide mutex serialises them; run with -race to check that nothing
// is touched outside it.
func TestDocumentConcurrentUse(t *testing.T) {
	d := NewDocument(1)
	other := NewDocument(2)
	if err := other.Text("t").Insert(0, "remote"); err != nil {
		t.Fatal(err)
	}
	remote, err := other.ExportUpdates()
	if err != nil {
		t.Fatal(err)
	}
	const rounds = 50
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	run := func(f func(i int) error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				if err := f(i); err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	run(func(i int) error { return d.Text("t").Insert(0, "a") })
	run(func(i int) error { return d.Map("m").Set(fmt.Sprint(i%5), int64(i)) })
	run(func(i int) error { return d.List("l").Insert(0, int64(i)) })
	run(func(i int) error { return d.Counter("n").Increment(1) })
	run(func(i int) error {
		if i%10 == 0 {
			return d.Import(remote)
		}
		d.Commit()
		return nil
	})
	run(func(i int) error {
		if _, err := d.JSON(); err != nil {
			return err
		}
		_, err := d.ExportUpdates()
		return err
	})
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	state, err := d.JSON()
	if err != nil {
		t.Fatal(err)
	}
	if got := d.Text("t").Len(); got != rounds+len("remote") {
		t.Errorf("text has %d characters, want %d", got, rounds+len("remote"))
	}
	if got := d.List("l").Len(); got != rounds {
		t.Errorf("list has %d items, want %d", got, rounds)
	}
	if got := d.Counter("n").Value(); got != rounds {
		t.Errorf("counter is %v, want %d", got, rounds)
	}
	// Another document that imports the export shows the same state.
	blob, err := d.ExportUpdates()
	if err != nil {
		t.Fatal(err)
	}
	e := NewDocument(3)
	if err := e.Import(blob); err != nil {
		t.Fatal(err)
	}
	again, err := e.JSON()
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(state)
	b, _ := json.Marshal(again)
	if string(a) != string(b) {
		t.Errorf("an importer shows\n %s\nwant\n %s", b, a)
	}
}

// TestDocumentErrors checks that misuse is an error, never a panic, and
// changes nothing.
func TestDocumentErrors(t *testing.T) {
	d := NewDocument(1)
	text := d.Text("t")
	if err := text.Insert(0, "a😀b"); err != nil {
		t.Fatal(err)
	}
	list := d.List("l")
	if err := list.Insert(0, int64(1), "x"); err != nil {
		t.Fatal(err)
	}
	m := d.Map("m")
	before, _ := d.JSON()
	for name, err := range map[string]error{
		"insert past the end":       text.Insert(4, "x"),
		"insert before the start":   text.Insert(-1, "x"),
		"delete past the end":       text.Delete(2, 2),
		"delete a negative count":   text.Delete(1, -1),
		"insert invalid UTF-8":      text.Insert(0, "\xff"),
		"list insert past the end":  list.Insert(3, int64(1)),
		"list delete past the end":  list.Delete(1, 2),
		"store a channel":           m.Set("k", make(chan int)),
		"store a uint64 past int64": m.Set("k", uint64(math.MaxUint64)),
		"store a struct in a list":  list.Insert(0, struct{}{}),
		"import garbage":            d.Import([]byte("not a loro blob")),
	} {
		if err == nil {
			t.Errorf("%s: no error", name)
		} else if !strings.HasPrefix(err.Error(), "loro") {
			t.Errorf("%s: error %q does not start with loro", name, err)
		}
	}
	if _, err := list.Get(2); err == nil {
		t.Error("list get past the end: no error")
	}
	after, _ := d.JSON()
	a, _ := json.Marshal(before)
	b, _ := json.Marshal(after)
	if string(a) != string(b) {
		t.Errorf("failed calls changed the document:\n %s\n %s", a, b)
	}

	reserved := NewDocument(math.MaxUint64)
	if err := reserved.Text("t").Insert(0, "x"); err == nil {
		t.Error("a document with peer id 2^64-1 edited")
	}
}

// TestDocumentReads reads back what handles wrote, and what JSON shows.
func TestDocumentReads(t *testing.T) {
	d := NewDocument(7)
	text, m, list, n := d.Text("t"), d.Map("m"), d.List("l"), d.Counter("n")
	steps := []error{
		text.Insert(0, "héllo"),
		text.Insert(5, " 🦀"),
		text.Delete(0, 1),
		m.Set("s", "v"),
		m.Set("b", []byte{1, 2}),
		m.Set("x", map[string]any{"k": []any{1, int8(2), 2.5}}),
		m.Delete("s"),
		list.Insert(0, "a", "b", "c"),
		list.Delete(1, 1),
		n.Increment(3),
		n.Increment(-0.5),
	}
	for i, err := range steps {
		if err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
	}
	if got := text.String(); got != "éllo 🦀" {
		t.Errorf("text %q", got)
	}
	if got := text.Len(); got != 6 {
		t.Errorf("text length %d", got)
	}
	if _, ok := m.Get("s"); ok {
		t.Error("deleted key still set")
	}
	if v, ok := m.Get("x"); !ok || fmt.Sprint(v) != "map[k:[1 2 2.5]]" {
		t.Errorf("map value %v", v)
	}
	if v, err := list.Get(1); err != nil || v != "c" {
		t.Errorf("list item %v, %v", v, err)
	}
	if got := n.Value(); got != 2.5 {
		t.Errorf("counter %v", got)
	}
	state, err := d.JSON()
	if err != nil {
		t.Fatal(err)
	}
	got, _ := json.Marshal(state)
	want := `{"l":["a","c"],"m":{"b":"AQI=","x":{"k":[1,2,2.5]}},"n":2.5,"t":"éllo 🦀"}`
	if string(got) != want {
		t.Errorf("JSON\n got  %s\n want %s", got, want)
	}
	// What JSON returns is a copy.
	state["m"].(map[string]any)["x"].(map[string]any)["k"].([]any)[0] = "changed"
	if v, _ := m.Get("x"); fmt.Sprint(v) != "map[k:[1 2 2.5]]" {
		t.Errorf("changing JSON's result changed the document: %v", v)
	}
}

// TestDocumentImportIsAllOrNothing imports a blob one of whose changes
// EncodeUpdates would refuse: nothing of it is applied.
func TestDocumentImportIsAllOrNothing(t *testing.T) {
	src := NewDocument(1)
	if err := src.Text("t").Insert(0, "ok"); err != nil {
		t.Fatal(err)
	}
	src.Commit()
	if err := src.Map("m").Set("k", "v"); err != nil {
		t.Fatal(err)
	}
	blob, err := src.ExportUpdates()
	if err != nil {
		t.Fatal(err)
	}
	u, err := DecodeUpdates(blob)
	if err != nil {
		t.Fatal(err)
	}
	// A map write claiming two atoms: loro gives a map op one.
	last := &u.Changes[len(u.Changes)-1]
	op := &last.Ops[len(last.Ops)-1]
	if op.Kind != change.CMap {
		t.Fatalf("last op is a %v", op.Kind)
	}
	op.Len = 2
	bad, err := encodeUnchecked(u)
	if err != nil {
		t.Fatal(err)
	}
	d := NewDocument(2)
	if err := d.Import(bad); err == nil {
		t.Fatal("imported a change with a two-atom map write")
	}
	if state, _ := d.JSON(); len(state) != 0 {
		t.Errorf("a refused import left %v", state)
	}
}

// encodeUnchecked writes u's changes as one block without EncodeUpdates'
// checks, to make a blob loro would not write.
func encodeUnchecked(u *Updates) ([]byte, error) {
	raw, err := encodeBlock(u.Changes, nil)
	if err != nil {
		return nil, err
	}
	return fast.Encode(fast.ModeFastUpdates, change.EncodeFastUpdates([][]byte{raw})), nil
}

// TestDocumentScriptValues pins the harness's conversion of script values,
// which follows loro-crdt's binding.
func TestDocumentScriptValues(t *testing.T) {
	for raw, want := range map[string]any{
		`3`:                     int64(3),
		`-0.5`:                  -0.5,
		`9007199254740992`:      int64(1 << 53),
		`9007199254740994`:      float64(1<<53 + 2),
		`1e21`:                  1e21,
		`{"$bin":"AQI="}`:       []byte{1, 2},
		`[1,{"x":2.25}]`:        []any{int64(1), map[string]any{"x": 2.25}},
		`{"$bin":"AQI=","y":1}`: map[string]any{"$bin": "AQI=", "y": int64(1)},
	} {
		got, err := scriptValue(json.RawMessage(raw))
		if err != nil {
			t.Fatal(err)
		}
		if fmt.Sprintf("%#v", got) != fmt.Sprintf("%#v", want) {
			t.Errorf("%s: got %#v, want %#v", raw, got, want)
		}
	}
}

// TestTextDeltaIsACopy changes a style value Delta handed out, and the
// document does not change: neither a later Delta nor the export.
func TestTextDeltaIsACopy(t *testing.T) {
	var blob []byte
	for _, c := range loadCorpus(t, "rich_corpus.json") {
		if c.Seed == 6 {
			blob, _ = base64.StdEncoding.DecodeString(c.Update)
		}
	}
	d := NewDocument(9)
	if err := d.Import(blob); err != nil {
		t.Fatal(err)
	}
	before, err := d.ExportUpdates()
	if err != nil {
		t.Fatal(err)
	}
	text := d.Text("t")
	delta, err := text.Delta()
	if err != nil {
		t.Fatal(err)
	}
	first, _ := json.Marshal(delta)
	changed := false
	for _, run := range delta {
		for _, v := range run.Attributes {
			if m, ok := v.(map[string]any); ok {
				m["k"] = "changed"
				changed = true
			}
		}
	}
	if !changed {
		t.Fatal("rich_corpus seed 6 has no object style value")
	}
	again, _ := text.Delta()
	second, _ := json.Marshal(again)
	if string(first) != string(second) {
		t.Errorf("changing Delta's result changed the document:\n %s\n %s", first, second)
	}
	after, err := d.ExportUpdates()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Error("changing Delta's result changed the export")
	}
}

// TestDeleteRangeDoesNotOverflow deletes ranges whose end overflows an int:
// loro checks pos <= len and n <= len-pos (handler.rs:92), so each is an
// error that changes nothing and takes no counter.
func TestDeleteRangeDoesNotOverflow(t *testing.T) {
	d := NewDocument(1)
	list, text := d.List("l"), d.Text("t")
	if err := list.Insert(0, "a", "b"); err != nil {
		t.Fatal(err)
	}
	if err := text.Insert(0, "ab"); err != nil {
		t.Fatal(err)
	}
	before, err := d.ExportUpdates()
	if err != nil {
		t.Fatal(err)
	}
	empty := d.List("empty")
	for name, f := range map[string]func() error{
		"list from 1, MaxInt items": func() error { return list.Delete(1, math.MaxInt) },
		"list from MaxInt, 1 item":  func() error { return list.Delete(math.MaxInt, 1) },
		"empty list from MaxInt":    func() error { return empty.Delete(math.MaxInt, 1) },
		"empty list from 0, MaxInt": func() error { return empty.Delete(0, math.MaxInt) },
		"text from 1, MaxInt chars": func() error { return text.Delete(1, math.MaxInt) },
		"text from MaxInt, 1 char":  func() error { return text.Delete(math.MaxInt, 1) },
		"text from MaxInt, MaxInt":  func() error { return text.Delete(math.MaxInt, math.MaxInt) },
		"list from -1, 1 item":      func() error { return list.Delete(-1, 1) },
	} {
		var err error
		func() {
			defer func() {
				if p := recover(); p != nil {
					err = fmt.Errorf("panic: %v", p)
				}
			}()
			err = f()
		}()
		if err == nil || !strings.HasPrefix(err.Error(), "loro: ") {
			t.Errorf("%s: %v", name, err)
		}
	}
	after, err := d.ExportUpdates()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Error("a refused delete changed the history")
	}
	if got := fmt.Sprintf("%d %s", list.Len(), text.String()); got != "2 ab" {
		t.Errorf("state %s", got)
	}
	// The next op takes the next counter, 4: none went to a refused delete.
	if err := list.Insert(0, "c"); err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	next := d.tx.nextCounter
	d.mu.Unlock()
	if next != 5 {
		t.Errorf("next counter %d, want 5", next)
	}
}

// TestReadsDoNotSpendTheEditBudget reads a text many times through Delta,
// each read walking all of it, and then edits it. Each operation has the
// whole step budget: reads do not use up what the edit needs, as nothing
// limits loro's edit after reads.
func TestReadsDoNotSpendTheEditBudget(t *testing.T) {
	d := NewDocument(1)
	text := d.Text("t")
	if err := text.Insert(0, strings.Repeat("x", 300)); err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	d.st.work.limit = 2000
	d.mu.Unlock()
	for i := 0; i < 20; i++ {
		if _, err := text.Delta(); err != nil {
			t.Fatalf("delta %d: %v", i, err)
		}
	}
	if err := text.Delete(0, 10); err != nil {
		t.Fatalf("delete after reads: %v", err)
	}
	if text.Len() != 290 {
		t.Errorf("length %d", text.Len())
	}
}
