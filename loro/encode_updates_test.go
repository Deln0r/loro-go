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

// TestTreeDeleteHasNoPosition rebuilds the first input FuzzEncodeUpdates
// found. A tree delete named a position index past its block's positions,
// which decoded to no fractional index; EncodeUpdates wrote index 0, as loro
// writes for every delete, and that decoded to the block's first position. A
// delete has no position: loro writes 0 and its decoder returns the delete
// before it looks the index up. So DecodeUpdates no longer shows one for a
// delete, whatever index the block names, and the round trip holds. The
// block here creates node 0@1 at position 80 and deletes it twice, naming
// index 0 and index 3 of a one-position arena. (The fuzzer's own input put
// its tree ops in a container of kind 48, which EncodeUpdates now refuses.)
func TestTreeDeleteHasNoPosition(t *testing.T) {
	treeOp := func(m change.RawTreeMove) []byte {
		var w change.ValueWriter
		w.RawTreeMove(m)
		return w.Bytes()
	}
	deleted := change.RawTreeMove{ParentPeerIdx: 1, ParentCounter: math.MaxInt32}
	var values []byte
	values = append(values, treeOp(change.RawTreeMove{ParentNull: true})...)
	for _, idx := range []int64{0, 3} {
		deleted.PositionIdx = idx
		values = append(values, treeOp(deleted)...)
	}
	blk := &change.Block{
		CounterLen: 3, LamportLen: 3, NChanges: 1,
		Header: change.EncodeHeader(&change.ChangeHeader{
			Peers: []uint64{1, math.MaxUint64}, DepOnSelf: []bool{false}, DepLens: []uint64{0},
		}),
		ChangeMeta: change.EncodeChangeMeta(&change.ChangeMeta{Timestamps: []int64{0}, CommitMsgs: []string{""}}),
		CIDs:       change.EncodeContainers([]change.Container{{IsRoot: true, Kind: change.CTree}}),
		Keys:       change.EncodeKeys([]string{"tr"}),
		Positions:  change.EncodePositions([][]byte{{0x80}}),
		Ops: change.EncodeOps(&change.Ops{
			ContainerIdx: []int64{0, 0, 0}, Prop: []int64{0, 0, 0},
			ValueKind: []change.ValueKind{change.VKRawTreeMove, change.VKRawTreeMove, change.VKRawTreeMove},
			Len:       []int64{1, 1, 1},
		}),
		Values: values,
	}
	u, err := DecodeUpdates(fast.Encode(fast.ModeFastUpdates, change.EncodeFastUpdates([][]byte{change.EncodeBlock(blk)})))
	if err != nil {
		t.Fatal(err)
	}
	var fis []string
	for _, op := range u.Changes[0].Ops {
		fis = append(fis, op.Value.(TreeNode).FI)
	}
	if want := []string{"80", "", ""}; !slices.Equal(fis, want) {
		t.Errorf("fractional indexes %q, want %q: a delete has none", fis, want)
	}
	blob, err := EncodeUpdates(u)
	if err != nil {
		t.Fatal(err)
	}
	back, err := DecodeUpdates(blob)
	if err != nil {
		t.Fatal(err)
	}
	if d := diffUpdates(u, back); d != "" {
		t.Error(d)
	}
}

// TestBlockFillsToExactly4096 pins where loro closes a block: at an
// estimated size of 4096 the block still takes the change, past it not
// (ChangesBlock::push_change, change_store.rs). The fixture's first 819
// changes are estimated at exactly 4096 together and share a block; the
// 820th opens a second. EncodeUpdates, grouping on its own, must split them
// the same way.
func TestBlockFillsToExactly4096(t *testing.T) {
	blob := readFixture(t, "encode_block_edge.update.bin")
	u, err := DecodeUpdates(blob)
	if err != nil {
		t.Fatal(err)
	}
	if want := []int{819, 1}; !slices.Equal(u.Blocks, want) {
		t.Fatalf("loro wrote blocks %v, want %v", u.Blocks, want)
	}
	est := 0
	for _, ch := range u.Changes[:819] {
		est += estimateChange(ch)
	}
	if est != maxBlockSize {
		t.Errorf("the first block is estimated at %d, want exactly %d", est, maxBlockSize)
	}
	u.Blocks = nil
	got, err := EncodeUpdates(u)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, blob) {
		back, _ := DecodeUpdates(got)
		t.Errorf("grouped by EncodeUpdates: blocks %v, loro wrote [819 1]", back.Blocks)
	}
}

