package loro

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"

	"github.com/Deln0r/loro-go/encoding/change"
)

// ContainerKind is the kind of a container: Text, Map, List, MovableList,
// Tree or Counter.
type ContainerKind = change.ContainerType

// The six container kinds.
const (
	KindMap         ContainerKind = change.CMap
	KindList        ContainerKind = change.CList
	KindText        ContainerKind = change.CText
	KindTree        ContainerKind = change.CTree
	KindMovableList ContainerKind = change.CMovableList
	KindCounter     ContainerKind = change.CCounter
)

// Container is a handle on a container of any kind: *Text, *Map, *List,
// *MovableList, *Tree or *Counter.
type Container interface {
	// Kind is the container's kind.
	Kind() ContainerKind
	// ID is the container's id as loro writes it: "cid:root-name:Kind"
	// for a root container, "cid:counter@peer:Kind" for one stored in
	// another, the counter and peer of the op that placed it.
	ID() string
	// IsDeleted reports whether the container is gone from the document:
	// a container stored in another is deleted once the value holding it
	// is overwritten or deleted, or a container holding it is. Its
	// contents stay readable; edits through it are refused.
	IsDeleted() bool
	// JSON is the container's value as toJSON shows it, the containers in
	// it included.
	JSON() (any, error)
}

// handleOf returns the handle for container k.
func (d *Document) handleOf(k contKey) Container {
	h := handle{d: d, k: k, st: d.st}
	switch k.kind {
	case KindText:
		return &Text{h}
	case KindMap:
		return &Map{h}
	case KindList:
		return &List{h}
	case KindMovableList:
		return &MovableList{h}
	case KindTree:
		return &Tree{h}
	}
	return &Counter{h}
}

func (h handle) kind() ContainerKind { return h.k.kind }

func (h handle) id() string { return h.k.String() }

func (h handle) isDeleted() bool {
	h.d.mu.Lock()
	defer h.d.mu.Unlock()
	return h.d.deleted(h.k)
}

func (h handle) json() (any, error) {
	h.d.mu.Lock()
	defer h.d.mu.Unlock()
	st := h.state()
	r := &resolver{conts: st.conts, seqs: st.seqs, vals: map[contKey]any{}, state: map[contKey]uint8{}}
	if err := r.resolve([]contKey{h.k}); err != nil {
		return nil, err
	}
	return copyValue(r.vals[h.k]), nil
}

// Kind returns KindText.
func (t *Text) Kind() ContainerKind { return t.h.kind() }

// ID returns the container's id.
func (t *Text) ID() string { return t.h.id() }

// IsDeleted reports whether the container is gone from the document.
func (t *Text) IsDeleted() bool { return t.h.isDeleted() }

// JSON returns the text.
func (t *Text) JSON() (any, error) { return t.h.json() }

// Kind returns KindMap.
func (m *Map) Kind() ContainerKind { return m.h.kind() }

// ID returns the container's id.
func (m *Map) ID() string { return m.h.id() }

// IsDeleted reports whether the container is gone from the document.
func (m *Map) IsDeleted() bool { return m.h.isDeleted() }

// JSON returns the map's value, the containers in it included.
func (m *Map) JSON() (any, error) { return m.h.json() }

// Kind returns KindList.
func (l *List) Kind() ContainerKind { return l.h.kind() }

// ID returns the container's id.
func (l *List) ID() string { return l.h.id() }

// IsDeleted reports whether the container is gone from the document.
func (l *List) IsDeleted() bool { return l.h.isDeleted() }

// JSON returns the list's items, the containers in it included.
func (l *List) JSON() (any, error) { return l.h.json() }

// Kind returns KindCounter.
func (c *Counter) Kind() ContainerKind { return c.h.kind() }

// ID returns the container's id.
func (c *Counter) ID() string { return c.h.id() }

// IsDeleted reports whether the container is gone from the document.
func (c *Counter) IsDeleted() bool { return c.h.isDeleted() }

// JSON returns the counter's value.
func (c *Counter) JSON() (any, error) { return c.h.json() }

