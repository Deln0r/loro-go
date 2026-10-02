// Package loro is the public entry point of the pure-Go Loro port: it decodes
// loro-crdt FastUpdates blobs into semantic changes and reconstructs document
// state. The wire codec lives under encoding/.
package loro

import (
	"encoding/hex"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"

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

// fiHex renders the fractional index at idx as hex, matching loro's toJSON,
// which uses UPPERCASE hex. Every fixture before tree_wide happened to have an
// index of "80", so the case never showed until indices grew letter digits.
func fiHex(positions [][]byte, idx int64) string {
	if idx < 0 || int(idx) >= len(positions) {
		return ""
	}
	return strings.ToUpper(hex.EncodeToString(positions[idx]))
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
	Container string               // root container name, or "counter@peer" id for a non-root container
	IsRoot    bool                 // false for nested containers (e.g. a tree node's meta map)
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
// same peer and counters [Counter, Counter+|Len|). Counter is the LOWEST id in
// the range whichever way the delete ran; a negative Len records that the
// elements were removed backwards, highest id first, which is what two
// backspaces in a row produce once loro folds them into one op.
type DeleteSpan struct {
	Peer    uint64
	Counter int64
	Len     int64
}

// Normalize returns the span as (lowest counter, count >= 0).
func (d DeleteSpan) Normalize() (start, n int64) {
	if d.Len >= 0 {
		return d.Counter, d.Len
	}
	return d.Counter, -d.Len
}

// atomFor returns which atom of the delete removed the element with counter c
// (the delete op's k-th id removes the k-th element in deletion order), or
// false when c is outside the span.
func (d DeleteSpan) atomFor(c int64) (int64, bool) {
	start, n := d.Normalize()
	if c < start || c >= start+n {
		return 0, false
	}
	if d.Len < 0 {
		return start + n - 1 - c, true
	}
	return c - start, true
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
	// Deps is the frontier the author had when it made the change: the last op
	// it had seen from each peer, minimised, so an op reached through another
	// dep is left out. That includes the author's own previous op, which is
	// listed only when nothing else already covers it.
	Deps []ID
	Ops  []Op
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
	// Every change but the last contributes at least one byte to the header
	// columns, so NChanges cannot exceed the block's total payload. Bounding it on
	// the raw uint64 (before the int conversion) stops a huge count from driving
	// the n-sized allocations below.
	avail := len(blk.Header) + len(blk.ChangeMeta) + len(blk.CIDs) + len(blk.Keys) +
		len(blk.Positions) + len(blk.Ops) + len(blk.DeleteIDs) + len(blk.Values)
	if blk.NChanges < 1 || blk.NChanges > uint64(avail)+1 {
		return nil, fmt.Errorf("loro: implausible change count %d", blk.NChanges)
	}
	n := int(blk.NChanges)
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
	// derived from counter_len.
	//
	// Lamports follow the same shape. The header's lamports column holds the
	// lamports of changes 0..n-2, and the last change's lamport is derived from
	// the block's lamport span: lamport_start + lamport_len - atomLen(last).
	// That is loro's own decoder (block_meta_encode.rs), and its encoder writes
	// the column under `if !is_last`.
	//
	// An earlier version read the column as the lamports of changes 1..n-1, so
	// every change after the first took the lamport of the change before it.
	// It went unnoticed because loro merges consecutive local commits into one
	// change, so almost every fixture had one change per block and never read
	// the column. Separate changes appear as soon as a peer edits after
	// importing someone else's edits, which is ordinary collaboration; there a
	// peer's later edit could sort before its earlier ones, and map writes, text
	// and list order, and tree moves were resolved against the wrong clock.
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
	if len(hdr.Lamports) != n-1 {
		return nil, fmt.Errorf("loro: %d header lamports for %d changes", len(hdr.Lamports), n)
	}
	lastLamport := int64(blk.LamportStart) + int64(blk.LamportLen) - atomLens[n-1]
	if int64(blk.LamportStart) < 0 || int64(blk.LamportLen) < 0 || lastLamport < 0 {
		return nil, fmt.Errorf("loro: lamport span %d+%d cannot end with a change of %d atoms",
			blk.LamportStart, blk.LamportLen, atomLens[n-1])
	}
	lamportOf := func(i int) int64 {
		if i < n-1 {
			return hdr.Lamports[i]
		}
		return lastLamport
	}
	changes := make([]Change, n)
	for i := range changes {
		changes[i] = Change{
			ID:        ID{Peer: hdr.Peers[0], Counter: int64(blk.CounterStart) + starts[i]},
			Lamport:   lamportOf(i),
			Timestamp: cm.Timestamps[i],
		}
	}
	if err := decodeDeps(hdr, changes); err != nil {
		return nil, err
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
		if c.IsRoot {
			if c.KeyOrCounter >= 0 && int(c.KeyOrCounter) < len(keys) {
				name = keys[c.KeyOrCounter]
			}
		} else {
			// A nested container (e.g. a tree node's meta map) is addressed by its
			// creating id; name it "counter@peer" so it matches the owning node id.
			name = fmtID(hdr.Peers, int64(c.PeerIdx), c.KeyOrCounter)
		}
		val, err := vr.OpContent(ops.ValueKind[i])
		if err != nil {
			return nil, err
		}
		op := Op{
			Container: name,
			IsRoot:    c.IsRoot,
			Kind:      c.Kind,
			VKind:     ops.ValueKind[i],
			Value:     val,
			Peer:      hdr.Peers[0],
			Counter:   int64(blk.CounterStart) + cum,
			Lamport:   lamportOf(chIdx) + (cum - starts[chIdx]),
			Len:       ops.Len[i],
		}
		if err := checkInsertAtoms(op); err != nil {
			return nil, err
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
			span := DeleteSpan{Peer: hdr.Peers[d.PeerIdx], Counter: d.Counter, Len: d.Len}
			if err := checkDeleteSpan(op, span); err != nil {
				return nil, err
			}
			op.Value = span
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

// decodeDeps fills in each change's dependencies from the block header, the way
// loro's own decoder reads them (block_meta_encode.rs). A dependency on the
// author's own previous op is a flag, because loro moves any dep on its own
// peer there; every other dependency is a peer index and a counter, the
// counter naming the last op seen, inclusive.
func decodeDeps(hdr *change.ChangeHeader, changes []Change) error {
	if len(hdr.DepOnSelf) != len(changes) || len(hdr.DepLens) != len(changes) ||
		len(hdr.DepPeerIdxs) != len(hdr.DepCounters) {
		return fmt.Errorf("loro: dependency columns do not match %d changes", len(changes))
	}
	next := 0
	for i := range changes {
		own := changes[i].ID
		var deps []ID
		if hdr.DepOnSelf[i] {
			if own.Counter == 0 {
				return fmt.Errorf("loro: change %d@%d depends on an op before its peer's first", own.Counter, own.Peer)
			}
			deps = append(deps, ID{Peer: own.Peer, Counter: own.Counter - 1})
		}
		if hdr.DepLens[i] > uint64(len(hdr.DepPeerIdxs)-next) {
			return fmt.Errorf("loro: change %d@%d lists more dependencies than the block holds", own.Counter, own.Peer)
		}
		for k := uint64(0); k < hdr.DepLens[i]; k++ {
			pi, ctr := hdr.DepPeerIdxs[next], hdr.DepCounters[next]
			next++
			if pi >= uint64(len(hdr.Peers)) {
				return fmt.Errorf("loro: dependency peer index %d out of range", pi)
			}
			if ctr < 0 || ctr > math.MaxInt32 {
				return fmt.Errorf("loro: dependency counter %d out of range", ctr)
			}
			dep := ID{Peer: hdr.Peers[pi], Counter: ctr}
			// A change cannot have seen its own op, or one its author made later.
			if dep.Peer == own.Peer && dep.Counter >= own.Counter {
				return fmt.Errorf("loro: change %d@%d depends on %d@%d, which it precedes", own.Counter, own.Peer, dep.Counter, dep.Peer)
			}
			deps = append(deps, dep)
		}
		changes[i].Deps = deps
	}
	if next != len(hdr.DepPeerIdxs) {
		return fmt.Errorf("loro: %d dependencies left unassigned", len(hdr.DepPeerIdxs)-next)
	}
	return nil
}

// checkDeleteSpan rejects a delete whose span cannot be real: counters outside
// loro's i32 range, an end that overflows, or a span that removes a different
// number of elements than the op has atoms. Each atom of a delete removes
// exactly one element; loro never writes the two counts out of step.
func checkDeleteSpan(op Op, d DeleteSpan) error {
	start, n := d.Normalize()
	if start < 0 || n < 0 || start > math.MaxInt32 || n > math.MaxInt32+1-start {
		return fmt.Errorf("loro: delete %d@%d spans counters %d..%d", op.Counter, op.Peer, start, start+n)
	}
	if n != op.Len {
		return fmt.Errorf("loro: delete %d@%d has length %d but its span removes %d", op.Counter, op.Peer, op.Len, n)
	}
	return nil
}

// checkInsertAtoms rejects a sequence insert whose value holds a different
// number of atoms than its op length. The length decides the ids the insert
// occupies (its counters run from Counter to Counter+Len) and how duplicates
// are recognised; the value decides how many elements the merge creates. When
// they disagree, elements get ids that belong to the next op, and an insert can
// end up as its own left neighbour. The fuzzer produced exactly that: two text
// inserts of length 1 carrying two characters each, which sent the sequence
// flatten into unbounded recursion. loro never writes such an op.
func checkInsertAtoms(op Op) error {
	var atoms int
	switch {
	case op.Kind == change.CText && op.VKind == change.VKStr:
		s, _ := op.Value.(string)
		atoms = utf8.RuneCountInString(s)
	case (op.Kind == change.CList || op.Kind == change.CMovableList) && op.VKind == change.VKLoroValue:
		lst, ok := op.Value.([]any)
		if !ok {
			return fmt.Errorf("loro: list insert %d@%d carries %T, not a list of values", op.Counter, op.Peer, op.Value)
		}
		atoms = len(lst)
	default:
		return nil
	}
	if int64(atoms) != op.Len {
		return fmt.Errorf("loro: insert %d@%d has length %d but carries %d atoms", op.Counter, op.Peer, op.Len, atoms)
	}
	return nil
}
