package loro

import "math/bits"

// frontiers is loro's Frontiers (version/frontiers.rs) as far as the order
// of its ids goes. loro keeps two or more ids in an FxHashMap from peer to
// counter and writes a change's deps in that map's iteration order, so the
// bytes of a change with deps on two other peers depend on where the hash
// table put them. loro-crdt's npm package is a 32-bit WebAssembly build:
// FxHasher (rustc-hash 2.1) hashes a peer id in two 32-bit words, and the
// table is hashbrown's portable one, whose control groups are 4 bytes wide.
// fxTable replays that table: where each id lands depends on the ids and on
// the order they were inserted and removed in.
//
// On the 9 pairs of peers that depend on each other in the script corpus,
// loro lists the two in the order this gives, the colliding pair 3 and 7
// included.
type frontiers struct {
	n   int // 0, 1 (one) or a map (tab)
	one ID
	tab *fxTable
}

func frontiersOf(ids []ID) frontiers {
	var f frontiers
	for _, id := range ids {
		f.push(id)
	}
	return f
}

func (f *frontiers) len() int {
	switch {
	case f.tab != nil:
		return f.tab.items
	case f.n == 1:
		return 1
	}
	return 0
}

// ids lists the ids in loro's iteration order.
func (f *frontiers) ids() []ID {
	switch {
	case f.tab != nil:
		return f.tab.ids()
	case f.n == 1:
		return []ID{f.one}
	}
	return nil
}

func (f *frontiers) contains(id ID) bool {
	if f.tab != nil {
		c, ok := f.tab.get(id.Peer)
		return ok && c == id.Counter
	}
	return f.n == 1 && f.one == id
}

// push is Frontiers::push: an id of a peer already listed replaces it if it
// is later.
func (f *frontiers) push(id ID) {
	switch {
	case f.tab != nil:
		f.tab.insertMax(id)
	case f.n == 0:
		f.n, f.one = 1, id
	case f.one.Peer != id.Peer:
		t := &fxTable{}
		t.insertMax(f.one)
		t.insertMax(id)
		f.n, f.tab = 2, t
	case f.one.Counter < id.Counter:
		f.one = id
	}
}

// remove is Frontiers::remove: id leaves only if it is listed exactly.
func (f *frontiers) remove(id ID) {
	switch {
	case f.tab != nil:
		if !f.tab.remove(id) {
			return
		}
		switch f.tab.items {
		case 0:
			*f = frontiers{}
		case 1:
			*f = frontiers{n: 1, one: f.tab.ids()[0]}
		}
	case f.n == 1 && f.one == id:
		*f = frontiers{}
	}
}

func (f *frontiers) equal(ids []ID) bool {
	if f.len() != len(ids) {
		return false
	}
	for _, id := range ids {
		if !f.contains(id) {
			return false
		}
	}
	return true
}

// updateOnNewChange is Frontiers::update_frontiers_on_new_change
// (version/frontiers.rs:233). deps are in their own iteration order.
func (f *frontiers) updateOnNewChange(last ID, deps []ID) {
	if f.len() <= 8 && f.equal(deps) {
		*f = frontiers{n: 1, one: last}
		return
	}
	for _, d := range deps {
		f.remove(d)
	}
	f.push(last)
}

// depsOrder is the order loro writes deps in once it has read them back
// from a blob in the order given: Frontiers built by pushing them one by
// one.
func depsOrder(deps []ID) []ID {
	if len(deps) < 2 {
		return deps
	}
	f := frontiersOf(deps)
	return f.ids()
}

// fxTable is a hashbrown RawTable of (peer, counter) with FxHasher on a
// 32-bit target, as far as where entries sit: control bytes with 4-byte
// groups and their trailing mirror, triangular probing, tombstones, and
// growth by rehashing in iteration order.
type fxTable struct {
	ctrl       []byte // buckets + fxGroup bytes, nil while unallocated
	keys       []uint64
	vals       []int64
	items      int
	growthLeft int
}

const (
	fxGroup   = 4
	fxEmpty   = 0xff
	fxDeleted = 0x80
)

// fxHash is FxHasher::finish after write_u64 on a 32-bit target
// (rustc-hash 2.1): the two halves added and multiplied in turn, rotated
// left by 15.
func fxHash(peer uint64) uint32 {
	const k = 0x93d765dd
	var h uint32
	h = (h + uint32(peer)) * k
	h = (h + uint32(peer>>32)) * k
	return bits.RotateLeft32(h, 15)
}

func fxH2(h uint32) byte { return byte(h>>25) & 0x7f }

func (t *fxTable) buckets() int {
	if t.ctrl == nil {
		return 0
	}
	return len(t.ctrl) - fxGroup
}

func (t *fxTable) mask() int { return max(t.buckets()-1, 0) }

func (t *fxTable) clone() *fxTable {
	return &fxTable{
		ctrl: append([]byte(nil), t.ctrl...), keys: append([]uint64(nil), t.keys...),
		vals: append([]int64(nil), t.vals...), items: t.items, growthLeft: t.growthLeft,
	}
}

// group returns the 4 control bytes from pos.
func (t *fxTable) group(pos int) []byte { return t.ctrl[pos : pos+fxGroup] }

func capacityOf(mask int) int {
	if mask < 8 {
		return mask
	}
	return (mask + 1) / 8 * 7
}

func (t *fxTable) setCtrl(i int, c byte) {
	t.ctrl[i] = c
	t.ctrl[((i-fxGroup)&t.mask())+fxGroup] = c
}

