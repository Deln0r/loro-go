package loro

import (
	"fmt"
	"maps"
	"math"
	"slices"
	"unicode/utf8"

	"github.com/Deln0r/loro-go/encoding/change"
)

// storeBlockLimit is loro's MAX_BLOCK_SIZE (oplog/change_store.rs:42): the
// estimated size past which a change block of one peer takes no new change.
const storeBlockLimit = 4096

// provKind says where an op's value lives in loro's memory, which decides
// whether loro can fold the next op into it (review item 2). loro folds two
// text inserts only when their bytes are adjacent in one buffer, and a list
// insert only when its values can grow without copying a buffer another op
// shares (op.rs ValueSlice::is_mergable). Neither shows in the op itself.
type provKind uint8

const (
	// provNone: a value loro folds nothing into: a list insert read from
	// a blob whose items share a buffer with other inserts, and any op that
	// is not an insert (a delete folds by its span alone).
	provNone provKind = iota
	// provText: a text insert, a byte range of one buffer: the document's
	// string arena for a local insert (arena.rs:671 alloc_str_with_slice),
	// the buffer of the block it was read from for an imported one.
	provText
	// provList: a list insert whose values can grow: a local one, which
	// loro keeps in a buffer of its own (op.rs ValueSlice::new), or an
	// imported one of a single item, which it keeps inline.
	provList
)

// opProv is where one op's value lives: for a local text insert, the arena
// buffer generation and the byte range in it.
type opProv struct {
	kind       provKind
	gen        int
	start, end int
}

// strArena models the byte buffer loro appends local text inserts to
// (append-only-bytes, behind arena/str_arena.rs). When an insert does not fit,
// the buffer is replaced by one of the next power of two at least 32 bytes
// long, and slices of the old buffer can no longer be joined with new ones.
// Measured on loro-crdt 1.17.1: typing 3000 one-byte characters with a commit
// after each gives one change whose inserts break at bytes 32, 64, 128, 256,
// 512, 1024 and 2048; a 1000-byte insert after 10 bytes opens a 1024-byte
// buffer, and a 5000-byte one an 8192-byte buffer.
type strArena struct {
	len, cap, gen int
}

func (a *strArena) alloc(n int) opProv {
	if a.len+n > a.cap {
		a.gen++
		a.cap = 32
		for a.cap < a.len+n {
			a.cap *= 2
		}
	}
	p := opProv{kind: provText, gen: a.gen, start: a.len, end: a.len + n}
	a.len += n
	return p
}

// storedChange is a change as a Document keeps it, with where each op's
// value lives.
type storedChange struct {
	ch   Change
	prov []opProv // one per op of ch.Ops
}

func (c *storedChange) start() int64 { return c.ch.ID.Counter }

func (c *storedChange) end() int64 {
	end := c.ch.ID.Counter
	for _, op := range c.ch.Ops {
		end += op.Len
	}
	return end
}

func (c *storedChange) lastID() ID { return ID{Peer: c.ch.ID.Peer, Counter: c.end() - 1} }

// clone copies the change deep enough that merging ops into the copy leaves
// the original alone.
func (c *storedChange) clone() *storedChange {
	out := *c
	out.ch.Deps = slices.Clone(c.ch.Deps)
	out.ch.Ops = slices.Clone(c.ch.Ops)
	out.prov = slices.Clone(c.prov)
	return &out
}

// setLamport moves the change, and every op in it, to start at lamport l.
func (c *storedChange) setLamport(l int64) {
	c.ch.Lamport = l
	for i := range c.ch.Ops {
		c.ch.Ops[i].Lamport = l + (c.ch.Ops[i].Counter - c.ch.ID.Counter)
	}
}

