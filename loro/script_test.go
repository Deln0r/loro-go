package loro

import (
	"bytes"
	"cmp"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/Deln0r/loro-go/encoding/change"
)

// script is one edit script from testdata/gen/gen_scripts.mjs: the peers by
// id, the actions loro-crdt ran, and every blob a peer exported.
type script struct {
	Seed    int               `json:"seed"`
	Name    string            `json:"name"`
	Peers   []uint64          `json:"peers"`
	Actions []json.RawMessage `json:"actions"`
	Blobs   []string          `json:"blobs"`
}

func (s *script) label() string {
	if s.Name != "" {
		return s.Name
	}
	return fmt.Sprintf("seed %d", s.Seed)
}

type scriptAction struct {
	A    string          `json:"a"`
	P    int             `json:"p"`
	C    string          `json:"c"`
	Pos  int             `json:"pos"`
	N    int             `json:"n"`
	S    string          `json:"s"`
	K    string          `json:"k"`
	V    json.RawMessage `json:"v"`
	Kind string          `json:"kind"`
	Msg  *string         `json:"msg"`
	TS   *int64          `json:"ts"`
	Blob int             `json:"blob"`
	// Refused is loro-crdt's error when it refused the import.
	Refused string `json:"refused"`
	Peers   []struct {
		JSON   json.RawMessage `json:"json"`
		Export int             `json:"export"`
	} `json:"peers"`
}

func loadScripts(t testing.TB, name string) []script {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "testdata", "fixtures", name))
	if err != nil {
		t.Fatalf("scripts: %v", err)
	}
	var out []script
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("scripts: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("no scripts")
	}
	return out
}

// scriptValue converts a value from a script the way loro-crdt's binding
// converted the JavaScript value (loro-wasm convert.rs:624): a whole number
// within ±2^53 is an integer and any other number a float; {"$bin": base64}
// is a byte string.
func scriptValue(raw json.RawMessage) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return jsValue(v)
}

func jsValue(v any) (any, error) {
	switch x := v.(type) {
	case json.Number:
		f, err := x.Float64()
		if err != nil {
			return nil, err
		}
		if f == math.Trunc(f) && math.Abs(f) <= 1<<53 {
			return int64(f), nil
		}
		return f, nil
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			c, err := jsValue(e)
			if err != nil {
				return nil, err
			}
			out[i] = c
		}
		return out, nil
	case map[string]any:
		if b, ok := x["$bin"].(string); ok && len(x) == 1 {
			return base64.StdEncoding.DecodeString(b)
		}
		out := make(map[string]any, len(x))
		for k, e := range x {
			c, err := jsValue(e)
			if err != nil {
				return nil, err
			}
			out[k] = c
		}
		return out, nil
	}
	return v, nil
}

// tagged renders a Document's JSON as the script records loro's toJSON: a
// byte string as {"$bin": base64} and every number as a float.
func tagged(v any) any {
	switch x := v.(type) {
	case []byte:
		return map[string]any{"$bin": base64.StdEncoding.EncodeToString(x)}
	case int64:
		return float64(x)
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = tagged(e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = tagged(e)
		}
		return out
	}
	return v
}

// atom is one id of a change, with everything loro records for it: its
// container and kind, the position its author saw, its value (a character,
// a list item, a map value, a counter step) or for a delete the id it
// removes, and its causal past, the version vector of what its author had
// seen with itself included.
type atom struct {
	ID      ID
	Lamport int64
	Cont    contKey
	VKind   change.ValueKind
	Pos     int64
	Key     string
	Value   any
	Past    map[uint64]int64
}

