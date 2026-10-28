package loro

import (
	"cmp"
	"fmt"
	"maps"
	"math"
	"slices"
	"sync"
	"time"

	"github.com/Deln0r/loro-go/encoding/change"
)

// Document is a live Loro peer: it imports updates and snapshots exported by
// loro-crdt, edits its containers through handles, commits, and exports its
// history. It follows loro-crdt 1.17.1 with auto-commit on, as loro-crdt's
// JavaScript LoroDoc runs: edits gather in an open transaction, and Commit,
// Import and ExportUpdates close it as one change.
//
// What a Document records matches what loro records for the same edits: the
// ids, lamports, dependencies and timestamps of its changes, the position,
// value and delete targets of every op, which changes merge and which ops
// fold into one. TestScriptCorpus holds it to loro-crdt on scripts of edits,
// commits and syncs by one to three peers.
//
// Containers are addressed by name and kind, as loro addresses its root
// containers: Text("x") and Map("x") are two containers. Taking a handle
// makes the container part of the document, so JSON shows it even before
// anything is written to it.
//
// Handles exist for every kind of container, root or stored in another
// (Map.SetContainer, List.InsertContainer, MovableList.InsertContainer and
// SetContainer, Tree.Meta, Map.EnsureMergeable, GetContainer), and a Text
// can be styled with Mark and Unmark (see ConfigTextStyle). A container
// stored in another is deleted once its place there is written over or
// deleted: its contents stay readable, and edits through it are refused.
//
// One mutex guards the whole document. Every method of a Document and of its
// handles takes it, so a Document may be used from several goroutines; their
// calls run one at a time.
type Document struct {
	mu   sync.Mutex
	peer uint64

	log *opLog
	st  *docState
	tx  *txn

	recordTimestamp bool
	mergeInterval   int64
	now             func() int64
	textStyles      map[string]ExpandType

	// rootOrder is the order in which root containers became known, as
	// loro's arena registers them: when a handle is taken, and op by op as
	// imported blocks are read. It decides which of two roots of one name
	// JSON shows.
	rootOrder  []contKey
	registered map[contKey]bool
	acquired   map[contKey]bool
	// blockGen numbers the buffers loro decodes imported blocks into, so
	// that text of two blocks never looks adjacent.
	blockGen int

	// knock switches off loro rules, one at a time, for tests that show a
	// named script needs the rule. It is always empty outside tests.
	knock ruleSet
}

// defaultMergeInterval is loro's default change merge interval in seconds
// (configure.rs:44).
const defaultMergeInterval = 1000

// NewDocument returns an empty document whose edits carry the given peer id.
// Peer id math.MaxUint64 is reserved by loro; a document with it can import
// and read but not edit.
func NewDocument(peer uint64) *Document {
	return &Document{
		peer:          peer,
		log:           newOpLog(),
		st:            newDocState(),
		mergeInterval: defaultMergeInterval,
		textStyles:    defaultTextStyles(),
		now:           func() int64 { return time.Now().Unix() },
		registered:    map[contKey]bool{},
		acquired:      map[contKey]bool{},
	}
}

// PeerID returns the peer id the document's edits carry.
func (d *Document) PeerID() uint64 { return d.peer }

// SetRecordTimestamp sets whether a commit records the current time, in
// seconds, as its change's timestamp. It is off by default, as in loro, and
// a change then has timestamp 0 unless one is requested; either way a change
// never gets a timestamp below that of the changes it depends on.
func (d *Document) SetRecordTimestamp(record bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.recordTimestamp = record
}

// SetChangeMergeInterval sets, in seconds, how close in time a commit must
// be to the change before it to be merged into it. The default is 1000, as
// in loro. A commit merges into the previous change only if it continues it:
// same peer, nothing imported in between, and the same commit message.
func (d *Document) SetChangeMergeInterval(seconds int64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.mergeInterval = seconds
}

