package loro

import (
	"bytes"
	"cmp"
	"encoding/hex"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Deln0r/loro-go/encoding/change"
	"github.com/Deln0r/loro-go/encoding/fast"
)

// EncodeUpdates writes u as a loro-crdt FastUpdates blob; it is the inverse of
// DecodeUpdates. DecodeUpdates then EncodeUpdates gives back the bytes
// loro-crdt exported, with one exception: a map of two or more keys nested in
// a value. loro writes that map's keys in the order of its hash map, which a
// decoded Go map does not keep, so EncodeUpdates writes them sorted; the
// bytes differ and loro-crdt imports them to the same document.
//
// Every Change is written as one change, ops as given: EncodeUpdates neither
// merges nor splits changes or ops. It checks what loro's format assumes of
// them:
//
//   - a change has at least one op, and its ops follow one another: the first
//     starts at the change's ID, each next one where the one before it ends,
//     each carries the change's peer and the lamport its place in the change
//     gives it, and an insert or a delete is as long as what it inserts or
//     removes;
//   - a dependency on the author's own peer names the op just before the
//     change, because loro stores it as a flag, and there is at most one;
//   - counters, lamports and dependency counters fit loro's 32-bit fields.
//
// When u.Blocks is set, as DecodeUpdates sets it, the changes are written in
// those blocks and in that order, and each block must hold consecutive
// changes of one peer. When it is nil, they are grouped the way loro's export
// groups changes it does not merge (change_store.rs): sorted by peer and
// counter, one peer's consecutive changes share a block until its estimated
// size would pass loro's MAX_BLOCK_SIZE of 4096. Either way, a peer's changes
// must not overlap, and must come out in counter order. On every plain export in the fixtures that grouping is loro's
// own, long histories included; an export of id ranges is not grouped that
// way, and needs Blocks.
func EncodeUpdates(u *Updates) ([]byte, error) {
	return encodeUpdates(u, nil)
}

// encodeUpdates is EncodeUpdates with the key order of nested maps supplied:
// keyOrder, when not nil, is handed to every block's ValueWriter.
func encodeUpdates(u *Updates, keyOrder func(map[string]any) []string) ([]byte, error) {
	if u == nil {
		return nil, fmt.Errorf("loro: no updates to encode")
	}
	for _, ch := range u.Changes {
		if err := checkChange(ch); err != nil {
			return nil, err
		}
	}
	blocks, err := groupBlocks(u)
	if err != nil {
		return nil, err
	}
	if err := checkPeerOrder(blocks); err != nil {
		return nil, err
	}
	raw := make([][]byte, len(blocks))
	for i, b := range blocks {
		if raw[i], err = encodeBlock(b, keyOrder); err != nil {
			return nil, err
		}
	}
	return fast.Encode(fast.ModeFastUpdates, change.EncodeFastUpdates(raw)), nil
}

// maxBlockSize is loro's MAX_BLOCK_SIZE (change_store.rs). A block takes the
// next change of its peer unless the change's estimated size plus the block's
// would pass it.
const maxBlockSize = 4096

// groupBlocks splits u.Changes into the blocks to write, in order.
func groupBlocks(u *Updates) ([][]Change, error) {
	if u.Blocks != nil {
		var blocks [][]Change
		rest := u.Changes
		for _, n := range u.Blocks {
			if n < 1 || n > len(rest) {
				return nil, fmt.Errorf("loro: Blocks lists %d changes in a block where %d remain", n, len(rest))
			}
			blocks = append(blocks, rest[:n])
			rest = rest[n:]
		}
		if len(rest) > 0 {
			return nil, fmt.Errorf("loro: Blocks covers %d changes of %d", len(u.Changes)-len(rest), len(u.Changes))
		}
		for _, b := range blocks {
			for i := 1; i < len(b); i++ {
				if b[i].ID.Peer != b[0].ID.Peer || b[i].ID.Counter != changeEnd(b[i-1]) {
					return nil, fmt.Errorf("loro: change %d@%d does not follow change %d@%d in its block",
						b[i].ID.Counter, b[i].ID.Peer, b[i-1].ID.Counter, b[i-1].ID.Peer)
				}
			}
		}
		return blocks, nil
	}

	// loro's export inserts each peer's changes into a fresh store, which keys
	// its blocks by the id of their first change, so blocks come out sorted by
	// (peer, counter) whatever order the changes were found in.
	chs := slices.Clone(u.Changes)
	slices.SortStableFunc(chs, func(a, b Change) int {
		return cmp.Or(cmp.Compare(a.ID.Peer, b.ID.Peer), cmp.Compare(a.ID.Counter, b.ID.Counter))
	})
	var blocks [][]Change
	start, size := 0, 0
	for i, ch := range chs {
		est := estimateChange(ch)
		if i > 0 {
			prev := chs[i-1]
			// ChangesBlock::push_change: a change joins the block unless the
			// two sizes together pass the limit. A peer's next change after a
			// gap cannot join it at all.
			if ch.ID.Peer == prev.ID.Peer && ch.ID.Counter == changeEnd(prev) && size+est <= maxBlockSize {
				size += est
				continue
			}
			blocks = append(blocks, chs[start:i])
		}
		start, size = i, est
	}
	if len(chs) > 0 {
		blocks = append(blocks, chs[start:])
	}
	return blocks, nil
}

