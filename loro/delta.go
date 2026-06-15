package loro

import (
	"fmt"
	"sort"

	"github.com/Deln0r/loro-go/encoding/change"
)

// DeltaOp is one run of a rich-text delta: a string with the style attributes
// active over it (nil when unstyled). This mirrors loro's text.toDelta().
type DeltaOp struct {
	Insert     string
	Attributes map[string]any
}

// element kinds in the anchor-aware text sequence.
const (
	elChar = iota
	elMarkStart
	elMarkEnd
)

type tElem struct {
	kind   int
	r      string
	key    string
	value  any
	markID int64
}

// TextDelta reconstructs the Quill-style delta for a Text container, matching
// loro's toDelta(). Loro encodes a mark as a span [start, start+len) in a
// coordinate space that INCLUDES earlier marks' anchor elements, so the delta
// cannot be derived from visible positions alone. We rebuild the sequence with
// mark anchors as real elements (start anchor at op `start`, end anchor at
// `start+len`), then walk it tracking active styles. The trailing mark_end op
// (Null, position 0) carries no usable position and is ignored; the span comes
// from the MarkStart op.
//
// Exact for non-concurrent histories. Mark anchoring under concurrent edits
// (expand rules) and deletes inside marked ranges are not yet modeled.
func TextDelta(u *Updates, container string) ([]DeltaOp, error) {
	var ops []Op
	for _, ch := range u.Changes {
		for _, op := range ch.Ops {
			if op.Container != container || op.Kind != change.CText {
				continue
			}
			if op.VKind == change.VKStr || op.VKind == change.VKMarkStart {
				ops = append(ops, op)
			}
		}
	}
	ops = sortedOps(ops) // (lamport, peer, counter): replay order

	var seq []tElem
	for _, op := range ops {
		switch op.VKind {
		case change.VKStr:
			s, ok := op.Value.(string)
			if !ok {
				return nil, fmt.Errorf("loro: text op value is %T, want string", op.Value)
			}
			rs := []rune(s)
			ins := make([]tElem, len(rs))
			for k, r := range rs {
				ins[k] = tElem{kind: elChar, r: string(r)}
			}
			seq = splice(seq, int(op.Pos), ins...)
		case change.VKMarkStart:
			mi, ok := op.Value.(MarkInfo)
			if !ok {
				continue
			}
			start := int(mi.Start)
			end := start + int(mi.Len)
			// end anchor first; inserting the start anchor then shifts it right.
			seq = splice(seq, end, tElem{kind: elMarkEnd, markID: op.Counter})
			seq = splice(seq, start, tElem{kind: elMarkStart, key: mi.Key, value: mi.Value, markID: op.Counter})
		}
	}

	active := map[int64]MarkInfo{}
	var out []DeltaOp
	for _, e := range seq {
		switch e.kind {
		case elMarkStart:
			active[e.markID] = MarkInfo{Key: e.key, Value: e.value}
		case elMarkEnd:
			delete(active, e.markID)
		case elChar:
			attrs := activeAttrs(active)
			if len(out) > 0 && sameAttrs(out[len(out)-1].Attributes, attrs) {
				out[len(out)-1].Insert += e.r
				continue
			}
			out = append(out, DeltaOp{Insert: e.r, Attributes: attrs})
		}
	}
	return out, nil
}

// activeAttrs flattens the active marks into an attribute map (nil if none).
// Same-key marks resolve by ascending markID so the later mark wins.
func activeAttrs(active map[int64]MarkInfo) map[string]any {
	if len(active) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(active))
	for id := range active {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	out := map[string]any{}
	for _, id := range ids {
		m := active[id]
		out[m.Key] = m.Value
	}
	return out
}

func splice(s []tElem, at int, items ...tElem) []tElem {
	if at < 0 {
		at = 0
	}
	if at > len(s) {
		at = len(s)
	}
	out := make([]tElem, 0, len(s)+len(items))
	out = append(out, s[:at]...)
	out = append(out, items...)
	out = append(out, s[at:]...)
	return out
}

func sameAttrs(a, b map[string]any) bool {
	if len(a) != len(b) {
		return false
	}
	for k, va := range a {
		if vb, ok := b[k]; !ok || vb != va {
			return false
		}
	}
	return true
}