// GetContainer returns a handle on the container with the given id, as ID
// gives it: "cid:root-name:Kind" or "cid:counter@peer:Kind". Taking a root
// container's handle makes it part of the document, as the root getters
// do; a container stored in another must be one the document knows.
func (d *Document) GetContainer(cid string) (Container, error) {
	rest, ok := strings.CutPrefix(cid, "cid:")
	if !ok {
		return nil, fmt.Errorf("loro: %q is not a container id", cid)
	}
	i := strings.LastIndexByte(rest, ':')
	if i < 0 {
		return nil, fmt.Errorf("loro: %q is not a container id", cid)
	}
	kind, ok := kindByName(rest[i+1:])
	if !ok {
		return nil, fmt.Errorf("loro: %q names no container kind loro writes", cid)
	}
	name := rest[:i]
	if root, ok := strings.CutPrefix(name, "root-"); ok {
		h := d.handle(root, kind)
		return d.handleOf(h.k), nil
	}
	id, err := parseStoredID(name)
	if err != nil {
		return nil, fmt.Errorf("loro: %q: %w", cid, err)
	}
	k := childKey(id, kind)
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.opAt(id); !ok {
		return nil, fmt.Errorf("loro: the document has no container %s", cid)
	}
	return d.handleOf(k), nil
}

// editableKind reports whether kind is one of the six: an imported change
// can store a container of any kind byte, which loro keeps as an Unknown
// container that no handler edits.
func editableKind(kind ContainerKind) bool { return kind <= KindCounter }

func kindByName(s string) (ContainerKind, bool) {
	for k := KindMap; k <= KindCounter; k++ {
		if k.String() == s {
			return k, true
		}
	}
	return 0, false
}

// opAt returns the op holding id, applied or in the open transaction.
func (d *Document) opAt(id ID) (Op, bool) {
	if t := d.tx; t != nil && id.Peer == d.peer {
		for _, op := range t.ops {
			if op.Counter <= id.Counter && id.Counter < op.Counter+op.Len {
				return op, true
			}
		}
	}
	c := d.log.store.find(id)
	if c == nil {
		return Op{}, false
	}
	for _, op := range c.ch.Ops {
		if op.Counter <= id.Counter && id.Counter < op.Counter+op.Len {
			return op, true
		}
	}
	return Op{}, false
}

// deleted is DocState::is_deleted (state/dead_containers_cache.rs:47): a
// container is deleted when, walking from it to a root through the
// containers that hold it, one no longer holds the one below it. A root
// container is never deleted; a mergeable child is held by its parent map
// while the map's value at its key is its marker; any other container is
// held by the container of the op that placed it, while that op's value is
// still there.
func (d *Document) deleted(k contKey) bool {
	for steps := 0; ; steps++ {
		if k.root && !isMergeableName(k.name) {
			return false
		}
		parent, held := d.holder(k)
		if !held || steps > 1<<16 {
			return true
		}
		k = parent
	}
}

// holder returns the container that holds k and whether it holds it now.
func (d *Document) holder(k contKey) (contKey, bool) {
	st := d.st
	if k.root {
		parent, key, ok := mergeableParentOf(k.name)
		if !ok {
			return contKey{}, false
		}
		op, set := st.mapEntry(parent, key)
		if !set {
			return parent, false
		}
		v, isBin := op.Value.([]byte)
		if !isBin {
			return parent, false
		}
		child, ok := newMergeableParent(parent).child(key, v)
		return parent, ok && child == k
	}
	id, err := parseStoredID(k.name)
	if err != nil {
		return contKey{}, false
	}
	op, ok := d.opAt(id)
	if !ok {
		return contKey{}, false
	}
	parent := keyOf(op)
	switch {
	case op.Kind == KindMap && op.VKind == change.VKLoroValue:
		win, set := st.mapEntry(parent, op.MapKey)
		ct, isCont := win.Value.(change.ContainerType)
		return parent, set && win.Peer == id.Peer && win.Counter == id.Counter && isCont && ct == k.kind
	case op.Kind == KindList && op.VKind == change.VKLoroValue:
		s := st.seqs[parent]
		if s == nil {
			return parent, false
		}
		n := s.byID[id]
		if n == nil || len(n.deleters) != 0 {
			return parent, false
		}
		ct, isCont := n.value.(change.ContainerType)
		return parent, isCont && ct == k.kind
	case op.Kind == KindMovableList:
		s := st.seqs[parent]
		if s == nil {
			return parent, false
		}
		var elem ListElem
		switch v := op.Value.(type) {
		case ListSetValue:
			elem = v.Elem
		case []any:
			elem = ListElem{Peer: op.Peer, Lamport: op.Lamport + (id.Counter - op.Counter)}
		default:
			return parent, false
		}
		e := s.elems[elem]
		if e == nil || e.valueID != id || e.pos == nil || len(e.pos.deleters) != 0 {
			return parent, false
		}
		ct, isCont := e.value.(change.ContainerType)
		return parent, isCont && ct == k.kind
	case op.Kind == KindTree && k.kind == KindMap:
		// A tree node's meta map, named by the node.
		return parent, st.tree(parent).alive(k.name)
	}
	return parent, false
}