// checkPeerOrder requires every peer's changes to come out in counter order
// without overlapping, across blocks as within them. loro keeps the ids it
// imports first and drops what a later change repeats: loro-crdt 1.17.1
// showed "a" for two changes at 0@1 in separate blocks, "a" then "b", and
// "ab" for changes at 0@1 ("ab") and 1@1 ("c"), losing the "c". A later
// range written first it does import, holding it until the earlier one
// arrives, but its exports never do that, layers of id ranges included, so
// neither does EncodeUpdates.
func checkPeerOrder(blocks [][]Change) error {
	ends := map[uint64]Change{}
	for _, b := range blocks {
		for _, ch := range b {
			if prev, ok := ends[ch.ID.Peer]; ok && ch.ID.Counter < changeEnd(prev) {
				return fmt.Errorf("loro: change %d@%d overlaps or comes before change %d@%d, written ahead of it",
					ch.ID.Counter, ch.ID.Peer, prev.ID.Counter, prev.ID.Peer)
			}
			ends[ch.ID.Peer] = ch
		}
	}
	return nil
}

// changeEnd is the counter just past a change's last op.
func changeEnd(ch Change) int64 {
	end := ch.ID.Counter
	for _, op := range ch.Ops {
		end += op.Len
	}
	return end
}

// estimateChange is loro's Change::estimate_storage_size (change.rs): a few
// bytes for the id, lamport and timestamp, four for every dependency past the
// first, and each op's estimate.
func estimateChange(ch Change) int {
	n := 2 + 1 + 1 + (max(len(ch.Deps), 1)-1)*4
	for _, op := range ch.Ops {
		n += estimateOp(op)
	}
	return n
}

// estimateOp is loro's estimate of one op's stored size, by its content
// (InnerContent::estimate_storage_size in op/content.rs and
// InnerListOp::estimate_storage_size in list_op.rs). A text insert counts its
// UTF-8 bytes, a list insert four per element.
func estimateOp(op Op) int {
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
		default: // a mark's end
			return 1
		}
	default: // a container kind loro does not know is stored as an unknown op
		return 6
	}
}

// contID identifies a container the way loro's container arena does: a root
// container by kind and name, any other by kind and the id that created it.
type contID struct {
	root    bool
	kind    change.ContainerType
	name    string
	peer    uint64
	counter int64
}

// blockEncoder writes one change block. Its registers assign arena indexes in
// first-use order, as loro's ValueRegister does, and encodeBlock calls them in
// the order loro's encode_block does, which is what makes the arenas come out
// in loro's order.
type blockEncoder struct {
	peers   []uint64
	peerIdx map[uint64]int
	keys    []string
	keyIdx  map[string]int
	conts   []contID
	contIdx map[contID]int
	posIdx  map[string]int
	pos     [][]byte

	ops  change.Ops
	dels []change.DeleteID
	vw   change.ValueWriter
}

func (e *blockEncoder) peer(p uint64) int64 {
	if i, ok := e.peerIdx[p]; ok {
		return int64(i)
	}
	e.peerIdx[p] = len(e.peers)
	e.peers = append(e.peers, p)
	return int64(len(e.peers) - 1)
}