// CommitOptions are the options of one commit.
type CommitOptions struct {
	// Message is the commit message, stored with the change; empty means
	// none.
	Message string
	// Timestamp, when HasTimestamp is set, is the change's timestamp in
	// seconds since the Unix epoch, in place of the recorded one.
	Timestamp    int64
	HasTimestamp bool
}

// Commit closes the open transaction as one change. With nothing to commit
// it does nothing.
func (d *Document) Commit() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.commit("", 0, false)
}

// CommitWith commits as Commit does, with a message or a timestamp. With
// nothing to commit the options are dropped, as loro drops them.
func (d *Document) CommitWith(opts CommitOptions) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.commit(opts.Message, opts.Timestamp, opts.HasTimestamp)
}

// Import applies a FastUpdates or FastSnapshot blob exported by loro-crdt or
// by a Document. It first commits the open transaction, so edits made before
// an import and edits made after it are two changes, the second depending on
// what was imported (loro.rs:698, :159).
//
// A change whose dependencies have not arrived waits, as in loro: it changes
// neither the state, nor the version later edits depend on, nor their
// lamports, and it is not exported. It is applied once its dependencies are
// imported, minus whatever part of it arrived in between. A change already
// applied is skipped, and the known part of one that overlaps is trimmed.
//
// A snapshot imported into a document with no history is taken whole, and
// the root containers its state holds show even if no op wrote to them. Into
// a document with history, a snapshot is imported as the updates it holds.
//
// A blob is refused with an error, and nothing of it applied, when it does
// not decode or is a shallow snapshot; when it holds a change ExportUpdates
// could not write back as loro reads it (see EncodeUpdates); when an op id
// the document has is used for another op, or a change skips counters of
// its peer, as loro refuses (oplog/known_history.rs); and when an op's
// position lies past what its author saw, which loro's state refuses
// (state/list_state.rs:585). The open transaction is committed all the
// same, as loro commits it before it reads the blob.
func (d *Document) Import(blob []byte) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.knocked(ruleImportCommits) {
		d.commit("", 0, false)
	}
	b, err := decodeBlob(blob, d)
	if err != nil {
		return err
	}
	var changes []*storedChange
	for _, block := range b.blocks {
		// decodeBlob refused what the document could not export again.
		changes = append(changes, block...)
	}
	// loro sorts a blob's changes by lamport before it checks and applies
	// them (fast_snapshot.rs:412), with Rust's unstable sort: the order of
	// changes of equal lamport is where their peers land in the hash maps
	// of the version vector and the frontiers.
	sortByLamport(changes, d)
	if err := d.log.checkKnown(changes); err != nil {
		return err
	}

	// From here the import changes the document. If the merge refuses what
	// it applied, everything goes back as it was: loro rolls an import back
	// whose diff its state cannot apply.
	saved := d.save()
	snapshotInPlace := b.snapshot && d.canTakeSnapshot()
	if snapshotInPlace {
		// loro replaces the state of an empty document with the snapshot's
		// (loro.rs:745 decode_snapshot), so a root acquired before is gone
		// and the roots the snapshot's state holds are there.
		clear(d.acquired)
		for _, k := range b.stateRoots {
			d.register(k)
			d.acquired[k] = true
		}
	}
	for _, block := range b.blocks {
		for _, c := range block {
			for _, op := range c.ch.Ops {
				if op.IsRoot && !isMergeableName(op.Container) {
					d.register(keyOf(op))
				}
			}
		}
	}
	if b.snapshot && !snapshotInPlace {
		changes = d.snapshotChanges(changes)
	}
	d.materializePending(changes)
	applied, err := d.log.importChanges(changes, d)
	if err == nil && applied {
		err = d.rebuild()
	}
	if err != nil {
		d.restore(saved)
		return err
	}
	if snapshotInPlace {
		d.takeSnapshotVersion(b.vv, b.fr)
	}
	return nil
}

