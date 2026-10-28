package loro

import (
	"fmt"
	"strings"

	"github.com/Deln0r/loro-go/encoding/change"
)

// ExpandType says whether text inserted at the edge of a style takes the
// style (loro's ExpandType, container/richtext.rs:169).
type ExpandType uint8

const (
	// ExpandAfter: text inserted right after the styled range takes the
	// style.
	ExpandAfter ExpandType = iota
	// ExpandBefore: text inserted right before the styled range takes the
	// style.
	ExpandBefore
	// ExpandBoth: text inserted at either edge takes the style.
	ExpandBoth
	// ExpandNone: text inserted at an edge does not take the style.
	ExpandNone
)

func (e ExpandType) String() string {
	switch e {
	case ExpandAfter:
		return "after"
	case ExpandBefore:
		return "before"
	case ExpandBoth:
		return "both"
	case ExpandNone:
		return "none"
	}
	return fmt.Sprintf("ExpandType(%d)", uint8(e))
}

// reverse is ExpandType::reverse (container/richtext.rs:218): what an unmark
// uses, so that text inserted at the edge of an unmarked range keeps the
// style when the mark would have spread to it and loses it otherwise.
// Before and After stay as they are; Both and None swap.
func (e ExpandType) reverse() ExpandType {
	switch e {
	case ExpandBoth:
		return ExpandNone
	case ExpandNone:
		return ExpandBoth
	}
	return e
}

// infoFlag is TextStyleInfoFlag::new (container/richtext.rs:265): the alive
// bit and the two expand bits a mark op carries.
func (e ExpandType) infoFlag() uint8 {
	f := uint8(0x80)
	if e == ExpandBefore || e == ExpandBoth {
		f |= expandBeforeFlag
	}
	if e == ExpandAfter || e == ExpandBoth {
		f |= expandAfterFlag
	}
	return f
}

// defaultTextStyles is StyleConfigMap::default_rich_text_config
// (container/richtext/config.rs:52), what a new loro document starts with.
func defaultTextStyles() map[string]ExpandType {
	return map[string]ExpandType{
		"bold":      ExpandAfter,
		"italic":    ExpandAfter,
		"underline": ExpandAfter,
		"link":      ExpandNone,
		"highlight": ExpandNone,
		"comment":   ExpandNone,
		"code":      ExpandNone,
	}
}

// ConfigTextStyle replaces the style keys the document can mark with and how
// each expands, as loro's config_text_style does (loro.rs:575): keys left
// out, the defaults included, can no longer be marked. A document starts
// with bold, italic and underline expanding after, and link, highlight,
// comment and code expanding to neither side. A key with a colon is marked
// with the configuration of the part before it, so "comment" also configures
// "comment:123"; a configured key itself cannot hold a colon, which loro
// refuses too (it panics, config.rs:22).
func (d *Document) ConfigTextStyle(styles map[string]ExpandType) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	next := make(map[string]ExpandType, len(styles))
	for k, e := range styles {
		if strings.Contains(k, ":") {
			return fmt.Errorf("loro: style key %q holds a colon; configure the part before it", k)
		}
		if e > ExpandNone {
			return fmt.Errorf("loro: style key %q has no expand type %d", k, uint8(e))
		}
		next[k] = e
	}
	if d.knocked(ruleConfigReplaces) {
		for k, e := range d.textStyles {
			if _, ok := next[k]; !ok {
				next[k] = e
			}
		}
	}
	d.textStyles = next
	return nil
}

// styleFlag is StyleConfigMap::get_style_flag and get_style_flag_for_unmark
// (config.rs:32): the info byte of a mark of key, looked up by the part of
// the key before its first colon, reversed for an unmark; false when the key
// is not configured.
func (d *Document) styleFlag(key string, unmark bool) (uint8, bool) {
	look := key
	if i := strings.IndexByte(key, ':'); i >= 0 && !d.knocked(ruleColonPrefix) {
		look = key[:i]
	}
	e, ok := d.textStyles[look]
	if !ok {
		return 0, false
	}
	if unmark && !d.knocked(ruleUnmarkReverse) {
		e = e.reverse()
	}
	return e.infoFlag(), true
}

