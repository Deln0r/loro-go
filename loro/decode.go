// Package loro is the public entry point of the pure-Go Loro port: it decodes
// loro-crdt FastUpdates blobs into semantic changes and reconstructs document
// state. The wire codec lives under encoding/.
package loro

import (
	"encoding/hex"
	"fmt"

	"github.com/Deln0r/loro-go/encoding/change"
	"github.com/Deln0r/loro-go/encoding/fast"
)

// fmtID formats a (peerIdx, counter) op id as "counter@peer", matching loro toJSON.
func fmtID(peers []uint64, peerIdx, counter int64) string {
	if peerIdx < 0 || int(peerIdx) >= len(peers) {
		return fmt.Sprintf("%d@?", counter)
	}
	return fmt.Sprintf("%d@%d", counter, peers[peerIdx])
}

// fiHex renders the fractional index at idx as hex (loro toJSON form).
func fiHex(positions [][]byte, idx int64) string {
	if idx < 0 || int(idx) >= len(positions) {
		return ""
	}
	return hex.EncodeToString(positions[idx])
}

// markKey resolves a mark's style-key index against the key pool.
func markKey(keys []string, idx int64) string {
	if idx >= 0 && int(idx) < len(keys) {
		return keys[idx]
	}
	return ""
}

// ID identifies an operation/change by its originating peer and counter.
type ID struct {
	Peer    uint64
	Counter int64
}

// Op is one decoded operation with its container and content resolved.
type Op struct {
	Container string               // root container name
	Kind      change.ContainerType // Map / List / Text / ...
	VKind     change.ValueKind     // op content kind (insert / mark / move / tree-move / ...)
	Pos       int64                // List/Text insert position; move target; delete position
	MapKey    string               // Map key (empty for non-map)
	Value     any                  // Text: string; Map: scalar; List: []any; Tree: TreeNode; DeleteSeq: DeleteSpan
	MoveFrom  int64                // MovableList move source index
	Len       int64                // atom length (delete ops: number of elements removed)

	Peer    uint64 // op author
	Counter int64  // op id counter (first element for multi-element ops)
	Lamport int64  // op lamport (first element)
}

// DeleteSpan is the id range a text/list delete op removes: elements with the
// same peer and counters [Counter, Counter+Len) (Len may be negative for
// reverse deletion; Normalize resolves it).
type DeleteSpan struct {
	Peer    uint64
	Counter int64
	Len     int64
}

// Normalize returns the span as (first counter, count >= 0).
func (d DeleteSpan) Normalize() (start, n int64) {
	if d.Len >= 0 {
		return d.Counter, d.Len
	}
	return d.Counter + d.Len + 1, -d.Len
}

// TreeNode is a decoded Tree create/move op target.
type TreeNode struct {
	ID        string // "counter@peer"
	HasParent bool
	Parent    string // "counter@peer" when HasParent
	FI        string // fractional index, hex
}

// MarkInfo is a decoded rich-text mark: the style key/value applied over the
// visible range [Start, Start+Len). Info holds loro's expand flags (anchor
// behavior), not yet interpreted.
type MarkInfo struct {
	Start int64
	Len   int64
	Key   string
	Value any
	Info  uint8
}

// Change is one decoded change (a batch of ops from one peer).
type Change struct {
	ID        ID
	Lamport   int64
	Timestamp int64
	Ops       []Op
}

// Updates is a decoded FastUpdates blob.
type Updates struct {
	Changes []Change
}

// DecodeUpdates decodes a loro-crdt FastUpdates export into semantic changes.
// The checksum is verified. Blocks carrying multiple changes are partitioned
// into their individual changes.
func DecodeUpdates(blob []byte) (*Updates, error) {
	h, err := fast.ParseHeader(blob)
	if err != nil {
		return nil, err
	}
	if h.Mode != fast.ModeFastUpdates {
		return nil, fmt.Errorf("loro: expected FastUpdates (mode 4), got mode %d", h.Mode)
	}
	if err := fast.VerifyChecksum(blob); err != nil {
		return nil, err
	}
	blocks, err := change.SplitBlocks(h.Body)
	if err != nil {
		return nil, err
	}
	u := &Updates{}
	for _, raw := range blocks {
		blk, err := change.ParseBlock(raw)
		if err != nil {
			return nil, err
		}
		chs, err := decodeBlock(blk)
		if err != nil {
			return nil, err
		}
		u.Changes = append(u.Changes, chs...)
	}
	return u, nil
}