// slice is loro's Change::slice (change.rs:209): the atoms [from, to) of the
// change. A slice that starts past the change's first atom depends only on
// the atom before it, of its own peer, and its lamport moves with it.
func (c *storedChange) slice(from, to int64) *storedChange {
	out := &storedChange{}
	out.ch = Change{
		ID:        ID{Peer: c.ch.ID.Peer, Counter: c.ch.ID.Counter + from},
		Lamport:   c.ch.Lamport + from,
		Timestamp: c.ch.Timestamp,
		Deps:      slices.Clone(c.ch.Deps),
		CommitMsg: c.ch.CommitMsg,
	}
	if from > 0 {
		out.ch.Deps = []ID{{Peer: c.ch.ID.Peer, Counter: c.ch.ID.Counter + from - 1}}
	}
	lo, hi := c.ch.ID.Counter+from, c.ch.ID.Counter+to
	for i, op := range c.ch.Ops {
		span := idRange{start: op.Counter, end: op.Counter + op.Len}
		keep := idRange{start: max(span.start, lo), end: min(span.end, hi)}
		if keep.start >= keep.end {
			continue
		}
		sliced, _ := sliceOp(op, c.prov[i], span, keep)
		out.ch.Ops = append(out.ch.Ops, sliced.op)
		out.prov = append(out.prov, sliced.prov)
	}
	return out
}

type provOp struct {
	op   Op
	prov opProv
}

// sliceOp narrows op, occupying span, to keep, carrying its provenance: a
// text slice keeps the bytes of the kept characters. clipOp in merge.go does
// the op; ops of one atom are never cut.
func sliceOp(op Op, p opProv, span, keep idRange) (provOp, bool) {
	clipped, ok := clipOp(op, span, keep)
	if !ok {
		return provOp{op, p}, false
	}
	if p.kind == provText {
		if s, isStr := op.Value.(string); isStr {
			p.start += runeBytes(s, keep.start-span.start)
			p.end = p.start + len(clipped.Value.(string))
		}
	}
	return provOp{clipped, p}, true
}

// runeBytes is the byte length of the first n runes of s.
func runeBytes(s string, n int64) int {
	b := 0
	for ; n > 0 && b < len(s); n-- {
		_, size := utf8.DecodeRuneInString(s[b:])
		b += size
	}
	return b
}

// storedOpSize is loro's estimate of an op's stored size
// (op/content.rs:71 and container/list/list_op.rs:133): a text insert
// counts its UTF-8 bytes, a list insert four bytes per element.
func storedOpSize(op Op) int {
	switch op.Kind {
	case change.CMap:
		return 3
	case change.CTree:
		return 8
	case change.CCounter:
		return 4
	case change.CText, change.CList, change.CMovableList:
		switch op.VKind {
		case change.VKStr:
			s, _ := op.Value.(string)
			return len(s)
		case change.VKLoroValue:
			return 4 * int(op.Len)
		case change.VKDeleteSeq, change.VKListMove:
			return 8
		case change.VKListSet:
			return 7
		case change.VKMarkStart:
			return 10
		default:
			return 1
		}
	default:
		return 6
	}
}

// storedChangeSize is loro's Change::estimate_storage_size (change.rs:121).
func storedChangeSize(ch *Change) int {
	n := 2 + 1 + 1 + (max(len(ch.Deps), 1)-1)*4
	for _, op := range ch.Ops {
		n += storedOpSize(op)
	}
	return n
}

// storeBlock is one in-memory change block of loro's change store: changes
// of one peer with consecutive counters, and loro's running estimate of its
// size.
type storeBlock struct {
	changes []*storedChange
	size    int
	end     int64
}

// changeStore is loro's ChangeStore as far as it decides how changes and ops
// are grouped (oplog/change_store.rs): each peer's changes in blocks, a new
// change merged into the last change of its peer's last block when loro
// would, or appended to that block while it has room.
type changeStore struct {
	peers map[uint64][]*storeBlock
}

func newChangeStore() *changeStore { return &changeStore{peers: map[uint64][]*storeBlock{}} }

// insert is insert_change_inner (change_store.rs:1344). A change estimated
// above the block limit is split first when split is set, as the document's
// own store does and an export's store does not. interval is the merge
// interval in seconds: the configured one for a local change, 0 otherwise.
func (s *changeStore) insert(c *storedChange, split bool, interval int64, d *Document) {
	size := storedChangeSize(&c.ch)
	if size > storeBlockLimit && split {
		s.splitThenInsert(c, d)
		return
	}
	peer := c.ch.ID.Peer
	blocks := s.peers[peer]
	if n := len(blocks); n > 0 && blocks[n-1].end == c.start() {
		if blocks[n-1].push(c, size, interval, d) {
			return
		}
	}
	s.peers[peer] = append(blocks, &storeBlock{changes: []*storedChange{c}, size: size, end: c.end()})
}

