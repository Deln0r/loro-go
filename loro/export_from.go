package loro

import (
	"cmp"
	"slices"
)

// ExportUpdatesFrom commits the open transaction, as loro's export does, and
// returns as a FastUpdates blob the part of the document's history that the
// version vector from does not hold: loro-crdt's
// export({mode: "update", from}). A peer that sends its Version gets back
// what it lacks; an empty version vector gives the whole history, as
// ExportUpdates does, and one that holds everything gives a blob with no
// change, which imports as nothing.
//
// It writes what loro writes (change_store.rs:1088 export_blocks_from). A
// change from holds a part of is cut where from ends: the part exported
// starts at the next id, one lamport later for each op left out, and
// depends only on the op of its own peer just before it, as loro's
// Change::slice makes it. Every change exported, cut or whole, goes into a
// fresh change store in peer and counter order, where a change that
// continues the one before it merges into it as an import would, and the
// store's blocks are the blob's blocks. A counter of 0 or below in from is
// counted as 0, and peers the document has no op of are ignored. Here alone
// loro-go departs from loro-crdt: a counter so far below 0 that loro's
// 32-bit subtraction wraps makes loro-crdt panic (change_store.rs:1117).
func (d *Document) ExportUpdatesFrom(from VersionVector) ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.commit("", 0, false)
	return EncodeUpdates(exportUpdates(d.log.exportBlocksFrom(from, d)))
}

// exportBlocksFrom is export_blocks_from: for each peer, in ascending order,
// the changes holding ops past from, cut where from ends, put into a fresh
// store that never splits a change and merges under an interval of 0.
// Ascending order is how loro's store keys its blocks; it walks peers in
// hash map order, which changes nothing, since a peer's changes only merge
// with the same peer's.
func (l *opLog) exportBlocksFrom(from VersionVector, d *Document) [][]*storedChange {
	fresh := newChangeStore()
	var kept [][]*storedChange // the document's own blocks, for ruleExportRegroup
	for _, p := range l.store.sortedPeers() {
		start, _ := from.Get(p)
		start = max(start, 0)
		if l.vv[p] <= start {
			continue
		}
		for _, b := range l.store.peers[p] {
			if b.end <= start {
				continue
			}
			var block []*storedChange
			for _, c := range b.changes {
				if c.end() <= start {
					continue
				}
				cut := cutChange(c, start, d)
				if d.knocked(ruleExportRegroup) {
					block = append(block, cut)
					continue
				}
				fresh.insert(cut, false, 0, d)
			}
			kept = append(kept, block)
		}
	}
	if d.knocked(ruleExportRegroup) {
		return kept
	}
	var out [][]*storedChange
	for _, p := range fresh.sortedPeers() {
		for _, b := range fresh.peers[p] {
			out = append(out, b.changes)
		}
	}
	return out
}

// cutChange is the part of c from counter start on, a copy: the whole
// change when it starts there or later, otherwise Change::slice
// (change.rs:203), which starts the part one lamport later for each op cut
// and makes it depend on the op of its own peer just before it.
func cutChange(c *storedChange, start int64, d *Document) *storedChange {
	if c.start() >= start || d.knocked(ruleExportCut) {
		return c.clone()
	}
	out := c.slice(start-c.start(), c.end()-c.start())
	if d.knocked(ruleCutDeps) {
		out.ch.Deps = slices.Clone(c.ch.Deps)
	}
	if d.knocked(ruleCutLamport) {
		out.setLamport(c.ch.Lamport)
	}
	return out
}

// IDSpan is a range of one peer's ops: the counters from Start up to End,
// End excluded. A span with End below Start names the same ops as one from
// End+1 up to Start+1, as loro reads a reversed span.
type IDSpan struct {
	Peer       uint64
	Start, End int64
}

// ExportUpdatesInRange commits the open transaction and returns as a
// FastUpdates blob the ops the spans name, loro-crdt's
// export({mode: "updates-in-range", spans}) with a span {id, len} given as
// IDSpan{id.peer, id.counter, id.counter + len}. Ops the document does not
// have are left out, and counters below 0 count as 0.
//
// It writes what loro writes (change_store.rs:337 export_blocks_in_range):
// each peer's spans are merged where they overlap or touch, and the k-th
// range of every peer goes into the k-th of a row of fresh change stores,
// whose blocks are written one store after another. A change a range holds
// a part of is cut to that part, as ExportUpdatesFrom cuts it.
func (d *Document) ExportUpdatesInRange(spans []IDSpan) ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.commit("", 0, false)
	return EncodeUpdates(exportUpdates(d.log.exportBlocksInRange(spans, d)))
}

// exportBlocksInRange is export_blocks_in_range.
func (l *opLog) exportBlocksInRange(spans []IDSpan, d *Document) [][]*storedChange {
	ranges := map[uint64][]idRange{}
	for _, s := range spans {
		start, end := s.Start, s.End
		if end < start {
			start, end = end+1, start+1
		}
		start, end = max(start, 0), max(end, 0)
		if start < end {
			ranges[s.Peer] = append(ranges[s.Peer], idRange{start: start, end: end})
		}
	}
	var layers [][]IDSpan
	for p, rs := range ranges {
		slices.SortFunc(rs, func(a, b idRange) int { return cmp.Or(cmp.Compare(a.start, b.start), cmp.Compare(a.end, b.end)) })
		var merged []idRange
		for _, r := range rs {
			if n := len(merged); n > 0 && r.start <= merged[n-1].end {
				merged[n-1].end = max(merged[n-1].end, r.end)
				continue
			}
			merged = append(merged, r)
		}
		for i, r := range merged {
			if d.knocked(ruleRangeLayers) {
				i = 0
			}
			if len(layers) <= i {
				layers = append(layers, nil)
			}
			layers[i] = append(layers[i], IDSpan{Peer: p, Start: r.start, End: r.end})
		}
	}
	var out [][]*storedChange
	for _, layer := range layers {
		fresh := newChangeStore()
		for _, s := range layer {
			for _, b := range l.store.peers[s.Peer] {
				for _, c := range b.changes {
					from, to := max(s.Start, c.start()), min(s.End, c.end())
					if from >= to {
						continue
					}
					fresh.insert(c.slice(from-c.start(), to-c.start()), false, 0, d)
				}
			}
		}
		for _, p := range fresh.sortedPeers() {
			for _, b := range fresh.peers[p] {
				out = append(out, b.changes)
			}
		}
	}
	return out
}
