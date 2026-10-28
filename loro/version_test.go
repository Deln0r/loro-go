package loro

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"sync"
	"testing"
)

// versionsHistory is one history of testdata/gen/gen_versions.mjs: a
// replay of it in loro-crdt, with its version, frontiers and exports from
// many versions.
type versionsHistory struct {
	Seed           int             `json:"seed"`
	Shape          string          `json:"shape"`
	Peers          int             `json:"peers"`
	Update         string          `json:"update"`
	VV             string          `json:"vv"`
	Frontiers      [][2]any        `json:"frontiers"`
	FrontiersBytes string          `json:"frontiersBytes"`
	Versions       []versionsEntry `json:"versions"`
	Ranges         []versionsRange `json:"ranges"`
	Blobs          []string        `json:"blobs"`
	// Changes is how many changes the history holds.
	Changes int `json:"changes"`
	// Snapshot, when set, is the history as a snapshot, and SnapshotVV and
	// SnapshotFrontiers the version and frontiers of an empty document
	// that imported it.
	Snapshot          string   `json:"snapshot"`
	SnapshotVV        string   `json:"snapshotVV"`
	SnapshotFrontiers [][2]any `json:"snapshotFrontiers"`
}

type versionsEntry struct {
	Kind    string   `json:"kind"`
	Entries [][2]any `json:"entries"`
	SetEnd  bool     `json:"setEnd"`
	Bytes   string   `json:"bytes"`
	// Redecoded is set where decoding bytes and encoding them again gives
	// other bytes: decoding sizes the map for its entries up front.
	Redecoded string `json:"redecoded"`
	// Export indexes Blobs, or is -1 for Update.
	Export int `json:"export"`
}

func loadVersionsCorpus(t testing.TB) []versionsHistory {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "testdata", "fixtures", "versions_corpus.json"))
	if err != nil {
		t.Fatal(err)
	}
	var out []versionsHistory
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if len(out) == 0 {
		t.Fatal("no histories")
	}
	return out
}

func unb64(s string) []byte {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

// jsID reads a [peer, counter] pair as the fixtures write it: the peer a
// decimal string, the counter a number.
func jsID(p [2]any) (ID, error) {
	s, _ := p[0].(string)
	peer, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return ID{}, err
	}
	c, _ := p[1].(float64)
	return ID{Peer: peer, Counter: int64(c)}, nil
}

// versionsRange is an export of id ranges: spans as [peer, counter, len].
type versionsRange struct {
	Spans  [][3]any `json:"spans"`
	Export int      `json:"export"`
}

// export is loro's export from version v.
func (h *versionsHistory) export(v versionsEntry) []byte {
	return h.blob(v.Export)
}

// blob is the blob at index i, the history itself for -1.
func (h *versionsHistory) blob(i int) []byte {
	if i < 0 {
		return unb64(h.Update)
	}
	return unb64(h.Blobs[i])
}

func jsIDs(ps [][2]any) []ID {
	var out []ID
	for _, p := range ps {
		id, err := jsID(p)
		if err != nil {
			panic(err)
		}
		out = append(out, id)
	}
	return out
}

// versionsResult counts what the corpus matched.
type versionsResult struct {
	histories, vv, frontiers, frontiersBytes                           int
	snapshots, snapshotVV, snapshotFrontiers                           int
	versions, decoded, redecoded, built, builtOf, exports, exportBytes int
	ranges, rangeBytes                                                 int
	// kinds counts byte-identical exports by kind of version, of all.
	kinds    map[string][2]int
	diffs    map[string]int
	failures []string
}

func (r *versionsResult) fail(format string, args ...any) {
	r.failures = append(r.failures, fmt.Sprintf(format, args...))
}