// push is ChangesBlock::push_change (change_store.rs:2143): the change is
// merged into the block's last change when Change::can_merge_right allows it
// and the block has room, or when the block is full but the change is a
// single op that folds into the last op; otherwise it is appended while the
// block has room. A merged change keeps the first change's timestamp and
// message, and only ops that did not fold add to the estimate.
func (b *storeBlock) push(c *storedChange, size int, interval int64, d *Document) bool {
	full := size+b.size > storeBlockLimit && !d.knocked(ruleBlockLimit)
	last := b.changes[len(b.changes)-1]
	if canMergeRight(last, c, interval, d) &&
		(!full || (len(c.ch.Ops) == 1 && canFold(lastOp(last), c.ch.Ops[0], c.prov[0], d))) {
		for i, op := range c.ch.Ops {
			if !last.pushOp(op, c.prov[i], d) {
				b.size += storedOpSize(op)
			}
		}
	} else {
		if full {
			return false
		}
		b.size += size
		b.changes = append(b.changes, c)
	}
	b.end = c.end()
	return true
}

func lastOp(c *storedChange) provOp {
	i := len(c.ch.Ops) - 1
	return provOp{c.ch.Ops[i], c.prov[i]}
}

// pushOp appends op to the change, folding it into the last op when loro's
// RleVec push would (rle crate, RleVec::push), and reports whether it folded.
func (c *storedChange) pushOp(op Op, p opProv, d *Document) bool {
	if n := len(c.ch.Ops); n > 0 {
		if canFold(provOp{c.ch.Ops[n-1], c.prov[n-1]}, op, p, d) {
			c.ch.Ops[n-1], c.prov[n-1] = foldOps(provOp{c.ch.Ops[n-1], c.prov[n-1]}, provOp{op, p})
			return true
		}
	}
	c.ch.Ops = append(c.ch.Ops, op)
	c.prov = append(c.prov, p)
	return false
}

// canMergeRight is Change::can_merge_right (change.rs:268): b continues a's
// peer right after it, depends on nothing but a's last op, comes within the
// merge interval of a's timestamp and carries the same message.
func canMergeRight(a, b *storedChange, interval int64, d *Document) bool {
	if d.knocked(ruleMergeInterval) {
		interval = 1 << 62
	}
	return b.ch.ID.Peer == a.ch.ID.Peer &&
		b.ch.ID.Counter == a.end() &&
		len(b.ch.Deps) == 1 && b.ch.Deps[0].Peer == a.ch.ID.Peer &&
		b.ch.Timestamp-a.ch.Timestamp <= interval &&
		a.ch.CommitMsg == b.ch.CommitMsg
}

// splitThenInsert is split_change_then_insert (change_store.rs:1634): a
// change too big for one block goes in as pieces that each fit, an op cut
// where the room runs out. The pieces go in as non-local changes.
func (s *changeStore) splitThenInsert(c *storedChange, d *Document) {
	piece := &storedChange{ch: Change{ID: c.ch.ID, Lamport: c.ch.Lamport, Timestamp: c.ch.Timestamp, Deps: c.ch.Deps, CommitMsg: c.ch.CommitMsg}}
	size := storedChangeSize(&piece.ch)
	flush := func() {
		if len(piece.ch.Ops) == 0 {
			return
		}
		end := piece.end()
		next := &storedChange{
			ch: Change{
				ID:        ID{Peer: piece.ch.ID.Peer, Counter: end},
				Lamport:   piece.ch.Lamport + (end - piece.ch.ID.Counter),
				Timestamp: piece.ch.Timestamp,
				Deps:      []ID{{Peer: piece.ch.ID.Peer, Counter: end - 1}},
				CommitMsg: piece.ch.CommitMsg,
			},
		}
		s.insert(piece, false, 0, d)
		piece = next
		size = storedChangeSize(&piece.ch)
	}
	for i, op := range c.ch.Ops {
		cur := provOp{op, c.prov[i]}
		if storedOpSize(op) >= storeBlockLimit-size {
			flush()
		}
		for {
			fit, ok := fitsIn(cur.op, storeBlockLimit-size)
			if !ok {
				break
			}
			span := idRange{start: cur.op.Counter, end: cur.op.Counter + cur.op.Len}
			head, _ := sliceOp(cur.op, cur.prov, span, idRange{start: span.start, end: span.start + fit})
			piece.pushOp(head.op, head.prov, d)
			flush()
			if fit >= cur.op.Len {
				cur.op.Len = 0
				break
			}
			cur, _ = sliceOp(cur.op, cur.prov, span, idRange{start: span.start + fit, end: span.end})
		}
		if cur.op.Len == 0 {
			continue
		}
		size += storedOpSize(cur.op)
		if size > storeBlockLimit && len(piece.ch.Ops) > 0 {
			flush()
		}
		piece.pushOp(cur.op, cur.prov, d)
	}
	if len(piece.ch.Ops) > 0 {
		s.insert(piece, false, 0, d)
	}
}