// savedDoc is what an import may change besides the state.
type savedDoc struct {
	log        *opLog
	acquired   map[contKey]bool
	registered map[contKey]bool
	rootOrder  []contKey
}

func (d *Document) save() savedDoc {
	return savedDoc{log: d.log.clone(), acquired: maps.Clone(d.acquired), registered: maps.Clone(d.registered), rootOrder: slices.Clone(d.rootOrder)}
}

func (d *Document) restore(s savedDoc) {
	d.log, d.acquired, d.registered, d.rootOrder = s.log, s.acquired, s.registered, s.rootOrder
}

// snapshotChanges is what loro makes of a snapshot imported into a document
// with history (change_store.rs:489 decode_snapshot_for_updates): the
// changes sorted by lamport, their known parts trimmed, and every op rebuilt
// through the document's arena as a local op is, so its text takes bytes of
// the arena and it folds into the op before it where a local op would.
func (d *Document) snapshotChanges(changes []*storedChange) []*storedChange {
	slices.SortStableFunc(changes, func(a, b *storedChange) int { return cmp.Compare(a.ch.Lamport, b.ch.Lamport) })
	var out []*storedChange
	for _, c := range changes {
		if c = d.log.trimKnown(c, d); c == nil {
			continue
		}
		ops := c.ch.Ops
		rebuilt := &storedChange{ch: c.ch}
		rebuilt.ch.Ops = nil
		for _, op := range ops {
			var p opProv
			switch {
			case op.Kind == change.CText && op.VKind == change.VKStr:
				s, _ := op.Value.(string)
				p = d.log.arena.alloc(len(s))
			case isListInsert(op):
				p = opProv{kind: provList}
			}
			rebuilt.pushOp(op, p, d)
		}
		out = append(out, rebuilt)
	}
	return out
}

// canTakeSnapshot is loro's can_reset_with_snapshot (loro.rs:601): no
// history, pending changes included, no text ever inserted, and every
// container's state empty. A Counter's state never counts as empty
// (state/counter_state.rs:43), so a Counter handle taken before keeps the
// snapshot from replacing the state.
func (d *Document) canTakeSnapshot() bool {
	if !d.log.empty() || d.log.arena.len != 0 || d.tx != nil {
		return false
	}
	for k := range d.acquired {
		if k.kind == change.CCounter {
			return false
		}
	}
	return true
}

// materializePending gives state to the root containers of an import none
// of whose new changes can be applied yet: loro creates them, empty, so
// toJSON shows them while their ops wait (loro.rs:928,
// pending_root_containers_to_materialize). When any new change applies,
// loro creates none of them.
func (d *Document) materializePending(changes []*storedChange) {
	var roots []contKey
	for _, c := range changes {
		if c.end() <= d.log.vv[c.ch.ID.Peer] {
			continue
		}
		if _, ok := d.log.lamportFromDeps(c.ch.Deps); ok {
			return
		}
		for _, op := range c.ch.Ops {
			if op.IsRoot && !isMergeableName(op.Container) {
				roots = append(roots, keyOf(op))
			}
		}
	}
	for _, k := range roots {
		d.acquired[k] = true
	}
}

// ExportUpdates commits the open transaction, as loro's export does, and
// returns the document's whole history as a FastUpdates blob: every change
// applied, none that waits for a dependency. The changes are merged and put
// in blocks as loro's export merges and groups them.
func (d *Document) ExportUpdates() ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.commit("", 0, false)
	return EncodeUpdates(exportUpdates(d.log.exportBlocks(d)))
}

// exportUpdates is the history ExportUpdates writes: its changes in the
// blocks loro's export puts them in.
func exportUpdates(blocks [][]*storedChange) *Updates {
	u := &Updates{}
	for _, b := range blocks {
		for _, c := range b {
			u.Changes = append(u.Changes, c.ch)
		}
		u.Blocks = append(u.Blocks, len(b))
	}
	return u
}