// mergeableParentOf parses a mergeable child's root name back into its
// parent map and key: loro-common's parse_mergeable. The path's last
// unescaped ">" splits the parent's path from the key; a parent path with
// another ">" is itself a mergeable map.
func mergeableParentOf(name string) (contKey, string, bool) {
	payload, ok := strings.CutPrefix(name, mergeablePrefix)
	if !ok || !validMergeablePayload(payload) {
		return contKey{}, "", false
	}
	cut, first := -1, -1
	escaped := false
	for i, c := range payload {
		switch {
		case escaped:
			escaped = false
		case c == '\\':
			escaped = true
		case c == '>':
			if first < 0 {
				first = i
			}
			cut = i
		}
	}
	key := unescapeMergeable(payload[cut+1:])
	base := payload[:cut]
	if cut != first {
		return contKey{root: true, name: mergeablePrefix + base, kind: KindMap}, key, true
	}
	if root, ok := strings.CutPrefix(base, "$"); ok {
		return contKey{root: true, name: unescapeMergeable(root), kind: KindMap}, key, true
	}
	peer36, ctr36, _ := strings.Cut(strings.TrimPrefix(base, "@"), ":")
	peer, err1 := strconv.ParseUint(peer36, 36, 64)
	ctr, err2 := strconv.ParseInt(ctr36, 36, 64)
	if err1 != nil || err2 != nil {
		return contKey{}, "", false
	}
	return childKey(ID{Peer: peer, Counter: ctr}, KindMap), key, true
}

// unescapeMergeable reverses escapeMergeable.
func unescapeMergeable(s string) string {
	var b strings.Builder
	escaped := false
	for _, c := range s {
		if !escaped && c == '\\' {
			escaped = true
			continue
		}
		if escaped {
			switch c {
			case 's':
				c = '/'
			case '0':
				c = 0
			}
			escaped = false
		}
		b.WriteRune(c)
	}
	return b.String()
}

// mergeableChild returns the mergeable child of kind at key of map parent,
// and the marker that activates it: loro-common's new_mergeable and
// mergeable_marker. false when the parent cannot hold one.
func mergeableChild(parent contKey, key string, kind ContainerKind) (contKey, []byte, bool) {
	p := newMergeableParent(parent)
	if !p.ok {
		return contKey{}, nil, false
	}
	marker := append(append([]byte(nil), mergeableMagic[:]...), byte(kind), 0, 0, 0)
	crc := mergeableCRC(p, key, kind)
	marker[5], marker[6], marker[7] = byte(crc>>16), byte(crc>>8), byte(crc)
	child, ok := p.child(key, marker)
	return child, marker, ok
}

// SetContainer stores a new container of kind at key and returns a handle
// on it. The container is named by the map write's id; a container stored
// before at the key, and every container in it, is deleted.
func (m *Map) SetContainer(key string, kind ContainerKind) (Container, error) {
	d := m.h.d
	d.mu.Lock()
	defer d.mu.Unlock()
	if !editableKind(kind) {
		return nil, fmt.Errorf("loro: no container kind %d", kind)
	}
	if err := d.editable(m.h.k); err != nil {
		return nil, err
	}
	st := m.h.state()
	op := m.h.op(change.VKLoroValue)
	op.MapKey = key
	op.Value = kind
	id := ID{Peer: d.peer, Counter: d.begin().nextCounter}
	if err := d.apply(st, op, opProv{}); err != nil {
		return nil, err
	}
	return d.placed(id, kind)
}

// GetContainer returns the container stored at key, false when the value
// there is no container. A mergeable child's marker gives its child.
func (m *Map) GetContainer(key string) (Container, bool) {
	d := m.h.d
	d.mu.Lock()
	defer d.mu.Unlock()
	op, ok := m.h.state().mapEntry(m.h.k, key)
	if !ok {
		return nil, false
	}
	switch v := op.Value.(type) {
	case change.ContainerType:
		if editableKind(v) {
			return d.handleOf(childKey(ID{Peer: op.Peer, Counter: op.Counter}, v)), true
		}
	case []byte:
		if child, ok := newMergeableParent(m.h.k).child(key, v); ok {
			return d.handleOf(child), true
		}
	}
	return nil, false
}