// atomsOf splits changes into atoms, sorted by id. How loro groups atoms
// into ops and ops into changes is left out: that is what byte identity
// adds.
func atomsOf(changes []Change) ([]atom, error) {
	byPeer := map[uint64][]*Change{}
	for i := range changes {
		c := &changes[i]
		byPeer[c.ID.Peer] = append(byPeer[c.ID.Peer], c)
	}
	for _, cs := range byPeer {
		slices.SortFunc(cs, func(a, b *Change) int { return cmp.Compare(a.ID.Counter, b.ID.Counter) })
	}
	find := func(id ID) *Change {
		for _, c := range byPeer[id.Peer] {
			if c.ID.Counter <= id.Counter && id.Counter < changeEnd(*c) {
				return c
			}
		}
		return nil
	}
	closure := map[*Change]map[uint64]int64{}
	var depsPast func(c *Change) (map[uint64]int64, error)
	depsPast = func(c *Change) (map[uint64]int64, error) {
		if v, ok := closure[c]; ok {
			return v, nil
		}
		v := map[uint64]int64{}
		for _, d := range c.Deps {
			z := find(d)
			if z == nil {
				return nil, fmt.Errorf("dependency %d@%d of change %d@%d is missing", d.Counter, d.Peer, c.ID.Counter, c.ID.Peer)
			}
			zv, err := depsPast(z)
			if err != nil {
				return nil, err
			}
			for p, e := range zv {
				v[p] = max(v[p], e)
			}
			v[d.Peer] = max(v[d.Peer], d.Counter+1)
		}
		closure[c] = v
		return v, nil
	}
	var out []atom
	for i := range changes {
		c := &changes[i]
		base, err := depsPast(c)
		if err != nil {
			return nil, err
		}
		for _, op := range c.Ops {
			for k := int64(0); k < op.Len; k++ {
				a := atom{
					ID:      ID{Peer: op.Peer, Counter: op.Counter + k},
					Lamport: op.Lamport + k,
					Cont:    keyOf(op),
					VKind:   op.VKind,
					Pos:     op.Pos,
					Key:     op.MapKey,
					Value:   op.Value,
				}
				switch v := op.Value.(type) {
				case string:
					a.Value = string([]rune(v)[k])
					a.Pos = op.Pos + k
				case []any:
					a.Value = v[k]
					a.Pos = op.Pos + k
				case DeleteSpan:
					start, n := v.Normalize()
					if v.Len >= 0 {
						a.Value = ID{Peer: v.Peer, Counter: start + k}
					} else {
						a.Value = ID{Peer: v.Peer, Counter: start + n - 1 - k}
						a.Pos = op.Pos - k
					}
				}
				past := copyVV(base)
				past[op.Peer] = max(past[op.Peer], a.ID.Counter+1)
				a.Past = past
				out = append(out, a)
			}
		}
	}
	slices.SortFunc(out, func(a, b atom) int {
		return cmp.Or(cmp.Compare(a.ID.Peer, b.ID.Peer), cmp.Compare(a.ID.Counter, b.ID.Counter))
	})
	return out, nil
}

func copyVV(m map[uint64]int64) map[uint64]int64 {
	out := make(map[uint64]int64, len(m)+1)
	for k, v := range m {
		out[k] = v
	}
	return out
}

// firstAtomDiff describes the first atom where got and want differ.
func firstAtomDiff(got, want []atom) string {
	for i := 0; i < min(len(got), len(want)); i++ {
		if !reflect.DeepEqual(got[i], want[i]) {
			return fmt.Sprintf("atom %d:\n got  %+v\n want %+v", i, got[i], want[i])
		}
	}
	return fmt.Sprintf("%d atoms, want %d", len(got), len(want))
}

// shapeOf describes how a history groups its atoms: every change with its
// id, lamport, timestamp, message and deps, and its ops' ids and lengths,
// block by block. Two exports with the same shape and the same atoms differ
// at most in byte order loro takes from hash maps.
func shapeOf(u *Updates) string {
	var b strings.Builder
	at := 0
	for bi, n := range u.Blocks {
		fmt.Fprintf(&b, "block %d:\n", bi)
		for _, c := range u.Changes[at : at+n] {
			deps := slices.Clone(c.Deps)
			slices.SortFunc(deps, func(a, b ID) int { return cmp.Compare(a.Peer, b.Peer) })
			fmt.Fprintf(&b, " change %d@%d lamport %d ts %d msg %q deps %v:", c.ID.Counter, c.ID.Peer, c.Lamport, c.Timestamp, c.CommitMsg, deps)
			for _, op := range c.Ops {
				fmt.Fprintf(&b, " %d+%d", op.Counter, op.Len)
			}
			b.WriteString("\n")
		}
		at += n
	}
	return b.String()
}