// runVersionsCorpus imports each history into a Document and holds it to
// the replay loro-crdt recorded: the version vector's bytes, the frontiers
// in loro's order and their bytes, and for every version the decoded
// version vector's bytes again, the bytes of one built by SetEnd as
// parseJSON built it, and the export from it.
func runVersionsCorpus(hs []versionsHistory, knock ruleSet) *versionsResult {
	r := &versionsResult{kinds: map[string][2]int{}, diffs: map[string]int{}}
	for _, h := range hs {
		r.histories++
		where := fmt.Sprintf("seed %d (%s, %d peers)", h.Seed, h.Shape, h.Peers)
		d := NewDocument(math.MaxUint64)
		d.knock = knock
		if err := d.Import(unb64(h.Update)); err != nil {
			r.fail("%s: import: %v", where, err)
			continue
		}
		if got := d.Version().Encode(); bytes.Equal(got, unb64(h.VV)) {
			r.vv++
		} else {
			r.fail("%s: version % x, want % x", where, got, unb64(h.VV))
		}
		want := jsIDs(h.Frontiers)
		fr := d.Frontiers()
		if reflect.DeepEqual(fr, want) {
			r.frontiers++
		} else {
			r.fail("%s: frontiers %v, want %v", where, fr, want)
		}
		if b, err := EncodeFrontiers(fr); err == nil && bytes.Equal(b, unb64(h.FrontiersBytes)) {
			r.frontiersBytes++
		} else {
			r.fail("%s: frontiers bytes % x, want % x (%v)", where, b, unb64(h.FrontiersBytes), err)
		}
		if h.Snapshot != "" {
			r.snapshots++
			s := NewDocument(math.MaxUint64)
			s.knock = knock
			if err := s.Import(unb64(h.Snapshot)); err != nil {
				r.fail("%s: import the snapshot: %v", where, err)
			} else {
				if got := s.Version().Encode(); bytes.Equal(got, unb64(h.SnapshotVV)) {
					r.snapshotVV++
				} else {
					r.fail("%s: snapshot's version % x, want % x", where, got, unb64(h.SnapshotVV))
				}
				if got, want := s.Frontiers(), jsIDs(h.SnapshotFrontiers); reflect.DeepEqual(got, want) {
					r.snapshotFrontiers++
				} else {
					r.fail("%s: snapshot's frontiers %v, want %v", where, got, want)
				}
			}
		}
		for i, x := range h.Ranges {
			r.ranges++
			var spans []IDSpan
			for _, s := range x.Spans {
				id, err := jsID([2]any{s[0], s[1]})
				if err != nil {
					r.fail("%s range %d: %v", where, i, err)
				}
				n, _ := s[2].(float64)
				spans = append(spans, IDSpan{Peer: id.Peer, Start: id.Counter, End: id.Counter + int64(n)})
			}
			got, err := d.ExportUpdatesInRange(spans)
			switch want := h.blob(x.Export); {
			case err != nil:
				r.fail("%s range %d: %v", where, i, err)
			case bytes.Equal(got, want):
				r.rangeBytes++
			default:
				kind, why := classifyExport(got, want)
				r.fail("%s range %d %v: export differs in %s: %s", where, i, spans, kind, why)
			}
		}
		for i, v := range h.Versions {
			r.versions++
			at := fmt.Sprintf("%s version %d (%s)", where, i, v.Kind)
			vv, err := DecodeVersionVector(unb64(v.Bytes))
			if err != nil {
				r.fail("%s: decode: %v", at, err)
				continue
			}
			r.decoded++
			redecoded := v.Redecoded
			if redecoded == "" {
				redecoded = v.Bytes
			}
			if bytes.Equal(vv.Encode(), unb64(redecoded)) {
				r.redecoded++
			} else {
				r.fail("%s: decoded then encoded % x, want % x", at, vv.Encode(), unb64(redecoded))
			}
			if v.SetEnd {
				r.builtOf++
				var built VersionVector
				for _, e := range v.Entries {
					id, err := jsID(e)
					if err == nil {
						err = built.SetEnd(id.Peer, id.Counter)
					}
					if err != nil {
						r.fail("%s: %v", at, err)
					}
				}
				if bytes.Equal(built.Encode(), unb64(v.Bytes)) {
					r.built++
				} else {
					r.fail("%s: built by SetEnd % x, want % x", at, built.Encode(), unb64(v.Bytes))
				}
			}
			got, err := d.ExportUpdatesFrom(vv)
			if err != nil {
				r.fail("%s: export: %v", at, err)
				continue
			}
			wantExport := h.export(v)
			k := r.kinds[v.Kind]
			k[1]++
			if bytes.Equal(got, wantExport) {
				r.exportBytes++
				r.exports++
				k[0]++
			} else if kind, why := classifyExport(got, wantExport); kind == "" {
				r.exports++
				r.diffs["key order of a nested map"]++
			} else {
				r.diffs[kind]++
				r.fail("%s: export differs in %s: %s", at, kind, why)
			}
			r.kinds[v.Kind] = k
		}
	}
	return r
}