// fitsIn is Op::check_whether_slice_content_to_fit_in_size (op.rs:108): how
// many atoms of op fit in room bytes, when the whole op does not. Only a
// sequence insert can be cut.
func fitsIn(op Op, room int) (int64, bool) {
	if storedOpSize(op) <= room {
		return 0, false
	}
	n := int64(room)
	if op.VKind == change.VKLoroValue && op.Kind != change.CText {
		n = int64(room / 4)
	}
	if op.VKind != change.VKStr && op.VKind != change.VKLoroValue {
		return 0, false
	}
	return min(n, op.Len), true
}

// changes returns the store's changes, peer by peer in ascending order and
// each peer's in counter order.
func (s *changeStore) changes() []*storedChange {
	var out []*storedChange
	for _, p := range s.sortedPeers() {
		for _, b := range s.peers[p] {
			out = append(out, b.changes...)
		}
	}
	return out
}

func (s *changeStore) sortedPeers() []uint64 {
	peers := make([]uint64, 0, len(s.peers))
	for p := range s.peers {
		peers = append(peers, p)
	}
	slices.Sort(peers)
	return peers
}

// find returns the change holding id, or nil.
func (s *changeStore) find(id ID) *storedChange {
	blocks := s.peers[id.Peer]
	i, _ := slices.BinarySearchFunc(blocks, id.Counter, func(b *storeBlock, c int64) int {
		if b.end <= c {
			return -1
		}
		return 1
	})
	if i == len(blocks) {
		return nil
	}
	chs := blocks[i].changes
	j, _ := slices.BinarySearchFunc(chs, id.Counter, func(ch *storedChange, c int64) int {
		if ch.end() <= c {
			return -1
		}
		return 1
	})
	if j == len(chs) || chs[j].start() > id.Counter {
		return nil
	}
	return chs[j]
}

// opLog is a Document's history: the changes applied, in loro's change
// store, with the version and frontiers they make, and the changes received
// whose dependencies have not arrived (oplog.rs, oplog/pending_changes.rs).
type opLog struct {
	store *changeStore
	vv    map[uint64]int64 // per peer, the end of the counters applied
	fr    frontiers
	// pending holds changes waiting for an op, filed under the peer and
	// counter of the op they wait for, as loro files them.
	pending map[uint64]map[int64][]*storedChange
	arena   strArena
	// vvTab is vv as loro's hash map holds it, which decides the order
	// Version encodes its peers in (version.go).
	vvTab fxTable
}

func newOpLog() *opLog {
	return &opLog{store: newChangeStore(), vv: map[uint64]int64{}, pending: map[uint64]map[int64][]*storedChange{}}
}

func (l *opLog) empty() bool { return len(l.vv) == 0 && len(l.pending) == 0 }

