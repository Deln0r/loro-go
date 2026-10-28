package loro

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// versionsAction is one action of a named script of
// testdata/gen/gen_versions.mjs.
type versionsAction struct {
	A         string          `json:"a"`
	P         int             `json:"p"`
	C         string          `json:"c"`
	Pos       int             `json:"pos"`
	N         int             `json:"n"`
	S         string          `json:"s"`
	K         string          `json:"k"`
	V         json.RawMessage `json:"v"`
	Start     int             `json:"start"`
	End       int             `json:"end"`
	Msg       *string         `json:"msg"`
	Blob      int             `json:"blob"`
	VV        int             `json:"vv"`
	Frontiers [][2]any        `json:"frontiers"`
	FB        int             `json:"fb"`
	From      int             `json:"from"`
	Panicked  bool            `json:"panicked"`
	// Refused is loro-crdt's error when it refused the import.
	Refused string `json:"refused"`
	// Spans are an export_range's spans, as [peer, counter, len].
	Spans [][3]any `json:"spans"`
}

// versionsNamedResult counts the checks of one named script.
type versionsNamedResult struct {
	versions, versionsOK, exports, exportsOK int
	failures                                 []string
}

func (r *versionsNamedResult) fail(format string, args ...any) {
	r.failures = append(r.failures, fmt.Sprintf(format, args...))
}

// runVersionsScript replays a named script with one Document per peer and
// holds each at every "version" and "export_from" to loro-crdt. emit, when
// not nil, is handed each export from a version loro-crdt made too, with
// the version's bytes and the document that exported it.
func runVersionsScript(s *script, knock ruleSet, emit func(d *Document, from, got []byte)) *versionsNamedResult {
	r := &versionsNamedResult{}
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
		var a versionsAction
		if err := json.Unmarshal(raw, &a); err != nil {
			r.fail("action %d: %v", ai, err)
			return r
		}
		where := fmt.Sprintf("action %d (%s)", ai, a.A)
		d := docs[a.P]
		var err error
		switch a.A {
		case "text_ins":
			err = h.text(a.P, a.C).Insert(a.Pos, a.S)
		case "text_del":
			err = h.text(a.P, a.C).Delete(a.Pos, a.N)
		case "mark":
			var v any
			if v, err = scriptValue(a.V); err == nil {
				err = h.text(a.P, a.C).Mark(a.Start, a.End, a.K, v)
			}
		case "map_set":
			var v any
			if v, err = scriptValue(a.V); err == nil {
				err = h.mapH(a.P, a.C).Set(a.K, v)
			}
		case "list_ins":
			var v any
			if v, err = scriptValue(a.V); err == nil {
				err = h.list(a.P, a.C).Insert(a.Pos, v.([]any)...)
			}
		case "inc":
			var v any
			if v, err = scriptValue(a.V); err == nil {
				err = h.counter(a.P, a.C).Increment(float64(v.(int64)))
			}
		case "commit":
			if a.Msg == nil {
				d.Commit()
			} else {
				d.CommitWith(CommitOptions{Message: *a.Msg})
			}
		case "import":
			err = d.Import(blobs[a.Blob])
			if a.Refused != "" {
				if err == nil {
					err = fmt.Errorf("imported what loro-crdt refused (%s)", a.Refused)
				} else {
					err = nil
				}
			}
		case "version":
			r.versions++
			got, want := d.Version().Encode(), blobs[a.VV]
			fr := d.Frontiers()
			fb, ferr := EncodeFrontiers(fr)
			switch {
			case !bytes.Equal(got, want):
				r.fail("%s: version % x, want % x", where, got, want)
			case !reflect.DeepEqual(fr, jsIDs(a.Frontiers)):
				r.fail("%s: frontiers %v, want %v", where, fr, jsIDs(a.Frontiers))
			case ferr != nil || !bytes.Equal(fb, blobs[a.FB]):
				r.fail("%s: frontiers bytes % x, want % x (%v)", where, fb, blobs[a.FB], ferr)
			default:
				r.versionsOK++
			}
		case "export_from":
			r.exports++
			var from VersionVector
			if from, err = DecodeVersionVector(blobs[a.From]); err != nil {
				break
			}
			var got []byte
			if got, err = d.ExportUpdatesFrom(from); err != nil {
				break
			}
			want := blobs[a.Blob]
			if emit != nil && !a.Panicked {
				emit(d, blobs[a.From], got)
			}
			if a.Panicked {
				// loro-crdt panicked: a counter below 0 means 0.
				zeroed := from
				for p, c := range from.Map() {
					if c < 0 {
						_ = zeroed.SetEnd(p, 0)
					}
				}
				want, err = d.ExportUpdatesFrom(zeroed)
			}
			if bytes.Equal(got, want) {
				r.exportsOK++
			} else {
				kind, why := classifyExport(got, want)
				r.fail("%s: export from % x differs in %s: %s", where, blobs[a.From], kind, why)
			}
		case "export_range":
			r.exports++
			var spans []IDSpan
			for _, x := range a.Spans {
				id, e := jsID([2]any{x[0], x[1]})
				if e != nil {
					err = e
					break
				}
				n, _ := x[2].(float64)
				spans = append(spans, IDSpan{Peer: id.Peer, Start: id.Counter, End: id.Counter + int64(n)})
			}
			var got []byte
			if err == nil {
				got, err = d.ExportUpdatesInRange(spans)
			}
			if err == nil {
				if bytes.Equal(got, blobs[a.Blob]) {
					r.exportsOK++
				} else {
					kind, why := classifyExport(got, blobs[a.Blob])
					r.fail("%s: export of %v differs in %s: %s", where, spans, kind, why)
				}
			}
		default:
			err = fmt.Errorf("unknown action")
		}
		if err != nil {
			r.fail("%s: %v", where, err)
			return r
		}
	}
	return r
}