// classifyExport says how a Go export that is not loro's bytes differs from
// loro's: kind "" when the two hold the same changes in the same blocks, so
// that only the key order of a nested map can differ; otherwise the kind of
// difference and where it starts.
func classifyExport(got, want []byte) (kind, why string) {
	g, err1 := DecodeUpdates(got)
	w, err2 := DecodeUpdates(want)
	if err1 != nil || err2 != nil {
		return "decode", fmt.Sprintf("%v %v", err1, err2)
	}
	if gs, ws := shapeOf(g), shapeOf(w); gs != ws {
		return "changes or blocks", "\n" + gs + "want\n" + ws
	}
	ga, err1 := atomsOf(g.Changes)
	wa, err2 := atomsOf(w.Changes)
	if err1 == nil && err2 == nil && !reflect.DeepEqual(ga, wa) {
		return "atoms", firstAtomDiff(ga, wa)
	}
	if !reflect.DeepEqual(g.Changes, w.Changes) {
		return "change contents", ""
	}
	return "", ""
}

// TestVersionsCorpus holds Document.Version, Document.Frontiers,
// EncodeFrontiers, DecodeVersionVector, VersionVector.SetEnd and Encode, and
// Document.ExportUpdatesFrom to loro-crdt on the 320 random histories of
// versions_corpus.json, by one to twelve peers and of every kind of
// container, each exported from nine versions. All must match; the export
// counts are logged by kind of version.
func TestVersionsCorpus(t *testing.T) {
	r := runVersionsCorpus(loadVersionsCorpus(t), 0)
	for i, f := range r.failures {
		if i == 5 {
			t.Errorf("... and %d more", len(r.failures)-5)
			break
		}
		t.Error(f)
	}
	t.Logf("versions_corpus.json: %d histories: version bytes %d/%d, frontiers %d/%d, frontiers bytes %d/%d; as snapshots: version %d/%d, frontiers %d/%d",
		r.histories, r.vv, r.histories, r.frontiers, r.histories, r.frontiersBytes, r.histories,
		r.snapshotVV, r.snapshots, r.snapshotFrontiers, r.snapshots)
	t.Logf("%d versions: decoded %d, decoded then encoded %d, built by SetEnd %d/%d, exports %d (byte for byte %d)",
		r.versions, r.decoded, r.redecoded, r.built, r.builtOf, r.exports, r.exportBytes)
	t.Logf("%d exports of id ranges: byte for byte %d", r.ranges, r.rangeBytes)
	for _, k := range []string{"empty", "peer", "prefixes", "closed", "nonpositive", "full", "beyond", "unknown"} {
		t.Logf("  %-11s exports byte for byte %d/%d", k, r.kinds[k][0], r.kinds[k][1])
	}
	for why, n := range r.diffs {
		t.Logf("  differing: %d: %s", n, why)
	}
}