// JSON returns the document's state as loro-crdt's toJSON shows it: each root
// container by name, with the containers stored in it in place. Edits not
// yet committed are included, as in loro. The values are copies.
func (d *Document) JSON() (map[string]any, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	roots, vals, err := d.visibleRoots()
	if err != nil {
		return nil, err
	}
	out := make(map[string]any, len(roots))
	for _, k := range roots {
		out[k.name] = copyValue(vals[k])
	}
	return out, nil
}

// register records that root container k became known.
func (d *Document) register(k contKey) {
	if d.registered[k] {
		return
	}
	d.registered[k] = true
	d.rootOrder = append(d.rootOrder, k)
}

// editable reports why the document cannot make an op in container k, if it
// cannot: a reserved peer id (handler.rs:431), checked before an edit looks
// at anything else. A deleted container is refused later, by apply, as loro
// refuses it when the op is made (txn.rs:751): an edit that returns before
// making one, a move to where the node is or a map write of the value
// already there, succeeds on a deleted container.
func (d *Document) editable(contKey) error {
	if d.peer == math.MaxUint64 {
		return fmt.Errorf("loro: peer id %d is reserved and cannot edit", d.peer)
	}
	return nil
}

// rule names a loro rule a test can switch off with knock, to show that the
// named script for it fails without it.
type rule uint

const (
	ruleImportCommits       rule = iota // review item 3: import commits the open transaction
	rulePendingIsolated                 // item 14: pending changes do not raise local lamports
	rulePendingTrim                     // item 14: a released change loses its known prefix
	ruleTimestampFloor                  // item 5: a change's timestamp is at least its deps'
	ruleMergeInterval                   // item 5: changes merge only within the interval
	ruleBlockLimit                      // item 4: a full block takes no merged change
	ruleFoldLocalOnly                   // item 2: only adjacent local ops fold
	ruleDeleteSplit                     // item 7: a text delete splits at anchors and id gaps
	ruleMapSkipEqual                    // item 11: setting a key to its value emits nothing
	ruleMapDeleteAbsent                 // item 11: deleting an absent key still emits
	ruleCounterInt                      // item 12: small integral increments are I64
	ruleTypedRoots                      // item 15: of two roots of one name, the last non-empty shows
	ruleRootsVisible                    // item 15: a root shows once a handle is taken
	ruleHandleLive                      // item 16: a handle resolves its container on every call
	ruleMarkSkipApplied                 // item 8: a mark the range already has makes no op
	ruleUnmarkSkipAbsent                // item 8: unmarking a key the range lacks makes no op
	ruleMarkConfigured                  // item 8: marking an unconfigured key is an error
	ruleUnmarkReverse                   // item 8: an unmark's expand rule is the key's reversed
	ruleColonPrefix                     // item 8: "k:x" is marked with the configuration of "k"
	ruleConfigReplaces                  // item 8: ConfigTextStyle replaces the configuration
	ruleMarkRange                       // a mark's ends go where text inserted there would go
	ruleMovableOpIndex                  // item 9: MovableList ops record op indexes, not user ones
	ruleMovableMoveInPlace              // item 11: Move(i, i) makes no op
	ruleMovableSetSkipEqual             // item 11: MovableList.Set of the value it has still makes an op
	ruleTreeRearrange                   // item 10: equal neighbour indexes are spread out anew
	ruleTreeMoveInPlace                 // item 11: a tree move to where the node is makes no op
	ruleTreeCycle                       // item 11: a cyclic tree move is an error
	ruleDeletedRefused                  // item 16: edits through a deleted container are refused
	ruleMergeableID                     // item 16: a mergeable child is named after its map, key and kind
	ruleCounterAttach                   // item 16: a new Counter child is incremented by zero, an op
)

type ruleSet uint64

func (d *Document) knocked(r rule) bool { return d.knock&(1<<r) != 0 }
