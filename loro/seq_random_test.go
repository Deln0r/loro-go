package loro

import (
	"math/rand"
	"reflect"
	"strings"
	"testing"

	"github.com/Deln0r/loro-go/encoding/change"
)

// simHistory builds a random history of sequence edits by several peers that
// edit their own replicas and now and then sync. Every edit takes its position
// from the author's replica as the reference renders it, and every change
// records the author's frontier as its deps, so the history is shaped the way
// loro shapes one: inserts and deletes after imports, concurrent edits, and
// changes that depend on other peers' changes.
func simHistory(r *rand.Rand, npeers, steps int, isText bool) []Change {
	type replica struct {
		id      uint64
		known   []int // indexes into changes
		counter int64
		next    int64 // lamport for the next change
	}
	var changes []Change
	peers := make([]*replica, npeers)
	for i := range peers {
		peers[i] = &replica{id: uint64(i + 1)}
	}
	knownChanges := func(p *replica, extra *Change) []Change {
		out := make([]Change, 0, len(p.known)+1)
		for _, i := range p.known {
			out = append(out, changes[i])
		}
		if extra != nil {
			out = append(out, *extra)
		}
		return out
	}
	// frontier is the set of maximal op ids the replica holds.
	frontier := func(p *replica) []ID {
		known := knownChanges(p, nil)
		vvOf := refVersions(known)
		last := map[uint64]ID{}
		owner := map[uint64]Change{}
		for _, c := range known {
			end := c.ID.Counter + changeAtoms(c) - 1
			if cur, ok := last[c.ID.Peer]; !ok || end > cur.Counter {
				last[c.ID.Peer] = ID{Peer: c.ID.Peer, Counter: end}
				owner[c.ID.Peer] = c
			}
		}
		var out []ID
		for q, x := range last {
			dominated := false
			for q2 := range last {
				if q2 != q && vvOf(owner[q2]).has(x.Peer, x.Counter) {
					dominated = true
					break
				}
			}
			if !dominated {
				out = append(out, x)
			}
		}
		sortIDs(out)
		return out
	}
	kind, insKind := change.CList, change.VKLoroValue
	if isText {
		kind, insKind = change.CText, change.VKStr
	}

	for step := 0; step < steps; step++ {
		p := peers[r.Intn(npeers)]
		if r.Intn(10) < 3 && npeers > 1 {
			// Sync: p imports everything another replica holds.
			q := peers[r.Intn(npeers)]
			have := map[int]bool{}
			for _, i := range p.known {
				have[i] = true
			}
			for _, i := range q.known {
				if !have[i] {
					p.known = append(p.known, i)
					c := changes[i]
					if end := c.Lamport + changeAtoms(c); end > p.next {
						p.next = end
					}
				}
			}
			continue
		}
		ch := Change{ID: ID{Peer: p.id, Counter: p.counter}, Lamport: p.next, Deps: frontier(p)}
		for nops := 1 + r.Intn(2); nops > 0; nops-- {
			view := causalRefSeq(knownChanges(p, &ch), "t", isText)
			op := Op{Container: "t", IsRoot: true, Kind: kind, Peer: p.id, Counter: p.counter}
			op.Lamport = ch.Lamport + (op.Counter - ch.ID.Counter)
			if len(view) > 0 && r.Intn(100) < 35 {
				pos := r.Intn(len(view))
				k := 1
				for k < 3 && pos+k < len(view) && view[pos+k].peer == view[pos].peer && view[pos+k].counter == view[pos].counter+int64(k) {
					k++
				}
				span := DeleteSpan{Peer: view[pos].peer, Counter: view[pos].counter, Len: int64(k)}
				op.Pos = int64(pos)
				if r.Intn(5) == 0 {
					// Backwards, as backspace runs: same lowest id, negative
					// length, recorded at the last element it removes.
					span.Len = -int64(k)
					op.Pos = int64(pos + k - 1)
				}
				op.VKind, op.Value, op.Len = change.VKDeleteSeq, span, int64(k)
			} else {
				n := 1 + r.Intn(3)
				op.VKind, op.Pos, op.Len = insKind, int64(r.Intn(len(view)+1)), int64(n)
				if isText {
					var sb strings.Builder
					for i := 0; i < n; i++ {
						sb.WriteByte(byte('a' + r.Intn(26)))
					}
					op.Value = sb.String()
				} else {
					items := make([]any, n)
					for i := range items {
						items[i] = int64(r.Intn(1000))
					}
					op.Value = items
				}
			}
			ch.Ops = append(ch.Ops, op)
			p.counter += op.Len
		}
		p.next = ch.Lamport + changeAtoms(ch)
		changes = append(changes, ch)
		p.known = append(p.known, len(changes)-1)
	}
	return changes
}

// TestMergeMatchesCausalReference drives MergeState with random multi-peer
// histories, in shuffled order, and requires the reference's answer. The
// reference is held to loro-crdt itself by TestCausalRefMatchesLoro.
func TestMergeMatchesCausalReference(t *testing.T) {
	postSync, afterDelete := 0, 0
	for seed := 0; seed < 300; seed++ {
		r := rand.New(rand.NewSource(int64(seed)))
		isText := seed%2 == 0
		changes := simHistory(r, 2+r.Intn(2), 4+r.Intn(24), isText)
		for _, c := range changes {
			sawDelete := false
			for _, d := range c.Deps {
				if d.Peer != c.ID.Peer {
					postSync++
					break
				}
			}
			for _, op := range c.Ops {
				if op.VKind == change.VKDeleteSeq {
					sawDelete = true
				} else if sawDelete {
					afterDelete++
				}
			}
		}
		want := causalRefSeq(changes, "t", isText)
		shuffled := append([]Change(nil), changes...)
		r.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
		state, err := MergeState(&Updates{Changes: shuffled})
		if err != nil {
			t.Fatalf("seed %d: %v", seed, err)
		}
		got := state["t"]
		w := refRender(want, isText)
		if got == nil && (w == "" || reflect.DeepEqual(w, []any{})) {
			continue
		}
		if !reflect.DeepEqual(got, w) {
			t.Errorf("seed %d (%d changes): merge gives %v, reference gives %v", seed, len(changes), got, w)
		}
	}
	if postSync == 0 || afterDelete == 0 {
		t.Fatalf("histories lack post-sync changes (%d) or inserts after a delete in one change (%d)", postSync, afterDelete)
	}
	t.Logf("%d changes with deps on other peers, %d inserts after a delete in the same change", postSync, afterDelete)
}