func (e *blockEncoder) key(k string) int64 {
	if i, ok := e.keyIdx[k]; ok {
		return int64(i)
	}
	e.keyIdx[k] = len(e.keys)
	e.keys = append(e.keys, k)
	return int64(len(e.keys) - 1)
}

func (e *blockEncoder) container(c contID) int64 {
	if i, ok := e.contIdx[c]; ok {
		return int64(i)
	}
	e.contIdx[c] = len(e.conts)
	e.conts = append(e.conts, c)
	return int64(len(e.conts) - 1)
}

// encodeBlock writes the changes of one block, all by one peer and
// consecutive, following encode_block in loro's block_encode.rs step by step:
//
//  1. the peer register starts with the block's peer;
//  2. the tree positions are registered first, sorted, because loro collects
//     them into a BTreeSet before it encodes any op;
//  3. op by op, the op's container is registered, then its prop (a map key
//     goes into the key register), then its value: a delete's target peer, a
//     mark's key before the keys of maps nested in its value, the peer of
//     the element a move or a set names, a tree op's node peer before its
//     parent's;
//  4. the container arena is built from the registered containers, adding
//     each root container's name to the keys and each other container's
//     peer to the peers;
//  5. the change header registers the peer of every dependency on another
//     peer, change by change.
func encodeBlock(chs []Change, keyOrder func(map[string]any) []string) ([]byte, error) {
	e := &blockEncoder{
		peerIdx: map[uint64]int{},
		keyIdx:  map[string]int{},
		contIdx: map[contID]int{},
		posIdx:  map[string]int{},
	}
	e.vw.Key = func(k string) uint64 { return uint64(e.key(k)) }
	e.vw.KeyOrder = keyOrder
	peer := chs[0].ID.Peer
	e.peer(peer)
	e.positions(chs)

	hdr := &change.ChangeHeader{}
	meta := &change.ChangeMeta{}
	for i, ch := range chs {
		for _, op := range ch.Ops {
			if err := e.op(op); err != nil {
				return nil, err
			}
		}
		if i < len(chs)-1 {
			hdr.AtomLens = append(hdr.AtomLens, uint64(changeEnd(ch)-ch.ID.Counter))
			hdr.Lamports = append(hdr.Lamports, ch.Lamport)
		}
		meta.Timestamps = append(meta.Timestamps, ch.Timestamp)
		meta.CommitMsgs = append(meta.CommitMsgs, ch.CommitMsg)
	}

	conts := make([]change.Container, len(e.conts))
	for i, c := range e.conts {
		if c.root {
			conts[i] = change.Container{IsRoot: true, Kind: c.kind, KeyOrCounter: e.key(c.name)}
		} else {
			conts[i] = change.Container{Kind: c.kind, PeerIdx: uint64(e.peer(c.peer)), KeyOrCounter: c.counter}
		}
	}

	for _, ch := range chs {
		self := false
		var others uint64
		for _, d := range ch.Deps {
			if d.Peer == peer { // checkChange made it the op just before ch
				self = true
				continue
			}
			hdr.DepPeerIdxs = append(hdr.DepPeerIdxs, uint64(e.peer(d.Peer)))
			hdr.DepCounters = append(hdr.DepCounters, d.Counter)
			others++
		}
		hdr.DepOnSelf = append(hdr.DepOnSelf, self)
		hdr.DepLens = append(hdr.DepLens, others)
	}
	hdr.Peers = e.peers

	first, last := chs[0], chs[len(chs)-1]
	end := changeEnd(last)
	lamportEnd := last.Lamport + (end - last.ID.Counter)
	if lamportEnd < first.Lamport {
		return nil, fmt.Errorf("loro: change %d@%d ends at lamport %d, before its block starts at %d",
			last.ID.Counter, last.ID.Peer, lamportEnd, first.Lamport)
	}
	blk := &change.Block{
		CounterStart: uint64(first.ID.Counter),
		CounterLen:   uint64(end - first.ID.Counter),
		LamportStart: uint64(first.Lamport),
		LamportLen:   uint64(lamportEnd - first.Lamport),
		NChanges:     uint64(len(chs)),
		Header:       change.EncodeHeader(hdr),
		ChangeMeta:   change.EncodeChangeMeta(meta),
		CIDs:         change.EncodeContainers(conts),
		Keys:         change.EncodeKeys(e.keys),
		Positions:    change.EncodePositions(e.pos),
		Ops:          change.EncodeOps(&e.ops),
		DeleteIDs:    change.EncodeDeleteIDs(e.dels),
		Values:       e.vw.Bytes(),
	}
	return change.EncodeBlock(blk), nil
}