// TestVersionsCorpusNeedsItsRules is the positive control of
// TestVersionsCorpus: with one of loro's rules switched off, the corpus no
// longer matches loro-crdt. Two rules the random histories never reach, a
// regrouping that differs from the document's blocks and a local op's
// insert into a full map, have named scripts (TestVersionsNamedNeedTheirRules).
func TestVersionsCorpusNeedsItsRules(t *testing.T) {
	hs := loadVersionsCorpus(t)
	for _, c := range []struct {
		rule rule
		name string
	}{
		{ruleExportCut, "a change from holds a part of is exported from where from ends"},
		{ruleCutDeps, "the part exported depends on the op before it"},
		{ruleCutLamport, "the part exported starts later by the ops cut"},
		{ruleVersionOrder, "Version keeps loro's hash map order"},
		{ruleUnstableSort, "a blob's changes are ordered by Rust's unstable sort"},
		{ruleDecodeFold, "ops fold as a block is read"},
		{ruleSnapshotVersion, "a snapshot brings its version and frontiers"},
		{ruleFrontiersOrder, "Frontiers keeps loro's hash map order"},
		{ruleRangeLayers, "an export of id ranges writes a store per layer of ranges"},
	} {
		r := runVersionsCorpus(hs, 1<<c.rule)
		if len(r.failures) == 0 {
			t.Errorf("without %q the corpus still matches loro-crdt", c.name)
		}
		t.Logf("without %q: version %d/%d, frontiers %d/%d, snapshot version %d/%d and frontiers %d/%d, exports byte for byte %d/%d, of ranges %d/%d; failures %d",
			c.name, r.vv, r.histories, r.frontiers, r.histories, r.snapshotVV, r.snapshots, r.snapshotFrontiers, r.snapshots,
			r.exportBytes, r.versions, r.rangeBytes, r.ranges, len(r.failures))
	}
}

// TestDecodeVersionVectorSizesItsMap is the positive control of the
// "decoded then encoded" count of TestVersionsCorpus: a version vector
// decoded into a map that grows entry by entry, instead of one sized for
// its entries up front as serde sizes it, comes out in another order.
func TestDecodeVersionVectorSizesItsMap(t *testing.T) {
	differ, total := 0, 0
	for _, h := range loadVersionsCorpus(t) {
		for _, v := range h.Versions {
			total++
			want := v.Redecoded
			if want == "" {
				want = v.Bytes
			}
			vv, err := decodeVersionVector(unb64(v.Bytes), false)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(vv.Encode(), unb64(want)) {
				differ++
			}
		}
	}
	if differ == 0 {
		t.Fatal("a map that grows decodes every version vector as loro-crdt does")
	}
	t.Logf("a map that grows: %d of %d decoded version vectors encode otherwise than loro-crdt's", differ, total)
}