// find returns the bucket holding peer, or -1.
func (t *fxTable) find(peer uint64) int {
	if t.ctrl == nil {
		return -1
	}
	h := fxHash(peer)
	pos, stride := int(h)&t.mask(), 0
	for {
		g := t.group(pos)
		for i, c := range g {
			if c == fxH2(h) {
				if b := (pos + i) & t.mask(); t.keys[b] == peer {
					return b
				}
			}
		}
		for _, c := range g {
			if c == fxEmpty {
				return -1
			}
		}
		stride += fxGroup
		pos = (pos + stride) & t.mask()
	}
}

func (t *fxTable) get(peer uint64) (int64, bool) {
	if b := t.find(peer); b >= 0 {
		return t.vals[b], true
	}
	return 0, false
}

// insertSlot is find_insert_slot: the first empty or deleted bucket on the
// probe sequence.
func (t *fxTable) insertSlot(h uint32) int {
	pos, stride := int(h)&t.mask(), 0
	for {
		for i, c := range t.group(pos) {
			if c&0x80 != 0 {
				return (pos + i) & t.mask()
			}
		}
		stride += fxGroup
		pos = (pos + stride) & t.mask()
	}
}

// insertMax is InternalMap::insert: entry(peer).and_modify(max).or_insert,
// which grows the table first when the key is new and there is no room.
func (t *fxTable) insertMax(id ID) {
	if b := t.find(id.Peer); b >= 0 {
		t.vals[b] = max(t.vals[b], id.Counter)
		return
	}
	if t.growthLeft < 1 {
		t.reserveRehash()
	}
	h := fxHash(id.Peer)
	b := t.insertSlot(h)
	if t.ctrl[b] == fxEmpty {
		t.growthLeft--
	}
	t.setCtrl(b, fxH2(h))
	t.keys[b], t.vals[b] = id.Peer, id.Counter
	t.items++
}

// remove is remove_entry then erase: the bucket becomes empty, unless the
// empty buckets around it are too far apart for a probe to stop, and then a
// tombstone.
func (t *fxTable) remove(id ID) bool {
	b := t.find(id.Peer)
	if b < 0 || t.vals[b] != id.Counter {
		return false
	}
	before := t.group((b - fxGroup) & t.mask())
	after := t.group(b)
	lead := 0 // empty bytes at the end of before, counted from its last
	for i := fxGroup - 1; i >= 0 && before[i] != fxEmpty; i-- {
		lead++
	}
	trail := 0 // non-empty bytes at the start of after
	for i := 0; i < fxGroup && after[i] != fxEmpty; i++ {
		trail++
	}
	c := byte(fxEmpty)
	if lead+trail >= fxGroup {
		c = fxDeleted
	} else {
		t.growthLeft++
	}
	t.setCtrl(b, c)
	t.items--
	return true
}

func (t *fxTable) reserveRehash() {
	newItems := t.items + 1
	full := capacityOf(t.mask())
	if t.ctrl != nil && newItems <= full/2 {
		t.rehashInPlace()
		return
	}
	t.resize(max(newItems, full+1))
}

func (t *fxTable) resize(capacity int) {
	buckets := 8
	switch {
	case capacity < 4:
		buckets = 4
	case capacity >= 8:
		buckets = 1
		for buckets < capacity*8/7 {
			buckets *= 2
		}
	}
	old := t.clone()
	*t = fxTable{
		ctrl: make([]byte, buckets+fxGroup),
		keys: make([]uint64, buckets),
		vals: make([]int64, buckets),
	}
	for i := range t.ctrl {
		t.ctrl[i] = fxEmpty
	}
	t.growthLeft = capacityOf(buckets - 1)
	for b := 0; b < old.buckets(); b++ {
		if old.ctrl[b]&0x80 != 0 {
			continue
		}
		h := fxHash(old.keys[b])
		nb := t.insertSlot(h)
		t.setCtrl(nb, fxH2(h))
		t.keys[nb], t.vals[nb] = old.keys[b], old.vals[b]
		t.items++
		t.growthLeft--
	}
}

// rehashInPlace is hashbrown's rehash_in_place: tombstones cleared and every
// entry moved to the first free bucket of its probe sequence, unless it is
// already in the group the sequence starts in.
func (t *fxTable) rehashInPlace() {
	n := t.buckets()
	for i := 0; i < n; i++ {
		if t.ctrl[i]&0x80 == 0 {
			t.ctrl[i] = fxDeleted
		} else {
			t.ctrl[i] = fxEmpty
		}
	}
	copy(t.ctrl[n:], t.ctrl[:fxGroup])
	for i := 0; i < n; i++ {
		if t.ctrl[i] != fxDeleted {
			continue
		}
		for {
			h := fxHash(t.keys[i])
			ni := t.insertSlot(h)
			start := int(h) & t.mask()
			if ((i-start)&t.mask())/fxGroup == ((ni-start)&t.mask())/fxGroup {
				t.setCtrl(i, fxH2(h))
				break
			}
			prev := t.ctrl[ni]
			t.setCtrl(ni, fxH2(h))
			if prev == fxEmpty {
				t.setCtrl(i, fxEmpty)
				t.keys[ni], t.vals[ni] = t.keys[i], t.vals[i]
				break
			}
			t.keys[ni], t.keys[i] = t.keys[i], t.keys[ni]
			t.vals[ni], t.vals[i] = t.vals[i], t.vals[ni]
		}
	}
	t.growthLeft = capacityOf(t.mask()) - t.items
}

// ids lists the entries in bucket order, which is how hashbrown iterates.
func (t *fxTable) ids() []ID {
	var out []ID
	for b := 0; b < t.buckets(); b++ {
		if t.ctrl[b]&0x80 == 0 {
			out = append(out, ID{Peer: t.keys[b], Counter: t.vals[b]})
		}
	}
	return out
}
