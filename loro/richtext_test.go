package loro

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// TestScriptRichCorpus runs the 300 rich-text scripts of
// script_rich_corpus.json: marks and unmarks by one to three peers over all
// four expand rules, keys configured per peer, style values of every shape,
// inserts at the edges of styled ranges and deletes across anchors. Every
// Document is held to loro-crdt at every checkpoint: toJSON, toDelta of
// its text, and the atoms of its history; a mark loro refuses, it refuses.
func TestScriptRichCorpus(t *testing.T) {
	scripts := loadScripts(t, "script_rich_corpus.json")
	r := runScripts(t, scripts, 0)
	reportScripts(t, "script_rich_corpus.json", len(scripts), r)
}

// TestRichNamedScripts runs the named scripts of script_rich_named.json,
// one or more per rule of loro's marks, and holds each peer at each
// checkpoint to loro-crdt in full: toJSON, toDelta, atoms, how the export
// groups them, and its bytes.
func TestRichNamedScripts(t *testing.T) {
	for _, s := range loadScripts(t, "script_rich_named.json") {
		t.Run(s.Name, func(t *testing.T) {
			r := &checkResult{}
			runScript(&s, 0, r)
			for _, f := range r.failures {
				t.Error(f)
			}
			if r.checks == 0 || r.json != r.checks || r.deltas != r.deltaChecks || r.atoms != r.checks || r.shape != r.checks || r.bytes != r.checks {
				t.Errorf("%d checks: JSON %d, delta %d/%d, atoms %d, shape %d, bytes %d", r.checks, r.json, r.deltas, r.deltaChecks, r.atoms, r.shape, r.bytes)
			}
		})
	}
}

// TestRichNamedScriptsNeedTheirRule is the positive control of
// TestRichNamedScripts: with one mark rule switched off, the script written
// for it no longer matches loro-crdt.
func TestRichNamedScriptsNeedTheirRule(t *testing.T) {
	scripts := map[string]*script{}
	all := loadScripts(t, "script_rich_named.json")
	for i := range all {
		scripts[all[i].Name] = &all[i]
	}
	for _, c := range []struct {
		rule   rule
		script string
	}{
		{ruleMarkSkipApplied, "mark_skip_applied"},
		{ruleUnmarkSkipAbsent, "unmark_absent_key"},
		{ruleMarkConfigured, "mark_unconfigured_key"},
		{ruleUnmarkReverse, "expand_rules_and_flags"},
		{ruleColonPrefix, "colon_keys"},
		{ruleConfigReplaces, "config_replaces_defaults"},
		{ruleMarkRange, "mark_ends_among_anchors"},
	} {
		s := scripts[c.script]
		if s == nil {
			t.Fatalf("no script %s", c.script)
		}
		r := &checkResult{}
		runScript(s, 1<<c.rule, r)
		if len(r.failures) == 0 && r.checks > 0 && r.json == r.checks && r.deltas == r.deltaChecks &&
			r.atoms == r.checks && r.shape == r.checks && r.bytes == r.checks {
			t.Errorf("rule %d off: %s still matches loro-crdt", c.rule, c.script)
			continue
		}
		first := ""
		if len(r.failures) > 0 {
			first, _, _ = strings.Cut(r.failures[0], "\n")
		}
		t.Logf("rule %d off: %s: %d checks, JSON %d, delta %d/%d, atoms %d, bytes %d; %s",
			c.rule, c.script, r.checks, r.json, r.deltas, r.deltaChecks, r.atoms, r.bytes, first)
	}
}

// TestMarkErrors checks that misuse of marks and style configuration is an
// error that changes nothing.
func TestMarkErrors(t *testing.T) {
	d := NewDocument(1)
	text := d.Text("t")
	if err := text.Insert(0, "abc"); err != nil {
		t.Fatal(err)
	}
	before, err := d.ExportUpdates()
	if err != nil {
		t.Fatal(err)
	}
	for name, err := range map[string]error{
		"empty range":           text.Mark(1, 1, "bold", true),
		"reversed range":        text.Mark(2, 1, "bold", true),
		"negative start":        text.Mark(-1, 1, "bold", true),
		"past the end":          text.Mark(1, 4, "bold", true),
		"unconfigured key":      text.Mark(0, 1, "nope", true),
		"value of no kind":      text.Mark(0, 1, "bold", struct{}{}),
		"key not UTF-8":         text.Mark(0, 1, "\xff", true),
		"unmark past the end":   text.Unmark(0, 9, "bold"),
		"configured colon key":  d.ConfigTextStyle(map[string]ExpandType{"a:b": ExpandAfter}),
		"unknown expand":        d.ConfigTextStyle(map[string]ExpandType{"a": ExpandType(9)}),
		"mark on reserved peer": NewDocument(1<<64-1).Text("t").Mark(0, 1, "bold", true),
	} {
		if err == nil || !strings.HasPrefix(err.Error(), "loro: ") {
			t.Errorf("%s: %v", name, err)
		}
	}
	after, err := d.ExportUpdates()
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("a refused mark changed the history")
	}
	// A refused configuration leaves the one before: bold still marks.
	if err := text.Mark(0, 1, "bold", true); err != nil {
		t.Errorf("bold after a refused configuration: %v", err)
	}
}

// TestMarkDelta reads back marks through Delta on the live document, with
// the info bytes loro gives them: After for bold, None for link, and an
// unmark of link reversed to Both.
func TestMarkDelta(t *testing.T) {
	d := NewDocument(1)
	text := d.Text("t")
	steps := []error{
		text.Insert(0, "hello world"),
		text.Mark(0, 5, "bold", true),
		text.Mark(6, 11, "link", map[string]any{"href": "x"}),
		text.Unmark(8, 9, "link"),
	}
	for i, err := range steps {
		if err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
	}
	delta, err := text.Delta()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, op := range delta {
		got = append(got, op.Insert+fmtAttrs(op.Attributes))
	}
	if want := "hello{bold:true} |   | wo{link:map[href:x]} | r | ld{link:map[href:x]}"; strings.Join(got, " | ") != want {
		t.Errorf("delta %s\n want %s", strings.Join(got, " | "), want)
	}
	d.mu.Lock()
	var infos []uint8
	for _, op := range d.tx.ops {
		if m, ok := op.Value.(MarkInfo); ok {
			infos = append(infos, m.Info)
		}
	}
	d.mu.Unlock()
	if !slices.Equal(infos, []uint8{132, 128, 134}) {
		t.Errorf("info bytes %v, want [132 128 134]", infos)
	}
}

func fmtAttrs(a map[string]any) string {
	if a == nil {
		return ""
	}
	keys := make([]string, 0, len(a))
	for k := range a {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var b strings.Builder
	b.WriteString("{")
	for i, k := range keys {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, "%s:%v", k, a[k])
	}
	b.WriteString("}")
	return b.String()
}
