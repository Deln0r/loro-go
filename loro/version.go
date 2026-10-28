package loro

import (
	"cmp"
	"fmt"
	"maps"
	"math"
	"slices"

	"github.com/Deln0r/loro-go/encoding/postcard"
)

// VersionVector is loro's version vector: for each peer, the counter just
// past the last of its ops a document has, which is how many ops of that
// peer it holds. Peers it has no op of are absent. It is what a peer sends
// to say what it has, and what ExportUpdatesFrom exports past.
//
// loro keeps a version vector in a hash map and writes its peers in that
// map's order, which depends on how the map was filled, not only on what it
// holds. A VersionVector keeps that order, so Encode writes the bytes
// loro-crdt writes for the same version vector: Document.Version gives
// loro's oplogVersion(), DecodeVersionVector gives VersionVector.decode,
// and SetEnd follows setEnd. Two version vectors with the same peers and
// counters can encode differently, as in loro; compare them with Map.
//
// The zero value is an empty version vector. A VersionVector can be copied
// and shared: SetEnd changes only the one it is called on.
type VersionVector struct {
	t fxTable
}

// Get returns the counter just past peer's last op, and false when the
// version vector has no entry for peer.
func (v VersionVector) Get(peer uint64) (int64, bool) {
	return v.t.get(peer)
}

// Len is the number of peers the version vector has an entry for.
func (v VersionVector) Len() int { return v.t.items }

// Map returns the version vector's entries as a map from peer to counter.
func (v VersionVector) Map() map[uint64]int64 {
	out := make(map[uint64]int64, v.t.items)
	for _, id := range v.t.ids() {
		out[id.Peer] = id.Counter
	}
	return out
}

// SetEnd sets peer's counter to end, as loro-crdt's VersionVector.setEnd
// does: an end of 0 or less removes the peer. loro stores a counter as a
// 32-bit integer, so an end past math.MaxInt32 is an error.
func (v *VersionVector) SetEnd(peer uint64, end int64) error {
	if end > math.MaxInt32 {
		return fmt.Errorf("loro: counter %d of peer %d is past loro's 32-bit counters", end, peer)
	}
	// Copy the table first: a VersionVector copied by value shares it.
	t := v.t.clone()
	if end <= 0 {
		t.removeKey(peer)
	} else {
		t.insertAlways(ID{Peer: peer, Counter: end})
	}
	v.t = *t
	return nil
}

// Encode writes the version vector as loro-crdt's VersionVector.encode()
// does (version.rs:976): postcard of the hash map, the number of entries,
// then each peer and its counter as varints, in the map's order.
func (v VersionVector) Encode() []byte {
	out := postcard.AppendUvarint(nil, uint64(v.t.items))
	for _, id := range v.t.ids() {
		out = postcard.AppendUvarint(out, id.Peer)
		out = appendI32(out, id.Counter)
	}
	return out
}

// maxDecodeCapacity is how many entries serde reserves at most for a map it
// decodes: a mebibyte of (u64, i32) pairs, 16 bytes each on wasm32
// (serde's size_hint::cautious).
const maxDecodeCapacity = 1 << 20 / 16

// DecodeVersionVector reads a version vector written by Encode or by
// loro-crdt's VersionVector.encode(), as VersionVector.decode does
// (version.rs:981): the map is made with room for the number of entries the
// bytes announce, then each entry is inserted in order, a later entry for a
// peer replacing an earlier one. Like loro it ignores bytes after the last
// entry, and keeps counters of 0 or below as they are.
//
// Bytes that are not a version vector are an error. A count of entries the
// rest of the bytes cannot hold is refused before anything is allocated for
// it.
func DecodeVersionVector(b []byte) (VersionVector, error) {
	return decodeVersionVector(b, true)
}

// decodeVersionVector is DecodeVersionVector; without presize the map grows
// as entries go in, which a test uses to show that sizing it matters.
func decodeVersionVector(b []byte, presize bool) (VersionVector, error) {
	r := postcard.NewReader(b)
	n, err := readU32(r)
	if err != nil {
		return VersionVector{}, fmt.Errorf("loro: version vector: %w", err)
	}
	// Each entry takes at least two bytes, a peer and a counter.
	if uint64(n) > uint64(r.Remaining())/2 {
		return VersionVector{}, fmt.Errorf("loro: version vector of %d entries in %d bytes", n, r.Remaining())
	}
	var t fxTable
	if presize {
		t = fxWithCapacity(min(int(n), maxDecodeCapacity))
	}
	for i := uint32(0); i < n; i++ {
		peer, err := r.Uvarint()
		if err != nil {
			return VersionVector{}, fmt.Errorf("loro: version vector entry %d: %w", i, err)
		}
		ctr, err := readI32(r)
		if err != nil {
			return VersionVector{}, fmt.Errorf("loro: version vector entry %d: %w", i, err)
		}
		t.insertAlways(ID{Peer: peer, Counter: ctr})
	}
	return VersionVector{t: t}, nil
}