// TestVersionVectorAPI pins what the corpus does not: SetEnd removing a peer
// at 0, copies that do not share changes, errors for counters loro cannot
// hold, and bytes that are not a version vector or frontiers. The byte
// cases were measured on loro-crdt 1.17.1's VersionVector.decode and
// decodeFrontiers: an empty input and a varint past its type are refused,
// trailing bytes and a padded varint are not.
func TestVersionVectorAPI(t *testing.T) {
	var v VersionVector
	if err := v.SetEnd(5, 3); err != nil {
		t.Fatal(err)
	}
	w := v
	if err := w.SetEnd(7, 2); err != nil {
		t.Fatal(err)
	}
	if v.Len() != 1 || w.Len() != 2 {
		t.Fatalf("a copy shares SetEnd: %v %v", v.Map(), w.Map())
	}
	if err := w.SetEnd(5, 0); err != nil {
		t.Fatal(err)
	}
	if _, ok := w.Get(5); ok || w.Len() != 1 {
		t.Fatalf("SetEnd(5, 0) kept peer 5: %v", w.Map())
	}
	if c, ok := v.Get(5); !ok || c != 3 {
		t.Fatalf("Get(5) = %d, %v", c, ok)
	}
	if err := v.SetEnd(5, 1<<31); err == nil {
		t.Fatal("SetEnd took a counter past int32")
	}
	if !bytes.Equal(v.Encode(), []byte{1, 5, 6}) {
		t.Fatalf("encode % x", v.Encode())
	}
	if _, err := EncodeFrontiers([]ID{{Peer: 1, Counter: 1 << 31}}); err == nil {
		t.Fatal("EncodeFrontiers took a counter past int32")
	}
	if b, _ := EncodeFrontiers([]ID{{Peer: 7, Counter: 3}, {Peer: 5, Counter: -2}, {Peer: 7, Counter: 9}}); !bytes.Equal(b, []byte{2, 5, 3, 7, 18}) {
		t.Fatalf("frontiers % x", b)
	}
	for _, c := range []struct {
		in  []byte
		ok  bool
		out map[uint64]int64
	}{
		{nil, false, nil},
		{[]byte{0}, true, map[uint64]int64{}},
		{[]byte{1, 5, 6, 99, 98}, true, map[uint64]int64{5: 3}},
		{[]byte{2, 5, 6, 5, 8}, true, map[uint64]int64{5: 4}},
		{[]byte{1, 5, 1}, true, map[uint64]int64{5: -1}},
		{[]byte{0x81, 0x00, 5, 6}, true, map[uint64]int64{5: 3}},
		{[]byte{1, 5, 0x80, 0x80, 0x80, 0x80, 0x0f}, true, map[uint64]int64{5: 2013265920}},
		{[]byte{1, 5, 0x80, 0x80, 0x80, 0x80, 0x10}, false, nil},
		{[]byte{0x80, 0x80, 0x80, 0x80, 0x10}, false, nil},
		{[]byte{0x80, 0x80, 0x80, 0x80, 0x00}, true, map[uint64]int64{}},
		{[]byte{1, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x01, 2}, true, map[uint64]int64{math.MaxUint64: 1}},
		{[]byte{1, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x02, 2}, false, nil},
		{[]byte{0xff, 0xff, 0xff, 0xff, 0x0f, 1, 2}, false, nil},
	} {
		got, err := DecodeVersionVector(c.in)
		if (err == nil) != c.ok || (err == nil && !reflect.DeepEqual(got.Map(), c.out)) {
			t.Errorf("decode % x: %v, %v; want %v, ok %v", c.in, got.Map(), err, c.out, c.ok)
		}
	}
	if ids, err := DecodeFrontiers([]byte{3, 5, 2, 7, 4, 5, 8, 0xaa}); err != nil || !reflect.DeepEqual(ids, []ID{{Peer: 7, Counter: 2}, {Peer: 5, Counter: 4}}) {
		t.Errorf("decode frontiers: %v %v", ids, err)
	}
	if _, err := DecodeFrontiers(nil); err == nil {
		t.Error("decoded empty bytes as frontiers")
	}
}

// TestVersionConcurrentUse reads versions and exports from them while
// other goroutines edit and import; the race detector, with -race, checks
// that the document's mutex covers them.
func TestVersionConcurrentUse(t *testing.T) {
	d := NewDocument(1)
	other := NewDocument(2)
	if err := other.Text("t").Insert(0, "remote"); err != nil {
		t.Fatal(err)
	}
	remote, err := other.ExportUpdates()
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	run := func(f func(i int) error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				if err := f(i); err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	run(func(i int) error { return d.Text("t").Insert(0, "a") })
	run(func(i int) error {
		if i%10 == 0 {
			return d.Import(remote)
		}
		d.Commit()
		return nil
	})
	run(func(i int) error {
		v := d.Version()
		_ = d.Frontiers()
		_, err := d.ExportUpdatesFrom(v)
		return err
	})
	run(func(i int) error {
		_, err := d.ExportUpdatesInRange([]IDSpan{{Peer: 1, Start: 0, End: int64(i)}})
		return err
	})
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	all, err := d.ExportUpdatesFrom(VersionVector{})
	if err != nil {
		t.Fatal(err)
	}
	if want, _ := d.ExportUpdates(); !bytes.Equal(all, want) {
		t.Fatal("the export from an empty version is not the whole history's")
	}
	if got := d.Version().Map(); !reflect.DeepEqual(got, map[uint64]int64{1: 50, 2: 6}) {
		t.Fatalf("version %v", got)
	}
}