// clone copies the log deep enough that importing into the copy, which
// merges ops into the last change of a block and moves pending changes'
// lamports, leaves the original as it was.
func (l *opLog) clone() *opLog {
	out := &opLog{
		store:   newChangeStore(),
		vv:      maps.Clone(l.vv),
		fr:      l.fr,
		pending: make(map[uint64]map[int64][]*storedChange, len(l.pending)),
		arena:   l.arena,
		vvTab:   *l.vvTab.clone(),
	}
	if l.fr.tab != nil {
		out.fr.tab = l.fr.tab.clone()
	}
	for p, blocks := range l.store.peers {
		cp := make([]*storeBlock, len(blocks))
		for i, b := range blocks {
			nb := &storeBlock{size: b.size, end: b.end, changes: make([]*storedChange, len(b.changes))}
			for j, c := range b.changes {
				nb.changes[j] = c.clone()
			}
			cp[i] = nb
		}
		out.store.peers[p] = cp
	}
	for p, byCounter := range l.pending {
		cp := make(map[int64][]*storedChange, len(byCounter))
		for ctr, set := range byCounter {
			cs := make([]*storedChange, len(set))
			for i, c := range set {
				cs[i] = c.clone()
			}
			cp[ctr] = cs
		}
		out.pending[p] = cp
	}
	return out
}

func (l *opLog) has(id ID) bool { return id.Counter < l.vv[id.Peer] }

// lamportOf is the lamport of an applied op.
func (l *opLog) lamportOf(id ID) (int64, bool) {
	c := l.store.find(id)
	if c == nil {
		return 0, false
	}
	return c.ch.Lamport + (id.Counter - c.ch.ID.Counter), true
}

// nextLamport is loro's frontiers_to_next_lamport (oplog/loro_dag.rs:1381):
// one past the highest lamport among the frontiers.
func (l *opLog) nextLamport(frontiers []ID) int64 {
	var next int64
	for _, f := range frontiers {
		if lam, ok := l.lamportOf(f); ok {
			next = max(next, lam+1)
		}
	}
	return next
}

// greatestTimestamp is get_greatest_timestamp (oplog.rs:1568): the latest
// timestamp of the changes holding the frontiers. A merged change keeps the
// timestamp of the first change merged into it.
func (l *opLog) greatestTimestamp(frontiers []ID) int64 {
	var ts int64
	for _, f := range frontiers {
		if c := l.store.find(f); c != nil {
			ts = max(ts, c.ch.Timestamp)
		}
	}
	return ts
}

// lamportFromDeps is try_get_change_lamport_from_deps: one past the highest
// lamport among deps, false when one of them is not applied.
func (l *opLog) lamportFromDeps(deps []ID) (int64, bool) {
	var lam int64
	for _, d := range deps {
		dl, ok := l.lamportOf(d)
		if !ok || !l.has(d) {
			return 0, false
		}
		lam = max(lam, dl+1)
	}
	return lam, true
}

// updateFrontiers moves the frontiers past a change whose last op is last
// and whose deps, in their iteration order, are deps.
func (l *opLog) updateFrontiers(last ID, deps []ID) {
	l.fr.updateOnNewChange(last, deps)
}

// insertLocal adds a committed local change.
func (l *opLog) insertLocal(c *storedChange, interval int64, d *Document) {
	l.vv[c.ch.ID.Peer] = c.end()
	l.fr = frontiers{n: 1, one: c.lastID()}
	l.store.insert(c, true, interval, d)
	d.noteLocalOps()
}

// insertRemote adds an imported change whose deps are all applied. A change
// that depends on nothing but its own peer's op before it continues that
// op's lamport; loro asserts it does ("lamport is not continuous",
// oplog/loro_dag.rs:341), so a blob whose change claims a later lamport for
// itself, which only the pending path lets through, is refused. So is one
// whose lamports pass loro's u32.
func (l *opLog) insertRemote(c *storedChange, d *Document) error {
	if deps := c.ch.Deps; len(deps) == 1 && deps[0].Peer == c.ch.ID.Peer {
		if prev, ok := l.lamportOf(deps[0]); ok && c.ch.Lamport != prev+1 {
			return fmt.Errorf("loro: change %d@%d continues its peer at lamport %d, not %d", c.start(), c.ch.ID.Peer, c.ch.Lamport, prev+1)
		}
	}
	if c.ch.Lamport+(c.end()-c.start()) > math.MaxUint32 {
		return fmt.Errorf("loro: change %d@%d ends past lamport %d", c.start(), c.ch.ID.Peer, uint32(math.MaxUint32))
	}
	l.updateFrontiers(c.lastID(), c.ch.Deps)
	l.vv[c.ch.ID.Peer] = c.end()
	// extend_to_include_last_id (version.rs:851): a peer new to the map
	// goes in, one it holds takes the higher end.
	l.vvTab.insertMax(ID{Peer: c.ch.ID.Peer, Counter: c.end()})
	l.store.insert(c, true, 0, d)
	return nil
}