// EncodeFrontiers writes frontiers as loro-crdt's encodeFrontiers does
// (version/frontiers.rs:219): the ids, one per peer with the highest counter
// kept, sorted by peer, as a postcard list. A counter outside loro's 32-bit
// range is an error.
func EncodeFrontiers(ids []ID) ([]byte, error) {
	for _, id := range ids {
		if id.Counter < math.MinInt32 || id.Counter > math.MaxInt32 {
			return nil, fmt.Errorf("loro: frontier %d@%d has a counter outside loro's 32-bit range", id.Counter, id.Peer)
		}
	}
	f := frontiersOf(ids)
	list := f.ids()
	slices.SortFunc(list, func(a, b ID) int { return cmp.Or(cmp.Compare(a.Peer, b.Peer), cmp.Compare(a.Counter, b.Counter)) })
	out := postcard.AppendUvarint(nil, uint64(len(list)))
	for _, id := range list {
		out = postcard.AppendUvarint(out, id.Peer)
		out = appendI32(out, id.Counter)
	}
	return out, nil
}

// DecodeFrontiers reads frontiers written by EncodeFrontiers or by
// loro-crdt, and returns them as loro-crdt's decodeFrontiers does: one id
// per peer, the highest counter kept, in the order of loro's hash map
// (version/frontiers.rs:226). Bytes after the list are ignored. Bytes that
// are not a list of ids are an error.
func DecodeFrontiers(b []byte) ([]ID, error) {
	ids, err := decodeIDList(b)
	if err != nil {
		return nil, err
	}
	f := frontiersOf(ids)
	return f.ids(), nil
}

// decodeIDList reads the postcard list of ids frontiers are encoded as, in
// the order written.
func decodeIDList(b []byte) ([]ID, error) {
	r := postcard.NewReader(b)
	n, err := readU32(r)
	if err != nil {
		return nil, fmt.Errorf("loro: frontiers: %w", err)
	}
	if uint64(n) > uint64(r.Remaining())/2 {
		return nil, fmt.Errorf("loro: frontiers of %d ids in %d bytes", n, r.Remaining())
	}
	ids := make([]ID, 0, n)
	for i := uint32(0); i < n; i++ {
		peer, err := r.Uvarint()
		if err != nil {
			return nil, fmt.Errorf("loro: frontiers id %d: %w", i, err)
		}
		ctr, err := readI32(r)
		if err != nil {
			return nil, fmt.Errorf("loro: frontiers id %d: %w", i, err)
		}
		ids = append(ids, ID{Peer: peer, Counter: ctr})
	}
	return ids, nil
}

// readU32 reads a postcard varint of a 32-bit integer, as postcard reads a
// u32, an i32 before its zigzag, and a usize on wasm32: at most five bytes,
// the fifth holding at most four bits. A longer form is an error, a padded
// shorter one is not.
func readU32(r *postcard.Reader) (uint32, error) {
	var x uint32
	for i := 0; i < 5; i++ {
		c, err := r.Byte()
		if err != nil {
			return 0, err
		}
		x |= uint32(c&0x7f) << (7 * i)
		if c&0x80 == 0 {
			if i == 4 && c > 0x0f {
				return 0, postcard.ErrOverflow
			}
			return x, nil
		}
	}
	return 0, postcard.ErrOverflow
}

// readI32 reads a postcard i32: a zigzag varint of 32 bits.
func readI32(r *postcard.Reader) (int64, error) {
	u, err := readU32(r)
	if err != nil {
		return 0, err
	}
	return int64(int32(u>>1) ^ -int32(u&1)), nil
}

// appendI32 appends c, within the int32 range, as a postcard i32.
func appendI32(dst []byte, c int64) []byte {
	x := int32(c)
	return postcard.AppendUvarint(dst, uint64(uint32(x<<1)^uint32(x>>31)))
}

// fxWithCapacity is HashMap::with_capacity: a table of as many buckets as
// capacity entries need, or none for 0.
func fxWithCapacity(capacity int) fxTable {
	if capacity <= 0 {
		return fxTable{}
	}
	n := bucketsFor(capacity)
	t := fxTable{ctrl: make([]byte, n+fxGroup), keys: make([]uint64, n), vals: make([]int64, n)}
	for i := range t.ctrl {
		t.ctrl[i] = fxEmpty
	}
	t.growthLeft = capacityOf(n - 1)
	return t
}