// positions registers the fractional index of every tree create and move in
// the block, sorted and without duplicates, as loro collects them into a
// BTreeSet. A delete has none.
func (e *blockEncoder) positions(chs []Change) {
	for _, ch := range chs {
		for _, op := range ch.Ops {
			n, ok := op.Value.(TreeNode)
			if op.VKind != change.VKRawTreeMove || !ok || isTreeDelete(n) {
				continue
			}
			fi, _ := hex.DecodeString(n.FI) // checkOp read it
			if _, ok := e.posIdx[string(fi)]; !ok {
				e.posIdx[string(fi)] = 0
				e.pos = append(e.pos, fi)
			}
		}
	}
	slices.SortFunc(e.pos, bytes.Compare)
	for i, p := range e.pos {
		e.posIdx[string(p)] = i
	}
}

func isTreeDelete(n TreeNode) bool { return n.HasParent && n.Parent == deletedTreeRoot }

// checkChange checks one change before it is placed in a block: it has ops,
// they follow one another from the change's id with its peer and lamports,
// its dependencies are in range and depend on its own peer only through the
// op just before it, and its counters and lamports fit loro's fields, Counter
// an i32 and Lamport a u32. The bounds are compared before anything is added to them, so a
// counter near the top of int64 cannot wrap into range.
func checkChange(ch Change) error {
	if len(ch.Ops) == 0 {
		return fmt.Errorf("loro: change %d@%d has no ops", ch.ID.Counter, ch.ID.Peer)
	}
	if ch.ID.Counter < 0 || ch.ID.Counter > math.MaxInt32 || ch.Lamport < 0 || ch.Lamport > math.MaxUint32 {
		return fmt.Errorf("loro: change %d@%d at lamport %d is outside loro's counter or lamport range", ch.ID.Counter, ch.ID.Peer, ch.Lamport)
	}
	// A dependency's counter is an i32 in loro, so it is checked before the
	// rule for the own peer: a dependency at -1 on a change at counter 0
	// would otherwise pass as the op just before it.
	self := false
	for _, d := range ch.Deps {
		if d.Counter < 0 || d.Counter > math.MaxInt32 {
			return fmt.Errorf("loro: change %d@%d depends on counter %d, outside loro's range", ch.ID.Counter, ch.ID.Peer, d.Counter)
		}
		if d.Peer != ch.ID.Peer {
			continue
		}
		if self || d.Counter != ch.ID.Counter-1 {
			return fmt.Errorf("loro: change %d@%d depends on %d@%d; a change can depend on its own peer only through the op just before it",
				ch.ID.Counter, ch.ID.Peer, d.Counter, d.Peer)
		}
		self = true
	}
	if !utf8.ValidString(ch.CommitMsg) {
		return fmt.Errorf("loro: change %d@%d has a commit message that is not UTF-8, which loro refuses", ch.ID.Counter, ch.ID.Peer)
	}
	ctr := ch.ID.Counter
	for _, op := range ch.Ops {
		if op.Peer != ch.ID.Peer || op.Counter != ctr || op.Lamport != ch.Lamport+(ctr-ch.ID.Counter) {
			return fmt.Errorf("loro: op %d@%d (lamport %d) is not where change %d@%d (lamport %d) puts its next op, %d@%d (lamport %d)",
				op.Counter, op.Peer, op.Lamport, ch.ID.Counter, ch.ID.Peer, ch.Lamport, ctr, ch.ID.Peer, ch.Lamport+(ctr-ch.ID.Counter))
		}
		// ctr is at most MaxInt32 here, so neither side of either test can
		// overflow, and the change's end stays within both fields.
		if op.Len < 1 || op.Len > math.MaxInt32-ctr || op.Len > math.MaxUint32-(ch.Lamport+ctr-ch.ID.Counter) {
			return fmt.Errorf("loro: op %d@%d of length %d ends past loro's counter or lamport range", op.Counter, op.Peer, op.Len)
		}
		ctr += op.Len
		if err := checkOp(op); err != nil {
			return err
		}
	}
	return nil
}

