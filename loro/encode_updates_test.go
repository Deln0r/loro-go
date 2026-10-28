package loro

import (
	"bytes"
	"cmp"
	"encoding/base64"
	"fmt"
	"maps"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/Deln0r/loro-go/encoding/change"
	"github.com/Deln0r/loro-go/encoding/fast"
)

// encodeCorpora are the random-history corpora whose exports EncodeUpdates
// must give back. encode_corpus.json is the one written for it
// (testdata/gen/gen_encode.mjs); the others were written for the merge.
var encodeCorpora = []string{
	"seq_single_corpus.json", "seq_concurrent_corpus.json", "ordering_corpus.json",
	"movable_single_corpus.json", "movable_concurrent_corpus.json", "tree_corpus.json",
	"rich_corpus.json", "encode_corpus.json",
}

// exportBlob is one FastUpdates export by loro-crdt.
type exportBlob struct {
	label string
	blob  []byte
}

// exportGroups returns every FastUpdates export in the fixtures, grouped: one
// group per corpus, and one for the named fixture files, the frozen 1.16.3
// one included. A fixture file is taken when its header says FastUpdates, so
// partial exports (.early.bin, .wipe.bin and the like) count as well.
func exportGroups(t *testing.T) (names []string, groups map[string][]exportBlob) {
	t.Helper()
	groups = map[string][]exportBlob{}
	for _, name := range encodeCorpora {
		for _, c := range loadCorpus(t, name) {
			blob, err := base64.StdEncoding.DecodeString(c.Update)
			if err != nil {
				t.Fatalf("%s seed %d: %v", name, c.Seed, err)
			}
			groups[name] = append(groups[name], exportBlob{fmt.Sprintf("%s seed %d", name, c.Seed), blob})
		}
		names = append(names, name)
	}
	for _, dir := range []string{"fixtures", filepath.Join("fixtures", "frozen")} {
		paths, err := filepath.Glob(filepath.Join("..", "testdata", dir, "*.bin"))
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range paths {
			blob, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			if h, err := fast.ParseHeader(blob); err != nil || h.Mode != fast.ModeFastUpdates {
				continue
			}
			label, _ := filepath.Rel(filepath.Join("..", "testdata", "fixtures"), p)
			groups["fixtures"] = append(groups["fixtures"], exportBlob{label, blob})
		}
	}
	return append(names, "fixtures"), groups
}

// loroKeyOrder reads the order in which loro listed the keys of every map
// nested in a value of blob, and returns a ValueWriter.KeyOrder that hands
// them out in turn. The encoder writes values block by block and op by op as
// loro did, so the n-th map it writes is the n-th map loro wrote.
func loroKeyOrder(t *testing.T, blob []byte) func(map[string]any) []string {
	t.Helper()
	h, err := fast.ParseHeader(blob)
	if err != nil {
		t.Fatal(err)
	}
	raws, err := change.SplitBlocks(h.Body)
	if err != nil {
		t.Fatal(err)
	}
	var orders [][]string
	for _, raw := range raws {
		blk, err := change.ParseBlock(raw)
		if err != nil {
			t.Fatal(err)
		}
		ops, err := change.DecodeOps(blk.Ops)
		if err != nil {
			t.Fatal(err)
		}
		keys, err := change.DecodeKeys(blk.Keys)
		if err != nil {
			t.Fatal(err)
		}
		vr := change.NewValueReader(blk.Values, keys)
		vr.RecordKeyOrder()
		for i := 0; i < ops.N(); i++ {
			if _, err := vr.OpContent(ops.ValueKind[i]); err != nil {
				t.Fatal(err)
			}
		}
		orders = append(orders, vr.KeyOrders()...)
	}
	next := 0
	return func(map[string]any) []string {
		if next == len(orders) {
			return nil
		}
		next++
		return orders[next-1]
	}
}