// trimKnown is trim_the_known_part_of_change (oplog.rs:596): the part of c
// not applied yet, or nil.
func (l *opLog) trimKnown(c *storedChange, d *Document) *storedChange {
	end, ok := l.vv[c.ch.ID.Peer]
	if !ok || c.start() >= end {
		return c
	}
	if c.end() <= end {
		return nil
	}
	if d.knocked(rulePendingTrim) {
		return c
	}
	return c.slice(end-c.start(), c.end()-c.start())
}

// importChanges applies decoded changes the way loro's import does
// (encoding.rs:274 apply_decoded_changes_to_oplog): sorted by lamport, a
// change whose ops are all applied is skipped, one whose deps are applied
// goes in with its lamport recomputed and its known prefix trimmed, and the
// rest wait. It reports whether anything was applied. On an error the log
// is half changed, and the caller restores it.
func (l *opLog) importChanges(changes []*storedChange, d *Document) (bool, error) {
	before := l.appliedAtoms()
	sortByLamport(changes, d)
	var latest []ID
	var waiting []*storedChange
	for _, c := range changes {
		if c.end() <= l.vv[c.ch.ID.Peer] {
			continue
		}
		latest = append(latest, c.lastID())
		lam, ok := l.lamportFromDeps(c.ch.Deps)
		if !ok {
			waiting = append(waiting, c)
			continue
		}
		c.setLamport(lam)
		if c = l.trimKnown(c, d); c != nil {
			if err := l.insertRemote(c, d); err != nil {
				return false, err
			}
		}
	}
	if err := l.applyPending(latest, d); err != nil {
		return false, err
	}
	if err := l.parkOrApply(waiting, d); err != nil {
		return false, err
	}
	return l.appliedAtoms() != before, nil
}

func (l *opLog) appliedAtoms() int64 {
	var n int64
	for _, end := range l.vv {
		n += end
	}
	return n
}

// waitsFor is remote_change_apply_state (pending_changes.rs:423): whether c
// is applied already, can be applied, or waits for an op, and which: the op
// just before it on its own peer first, then each dep in order.
func (l *opLog) waitsFor(c *storedChange) (applied bool, missing ID, waits bool) {
	peer := c.ch.ID.Peer
	have := l.vv[peer]
	if have >= c.end() {
		return true, ID{}, false
	}
	if have < c.start() {
		return false, ID{Peer: peer, Counter: c.start() - 1}, true
	}
	for _, dep := range c.ch.Deps {
		if l.vv[dep.Peer]-1 < dep.Counter {
			return false, dep, true
		}
	}
	return false, ID{}, false
}

func (l *opLog) park(missing ID, c *storedChange) {
	byCounter := l.pending[missing.Peer]
	if byCounter == nil {
		byCounter = map[int64][]*storedChange{}
		l.pending[missing.Peer] = byCounter
	}
	byCounter[missing.Counter] = append(byCounter[missing.Counter], c)
}

// parkOrApply is extend_pending_changes_with_unknown_lamport
// (pending_changes.rs:281): each change waits, is dropped as applied, or is
// applied, and what it applied may release others.
func (l *opLog) parkOrApply(changes []*storedChange, d *Document) error {
	var applied []ID
	for _, c := range changes {
		done, missing, waits := l.waitsFor(c)
		switch {
		case waits:
			l.park(missing, c)
		case done:
		default:
			applied = append(applied, c.lastID())
			if err := l.applyRemote(c, d); err != nil {
				return err
			}
		}
	}
	if len(applied) > 0 {
		return l.applyPending(applied, d)
	}
	return nil
}

