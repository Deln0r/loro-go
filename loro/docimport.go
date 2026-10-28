package loro

import (
	"cmp"
	"encoding/binary"
	"fmt"
	"slices"

	"github.com/Deln0r/loro-go/encoding/change"
	"github.com/Deln0r/loro-go/encoding/fast"
	"github.com/Deln0r/loro-go/encoding/kv"
)

// decodedBlob is an imported blob read block by block: each block's
// changes, and for a snapshot the root containers its state section holds.
type decodedBlob struct {
	snapshot bool
	blocks   [][]*storedChange
	// stateRoots are the root containers a snapshot's state holds, in key
	// order. A root acquired with no op has state but no op in the history.
	stateRoots []contKey
	// vv and fr are a snapshot's version vector and frontiers, the ids as
	// written, which loro takes as the document's when the snapshot
	// replaces its state.
	vv VersionVector
	fr []ID
}

// decodeBlob reads a FastUpdates or FastSnapshot blob for Import. It decodes
// blocks as DecodeUpdates and DecodeSnapshot do, and keeps what Import needs
// beyond the changes: the order of the blocks and of the ops in them, in
// which loro registers containers, and the snapshot's state section.
func decodeBlob(blob []byte, d *Document) (*decodedBlob, error) {
	h, err := fast.ParseHeader(blob)
	if err != nil {
		return nil, err
	}
	if err := fast.VerifyChecksum(blob); err != nil {
		return nil, err
	}
	out := &decodedBlob{}
	var raws [][]byte
	switch h.Mode {
	case fast.ModeFastUpdates:
		if raws, err = change.SplitBlocks(h.Body); err != nil {
			return nil, err
		}
	case fast.ModeFastSnapshot:
		out.snapshot = true
		var state []byte
		if raws, state, out.vv, out.fr, err = snapshotSections(h.Body); err != nil {
			return nil, err
		}
		if out.stateRoots, err = stateRoots(state); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("loro: cannot import a blob of mode %d", h.Mode)
	}
	budget := &nameBudget{left: nameBudgetBase}
	for _, raw := range raws {
		blk, err := change.ParseBlock(raw)
		if err != nil {
			return nil, err
		}
		chs, err := decodeBlock(blk, budget)
		if err != nil {
			return nil, err
		}
		// A change the document could not export again is refused:
		// EncodeUpdates' checks are what loro reads back as written, and
		// loro's own import refuses most of what they catch. They hold
		// each op as the blob writes it, before the ops are folded, as
		// loro checks each op's positions as it reads it
		// (outdated_encode_reordered.rs:546 check_positions) and not the
		// op it folds it into.
		checked := !d.knocked(ruleCheckUnfolded)
		if checked {
			if err := checkChanges(chs); err != nil {
				return nil, err
			}
		}
		d.blockGen--
		block := decodedProvenance(chs, d.blockGen, d)
		if !checked {
			for _, c := range block {
				if err := checkChange(c.ch); err != nil {
					return nil, err
				}
			}
		}
		out.blocks = append(out.blocks, block)
	}
	if out.snapshot {
		if err := snapshotFrontiersHeld(out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// snapshotFrontiersHeld is loro's check of a snapshot's stored frontiers
// (change_store.rs:1244): each must lie in one of the snapshot's changes,
// or the snapshot is refused. Nothing more is asked of them.
func snapshotFrontiersHeld(b *decodedBlob) error {
	spans := map[uint64][]idRange{}
	for _, block := range b.blocks {
		for _, c := range block {
			spans[c.ch.ID.Peer] = append(spans[c.ch.ID.Peer], idRange{start: c.start(), end: c.end()})
		}
	}
	for _, rs := range spans {
		slices.SortFunc(rs, func(a, b idRange) int { return cmp.Compare(a.start, b.start) })
	}
	for _, id := range b.fr {
		rs := spans[id.Peer]
		i, _ := slices.BinarySearchFunc(rs, id.Counter, func(r idRange, c int64) int {
			if r.end <= c {
				return -1
			}
			return 1
		})
		if i == len(rs) || rs[i].start > id.Counter {
			return fmt.Errorf("loro: the snapshot's frontier %d@%d lies in none of its changes", id.Counter, id.Peer)
		}
	}
	return nil
}

// decodedProvenance pairs a decoded block's changes with where loro keeps
// their values once it has read the block (outdated_encode_reordered.rs:221
// DecodedBlockBuffers): the text of all its inserts in one buffer of the
// block's own, generation gen, in op order; a one-item list insert inline,
// which folds like a local one; the items of longer list inserts in one
// buffer the inserts share, in op order too, which an insert appends to
// only once it alone holds it. Ops that continue each other are folded as
// loro reads them (foldDecoded).
func decodedProvenance(chs []Change, gen int, d *Document) []*storedChange {
	items := &sharedItems{}
	for _, c := range chs {
		for _, op := range c.Ops {
			if isListInsert(op) && op.Len > 1 {
				items.holders++
				items.len += int(op.Len)
			}
		}
	}
	if d.knocked(ruleSharedOwner) {
		items.holders++
	}
	block := make([]*storedChange, len(chs))
	off, at := 0, 0
	for i, c := range chs {
		prov := make([]opProv, len(c.Ops))
		for j, op := range c.Ops {
			switch {
			case op.Kind == change.CText && op.VKind == change.VKStr:
				s, _ := op.Value.(string)
				prov[j] = opProv{kind: provText, gen: gen, start: off, end: off + len(s)}
				off += len(s)
			case isListInsert(op) && op.Len == 1:
				prov[j] = opProv{kind: provList}
			case isListInsert(op):
				prov[j] = opProv{kind: provShared, buf: items, start: at, end: at + int(op.Len)}
				at += int(op.Len)
			default:
				prov[j] = opProv{kind: provNone}
			}
		}
		c.Deps = depsOrder(c.Deps)
		block[i] = &storedChange{ch: c, prov: prov}
		if !d.knocked(ruleDecodeFold) {
			block[i] = foldDecoded(c, prov)
		}
	}
	return block
}

// checkChanges is checkChange on each change.
func checkChanges(chs []Change) error {
	for _, c := range chs {
		if err := checkChange(c); err != nil {
			return err
		}
	}
	return nil
}

// foldDecoded is how decode_block puts each op it reads into its change:
// RleVec::push (block_encode.rs:791), which folds an op into the one before
// it when they continue each other. A blob can hold two such ops when the
// change was put together from parts its exporter imported apart, and loro
// folds them as it reads them: text inserts whose bytes follow each other
// in the block's buffer, deletes that continue each other, and list inserts
// by listFolds. A list insert folded into another lets go of the block's
// shared buffer, so the one left holding it comes to own it and appends
// from then on (op.rs ValueSlice::merge, can_append).
func foldDecoded(c Change, prov []opProv) *storedChange {
	out := &storedChange{ch: c}
	out.ch.Ops = make([]Op, 0, len(c.Ops))
	for j, op := range c.Ops {
		if n := len(out.ch.Ops); n > 0 {
			a := provOp{out.ch.Ops[n-1], out.prov[n-1]}
			if decodedFolds(a, op, prov[j]) {
				out.ch.Ops[n-1], out.prov[n-1] = foldOps(a, provOp{op, prov[j]})
				continue
			}
		}
		out.ch.Ops = append(out.ch.Ops, op)
		out.prov = append(out.prov, prov[j])
	}
	return out
}

// decodedFolds is Op::is_mergable for two ops of one decoded block.
func decodedFolds(a provOp, b Op, pb opProv) bool {
	if keyOf(a.op) != keyOf(b) || a.op.Counter+a.op.Len != b.Counter || a.op.VKind != b.VKind {
		return false
	}
	switch {
	case a.op.VKind == change.VKStr && a.op.Kind == change.CText:
		return a.prov.kind == provText && pb.kind == provText && a.prov.gen == pb.gen &&
			a.prov.end == pb.start && a.op.Pos+a.op.Len == b.Pos
	case isListInsert(a.op):
		return a.op.Pos+a.op.Len == b.Pos && listFolds(a.prov, pb)
	case a.op.VKind == change.VKDeleteSeq:
		x, ok1 := delSpanOf(a.op)
		y, ok2 := delSpanOf(b)
		return ok1 && ok2 && x.mergeable(y)
	}
	return false
}

func isListInsert(op Op) bool {
	return op.VKind == change.VKLoroValue && (op.Kind == change.CList || op.Kind == change.CMovableList)
}

// snapshotSections splits a FastSnapshot body (encoding/fast_snapshot.rs:1):
// the oplog's change blocks, the state section, nil when the snapshot marks
// it empty or the state holds nothing, and the version vector and frontiers
// the oplog keeps under "vv" and "fr", which loro decodes on every snapshot
// import and refuses the snapshot without (change_store.rs:1197). A shallow
// snapshot is refused.
func snapshotSections(body []byte) (raws [][]byte, state []byte, vv VersionVector, fr []ID, err error) {
	section := func(b []byte) ([]byte, []byte, error) {
		if len(b) < 4 {
			return nil, nil, fmt.Errorf("loro: snapshot section length cut short")
		}
		n := binary.LittleEndian.Uint32(b)
		if uint64(n) > uint64(len(b)-4) {
			return nil, nil, fmt.Errorf("loro: snapshot section length %d exceeds body", n)
		}
		return b[4 : 4+int(n)], b[4+int(n):], nil
	}
	fail := func(e error) ([][]byte, []byte, VersionVector, []ID, error) {
		return nil, nil, VersionVector{}, nil, e
	}
	oplog, rest, err := section(body)
	if err != nil {
		return fail(err)
	}
	state, rest, err = section(rest)
	if err != nil {
		return fail(err)
	}
	shallow, rest, err := section(rest)
	if err != nil {
		return fail(err)
	}
	if len(rest) != 0 {
		return fail(fmt.Errorf("loro: %d bytes after the snapshot's sections", len(rest)))
	}
	if len(state) == 0 || string(state) == "E" {
		state = nil
	}
	entries, err := kv.ParseSSTable(oplog)
	if err != nil {
		return fail(err)
	}
	// A shallow snapshot (export mode "shallow-snapshot") starts its history
	// at a version past the beginning: loro reads it by its third section,
	// the state at that version, and by the shallow version the oplog keeps
	// under "sv" and "sf" (fast_snapshot.rs:87, :218). Its first changes
	// depend on ops it does not hold, which a Document cannot merge.
	shallowErr := fmt.Errorf("loro: shallow snapshots are not supported; export a full snapshot or updates")
	if len(shallow) != 0 {
		return fail(shallowErr)
	}
	var vvRaw, frRaw []byte
	for _, e := range entries {
		switch {
		case len(e.Key) == 12: // a change block; vv, fr, sv and sf are 2 bytes
			raws = append(raws, e.Value)
		case string(e.Key) == "sv" || string(e.Key) == "sf":
			return fail(shallowErr)
		case string(e.Key) == "vv":
			vvRaw = e.Value
		case string(e.Key) == "fr":
			frRaw = e.Value
		}
	}
	if vv, err = DecodeVersionVector(vvRaw); err != nil {
		return fail(fmt.Errorf("loro: snapshot: %w", err))
	}
	if fr, err = decodeIDList(frRaw); err != nil {
		return fail(fmt.Errorf("loro: snapshot: %w", err))
	}
	return raws, state, vv, fr, nil
}

// stateRoots lists the root containers a snapshot's state section holds. Its
// keys are container ids as ContainerID::to_bytes writes them
// (loro-common/src/lib.rs:634): for a root, the kind with the high bit set,
// the name's length and the name.
func stateRoots(state []byte) ([]contKey, error) {
	if state == nil {
		return nil, nil
	}
	entries, err := kv.ParseSSTable(state)
	if err != nil {
		return nil, err
	}
	var out []contKey
	for _, e := range entries {
		if len(e.Key) < 2 || e.Key[0]&0x80 == 0 {
			continue
		}
		kind := change.ContainerType(e.Key[0] & 0x7f)
		n, w := binary.Uvarint(e.Key[1:])
		if w <= 0 || kind > change.CCounter || n != uint64(len(e.Key)-1-w) {
			continue
		}
		name := string(e.Key[1+w:])
		if isMergeableName(name) {
			continue
		}
		out = append(out, contKey{root: true, name: name, kind: kind})
	}
	return out, nil
}