// hasMultiKeyMap reports whether any op of u holds a map of two or more keys
// nested in its value, the one class whose bytes loro writes in hash order.
func hasMultiKeyMap(u *Updates) bool {
	var walk func(v any) bool
	walk = func(v any) bool {
		switch x := v.(type) {
		case []any:
			return slices.ContainsFunc(x, walk)
		case map[string]any:
			if len(x) > 1 {
				return true
			}
			for _, e := range x {
				if walk(e) {
					return true
				}
			}
		case ListSetValue:
			return walk(x.Value)
		case MarkInfo:
			return walk(x.Value)
		}
		return false
	}
	for _, ch := range u.Changes {
		for _, op := range ch.Ops {
			if walk(op.Value) {
				return true
			}
		}
	}
	return false
}

// diffUpdates describes the first difference between two decoded exports, or
// returns "" when they hold the same changes in the same blocks. Floats are
// compared by their bits, so a NaN equals itself. A dependency on the
// author's own peer is a flag in the bytes, which DecodeUpdates lists first,
// so its place among the deps is not compared.
func diffUpdates(a, b *Updates) string {
	if !slices.Equal(a.Blocks, b.Blocks) {
		return fmt.Sprintf("blocks %v, then %v", a.Blocks, b.Blocks)
	}
	if len(a.Changes) != len(b.Changes) {
		return fmt.Sprintf("%d changes, then %d", len(a.Changes), len(b.Changes))
	}
	for i := range a.Changes {
		ca, cb := a.Changes[i], b.Changes[i]
		if ca.ID != cb.ID || ca.Lamport != cb.Lamport || ca.Timestamp != cb.Timestamp || ca.CommitMsg != cb.CommitMsg ||
			!slices.Equal(ownDepFirst(ca), ownDepFirst(cb)) || len(ca.Ops) != len(cb.Ops) {
			return fmt.Sprintf("change %d: %+v, then %+v", i, withoutOps(ca), withoutOps(cb))
		}
		for j := range ca.Ops {
			oa, ob := ca.Ops[j], cb.Ops[j]
			va, vb := oa.Value, ob.Value
			oa.Value, ob.Value = nil, nil
			if oa != ob || !sameDecoded(va, vb) {
				return fmt.Sprintf("change %d op %d: %+v %#v, then %+v %#v", i, j, oa, va, ob, vb)
			}
		}
	}
	return ""
}

func withoutOps(ch Change) Change {
	ch.Ops = nil
	return ch
}

