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
// The checksum is verified. Multi-change blocks are not yet supported.
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
	if n != 1 {
		return nil, fmt.Errorf("loro: multi-change blocks not yet supported (n=%d)", n)
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

	ch := Change{
		ID:        ID{Peer: hdr.Peers[0], Counter: int64(blk.CounterStart)},
		Lamport:   int64(blk.LamportStart),
		Timestamp: cm.Timestamps[0],
	}
	vr := change.NewValueReader(blk.Values)
	cum := int64(0)  // counter/lamport offset within the change
	delConsumed := 0 // index into deleteIDs, consumed in op order
	for i := 0; i < ops.N(); i++ {
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
			Lamport:   int64(blk.LamportStart) + cum,
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
		}
		cum += ops.Len[i]
		ch.Ops = append(ch.Ops, op)
	}
	return []Change{ch}, nil
}