// checkResult counts what one run of scripts matched, per peer at each
// checkpoint.
type checkResult struct {
	checks, json, atoms, shape, bytes int
	failures                          []string
	// pairs are the Go exports that differ from loro's in bytes, with
	// loro's, for loro-crdt to import side by side.
	pairs [][3]string
	// last is each peer's export and JSON at its script's last checkpoint,
	// by script and peer.
	last map[string]peerExport
}

type peerExport struct {
	Script string `json:"script"`
	Peer   uint64 `json:"peer"`
	Go     []byte `json:"go"`
	Loro   []byte `json:"loro"`
	JSON   any    `json:"json"`
}

func (r *checkResult) fail(format string, args ...any) {
	r.failures = append(r.failures, fmt.Sprintf(format, args...))
}

// runScript replays s with one Document per peer, rules knocked out as
// knock says, and holds each to loro-crdt at every checkpoint.
func runScript(s *script, knock ruleSet, r *checkResult) {
	docs := make([]*Document, len(s.Peers))
	for i, p := range s.Peers {
		docs[i] = NewDocument(p)
		docs[i].knock = knock
	}
	h := &scriptHandles{docs: docs, m: map[string]any{}}
	blobs := make([][]byte, len(s.Blobs))
	for i, b := range s.Blobs {
		blobs[i], _ = base64.StdEncoding.DecodeString(b)
	}
	for ai, raw := range s.Actions {
		var a scriptAction
		if err := json.Unmarshal(raw, &a); err != nil {
			r.fail("%s action %d: %v", s.label(), ai, err)
			return
		}
		where := fmt.Sprintf("%s action %d (%s)", s.label(), ai, a.A)
		var err error
		d := func() *Document { return docs[a.P] }
		switch a.A {
		case "text_ins":
			err = h.text(a.P, a.C).Insert(a.Pos, a.S)
		case "text_del":
			err = h.text(a.P, a.C).Delete(a.Pos, a.N)
		case "map_set":
			var v any
			if v, err = scriptValue(a.V); err == nil {
				err = h.mapH(a.P, a.C).Set(a.K, v)
			}
		case "map_del":
			err = h.mapH(a.P, a.C).Delete(a.K)
		case "list_ins":
			var v any
			if v, err = scriptValue(a.V); err == nil {
				err = h.list(a.P, a.C).Insert(a.Pos, v.([]any)...)
			}
		case "list_del":
			err = h.list(a.P, a.C).Delete(a.Pos, a.N)
		case "inc":
			var v any
			if v, err = scriptValue(a.V); err == nil {
				f := 0.0
				switch x := v.(type) {
				case int64:
					f = float64(x)
				case float64:
					f = x
				}
				err = h.counter(a.P, a.C).Increment(f)
			}
		case "acquire":
			switch a.Kind {
			case "text":
				h.text(a.P, a.C)
			case "map":
				h.mapH(a.P, a.C)
			case "list":
				h.list(a.P, a.C)
			case "counter":
				h.counter(a.P, a.C)
			}
		case "commit":
			if a.Msg == nil && a.TS == nil {
				d().Commit()
				break
			}
			opts := CommitOptions{}
			if a.Msg != nil {
				opts.Message = *a.Msg
			}
			if a.TS != nil {
				opts.Timestamp, opts.HasTimestamp = *a.TS, true
			}
			d().CommitWith(opts)
		case "merge_interval":
			var v any
			if v, err = scriptValue(a.V); err == nil {
				d().SetChangeMergeInterval(v.(int64))
			}
		case "export":
			_, err = d().ExportUpdates()
		case "import":
			err = d().Import(blobs[a.Blob])
			if a.Refused != "" {
				if err == nil {
					err = fmt.Errorf("imported what loro-crdt refused (%s)", a.Refused)
				} else {
					err = nil
				}
			}
		case "check":
			for i, want := range a.Peers {
				where := fmt.Sprintf("%s check at action %d peer %d", s.label(), ai, s.Peers[i])
				e := checkPeer(where, docs[i], want.JSON, blobs[want.Export], r)
				if r.last != nil && e != nil {
					e.Script, e.Peer = s.label(), s.Peers[i]
					r.last[fmt.Sprintf("%s/%d", s.label(), s.Peers[i])] = *e
				}
			}
		default:
			err = fmt.Errorf("unknown action")
		}
		if err != nil {
			r.fail("%s: %v", where, err)
			return
		}
	}
}