func ownDepFirst(ch Change) []ID {
	deps := slices.Clone(ch.Deps)
	slices.SortStableFunc(deps, func(x, y ID) int {
		return cmp.Compare(boolInt(x.Peer != ch.ID.Peer), boolInt(y.Peer != ch.ID.Peer))
	})
	return deps
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func sameDecoded(a, b any) bool {
	switch x := a.(type) {
	case float64:
		y, ok := b.(float64)
		return ok && math.Float64bits(x) == math.Float64bits(y)
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !sameDecoded(x[i], y[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for k, e := range x {
			if f, ok := y[k]; !ok || !sameDecoded(e, f) {
				return false
			}
		}
		return true
	case ListSetValue:
		y, ok := b.(ListSetValue)
		return ok && x.Elem == y.Elem && sameDecoded(x.Value, y.Value)
	case MarkInfo:
		y, ok := b.(MarkInfo)
		return ok && x.Start == y.Start && x.Len == y.Len && x.Key == y.Key && x.Info == y.Info && sameDecoded(x.Value, y.Value)
	}
	return reflect.DeepEqual(a, b)
}

// TestEncodeUpdatesReproducesLoro decodes every FastUpdates export in the
// fixtures, every history of the eight corpora and every fixture file, and
// encodes it again. The bytes must come back exactly, with one exception
// allowed: a blob holding a map of two or more keys nested in a value, whose
// keys loro lists in hash order and EncodeUpdates sorts. Such a blob must
// still decode to the same changes, and encoded with the key order loro used
// (read from its own bytes) it must come back exactly too. So every blob is
// reproduced byte for byte once the hash order is supplied, which shows that
// order is the only thing the encoder does not reproduce.
func TestEncodeUpdatesReproducesLoro(t *testing.T) {
	names, groups := exportGroups(t)
	total, totalSame := 0, 0
	for _, name := range names {
		same, class := 0, 0
		for _, e := range groups[name] {
			u, err := DecodeUpdates(e.blob)
			if err != nil {
				t.Fatalf("%s: decode: %v", e.label, err)
			}
			got, err := EncodeUpdates(u)
			if err != nil {
				t.Errorf("%s: encode: %v", e.label, err)
				continue
			}
			ordered, err := encodeUpdates(u, loroKeyOrder(t, e.blob))
			if err != nil {
				t.Errorf("%s: encode with loro's key order: %v", e.label, err)
				continue
			}
			if !bytes.Equal(ordered, e.blob) {
				t.Errorf("%s: with loro's key order the bytes still differ:\n got  % x\n want % x", e.label, ordered, e.blob)
				continue
			}
			if bytes.Equal(got, e.blob) {
				same++
				continue
			}
			class++
			if !hasMultiKeyMap(u) {
				t.Errorf("%s: bytes differ, yet no map of two or more keys is nested in a value", e.label)
			}
			back, err := DecodeUpdates(got)
			if err != nil {
				t.Errorf("%s: the re-encoded blob does not decode: %v", e.label, err)
			} else if d := diffUpdates(u, back); d != "" {
				t.Errorf("%s: the re-encoded blob decodes differently: %s", e.label, d)
			}
		}
		n := len(groups[name])
		if n == 0 {
			t.Errorf("%s: no exports", name)
		}
		if class == 0 {
			t.Logf("%s: %d of %d byte-identical", name, same, n)
		} else {
			t.Logf("%s: %d of %d byte-identical; the other %d hold a map of 2+ keys in a value, decode the same, and are byte-identical in loro's key order",
				name, same, n, class)
		}
		total += n
		totalSame += same
	}
	t.Logf("all: %d of %d byte-identical, %d of %d in loro's key order", totalSame, total, total, total)
}

// TestEncodeUpdatesGroupsBlocksLikeLoro drops the block grouping DecodeUpdates
// recorded and lets EncodeUpdates group the changes itself, as it must for
// changes made in Go. Where loro wrote its blocks in (peer, counter) order, as
// every plain export does, the result must be loro's bytes again (in loro's
// key order, as above): the same blocks, split where loro split them, long
// histories that fill several 4 KiB blocks of one peer included. The one
// fixture that is not in that order is an export of id ranges, where loro
// puts a peer's second range in a later layer of blocks; that needs Blocks.
func TestEncodeUpdatesGroupsBlocksLikeLoro(t *testing.T) {
	names, groups := exportGroups(t)
	same, multi, unsorted := 0, 0, 0
	for _, name := range names {
		for _, e := range groups[name] {
			u, err := DecodeUpdates(e.blob)
			if err != nil {
				t.Fatalf("%s: decode: %v", e.label, err)
			}
			sorted, perPeer := true, map[uint64]int{}
			at := 0
			for i, n := range u.Blocks {
				id := u.Changes[at].ID
				perPeer[id.Peer]++
				if i > 0 {
					prev := u.Changes[at-1].ID
					sorted = sorted && cmp.Or(cmp.Compare(prev.Peer, id.Peer), cmp.Compare(prev.Counter, id.Counter)) < 0
				}
				at += n
			}
			if slices.ContainsFunc(slices.Collect(maps.Values(perPeer)), func(n int) bool { return n > 1 }) {
				multi++
			}
			u.Blocks = nil
			got, err := encodeUpdates(u, loroKeyOrder(t, e.blob))
			if err != nil {
				t.Errorf("%s: encode: %v", e.label, err)
				continue
			}
			switch {
			case bytes.Equal(got, e.blob):
				same++
			case sorted:
				back, _ := DecodeUpdates(got)
				orig, _ := DecodeUpdates(e.blob)
				t.Errorf("%s: grouped by EncodeUpdates the bytes differ: blocks %v, loro wrote %v", e.label, back.Blocks, orig.Blocks)
			default:
				unsorted++
				if e.label != "encode_in_range.update.bin" {
					t.Errorf("%s: loro's blocks are not in (peer, counter) order", e.label)
				}
			}
		}
	}
	if unsorted != 1 {
		t.Errorf("%d exports with blocks out of (peer, counter) order, want 1 (encode_in_range)", unsorted)
	}
	if multi == 0 {
		t.Error("no export has a peer with more than one block")
	}
	t.Logf("%d exports byte-identical when EncodeUpdates groups the blocks, %d with a peer in several blocks; %d out of (peer, counter) order",
		same, multi, unsorted)
}

// textChange is a change by peer that inserts s at position 0 of root Text
// "t", depending on deps.
func textChange(peer uint64, counter, lamport int64, s string, deps ...ID) Change {
	n := int64(len([]rune(s)))
	return Change{
		ID: ID{Peer: peer, Counter: counter}, Lamport: lamport, Deps: deps,
		Ops: []Op{{Container: "t", IsRoot: true, Kind: change.CText, VKind: change.VKStr, Value: s, Len: n,
			Peer: peer, Counter: counter, Lamport: lamport}},
	}
}

// TestEncodeUpdatesGroupsChangesMadeInGo builds changes by hand, three peers
// listed out of order, and has EncodeUpdates group them: sorted by peer and
// counter, one block per peer while a block's estimated size stays within
// 4096, a new block past it.
func TestEncodeUpdatesGroupsChangesMadeInGo(t *testing.T) {
	var chs []Change
	chs = append(chs, textChange(9, 0, 0, "b"))
	// Peer 3 types 1200 changes of four bytes each. Each change is estimated
	// at 4 bytes of id, lamport and timestamp, plus its text: 8, so 512 fit in
	// a block before the next one would pass 4096.
	for i := int64(0); i < 1200; i++ {
		var deps []ID
		if i > 0 {
			deps = []ID{{Peer: 3, Counter: i*4 - 1}}
		}
		chs = append(chs, textChange(3, i*4, i*4, "abcd", deps...))
	}
	chs = append(chs, textChange(5, 0, 0, "😀", ID{Peer: 9, Counter: 0}))
	u := &Updates{Changes: chs}
	blob, err := EncodeUpdates(u)
	if err != nil {
		t.Fatal(err)
	}
	back, err := DecodeUpdates(blob)
	if err != nil {
		t.Fatal(err)
	}
	if want := []int{512, 512, 176, 1, 1}; !slices.Equal(back.Blocks, want) {
		t.Errorf("blocks %v, want %v", back.Blocks, want)
	}
	sorted := slices.Clone(chs)
	slices.SortStableFunc(sorted, func(a, b Change) int {
		return cmp.Or(cmp.Compare(a.ID.Peer, b.ID.Peer), cmp.Compare(a.ID.Counter, b.ID.Counter))
	})
	if d := diffUpdates(&Updates{Changes: sorted, Blocks: back.Blocks}, back); d != "" {
		t.Errorf("decoded back differently: %s", d)
	}
	again, err := EncodeUpdates(back)
	if err != nil || !bytes.Equal(again, blob) {
		t.Errorf("re-encoding the decoded blob with its blocks gave other bytes (err %v)", err)
	}
}

// TestEncodeUpdatesRejects lists changes loro's format cannot hold, or holds
// differently from what they say, and requires an error for each.
func TestEncodeUpdatesRejects(t *testing.T) {
	ok := textChange(1, 0, 0, "ab")
	next := textChange(1, 2, 2, "c", ID{Peer: 1, Counter: 1})
	cases := map[string]*Updates{
		"a change without ops":          {Changes: []Change{{ID: ID{Peer: 1}}}},
		"an op at the wrong counter":    {Changes: []Change{func() Change { c := ok; c.Ops = slices.Clone(c.Ops); c.Ops[0].Counter = 1; return c }()}},
		"an op at the wrong lamport":    {Changes: []Change{func() Change { c := ok; c.Ops = slices.Clone(c.Ops); c.Ops[0].Lamport = 5; return c }()}},
		"an op by another peer":         {Changes: []Change{func() Change { c := ok; c.Ops = slices.Clone(c.Ops); c.Ops[0].Peer = 2; return c }()}},
		"an insert of the wrong length": {Changes: []Change{func() Change { c := ok; c.Ops = slices.Clone(c.Ops); c.Ops[0].Len = 3; return c }()}},
		"a dep on an earlier own op":    {Changes: []Change{ok, textChange(1, 2, 2, "c", ID{Peer: 1, Counter: 0})}},
		"two deps on the own peer":      {Changes: []Change{ok, textChange(1, 2, 2, "c", ID{Peer: 1, Counter: 1}, ID{Peer: 1, Counter: 1})}},
		"a negative counter":            {Changes: []Change{textChange(1, -1, 0, "a")}},
		"overlapping changes":           {Changes: []Change{ok, textChange(1, 1, 1, "c")}},
		"Blocks short of the changes":   {Changes: []Change{ok, next}, Blocks: []int{1}},
		"Blocks past the changes":       {Changes: []Change{ok}, Blocks: []int{2}},
		"a block of two peers":          {Changes: []Change{ok, textChange(2, 0, 0, "x")}, Blocks: []int{2}},
		"a block with a gap":            {Changes: []Change{ok, textChange(1, 5, 5, "x")}, Blocks: []int{2}},
		"a tree op naming no node":      {Changes: []Change{{ID: ID{Peer: 1}, Ops: []Op{{Container: "tr", IsRoot: true, Kind: change.CTree, VKind: change.VKRawTreeMove, Value: TreeNode{ID: "0@?"}, Len: 1, Peer: 1}}}}},
		"a nested container name":       {Changes: []Change{{ID: ID{Peer: 1}, Ops: []Op{{Container: "meta", Kind: change.CMap, VKind: change.VKLoroValue, Value: int64(1), Len: 1, Peer: 1}}}}},
		"a value of the wrong type":     {Changes: []Change{{ID: ID{Peer: 1}, Ops: []Op{{Container: "c", IsRoot: true, Kind: change.CCounter, VKind: change.VKI64, Value: "one", Len: 1, Peer: 1}}}}},
	}
	for name, u := range cases {
		if _, err := EncodeUpdates(u); err == nil {
			t.Errorf("%s: encoded without error", name)
		} else if !strings.HasPrefix(err.Error(), "loro") {
			t.Errorf("%s: error %q does not say where it comes from", name, err)
		}
	}
	if _, err := EncodeUpdates(&Updates{Changes: []Change{ok, next}}); err != nil {
		t.Errorf("two valid changes: %v", err)
	}
}

// TestEmitEncodedForJS writes, for every export EncodeUpdates does not give
// back byte for byte, loro's blob and loro-go's next to each other under
// testdata/go_out/encode, for testdata/gen/validate_encode.mjs to import both
// into loro-crdt and compare.
func TestEmitEncodedForJS(t *testing.T) {
	dir := filepath.Join("..", "testdata", "go_out", "encode")
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	names, groups := exportGroups(t)
	written := 0
	for _, name := range names {
		for _, e := range groups[name] {
			u, err := DecodeUpdates(e.blob)
			if err != nil {
				t.Fatal(err)
			}
			got, err := EncodeUpdates(u)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Equal(got, e.blob) {
				continue
			}
			base := strings.NewReplacer(" ", "_", "/", "_", ".json", "", ".update.bin", "").Replace(e.label)
			if err := os.WriteFile(filepath.Join(dir, base+".loro.bin"), e.blob, 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, base+".go.bin"), got, 0o644); err != nil {
				t.Fatal(err)
			}
			written++
		}
	}
	if written == 0 {
		t.Fatal("no export differs from loro's bytes, so there is nothing for loro-crdt to check")
	}
	t.Logf("wrote %d pairs to %s", written, dir)
}