// opKinds lists, for each kind of container, the value kinds loro reads in
// it: decode_op in outdated_encode_reordered.rs, which decodes the ops of a
// Fast block, refuses any other as corrupted data, and encode_op writes only
// these. A Text holds inserts, deletes and the two ends of a mark (the end
// is Null), a Map writes and deletes, a List inserts and deletes, a
// MovableList those and moves and sets, a Tree moves (a create and a delete
// are moves), and a Counter steps of an i64 or an f64.
var opKinds = map[change.ContainerType][]change.ValueKind{
	change.CText:        {change.VKStr, change.VKDeleteSeq, change.VKMarkStart, change.VKNull},
	change.CMap:         {change.VKLoroValue, change.VKDeleteOnce},
	change.CList:        {change.VKLoroValue, change.VKDeleteSeq},
	change.CMovableList: {change.VKLoroValue, change.VKDeleteSeq, change.VKListMove, change.VKListSet},
	change.CTree:        {change.VKRawTreeMove},
	change.CCounter:     {change.VKI64, change.VKF64},
}

// checkOp checks one op against what loro reads back as written.
func checkOp(op Op) error {
	kinds, ok := opKinds[op.Kind]
	if !ok {
		// loro 1.17 decodes a container kind it does not know into an op it
		// keeps but cannot apply, and never writes one.
		return fmt.Errorf("loro: op %d@%d is in a container of kind %d, which loro does not write", op.Counter, op.Peer, op.Kind)
	}
	if !slices.Contains(kinds, op.VKind) {
		return fmt.Errorf("loro: op %d@%d writes value kind %d into a %v, which loro does not read", op.Counter, op.Peer, op.VKind, op.Kind)
	}
	// loro reads every string as UTF-8 and refuses the blob otherwise: keys
	// (decode_keys in block_encode.rs), which hold root names, map keys and
	// mark keys, string values (read_str in encoding/value.rs) and commit
	// messages (decode_block).
	if !utf8.ValidString(op.Container) || !utf8.ValidString(op.MapKey) || !utf8Strings(op.Value) {
		return fmt.Errorf("loro: op %d@%d holds a name, key or string that is not UTF-8, which loro refuses", op.Counter, op.Peer)
	}

	// What the value names: the element a move or a set addresses by peer
	// and lamport, the node and parent of a tree op, the container a nested
	// op is in. loro reads each into a field of fixed width and truncates
	// what does not fit: a set naming lamport 2^32 set the element of
	// lamport 0, and tree node 4294967296@1 imported as 0@1 (read_usize then
	// "as u32" or "as i32" in encoding/value.rs and decode_op). A lamport is
	// a u32, a counter an i32, and a move's source index a u32.
	if !op.IsRoot {
		if _, err := parseStoredID(op.Container); err != nil {
			return fmt.Errorf("loro: op %d@%d is in container %w", op.Counter, op.Peer, err)
		}
	}
	bad := func() error {
		return fmt.Errorf("loro: op %d@%d of value kind %d carries %T", op.Counter, op.Peer, op.VKind, op.Value)
	}
	switch op.VKind {
	case change.VKMarkStart:
		m, ok := op.Value.(MarkInfo)
		if !ok {
			return bad()
		}
		if m.Start != op.Pos {
			return fmt.Errorf("loro: mark %d@%d starts at %d but its op is at %d", op.Counter, op.Peer, m.Start, op.Pos)
		}
	case change.VKListMove:
		el, ok := op.Value.(ListElem)
		if !ok {
			return bad()
		}
		if el.Lamport < 0 || el.Lamport > math.MaxUint32 || op.MoveFrom < 0 || op.MoveFrom > math.MaxUint32 {
			return fmt.Errorf("loro: move %d@%d names lamport %d and index %d; loro reads both as u32", op.Counter, op.Peer, el.Lamport, op.MoveFrom)
		}
	case change.VKListSet:
		v, ok := op.Value.(ListSetValue)
		if !ok {
			return bad()
		}
		if v.Elem.Lamport < 0 || v.Elem.Lamport > math.MaxUint32 {
			return fmt.Errorf("loro: set %d@%d names lamport %d; loro reads it as a u32", op.Counter, op.Peer, v.Elem.Lamport)
		}
	case change.VKRawTreeMove:
		n, ok := op.Value.(TreeNode)
		if !ok {
			return bad()
		}
		if _, err := parseStoredID(n.ID); err != nil {
			return fmt.Errorf("loro: tree op %d@%d moves node %w", op.Counter, op.Peer, err)
		}
		if n.HasParent {
			if _, err := parseStoredID(n.Parent); err != nil {
				return fmt.Errorf("loro: tree op %d@%d moves under parent %w", op.Counter, op.Peer, err)
			}
		}
		if !isTreeDelete(n) {
			if _, err := hex.DecodeString(n.FI); err != nil {
				return fmt.Errorf("loro: tree op %d@%d has fractional index %q: %w", op.Counter, op.Peer, n.FI, err)
			}
		}
	}

	// An insert is as long as what it inserts and a delete as what it
	// removes. Every other op is one atom (content_len in op/content.rs and
	// list_op.rs): a longer one would claim counters loro's import does not
	// give it, so the receiver's version would stop short of the blob's.
	var err error
	switch {
	case op.VKind == change.VKDeleteSeq:
		d, ok := op.Value.(DeleteSpan)
		if !ok {
			return bad()
		}
		err = checkDeleteSpan(op, d)
	case op.VKind == change.VKStr, op.VKind == change.VKLoroValue && op.Kind != change.CMap:
		err = checkInsertAtoms(op)
	case op.Len != 1:
		err = fmt.Errorf("loro: op %d@%d has length %d; a %v op of value kind %d is one atom", op.Counter, op.Peer, op.Len, op.Kind, op.VKind)
	}
	if err != nil {
		return err
	}
	return checkPositions(op)
}

