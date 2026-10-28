package loro

import (
	"bytes"
	"reflect"
	"slices"
	"strings"

	"github.com/Deln0r/loro-go/encoding/change"
)

// DeltaOp is one run of a rich-text delta: a string with the style attributes
// active over it (nil when unstyled). This mirrors loro's text.toDelta().
type DeltaOp struct {
	Insert     string
	Attributes map[string]any
}

// markStart is the value of a mark's start anchor in a merged Text: the mark,
// and its writer, which decides between overlapping marks of one key.
type markStart struct {
	info MarkInfo
	w    writer
}

// markEnd is the value of a mark's end anchor. It belongs to the mark whose
// start anchor has the id just before its own.
type markEnd struct{}

// TextDelta returns a Text container's rich-text delta, as loro's
// text.toDelta() gives it: the text MergeState shows for the container, split
// into runs by the styles over it. It merges u as MergeState does, so it
// assumes the same of u and stops at the same step limit.
//
// container names the Text the way loro's container id does. A root Text is
// named by its name, and so is a mergeable one (map.ensureMergeableText),
// whose root name loro builds from the path to it: "🤝:$m>body" for key
// "body" of root map "m". A Text nested in a map, a list or a MovableList is
// named by its id, "counter@peer", the id of the op or list item that placed
// it. A root name is looked up first, so a root Text that happens to be
// called "3@1" hides the nested Text with that id. The result is the
// container's delta whether or not the document still shows it.
//
// A mark is a pair of anchors in the merged sequence, placed like characters,
// and it styles the text between them. A mark whose end anchor is missing, as
// in an export that stops between the two, or was deleted styles nothing, as
// in loro. Where marks of one key overlap, the one with the later (lamport,
// peer) decides the value. A null value, which an unmark writes, leaves the key
// out of the run's attributes, but the text under it stays a run of its own:
// loro keeps it apart from text that no mark of that key covers. Adjacent runs
// with equal styles are one run; values are equal when their contents are.
//
// Checked against loro-crdt's toDelta on three corpora of 300 random histories
// each in testdata/fixtures: Text with bold and italic marks by one author and
// by two or three peers editing concurrently, and rich text with marks of all
// four expand rules, string, object and array values, unmarks, inserts at the
// edges of styled runs and deletes of the whole text, by one to three peers.
// All 900 match.
func TextDelta(u *Updates, container string) ([]DeltaOp, error) {
	c := collect(u)
	seqs, err := mergeSequences(c.copies, c.units, &seqWork{limit: seqWorkLimit})
	if err != nil {
		return nil, err
	}
	s := seqs[contKey{root: true, name: container, kind: change.CText}]
	if s == nil {
		s = seqs[contKey{name: container, kind: change.CText}]
	}
	if s == nil {
		return nil, nil
	}
	return s.delta()
}