// scriptHandles keeps each handle a script takes, as an application keeps
// them: one per peer, kind and name, taken the first time it is needed and
// used across imports.
type scriptHandles struct {
	docs []*Document
	m    map[string]any
}

func (h *scriptHandles) get(p int, kind, name string, take func(*Document) any) any {
	k := fmt.Sprintf("%d/%s/%s", p, kind, name)
	if x, ok := h.m[k]; ok {
		return x
	}
	x := take(h.docs[p])
	h.m[k] = x
	return x
}

func (h *scriptHandles) text(p int, name string) *Text {
	return h.get(p, "text", name, func(d *Document) any { return d.Text(name) }).(*Text)
}

func (h *scriptHandles) mapH(p int, name string) *Map {
	return h.get(p, "map", name, func(d *Document) any { return d.Map(name) }).(*Map)
}

func (h *scriptHandles) list(p int, name string) *List {
	return h.get(p, "list", name, func(d *Document) any { return d.List(name) }).(*List)
}

func (h *scriptHandles) counter(p int, name string) *Counter {
	return h.get(p, "counter", name, func(d *Document) any { return d.Counter(name) }).(*Counter)
}

// checkPeer holds one Document to loro-crdt's toJSON and export of the same
// peer: the JSON must be equal and the atoms of the two histories too; the
// shape and the bytes of the export are counted.
func checkPeer(where string, d *Document, wantJSON json.RawMessage, wantBlob []byte, r *checkResult) *peerExport {
	r.checks++
	gotBlob, err := d.ExportUpdates()
	if err != nil {
		r.fail("%s: export: %v", where, err)
		return nil
	}
	got, err := d.JSON()
	if err != nil {
		r.fail("%s: JSON: %v", where, err)
		return nil
	}
	g, _ := json.Marshal(tagged(got))
	var w any
	_ = json.Unmarshal(wantJSON, &w)
	ws, _ := json.Marshal(w)
	if string(g) == string(ws) {
		r.json++
	} else {
		r.fail("%s: JSON\n got  %s\n want %s", where, g, ws)
	}

	want, err := DecodeUpdates(wantBlob)
	if err != nil {
		r.fail("%s: decode loro's export: %v", where, err)
		return nil
	}
	d.mu.Lock()
	var stored []Change
	for _, c := range d.log.store.changes() {
		stored = append(stored, c.ch)
	}
	exported := exportUpdates(d.log.exportBlocks(d))
	d.mu.Unlock()
	ga, err1 := atomsOf(stored)
	wa, err2 := atomsOf(want.Changes)
	switch {
	case err1 != nil || err2 != nil:
		r.fail("%s: atoms: %v %v", where, err1, err2)
	case reflect.DeepEqual(ga, wa):
		r.atoms++
	default:
		r.fail("%s: atoms: %s", where, firstAtomDiff(ga, wa))
	}
	if shapeOf(exported) == shapeOf(want) {
		r.shape++
	}
	if bytes.Equal(gotBlob, wantBlob) {
		r.bytes++
	} else {
		r.pairs = append(r.pairs, [3]string{where, string(gotBlob), string(wantBlob)})
	}
	return &peerExport{Go: gotBlob, Loro: wantBlob, JSON: tagged(got)}
}

func runScripts(t *testing.T, scripts []script, knock ruleSet) *checkResult {
	t.Helper()
	r := &checkResult{}
	for i := range scripts {
		runScript(&scripts[i], knock, r)
	}
	return r
}