// utf8Strings reports whether every string an op value holds is UTF-8: a
// text, any string in a value at any depth, the keys of a map in a value,
// and a mark's key.
func utf8Strings(v any) bool {
	switch x := v.(type) {
	case string:
		return utf8.ValidString(x)
	case []any:
		for _, e := range x {
			if !utf8Strings(e) {
				return false
			}
		}
	case map[string]any:
		for k, e := range x {
			if !utf8.ValidString(k) || !utf8Strings(e) {
				return false
			}
		}
	case MarkInfo:
		return utf8.ValidString(x.Key) && utf8Strings(x.Value)
	case ListSetValue:
		return utf8Strings(x.Value)
	}
	return true
}

// maxSeqPos is the largest position loro's decoder takes in a sequence op:
// UNKNOWN_SPAN_LEN - 1, with UNKNOWN_SPAN_LEN = u32::MAX / 4 (tracker.rs).
// InnerListOp::check_positions (list_op.rs) refuses anything past it, or
// below 0, as the blob is decoded; a position is a u32 there, so -1 arrives
// as 4294967295. Measured on loro-crdt 1.17.1: a list insert at 1073741823
// is refused at decode, one at 1073741822 is not.
const maxSeqPos = math.MaxUint32/4 - 1

// checkPositions holds the positions of a Text, List or MovableList op to
// what check_positions accepts: an insert's position, both ends of a delete
// (a backwards one ends at its position and starts before it), both indexes
// of a move, both ends of a mark. A set and a mark's end carry no position,
// and neither does an op in a Map, a Tree or a Counter: loro writes 0 there
// (get_op_prop), so 0 is the only position they take.
func checkPositions(op Op) error {
	in := func(p int64) bool { return p >= 0 && p <= maxSeqPos }
	var ok bool
	switch {
	case op.Kind == change.CMap || op.Kind == change.CTree || op.Kind == change.CCounter ||
		op.VKind == change.VKListSet || op.VKind == change.VKNull:
		ok = op.Pos == 0
	case op.VKind == change.VKDeleteSeq:
		// checkDeleteSpan bounded the length to the i32 range, so neither
		// sum can overflow. A backwards delete is recorded at its last
		// position (DeleteSpan::start and last in list_op.rs).
		n := op.Value.(DeleteSpan).Len
		start, last := op.Pos, op.Pos+n-1
		if n < 0 {
			start, last = op.Pos+1+n, op.Pos
		}
		ok = in(start) && in(last)
	case op.VKind == change.VKListMove:
		ok = in(op.Pos) && in(op.MoveFrom)
	case op.VKind == change.VKMarkStart:
		n := op.Value.(MarkInfo).Len
		ok = in(op.Pos) && n >= 0 && n <= maxSeqPos-op.Pos
	default: // an insert
		ok = in(op.Pos)
	}
	if !ok {
		return fmt.Errorf("loro: op %d@%d of value kind %d in a %v is at position %d, outside what loro reads", op.Counter, op.Peer, op.VKind, op.Kind, op.Pos)
	}
	return nil
}

