package loro

import (
	"encoding/binary"
	"fmt"

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
}

// decodeBlob reads a FastUpdates or FastSnapshot blob for Import. It decodes
// blocks as DecodeUpdates and DecodeSnapshot do, and keeps what Import needs
// beyond the changes: the order of the blocks and of the ops in them, in
// which loro registers containers, and the snapshot's state section.
func decodeBlob(blob []byte, nextGen func() int) (*decodedBlob, error) {
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
		if raws, state, err = snapshotSections(h.Body); err != nil {
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
		out.blocks = append(out.blocks, decodedProvenance(chs, nextGen()))
	}
	return out, nil
}

// decodedProvenance pairs a decoded block's changes with where loro keeps
// their values once it has read the block (outdated_encode_reordered.rs:221
// DecodedBlockBuffers): the text of all its inserts in one buffer of the
// block's own, generation gen, in op order; a one-item list insert inline,
// which folds like a local one; the items of longer list inserts in one
// buffer the inserts share, so one can grow in place only when it is the
// block's only such insert.
func decodedProvenance(chs []Change, gen int) []*storedChange {
	multi := 0
	for _, c := range chs {
		for _, op := range c.Ops {
			if isListInsert(op) && op.Len > 1 {
				multi++
			}
		}
	}
	block := make([]*storedChange, len(chs))
	off := 0
	for i, c := range chs {
		prov := make([]opProv, len(c.Ops))
		for j, op := range c.Ops {
			switch {
			case op.Kind == change.CText && op.VKind == change.VKStr:
				s, _ := op.Value.(string)
				prov[j] = opProv{kind: provText, gen: gen, start: off, end: off + len(s)}
				off += len(s)
			case isListInsert(op) && (op.Len == 1 || multi == 1):
				prov[j] = opProv{kind: provList}
			default:
				prov[j] = opProv{kind: provNone}
			}
		}
		c.Deps = depsOrder(c.Deps)
		block[i] = &storedChange{ch: c, prov: prov}
	}
	return block
}

func isListInsert(op Op) bool {
	return op.VKind == change.VKLoroValue && (op.Kind == change.CList || op.Kind == change.CMovableList)
}

// snapshotSections splits a FastSnapshot body (encoding/fast_snapshot.rs:1):
// the oplog's change blocks, and the state section, nil when the snapshot
// marks it empty or the state holds nothing.
func snapshotSections(body []byte) ([][]byte, []byte, error) {
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
	oplog, rest, err := section(body)
	if err != nil {
		return nil, nil, err
	}
	state, _, err := section(rest)
	if err != nil {
		return nil, nil, err
	}
	if len(state) == 0 || string(state) == "E" {
		state = nil
	}
	entries, err := kv.ParseSSTable(oplog)
	if err != nil {
		return nil, nil, err
	}
	var raws [][]byte
	for _, e := range entries {
		if len(e.Key) == 12 { // a change block; vv, fr, sv and sf are 2 bytes
			raws = append(raws, e.Value)
		}
	}
	return raws, state, nil
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