// EnsureMergeable returns the mergeable child of kind at key, writing its
// marker unless the key holds it already: loro's ensure_mergeable_*
// (handler.rs:4700). The child is named after the map and the key, so
// peers that each ensure one under the same key edit one container; and a
// child deleted by a write to its key comes back, with its contents, when
// it is ensured again. A key that holds a value other than null or a
// mergeable marker is an error, as a write would lose it. Ensuring another
// kind writes that kind's marker; each kind keeps its own contents.
func (m *Map) EnsureMergeable(key string, kind ContainerKind) (Container, error) {
	d := m.h.d
	d.mu.Lock()
	defer d.mu.Unlock()
	if !editableKind(kind) {
		return nil, fmt.Errorf("loro: no container kind %d", kind)
	}
	if err := d.editable(m.h.k); err != nil {
		return nil, err
	}
	if d.knocked(ruleMergeableID) {
		d.mu.Unlock()
		defer d.mu.Lock()
		return m.SetContainer(key, kind)
	}
	child, marker, ok := mergeableChild(m.h.k, key, kind)
	if !ok {
		return nil, fmt.Errorf("loro: map %s cannot hold a mergeable child", m.h.k)
	}
	st := m.h.state()
	cur, set := st.mapEntry(m.h.k, key)
	raw, isBin := cur.Value.([]byte)
	if set && cur.Value != nil {
		if _, isMarker := newMergeableParent(m.h.k).child(key, raw); !isBin || !isMarker {
			return nil, fmt.Errorf("loro: key %q holds a value that is not mergeable", key)
		}
	}
	if !set || !isBin || !bytes.Equal(raw, marker) {
		op := m.h.op(change.VKLoroValue)
		op.MapKey = key
		op.Value = marker
		if err := d.apply(st, op, opProv{}); err != nil {
			return nil, err
		}
	}
	return d.handleOf(child), nil
}

// InsertContainer inserts a new container of kind at index pos and returns
// a handle on it. The container is named by the insert op's id.
func (l *List) InsertContainer(pos int, kind ContainerKind) (Container, error) {
	d := l.h.d
	d.mu.Lock()
	defer d.mu.Unlock()
	if !editableKind(kind) {
		return nil, fmt.Errorf("loro: no container kind %d", kind)
	}
	if err := d.editable(l.h.k); err != nil {
		return nil, err
	}
	st := l.h.state()
	if length := listLen(st, l.h.k); pos < 0 || pos > length {
		return nil, outOfRange(pos, 0, length)
	}
	op := l.h.op(change.VKLoroValue)
	op.Pos = int64(pos)
	op.Value = []any{kind}
	id := ID{Peer: d.peer, Counter: d.begin().nextCounter}
	if err := d.apply(st, op, opProv{kind: provList}); err != nil {
		return nil, err
	}
	return d.placed(id, kind)
}

// GetContainer returns the container stored at index i, false when the item
// there is no container.
func (l *List) GetContainer(i int) (Container, bool) {
	d := l.h.d
	d.mu.Lock()
	defer d.mu.Unlock()
	st := l.h.state()
	if i < 0 || i >= listLen(st, l.h.k) {
		return nil, false
	}
	n, err := st.nthLive(l.h.k, int64(i))
	if err != nil || n == nil {
		return nil, false
	}
	ct, ok := n.value.(change.ContainerType)
	if !ok || !editableKind(ct) {
		return nil, false
	}
	return d.handleOf(childKey(ID{Peer: n.peer, Counter: n.counter}, ct)), true
}

// placed returns a handle on the new container of kind that op id stored.
// loro attaches a new child by replaying its detached value into it
// (handler.rs:466, 5087): nothing for an empty Text, Map, List,
// MovableList or Tree, but an increment by the detached value, zero, for a
// Counter, which loro records as an op of its own and so a counter more.
func (d *Document) placed(id ID, kind ContainerKind) (Container, error) {
	k := childKey(id, kind)
	if kind == KindCounter && !d.knocked(ruleCounterAttach) {
		op := handle{d: d, k: k, st: d.st}.op(change.VKI64)
		op.Value = int64(0)
		if err := d.apply(d.st, op, opProv{}); err != nil {
			return nil, err
		}
	}
	return d.handleOf(k), nil
}