// op writes one op's columns and value, registering what it names in loro's
// order (get_op_prop, then encode_op, in outdated_encode_reordered.rs).
func (e *blockEncoder) op(op Op) error {
	c := contID{root: op.IsRoot, kind: op.Kind, name: op.Container}
	if !op.IsRoot {
		id, _ := parseID(op.Container) // checkOp read it
		c = contID{kind: op.Kind, peer: id.Peer, counter: id.Counter}
	}
	e.ops.ContainerIdx = append(e.ops.ContainerIdx, e.container(c))
	prop := op.Pos
	if op.Kind == change.CMap {
		prop = e.key(op.MapKey)
	}
	e.ops.Prop = append(e.ops.Prop, prop)
	e.ops.ValueKind = append(e.ops.ValueKind, op.VKind)
	e.ops.Len = append(e.ops.Len, op.Len)

	// checkOp asserted each value's type below.
	switch op.VKind {
	case change.VKDeleteSeq:
		d := op.Value.(DeleteSpan)
		e.dels = append(e.dels, change.DeleteID{PeerIdx: e.peer(d.Peer), Counter: d.Counter, Len: d.Len})
		return nil
	case change.VKMarkStart:
		m := op.Value.(MarkInfo)
		return e.vw.Mark(change.Mark{Info: m.Info, Len: m.Len, KeyIdx: e.key(m.Key), Value: m.Value})
	case change.VKListMove:
		el := op.Value.(ListElem)
		e.vw.ListMove(change.ListMove{From: op.MoveFrom, FromIdx: e.peer(el.Peer), Lamport: el.Lamport})
		return nil
	case change.VKListSet:
		v := op.Value.(ListSetValue)
		return e.vw.ListSet(change.ListSet{PeerIdx: e.peer(v.Elem.Peer), Lamport: v.Elem.Lamport, Value: v.Value})
	case change.VKRawTreeMove:
		e.treeMove(op.Value.(TreeNode))
		return nil
	}
	return e.vw.OpContent(op.VKind, op.Value)
}

// treeMove writes a tree create, move or delete. A delete is a move under
// loro's deleted root and carries position 0 (block_encode.rs, encode_tree_op),
// whatever its FI says. checkOp read the ids and the fractional index.
func (e *blockEncoder) treeMove(n TreeNode) {
	subject, _ := parseID(n.ID)
	m := change.RawTreeMove{SubjectPeerIdx: e.peer(subject.Peer), SubjectCounter: subject.Counter, ParentNull: !n.HasParent}
	if n.HasParent {
		parent, _ := parseID(n.Parent)
		m.ParentPeerIdx, m.ParentCounter = e.peer(parent.Peer), parent.Counter
	}
	if !isTreeDelete(n) {
		fi, _ := hex.DecodeString(n.FI)
		m.PositionIdx = int64(e.posIdx[string(fi)])
	}
	e.vw.RawTreeMove(m)
}

// parseStoredID reads a "counter@peer" id that loro stores with an i32
// counter: a tree node, a tree parent, a container created by an op.
func parseStoredID(s string) (ID, error) {
	id, err := parseID(s)
	if err != nil {
		return ID{}, fmt.Errorf("%q: %w", s, err)
	}
	if id.Counter < 0 || id.Counter > math.MaxInt32 {
		return ID{}, fmt.Errorf("%q, whose counter is outside loro's i32", s)
	}
	return id, nil
}

// parseID reads a "counter@peer" id, as fmtID writes it.
func parseID(s string) (ID, error) {
	ctr, peer, ok := strings.Cut(s, "@")
	if !ok {
		return ID{}, fmt.Errorf("not a counter@peer id")
	}
	c, err := strconv.ParseInt(ctr, 10, 64)
	if err != nil {
		return ID{}, err
	}
	p, err := strconv.ParseUint(peer, 10, 64)
	if err != nil {
		return ID{}, err
	}
	return ID{Peer: p, Counter: c}, nil
}