// insertAlways is HashMap::insert, which loro's VersionVector uses to set a
// peer's end (set_last for a local op, set_end, decoding). Unlike the entry
// API it reserves room for one more entry before it looks the key up
// (hashbrown 0.17.1, the version Rust 1.98.1's standard library builds on:
// map.rs:1806 and raw.rs:1126 find_or_find_insert_index), so setting a peer
// a full table already holds grows the table all the same.
func (t *fxTable) insertAlways(id ID) {
	if t.growthLeft < 1 {
		t.reserveRehash()
	}
	if b := t.find(id.Peer); b >= 0 {
		t.vals[b] = id.Counter
		return
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

// removeKey is HashMap::remove: peer's entry goes, whatever its counter.
func (t *fxTable) removeKey(peer uint64) {
	if b := t.find(peer); b >= 0 {
		t.remove(ID{Peer: peer, Counter: t.vals[b]})
	}
}

// Version returns the document's version vector, loro-crdt's
// oplogVersion(): for each peer, the counter just past its last op the
// document holds. Changes waiting for a dependency are not in it. Ops not
// yet committed are, as loro counts each local op as it is made
// (loro_dag.rs:906 update_version_on_new_local_op); Version does not commit
// them. Its entries are in the order loro's hash map holds them, so Encode
// writes what loro-crdt's oplogVersion().encode() writes for the same
// history, imported and edited in the same order.
func (d *Document) Version() VersionVector {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.noteLocalOps()
	if d.knocked(ruleVersionOrder) {
		var v VersionVector
		peers := slices.Sorted(maps.Keys(d.log.vv))
		for _, p := range peers {
			v.t.insertAlways(ID{Peer: p, Counter: d.log.vv[p]})
		}
		return v
	}
	return VersionVector{t: *d.log.vvTab.clone()}
}

// Frontiers returns the ids of the document's latest ops, loro-crdt's
// oplogFrontiers(): the last op of each history the document holds that no
// other op it holds depends on, ops not yet committed included. They are in
// the order of loro's hash map, as oplogFrontiers lists them.
func (d *Document) Frontiers() []ID {
	d.mu.Lock()
	defer d.mu.Unlock()
	// A local op makes the frontiers that op alone: the transaction's deps
	// are the frontiers it started from (txn.rs:767).
	if t := d.tx; t != nil && t.nextCounter > t.startCounter {
		return []ID{{Peer: d.peer, Counter: t.nextCounter - 1}}
	}
	ids := d.log.fr.ids()
	if d.knocked(ruleFrontiersOrder) {
		slices.SortFunc(ids, func(a, b ID) int { return cmp.Compare(a.Peer, b.Peer) })
	}
	return ids
}

// noteLocalOps puts the local ops made since it last ran into the version
// vector's hash map the way loro does as each op is made: set_last, a
// HashMap::insert that reserves room even for a peer the map holds, so the
// second op of a peer that has just filled the map grows it. After two such
// inserts the map has room, and more change nothing.
func (d *Document) noteLocalOps() {
	st := d.st
	n := st.localOps - st.localOpsNoted
	st.localOpsNoted = st.localOps
	if n == 0 {
		return
	}
	end := d.log.vv[d.peer]
	if t := d.tx; t != nil {
		end = t.nextCounter
	}
	if d.knocked(ruleLocalReserve) {
		d.log.vvTab.insertMax(ID{Peer: d.peer, Counter: end})
		return
	}
	for i := 0; i < min(n, 2); i++ {
		d.log.vvTab.insertAlways(ID{Peer: d.peer, Counter: end})
	}
}

// takeSnapshotVersion is what loro does with the version vector and the
// frontiers stored in a snapshot that replaces an empty document's state:
// it takes them as they are decoded (loro_dag.rs:637
// set_version_by_fast_snapshot_import) instead of building them change by
// change, so their hash maps hold the peers in the order decoding put them
// in: the frontiers' ids inserted in the order they are written. fr is that
// list. Where they do not name what the snapshot's changes made, which only
// a crafted snapshot does, the ones the changes made stay.
func (d *Document) takeSnapshotVersion(vv VersionVector, fr []ID) {
	if d.knocked(ruleSnapshotVersion) {
		return
	}
	l := d.log
	if maps.Equal(vv.Map(), l.vv) {
		l.vvTab = vv.t
	}
	if f := frontiersOf(fr); f.len() == l.fr.len() && l.fr.equal(f.ids()) {
		l.fr = f
	}
}

// The rules of versions and exports from a version that a test can switch
// off with Document.knock, to show that the fixture written for each fails
// without it. They are numbered apart from the Document's own (document.go),
// so that either list can grow.
const (
	ruleExportCut       rule = 48 + iota // a change from holds a part of is exported from where from ends
	ruleCutDeps                          // that part depends on the op before it
	ruleCutLamport                       // and starts as many lamports later as ops were cut
	ruleExportRegroup                    // the changes exported are grouped anew in a fresh store
	ruleVersionOrder                     // Version keeps the order of loro's hash map
	ruleLocalReserve                     // a local op's insert into it reserves room
	ruleSnapshotVersion                  // a snapshot replacing the state brings its version and frontiers
	ruleUnstableSort                     // a blob's changes are ordered as Rust's unstable sort orders them
	ruleDecodeFold                       // ops that continue each other fold as a block is read
	ruleFrontiersOrder                   // Frontiers keeps the order of loro's hash map
	ruleRangeLayers                      // the k-th range of each peer goes into the k-th store
	ruleCheckUnfolded                    // a blob's ops are checked as written, before they fold
)