// applyPending is try_apply_pending (pending_changes.rs:333): for each op
// just applied, last first, the changes filed under its peer at or below its
// counter are tried again, in counter order.
func (l *opLog) applyPending(ids []ID, d *Document) error {
	for len(ids) > 0 {
		id := ids[len(ids)-1]
		ids = ids[:len(ids)-1]
		byCounter := l.pending[id.Peer]
		if byCounter == nil {
			continue
		}
		var counters []int64
		for c := range byCounter {
			if c <= id.Counter {
				counters = append(counters, c)
			}
		}
		slices.Sort(counters)
		var sets [][]*storedChange
		for _, c := range counters {
			sets = append(sets, byCounter[c])
			delete(byCounter, c)
		}
		if len(byCounter) == 0 {
			delete(l.pending, id.Peer)
		}
		for _, set := range sets {
			for _, c := range set {
				done, missing, waits := l.waitsFor(c)
				switch {
				case waits:
					l.park(missing, c)
				case done:
				default:
					ids = append(ids, c.lastID())
					if err := l.applyRemote(c, d); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

// applyRemote is apply_change_from_remote (pending_changes.rs:375): the
// change's lamport is raised past its deps' (calc_unknown_lamport_change),
// its known prefix trimmed, and the rest applied.
func (l *opLog) applyRemote(c *storedChange, d *Document) error {
	lam := c.ch.Lamport
	for _, dep := range c.ch.Deps {
		if dl, ok := l.lamportOf(dep); ok {
			lam = max(lam, dl+1)
		}
	}
	c.setLamport(lam)
	if c = l.trimKnown(c, d); c != nil {
		return l.insertRemote(c, d)
	}
	return nil
}

// pendingLamportEnd is the highest lamport a pending change would end at. It
// is only read when rulePendingIsolated is knocked out.
func (l *opLog) pendingLamportEnd() int64 {
	var end int64
	for _, byCounter := range l.pending {
		for _, set := range byCounter {
			for _, c := range set {
				end = max(end, c.ch.Lamport+(c.end()-c.start()))
			}
		}
	}
	return end
}

// exportBlocks is what loro's export writes for the whole history
// (change_store.rs:1088 export_blocks_from): every change, peer by peer,
// inserted into a fresh store as a non-local change and never split. That
// store merges changes again under a merge interval of 0, so its changes and
// blocks are what the exported blob holds.
func (l *opLog) exportBlocks(d *Document) [][]*storedChange {
	fresh := newChangeStore()
	for _, c := range l.store.changes() {
		fresh.insert(c.clone(), false, 0, d)
	}
	var out [][]*storedChange
	for _, p := range fresh.sortedPeers() {
		for _, b := range fresh.peers[p] {
			out = append(out, b.changes)
		}
	}
	return out
}

// checkKnown is loro's check_and_trim_known_part_of_changes
// (oplog/known_history.rs:104), the part that can refuse an import. When a
// blob brings anything new, every op id in it the document already has
// must name the same op: the same dependencies and the same content.
// Another op under a known id means two writers shared a peer id, and loro
// refuses the blob ("Every op id should be unique"). Then no change may
// skip counters of its peer while its deps are all there. changes are in
// the order the import applies them.
func (l *opLog) checkKnown(changes []*storedChange) error {
	fresh := false
	for _, c := range changes {
		if c.end() > l.vv[c.ch.ID.Peer] {
			fresh = true
			break
		}
	}
	if fresh {
		for _, c := range changes {
			if known := l.vv[c.ch.ID.Peer]; c.start() < known {
				if err := l.checkKnownPart(c, known); err != nil {
					return err
				}
			}
		}
	}
	// check_changes_do_not_skip_counters: replay the import's first pass on
	// counter ends alone.
	ends := map[uint64]int64{}
	endOf := func(p uint64) int64 {
		if e, ok := ends[p]; ok {
			return e
		}
		return l.vv[p]
	}
	for _, c := range changes {
		peer := c.ch.ID.Peer
		end := endOf(peer)
		if c.end() <= end {
			continue
		}
		ready := true
		for _, dep := range c.ch.Deps {
			if dep.Counter >= endOf(dep.Peer) {
				ready = false
				break
			}
		}
		if !ready {
			continue
		}
		if c.start() > end {
			return fmt.Errorf("loro: change %d@%d has its deps but not %d@%d, so it skips counters of its peer",
				c.start(), peer, c.start()-1, peer)
		}
		ends[peer] = c.end()
	}
	return nil
}

// checkKnownPart is check_known_part_of_change: c's atoms below known
// against the stored ones, segment by segment of the stored changes.
func (l *opLog) checkKnownPart(c *storedChange, known int64) error {
	peer := c.ch.ID.Peer
	end := min(c.end(), known)
	for ctr := c.start(); ctr < end; {
		local := l.store.find(ID{Peer: peer, Counter: ctr})
		if local == nil {
			return nil
		}
		segEnd := min(end, local.end())
		if !sameIDs(depsAt(c, ctr), depsAt(local, ctr)) {
			return usedID(peer, ctr)
		}
		for ; ctr < segEnd; ctr++ {
			a, ok1 := atomAt(c, ctr)
			b, ok2 := atomAt(local, ctr)
			if !ok1 || !ok2 || !sameAtom(a, b) {
				return usedID(peer, ctr)
			}
		}
	}
	return nil
}

func usedID(peer uint64, ctr int64) error {
	return fmt.Errorf("loro: op id %d@%d is already used by another op; two writers share peer id %d", ctr, peer, peer)
}

// depsAt is what the atom at ctr depends on: the change's deps for its
// first atom, the atom before it for any other.
func depsAt(c *storedChange, ctr int64) []ID {
	if ctr == c.start() {
		return c.ch.Deps
	}
	return []ID{{Peer: c.ch.ID.Peer, Counter: ctr - 1}}
}

// opAtom is one atom of an op as loro compares it (known_history.rs:450
// op_eq): its container, kind and position, and what it writes: a
// character, a list item, a map key and value, a counter step, the id a
// delete removes, or the op's whole value for an op of one atom.
type opAtom struct {
	cont  contKey
	vk    change.ValueKind
	pos   int64
	from  int64
	key   string
	value any
}

func atomAt(c *storedChange, ctr int64) (opAtom, bool) {
	for _, op := range c.ch.Ops {
		if ctr < op.Counter || ctr >= op.Counter+op.Len {
			continue
		}
		k := ctr - op.Counter
		a := opAtom{cont: keyOf(op), vk: op.VKind, pos: op.Pos, from: op.MoveFrom, key: op.MapKey, value: op.Value}
		// Only an insert's value is one item per atom: a map write of a
		// string or a list is one atom whatever the value's length, and an
		// empty one would index nothing.
		switch v := op.Value.(type) {
		case string:
			if op.VKind == change.VKStr {
				a.value, a.pos = string([]rune(v)[k]), op.Pos+k
			}
		case []any:
			if isListInsert(op) {
				a.value, a.pos = v[k], op.Pos+k
			}
		case DeleteSpan:
			start, n := v.Normalize()
			if v.Len >= 0 {
				a.value = ID{Peer: v.Peer, Counter: start + k}
			} else {
				a.value, a.pos = ID{Peer: v.Peer, Counter: start + n - 1 - k}, op.Pos-k
			}
		}
		return a, true
	}
	return opAtom{}, false
}

func sameAtom(a, b opAtom) bool {
	if a.cont != b.cont || a.vk != b.vk || a.pos != b.pos || a.from != b.from || a.key != b.key {
		return false
	}
	switch x := a.value.(type) {
	case float64:
		// A counter step equals itself even as NaN (op_eq).
		y, ok := b.value.(float64)
		return ok && (x == y || (math.IsNaN(x) && math.IsNaN(y)))
	case MarkInfo:
		y, ok := b.value.(MarkInfo)
		return ok && x.Start == y.Start && x.Len == y.Len && x.Key == y.Key && x.Info == y.Info && valueEqual(x.Value, y.Value)
	case ListSetValue:
		y, ok := b.value.(ListSetValue)
		return ok && x.Elem == y.Elem && valueEqual(x.Value, y.Value)
	case ID, TreeNode, ListElem, change.ContainerType:
		return a.value == b.value
	}
	return valueEqual(a.value, b.value)
}