// TestScriptCorpus runs the 300 random scripts of script_corpus.json, edits,
// commits and syncs by one to three peers, and holds every Document to
// loro-crdt at every checkpoint: the same toJSON, and the same atoms in its
// history as loro's export of the same peer (ids, lamports, containers,
// positions, values, delete targets, causal pasts). How many exports also
// group them as loro does, and how many are loro's bytes, is logged.
func TestScriptCorpus(t *testing.T) {
	scripts := loadScripts(t, "script_corpus.json")
	r := runScripts(t, scripts, 0)
	reportScripts(t, "script_corpus.json", len(scripts), r)
}

func reportScripts(t *testing.T, name string, n int, r *checkResult) {
	t.Helper()
	for i, f := range r.failures {
		if i == 5 {
			t.Errorf("... and %d more", len(r.failures)-5)
			break
		}
		t.Error(f)
	}
	t.Logf("%s: %d scripts, %d checks: JSON %d/%d, atoms %d/%d, shape %d/%d, bytes %d/%d",
		name, n, r.checks, r.json, r.checks, r.atoms, r.checks, r.shape, r.checks, r.bytes, r.checks)
	if r.json != r.checks || r.atoms != r.checks {
		t.Errorf("%s: JSON %d/%d, atoms %d/%d", name, r.json, r.checks, r.atoms, r.checks)
	}
}

// TestNamedScripts runs one small script per rule of loro's that a
// Document has to follow (review item numbers in testdata/gen/gen_scripts.mjs)
// and holds each peer at each checkpoint to loro-crdt in full: toJSON, the
// atoms of its history, how its export groups them, and its bytes.
func TestNamedScripts(t *testing.T) {
	for _, s := range loadScripts(t, "script_named.json") {
		t.Run(s.Name, func(t *testing.T) {
			r := &checkResult{}
			runScript(&s, 0, r)
			for _, f := range r.failures {
				t.Error(f)
			}
			if r.checks == 0 || r.json != r.checks || r.atoms != r.checks || r.shape != r.checks || r.bytes != r.checks {
				t.Errorf("%d checks: JSON %d, atoms %d, shape %d, bytes %d", r.checks, r.json, r.atoms, r.shape, r.bytes)
			}
		})
	}
}

// TestNamedScriptsNeedTheirRule is the positive control of TestNamedScripts:
// with one of loro's rules switched off, the script written for it no
// longer matches loro-crdt. A named script that still matched would not be
// testing its rule.
func TestNamedScriptsNeedTheirRule(t *testing.T) {
	scripts := map[string]*script{}
	all := loadScripts(t, "script_named.json")
	for i := range all {
		scripts[all[i].Name] = &all[i]
	}
	for _, c := range []struct {
		rule   rule
		script string
	}{
		{ruleImportCommits, "import_commits_txn"},
		{rulePendingIsolated, "pending_tail_before_prefix"},
		{rulePendingTrim, "pending_overlap_released"},
		{ruleTimestampFloor, "timestamps_floor_and_interval"},
		{ruleMergeInterval, "timestamps_floor_and_interval"},
		{ruleBlockLimit, "change_merge_block_limit"},
		{ruleFoldLocalOnly, "fold_local_ops_only"},
		{ruleDeleteSplit, "text_delete_split"},
		{ruleMapSkipEqual, "map_noop_rules"},
		{ruleMapDeleteAbsent, "map_noop_rules"},
		{ruleCounterInt, "counter_encoding"},
		{ruleTypedRoots, "typed_roots_and_acquire"},
		{ruleRootsVisible, "typed_roots_and_acquire"},
		{ruleHandleLive, "handle_survives_import"},
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
			first = r.failures[0]
			if i := strings.IndexByte(first, '\n'); i >= 0 {
				first = first[:i]
			}
		}
		t.Logf("rule %d off: %s: %d checks, JSON %d, atoms %d, shape %d, bytes %d; %s",
			c.rule, c.script, r.checks, r.json, r.atoms, r.shape, r.bytes, first)
	}
}