// TestVersionsNamed runs the named scripts of versions_named.json, in which
// Go Documents edit, commit and import as loro-crdt peers did, and holds
// each version, frontiers and export from a version to loro-crdt's bytes.
func TestVersionsNamed(t *testing.T) {
	for _, s := range loadScripts(t, "versions_named.json") {
		t.Run(s.Name, func(t *testing.T) {
			r := runVersionsScript(&s, 0, nil)
			for _, f := range r.failures {
				t.Error(f)
			}
			if r.versions+r.exports == 0 {
				t.Error("nothing checked")
			}
			t.Logf("versions %d/%d, exports %d/%d", r.versionsOK, r.versions, r.exportsOK, r.exports)
		})
	}
}

// TestVersionsNamedNeedTheirRules is the positive control of
// TestVersionsNamed: with one of loro's rules switched off, the script
// written for it no longer matches loro-crdt.
func TestVersionsNamedNeedTheirRules(t *testing.T) {
	scripts := map[string]*script{}
	all := loadScripts(t, "versions_named.json")
	for i := range all {
		scripts[all[i].Name] = &all[i]
	}
	for _, c := range []struct {
		rule   rule
		script string
	}{
		{ruleExportCut, "local_history_from_versions"},
		{ruleCutDeps, "cut_change_with_deps"},
		{ruleCutLamport, "cut_change_with_deps"},
		{ruleExportRegroup, "export_regroups_blocks"},
		{ruleLocalReserve, "local_op_grows_full_map"},
		{ruleLocalReserve, "local_op_in_full_map"},
		{ruleSnapshotVersion, "snapshot_brings_version"},
		{ruleSnapshotVersion, "snapshot_stored_version_installed"},
		{ruleSharedOwner, "decoded_list_inserts_fold"},
		{ruleSliceShares, "list_slices_share_their_items"},
		{ruleCheckUnfolded, "ops_checked_before_folding"},
	} {
		s := scripts[c.script]
		if s == nil {
			t.Fatalf("no script %s", c.script)
		}
		r := runVersionsScript(s, 1<<c.rule, nil)
		if len(r.failures) == 0 {
			t.Errorf("rule %d off: %s still matches loro-crdt", c.rule, c.script)
			continue
		}
		first, _, _ := strings.Cut(r.failures[0], "\n")
		t.Logf("rule %d off: %s: versions %d/%d, exports %d/%d; %s", c.rule, c.script, r.versionsOK, r.versions, r.exportsOK, r.exports, first)
	}
}