// solo is a change by peer 1 at counter 0 holding the one op o.
func solo(o Op) Change {
	o.Peer, o.Counter, o.Lamport = 1, 0, 0
	if o.Len == 0 {
		o.Len = 1
	}
	return Change{ID: ID{Peer: 1}, Ops: []Op{o}}
}

// at moves a change and its ops to counter ctr and lamport lamport.
func at(ch Change, ctr, lamport int64) Change {
	ch.ID.Counter, ch.Lamport = ctr, lamport
	ch.Ops = slices.Clone(ch.Ops)
	for i := range ch.Ops {
		ch.Ops[i].Counter, ch.Ops[i].Lamport = ctr, lamport
		ctr += ch.Ops[i].Len
		lamport += ch.Ops[i].Len
	}
	return ch
}

// rootOp is an op in the root container name of kind k.
func rootOp(k change.ContainerType, name string, vk change.ValueKind, v any) Op {
	return Op{Container: name, IsRoot: true, Kind: k, VKind: vk, Value: v}
}

// textIns inserts s at pos in root Text "t".
func textIns(pos int64, s string) Op {
	o := rootOp(change.CText, "t", change.VKStr, s)
	o.Pos, o.Len = pos, int64(len([]rune(s)))
	return o
}

func updatesOf(chs ...Change) *Updates { return &Updates{Changes: chs} }

// mapWrite writes v under key in root Map "m".
func mapWrite(key string, v any) Op {
	o := rootOp(change.CMap, "m", change.VKLoroValue, v)
	o.MapKey = key
	return o
}

// treeOp moves node id under parent ("" for none) at position 80 of root
// Tree "tr".
func treeOp(id, parent string) Op {
	n := TreeNode{ID: id, FI: "80"}
	if parent != "" {
		n.HasParent, n.Parent = true, parent
	}
	return rootOp(change.CTree, "tr", change.VKRawTreeMove, n)
}

// markOp marks [pos, pos+n) of root Text "t" with key.
func markOp(pos, n int64, key string, v any) Op {
	o := rootOp(change.CText, "t", change.VKMarkStart, MarkInfo{Start: pos, Len: n, Key: key, Value: v})
	o.Pos = pos
	return o
}

// moveOp moves the element of peer 2 at lamport from index from to index to
// of root MovableList "ml".
func moveOp(from, to, lamport int64) Op {
	o := rootOp(change.CMovableList, "ml", change.VKListMove, ListElem{Peer: 2, Lamport: lamport})
	o.MoveFrom, o.Pos = from, to
	return o
}

// setOp sets the element of peer 2 at lamport in root MovableList "ml".
func setOp(lamport int64, v any) Op {
	return rootOp(change.CMovableList, "ml", change.VKListSet, ListSetValue{Elem: ListElem{Peer: 2, Lamport: lamport}, Value: v})
}

// nestedOp writes 1 under "k" in the Map created by op id.
func nestedOp(id string) Op {
	o := mapWrite("k", int64(1))
	o.IsRoot, o.Container = false, id
	return o
}

// textDel deletes from root Text "t" at pos the span of peer 2 starting at
// counter ctr, n atoms long (negative n: backwards).
func textDel(pos, ctr, n int64) Op {
	o := rootOp(change.CText, "t", change.VKDeleteSeq, DeleteSpan{Peer: 2, Counter: ctr, Len: n})
	o.Pos, o.Len = pos, max(n, -n)
	return o
}

// listIns inserts the number 1 at pos in root List "l".
func listIns(pos int64) Op {
	o := rootOp(change.CList, "l", change.VKLoroValue, []any{int64(1)})
	o.Pos = pos
	return o
}

func withPos(o Op, p int64) Op {
	o.Pos = p
	return o
}

func withLen(o Op, n int64) Op {
	o.Len = n
	return o
}