func decodeBlock(blk *change.Block) ([]Change, error) {
	n := int(blk.NChanges)
	if n < 1 {
		return nil, fmt.Errorf("loro: block with %d changes", n)
	}
	hdr, err := change.DecodeHeader(blk.Header, n)
	if err != nil {
		return nil, err
	}
	cm, err := change.DecodeChangeMeta(blk.ChangeMeta, n)
	if err != nil {
		return nil, err
	}
	conts, err := change.DecodeContainers(blk.CIDs)
	if err != nil {
		return nil, err
	}
	keys, err := change.DecodeKeys(blk.Keys)
	if err != nil {
		return nil, err
	}
	ops, err := change.DecodeOps(blk.Ops)
	if err != nil {
		return nil, err
	}
	positions, err := change.DecodePositions(blk.Positions)
	if err != nil {
		return nil, err
	}
	deleteIDs, err := change.DecodeDeleteIDs(blk.DeleteIDs)
	if err != nil {
		return nil, err
	}
	if len(hdr.Peers) == 0 {
		return nil, fmt.Errorf("loro: empty peer table")
	}

	// Partition the block's op stream into its n changes. Change i covers
	// atomLens[i] atoms; atomLens[0..n-2] come from the header, the last is
	// derived from counter_len. Change 0's lamport is the block's lamport_start;
	// changes 1..n-1 carry theirs in the header lamports column (a later change
	// may dep on another peer, so lamports are not simply cumulative).
	atomLens := make([]int64, n)
	var atomSum int64
	for i := 0; i < n-1; i++ {
		atomLens[i] = int64(hdr.AtomLens[i])
		atomSum += atomLens[i]
	}
	atomLens[n-1] = int64(blk.CounterLen) - atomSum
	if atomLens[n-1] < 0 {
		return nil, fmt.Errorf("loro: atom lengths exceed counter_len")
	}
	starts := make([]int64, n) // change start offsets within the block
	for i := 1; i < n; i++ {
		starts[i] = starts[i-1] + atomLens[i-1]
	}
	lamportOf := func(i int) int64 {
		if i == 0 {
			return int64(blk.LamportStart)
		}
		return hdr.Lamports[i-1]
	}
	changes := make([]Change, n)
	for i := range changes {
		changes[i] = Change{
			ID:        ID{Peer: hdr.Peers[0], Counter: int64(blk.CounterStart) + starts[i]},
			Lamport:   lamportOf(i),
			Timestamp: cm.Timestamps[i],
		}
	}

	vr := change.NewValueReader(blk.Values)
	cum := int64(0)  // counter offset within the block
	chIdx := 0       // which change the current op belongs to
	delConsumed := 0 // index into deleteIDs, consumed in op order
	for i := 0; i < ops.N(); i++ {
		for chIdx+1 < n && cum >= starts[chIdx+1] {
			chIdx++
		}
		ci := ops.ContainerIdx[i]
		if ci < 0 || int(ci) >= len(conts) {
			return nil, fmt.Errorf("loro: container index %d out of range", ci)
		}
		c := conts[ci]
		name := ""
		if c.IsRoot && c.KeyOrCounter >= 0 && int(c.KeyOrCounter) < len(keys) {
			name = keys[c.KeyOrCounter]
		}
		val, err := vr.OpContent(ops.ValueKind[i])
		if err != nil {
			return nil, err
		}
		op := Op{
			Container: name,
			Kind:      c.Kind,
			VKind:     ops.ValueKind[i],
			Value:     val,
			Peer:      hdr.Peers[0],
			Counter:   int64(blk.CounterStart) + cum,
			Lamport:   lamportOf(chIdx) + (cum - starts[chIdx]),
			Len:       ops.Len[i],
		}
		if op.VKind == change.VKDeleteSeq {
			if delConsumed >= len(deleteIDs) {
				return nil, fmt.Errorf("loro: DeleteSeq op without delete_start_ids entry")
			}
			d := deleteIDs[delConsumed]
			delConsumed++
			if d.PeerIdx < 0 || int(d.PeerIdx) >= len(hdr.Peers) {
				return nil, fmt.Errorf("loro: delete peer index %d out of range", d.PeerIdx)
			}
			op.Value = DeleteSpan{Peer: hdr.Peers[d.PeerIdx], Counter: d.Counter, Len: d.Len}
		}
		if c.Kind == change.CMap {
			if p := ops.Prop[i]; p >= 0 && int(p) < len(keys) {
				op.MapKey = keys[p]
			}
		} else {
			op.Pos = ops.Prop[i]
		}
		switch tv := val.(type) {
		case change.RawTreeMove:
			node := TreeNode{ID: fmtID(hdr.Peers, tv.SubjectPeerIdx, tv.SubjectCounter), FI: fiHex(positions, tv.PositionIdx)}
			if !tv.ParentNull {
				node.HasParent = true
				node.Parent = fmtID(hdr.Peers, tv.ParentPeerIdx, tv.ParentCounter)
			}
			op.Value = node
		case change.ListMove:
			op.MoveFrom = tv.From
			op.Value = nil
		case change.Mark:
			op.Value = MarkInfo{Start: op.Pos, Len: tv.Len, Info: tv.Info, Key: markKey(keys, tv.KeyIdx), Value: tv.Value}
		}
		cum += ops.Len[i]
		changes[chIdx].Ops = append(changes[chIdx].Ops, op)
	}
	return changes, nil
}