// delta renders a merged Text as TextDelta describes. Styles change only at
// anchors, so they are worked out there, and the characters between two
// anchors go into the current run as they come.
func (s *seqState) delta() ([]DeltaOp, error) {
	// A mark counts when both of its anchors are in, the start first, and
	// neither was deleted.
	begun := map[ID]bool{}
	complete := map[ID]bool{}
	for _, n := range s.nodes {
		if !n.anchor || len(n.deleters) != 0 {
			continue
		}
		switch n.value.(type) {
		case markStart:
			begun[ID{Peer: n.peer, Counter: n.counter}] = true
		case markEnd:
			if start := (ID{Peer: n.peer, Counter: n.counter - 1}); begun[start] {
				complete[start] = true
			}
		}
	}
	if err := s.work.spend(len(s.nodes)); err != nil {
		return nil, err
	}

	open := map[ID]*markStart{}        // marks begun and not yet ended
	byKey := map[string][]*markStart{} // the open marks of each key
	winner := map[string]*markStart{}  // the open mark that decides each key
	changed := false
	settle := func(key string) error {
		var win *markStart
		for _, m := range byKey[key] {
			if win == nil || m.w.after(win.w) {
				win = m
			}
		}
		if win == nil {
			delete(winner, key)
		} else {
			winner[key] = win
		}
		changed = true
		// A key is hashed at each of its marks' anchors, and a long key costs
		// its length each time.
		return s.work.spend(len(byKey[key]) + 1 + len(key))
	}

	// A run's styles are every key a mark decides, a null value included;
	// its attributes leave the null ones out.
	var out []DeltaOp
	var run strings.Builder
	var runStyles map[string]any
	flush := func() {
		if run.Len() == 0 {
			return
		}
		var attrs map[string]any
		for key, v := range runStyles {
			if v == nil {
				continue
			}
			if attrs == nil {
				attrs = map[string]any{}
			}
			attrs[key] = v
		}
		out = append(out, DeltaOp{Insert: run.String(), Attributes: attrs})
		run.Reset()
	}
	for _, n := range s.nodes {
		if len(n.deleters) != 0 {
			continue
		}
		if !n.anchor {
			str, _ := n.value.(string)
			if changed {
				changed = false
				styles := make(map[string]any, len(winner))
				cost := len(winner)
				for key, m := range winner {
					styles[key] = m.info.Value
					cost += len(key)
				}
				if !sameAttrs(runStyles, styles, &cost) {
					flush()
					runStyles = styles
				}
				if err := s.work.spend(cost); err != nil {
					return nil, err
				}
			}
			run.WriteString(str)
			continue
		}
		switch v := n.value.(type) {
		case markStart:
			id := ID{Peer: n.peer, Counter: n.counter}
			if !complete[id] {
				continue
			}
			open[id] = &v
			byKey[v.info.Key] = append(byKey[v.info.Key], &v)
			if err := settle(v.info.Key); err != nil {
				return nil, err
			}
		case markEnd:
			id := ID{Peer: n.peer, Counter: n.counter - 1}
			m, ok := open[id]
			if !ok {
				continue
			}
			delete(open, id)
			key := m.info.Key
			if i := slices.Index(byKey[key], m); i >= 0 {
				byKey[key] = slices.Delete(byKey[key], i, i+1)
			}
			if err := settle(key); err != nil {
				return nil, err
			}
		}
	}
	flush()
	return out, nil
}

// sameAttrs reports whether two runs' styles are equal, adding the elements it
// compared to cost.
func sameAttrs(a, b map[string]any, cost *int) bool {
	if len(a) != len(b) {
		return false
	}
	for k, va := range a {
		vb, ok := b[k]
		if !ok || !sameValue(va, vb, cost) {
			return false
		}
	}
	return true
}

// sameValue compares two style values by content, as loro does: lists and
// maps element by element, and NaN equal to NaN. A list or map compared with
// itself is equal at once, which is the usual case: a style that carries on
// across an anchor is the same value on both sides. It adds what it compared
// to cost, an element at a time and a byte at a time for keys and strings.
func sameValue(a, b any, cost *int) bool {
	*cost++
	switch x := a.(type) {
	case float64:
		y, ok := b.(float64)
		return ok && (x == y || x != x && y != y)
	case string:
		y, ok := b.(string)
		*cost += len(x)
		return ok && x == y
	case []byte:
		y, ok := b.([]byte)
		*cost += len(x)
		return ok && bytes.Equal(x, y)
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		if len(x) == 0 || &x[0] == &y[0] {
			return true
		}
		for i := range x {
			if !sameValue(x[i], y[i], cost) {
				return false
			}
		}
		return true
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok || len(x) != len(y) {
			return false
		}
		if reflect.ValueOf(x).UnsafePointer() == reflect.ValueOf(y).UnsafePointer() {
			return true
		}
		for k, v := range x {
			*cost += len(k)
			w, ok := y[k]
			if !ok || !sameValue(v, w, cost) {
				return false
			}
		}
		return true
	}
	return reflect.DeepEqual(a, b)
}