func withDeps(ch Change, deps ...ID) Change {
	ch.Deps = deps
	return ch
}

// encodeCase is a set of changes, named for the test log.
type encodeCase struct {
	name string
	u    *Updates
}

// TestEncodeUpdatesRefusesWhatLoroCannotRead holds EncodeUpdates to the
// blobs loro-crdt 1.17.1 reads back as written. Each refused case was encoded
// by EncodeUpdates before it refused it, and imported into loro-crdt; the
// comment over each group says what loro did with it. The accepted cases sit
// at the edge of each limit, so the checks refuse no more than loro does.
func TestEncodeUpdatesRefusesWhatLoroCannotRead(t *testing.T) {
	refused := []encodeCase{
		// Counters and lamports past loro's fields, an i32 and a u32. A change
		// at counter 2^63-1 wrapped past the range check and was written;
		// loro refused it ("a varint that didn't terminate").
		{"a change at counter 2^63-1", updatesOf(at(solo(textIns(0, "a")), math.MaxInt64, 0))},
		{"a change at counter 2^31", updatesOf(at(solo(textIns(0, "a")), math.MaxInt32+1, 0))},
		{"a change ending past counter 2^31-1", updatesOf(at(solo(textIns(0, "ab")), math.MaxInt32-1, 0))},
		{"a change at lamport 2^32", updatesOf(at(solo(textIns(0, "a")), 0, 1<<32))},
		{"a change ending past lamport 2^32-1", updatesOf(at(solo(textIns(0, "ab")), 0, math.MaxUint32-1))},
		{"an op of length 2^63-1", updatesOf(func() Change { c := solo(textIns(0, "a")); c.Ops[0].Len = math.MaxInt64; return c }())},

		// A dependency at counter -1 on the author's own peer, on a change at
		// counter 0, passed as the op just before it and became the self
		// flag. loro-go then refused its own blob, and loro-crdt panicked on
		// import ("Id:-1@1 is not in change store").
		{"a dependency on counter -1 of the own peer", updatesOf(withDeps(solo(textIns(0, "a")), ID{Peer: 1, Counter: -1}))},
		{"a dependency on counter -1 of another peer", updatesOf(withDeps(solo(textIns(0, "a")), ID{Peer: 2, Counter: -1}))},
		{"a dependency on counter 2^31", updatesOf(withDeps(solo(textIns(0, "a")), ID{Peer: 2, Counter: math.MaxInt32 + 1}))},

		// A value kind loro does not read in that kind of container. A map
		// write of an i64 was written, and loro-crdt refused the blob as
		// corrupted data.
		{"an i64 map write", updatesOf(solo(rootOp(change.CMap, "m", change.VKI64, int64(5))))},
		{"a true map write", updatesOf(solo(rootOp(change.CMap, "m", change.VKTrue, true)))},
		{"a container index map write", updatesOf(solo(rootOp(change.CMap, "m", change.VKContainer, uint64(0))))},
		{"a value in a Text", updatesOf(solo(rootOp(change.CText, "t", change.VKLoroValue, []any{int64(1)})))},
		{"a string in a List", updatesOf(solo(rootOp(change.CList, "l", change.VKStr, "a")))},
		{"a set in a List", updatesOf(solo(rootOp(change.CList, "l", change.VKListSet, ListSetValue{Value: int64(1)})))},
		{"a mark in a MovableList", updatesOf(solo(rootOp(change.CMovableList, "ml", change.VKMarkStart, MarkInfo{Len: 1, Key: "b"})))},
		{"a value in a Tree", updatesOf(solo(rootOp(change.CTree, "tr", change.VKLoroValue, int64(1))))},
		{"a value in a Counter", updatesOf(solo(rootOp(change.CCounter, "c", change.VKLoroValue, int64(1))))},
		{"a container of unknown kind", updatesOf(solo(rootOp(change.ContainerType(7), "u", change.VKLoroValue, int64(1))))},

		// An op that is one atom, written longer. A map write of length 2
		// claimed counters 0 and 1; loro-crdt imported it and its version
		// was {1: 1}, so a peer would ask again for an op it already has.
		{"a map write of length 2", updatesOf(solo(withLen(mapWrite("k", int64(1)), 2)))},
		{"a map delete of length 2", updatesOf(solo(withLen(rootOp(change.CMap, "m", change.VKDeleteOnce, nil), 2)))},
		{"a tree op of length 2", updatesOf(solo(withLen(treeOp("0@1", ""), 2)))},
		{"a counter step of length 2", updatesOf(solo(withLen(rootOp(change.CCounter, "c", change.VKI64, int64(1)), 2)))},
		{"a mark of length 2", updatesOf(solo(withLen(markOp(0, 1, "b", true), 2)))},
		{"a mark's end of length 2", updatesOf(solo(withLen(rootOp(change.CText, "t", change.VKNull, nil), 2)))},
		{"a move of length 2", updatesOf(solo(withLen(moveOp(0, 0, 0), 2)))},
		{"a set of length 2", updatesOf(solo(withLen(setOp(0, "x"), 2)))},

		// An id inside a value wider than loro's field, which loro truncates:
		// a set naming the element of lamport 2^32 set the element of lamport
		// 0, and tree node 4294967296@1 imported as 0@1.
		{"a set naming lamport 2^32", updatesOf(solo(setOp(1<<32, "x")))},
		{"a set naming lamport -1", updatesOf(solo(setOp(-1, "x")))},
		{"a move naming lamport 2^32", updatesOf(solo(moveOp(0, 0, 1<<32)))},
		{"a move from index 2^32", updatesOf(solo(moveOp(1<<32, 0, 0)))},
		{"a tree node at counter 2^32", updatesOf(solo(treeOp("4294967296@1", "")))},
		{"a tree node at counter -1", updatesOf(solo(treeOp("-1@1", "")))},
		{"a tree parent at counter 2^31", updatesOf(solo(treeOp("0@1", "2147483648@1")))},
		{"a child container at counter 2^31", updatesOf(solo(nestedOp("2147483648@1")))},
		{"a child container at counter -1", updatesOf(solo(nestedOp("-1@1")))},
		{"a tree node without a peer", updatesOf(solo(treeOp("0@?", "")))},
		{"a nested container named like a root", updatesOf(solo(nestedOp("meta")))},
		{"a tree op at a fractional index that is not hex", updatesOf(solo(rootOp(change.CTree, "tr", change.VKRawTreeMove, TreeNode{ID: "0@1", FI: "zz"})))},
		// Already refused before: a delete's span is checked against the i32
		// counter range by checkDeleteSpan.
		{"a delete from counter 2^31", updatesOf(solo(textDel(0, 1<<31, 1)))},
		{"a delete past counter 2^31-1", updatesOf(solo(textDel(0, math.MaxInt32, 2)))},

		// A position loro's decoder refuses (check_positions, list_op.rs):
		// -1 arrived as 4294967295 and was refused, and so was 1073741823.
		{"a text insert at -1", updatesOf(solo(textIns(-1, "a")))},
		{"a list insert at 1073741823", updatesOf(solo(listIns(1073741823)))},
		{"a delete ending at 1073741823", updatesOf(solo(textDel(1073741822, 0, 2)))},
		{"a backwards delete starting at -1", updatesOf(solo(textDel(0, 0, -2)))},
		{"a delete at -1", updatesOf(solo(textDel(-1, 0, 1)))},
		{"a move to 1073741823", updatesOf(solo(moveOp(0, 1073741823, 0)))},
		{"a move from -1", updatesOf(solo(moveOp(-1, 0, 0)))},
		{"a mark ending at 1073741823", updatesOf(solo(markOp(1073741822, 1, "b", true)))},
		{"a mark of length -1", updatesOf(solo(markOp(5, -1, "b", true)))},
		{"a set with a position", updatesOf(solo(withPos(setOp(0, "x"), 5)))},
		{"a mark's end with a position", updatesOf(solo(withPos(rootOp(change.CText, "t", change.VKNull, nil), 2)))},
		{"a tree op with a position", updatesOf(solo(withPos(treeOp("0@1", ""), 3)))},
		{"a counter step with a position", updatesOf(solo(withPos(rootOp(change.CCounter, "c", change.VKI64, int64(1)), 1)))},
		{"a map write with a position", updatesOf(solo(withPos(mapWrite("k", int64(1)), 1)))},

		// A string that is not UTF-8: loro-crdt refused each of the text,
		// the commit message, the map key and the string value as corrupted
		// data.
		{"text that is not UTF-8", updatesOf(solo(textIns(0, "\xff")))},
		{"a commit message that is not UTF-8", updatesOf(func() Change { c := solo(textIns(0, "a")); c.CommitMsg = "\xff"; return c }())},
		{"a map key that is not UTF-8", updatesOf(solo(mapWrite("\xff", int64(1))))},
		{"a string value that is not UTF-8", updatesOf(solo(mapWrite("k", "\xff")))},
		{"a nested key that is not UTF-8", updatesOf(solo(mapWrite("k", map[string]any{"\xff": int64(1)})))},
		{"a string in a list item that is not UTF-8", updatesOf(solo(rootOp(change.CList, "l", change.VKLoroValue, []any{[]any{"\xff"}})))},
		{"a mark key that is not UTF-8", updatesOf(solo(markOp(0, 1, "\xff", true)))},
		{"a mark value that is not UTF-8", updatesOf(solo(markOp(0, 1, "b", "\xff")))},
		{"a set value that is not UTF-8", updatesOf(solo(setOp(0, "\xff")))},
		{"a root name that is not UTF-8", updatesOf(solo(rootOp(change.CCounter, "\xff", change.VKI64, int64(1))))},
	}
	for _, c := range refused {
		if _, err := EncodeUpdates(c.u); err == nil {
			t.Errorf("%s: encoded without error", c.name)
		} else if !strings.HasPrefix(err.Error(), "loro") {
			t.Errorf("%s: error %q does not say where it comes from", c.name, err)
		}
	}

	accepted := []encodeCase{
		{"a change ending at counter 2^31-1", updatesOf(at(solo(textIns(0, "a")), math.MaxInt32-1, 0))},
		{"a change ending at lamport 2^32-1", updatesOf(at(solo(textIns(0, "a")), 0, math.MaxUint32-1))},
		{"a dependency on the own previous op", updatesOf(solo(textIns(0, "a")), withDeps(at(solo(textIns(0, "b")), 1, 1), ID{Peer: 1, Counter: 0}))},
		{"a dependency on counter 2^31-1", updatesOf(withDeps(solo(textIns(0, "a")), ID{Peer: 2, Counter: math.MaxInt32}))},
		{"a fractional counter step", updatesOf(solo(rootOp(change.CCounter, "c", change.VKF64, 0.5)))},
		{"a map delete", updatesOf(solo(rootOp(change.CMap, "m", change.VKDeleteOnce, nil)))},
		{"a mark's end", updatesOf(solo(rootOp(change.CText, "t", change.VKNull, nil)))},
		{"a set naming lamport 2^32-1", updatesOf(solo(setOp(math.MaxUint32, "x")))},
		{"a tree node at counter 2^31-1", updatesOf(solo(treeOp("2147483647@1", "")))},
		{"a tree delete", updatesOf(solo(treeOp("0@1", deletedTreeRoot)))},
		{"a child container at counter 2^31-1", updatesOf(solo(nestedOp("2147483647@1")))},
		{"a delete of counter 2^31-1", updatesOf(solo(textDel(0, math.MaxInt32, 1)))},
		{"a list insert at 1073741822", updatesOf(solo(listIns(1073741822)))},
		{"a backwards delete ending at 0", updatesOf(solo(textDel(1, 0, -2)))},
		{"a mark ending at 1073741822", updatesOf(solo(markOp(1073741821, 1, "b", true)))},
		{"a move between the last positions", updatesOf(solo(moveOp(1073741822, 1073741821, 0)))},
		{"binary that is not UTF-8", updatesOf(solo(mapWrite("k", []byte{0xff})))},
		{"text beyond the BMP", updatesOf(solo(textIns(0, "😀é")))},
	}
	for _, c := range accepted {
		if _, err := EncodeUpdates(c.u); err != nil {
			t.Errorf("%s: %v", c.name, err)
		}
	}
}