// Mark styles the code points [start, end) with key set to value: a mark
// op and its end op, as loro's mark makes them (handler.rs:2534). Both ends
// go where text inserted at them would go, among the anchors of other marks
// by their expand rules. A null value unmarks.
//
// As in loro, a mark that changes nothing makes no op: one whose key every
// character and anchor in the range already has with an equal value, and
// an unmark of a key nothing in the range has, whether the key is
// configured or not. A mark of a key the document has not configured (see
// ConfigTextStyle) is an error.
func (t *Text) Mark(start, end int, key string, value any) error {
	d := t.h.d
	d.mu.Lock()
	defer d.mu.Unlock()
	return t.mark(start, end, key, value)
}

// Unmark removes the style key from the code points [start, end): a mark of
// key with a null value, whose expand rule is the reverse of the key's.
func (t *Text) Unmark(start, end int, key string) error {
	d := t.h.d
	d.mu.Lock()
	defer d.mu.Unlock()
	return t.mark(start, end, key, nil)
}

func (t *Text) mark(start, end int, key string, value any) error {
	d := t.h.d
	if start < 0 || start >= end {
		return fmt.Errorf("loro: a mark needs start < end, got %d..%d", start, end)
	}
	if err := d.editable(t.h.k); err != nil {
		return err
	}
	val, err := storeValue(value)
	if err != nil {
		return err
	}
	if !utf8Strings(key) {
		return fmt.Errorf("loro: style key %q is not UTF-8", key)
	}
	st := t.h.state()
	length := st.textLen(t.h.k)
	if int64(end) > length {
		return outOfRange(start, end-start, int(length))
	}
	unmark := val == nil
	es := st.textInsertIndex(t.h.k, int64(start))
	ee := st.textInsertIndex(t.h.k, int64(end))
	if d.knocked(ruleMarkRange) {
		es, ee = int64(start), int64(end)
	}
	applied, present := st.styleOver(t.h.k, es, ee, key, val)
	if applied && !d.knocked(ruleMarkSkipApplied) {
		return nil
	}
	if unmark && !present && !d.knocked(ruleUnmarkSkipAbsent) {
		return nil
	}
	flag, ok := d.styleFlag(key, unmark)
	if !ok {
		if !d.knocked(ruleMarkConfigured) {
			return fmt.Errorf("loro: style key %q is not configured; see ConfigTextStyle", key)
		}
		flag = ExpandAfter.infoFlag()
	}
	op := t.h.op(change.VKMarkStart)
	op.Pos = es
	op.Value = MarkInfo{Start: es, Len: ee - es, Key: key, Value: val, Info: flag}
	if err := d.apply(st, op, opProv{}); err != nil {
		return err
	}
	return d.apply(st, t.h.op(change.VKNull), opProv{})
}

// styleOver reports, for the entities [es, ee) of Text k (characters and
// anchors, by index among those no delete removed), whether every one of
// them has key with a value equal to value (range_has_key_value,
// style_range_map.rs:218), and whether any has key at all, with any value
// (range_contains_key). An entity has a key when a mark of it covers the
// entity, from its start anchor to its end anchor, both included; of
// several such marks the one with the later (lamport, peer) decides the
// value. A mark counts once both its anchors are in, as toDelta counts it.
func (st *docState) styleOver(k contKey, es, ee int64, key string, value any) (applied, present bool) {
	s := st.seqs[k]
	if s == nil || es >= ee {
		return false, false
	}
	complete := s.completeMarks()
	open := map[ID]markStart{}
	applied = true
	var at int64
	for _, n := range s.nodes {
		if len(n.deleters) != 0 {
			continue
		}
		var closing ID
		closes := false
		if n.anchor {
			switch v := n.value.(type) {
			case markStart:
				if id := (ID{Peer: n.peer, Counter: n.counter}); complete[id] {
					open[id] = v
				}
			case markEnd:
				closing = ID{Peer: n.peer, Counter: n.counter - 1}
				_, closes = open[closing]
			}
		}
		if at >= es && at < ee {
			var win markStart
			found := false
			for _, m := range open {
				if m.info.Key == key && (!found || m.w.after(win.w)) {
					win, found = m, true
				}
			}
			if !found {
				applied = false
			} else {
				present = true
				if !valueEqual(win.info.Value, value) {
					applied = false
				}
			}
		}
		if closes {
			delete(open, closing)
		}
		at++
		if at >= ee {
			break
		}
	}
	return applied, present
}

// completeMarks is the start anchors of the marks whose two anchors are
// both in the sequence and neither deleted.
func (s *seqState) completeMarks() map[ID]bool {
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
	return complete
}
