package loro

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/Deln0r/loro-go/encoding/change"
)

// contKey names a container as loro's ContainerID does: a root container by
// its name, any other by the id of the atom that placed it in its parent,
// formatted "counter@peer", and either one by its kind, since a Text and a
// List may share a name and two kinds of container may hang off one id.
type contKey struct {
	root bool
	name string
	kind change.ContainerType
}

func keyOf(op Op) contKey {
	return contKey{root: op.IsRoot, name: op.Container, kind: op.Kind}
}

// String renders the key as loro renders a container id: "cid:root-name:Kind"
// or "cid:counter@peer:Kind".
func (k contKey) String() string {
	if k.root {
		return "cid:root-" + k.name + ":" + kindName(k.kind)
	}
	return "cid:" + k.name + ":" + kindName(k.kind)
}

// kindName is loro's name for a container kind, Unknown(k) for one it does
// not know.
func kindName(k change.ContainerType) string {
	if k > change.CCounter {
		return fmt.Sprintf("Unknown(%d)", uint8(k))
	}
	return k.String()
}

// childKey names the container that a value holding a ContainerType places:
// loro gives the child the id of the atom that wrote the value, the map
// write, the list item or the MovableList set, and does not write it again.
func childKey(id ID, kind change.ContainerType) contKey {
	return contKey{name: strconv.FormatInt(id.Counter, 10) + "@" + strconv.FormatUint(id.Peer, 10), kind: kind}
}

// resolver builds the value of every container reachable from the roots,
// each once. A container's value holds its children's values, so it is
// built in two steps: shape lays out the container's own value with a
// childSlot where each child goes, and once every reachable container has a
// value, the slots are filled. Values are maps, slices, strings and numbers;
// a map or a slice is filled in place, so a parent copied into its own
// parent's slot before its children were filled still ends up whole.
//
// The walk is depth-first with an explicit stack, so a deep chain of nested
// containers costs heap, not goroutine stack. A container met again while it
// is still on the walk's path is its own ancestor. Only crafted input does
// that: a child is named by the op that placed it, and an author can only
// write that op into a parent it already has, one an earlier op created.
// loro-crdt 1.17.1 refuses such a document outright: importing one panics
// with "the parent links ... form a cycle", even when no root reaches the
// cycle. So there is no loro state to match, and MergeState returns an error
// rather than show part of a document loro cannot open. A cycle no root
// reaches cannot change the state and is not looked for; MergeState shows the
// rest of such a document.
//
// A container reached a second time by another path, which also only
// crafted input does (the same op id in two containers), is built once and
// its value shared, so the work stays proportional to the input.
type resolver struct {
	conts map[contKey]*cinfo
	seqs  map[contKey]*seqState
	vals  map[contKey]any
	state map[contKey]uint8 // 0 not seen, onPath, built
	slots []childSlot
}

const (
	onPath uint8 = 1
	built  uint8 = 2
)

// childSlot is where a child container's value goes: key in map m, or index
// in list.
type childSlot struct {
	m     map[string]any
	key   string
	list  []any
	index int
	child contKey
}

func (r *resolver) resolve(roots []contKey) error {
	type frame struct {
		key  contKey
		kids []childSlot
		next int
	}
	var stack []frame
	visit := func(k contKey) error {
		v, kids, err := r.shape(k)
		if err != nil {
			return err
		}
		r.vals[k] = v
		r.state[k] = onPath
		r.slots = append(r.slots, kids...)
		stack = append(stack, frame{key: k, kids: kids})
		return nil
	}
	for _, root := range roots {
		if r.state[root] != 0 {
			continue
		}
		if err := visit(root); err != nil {
			return err
		}
		for len(stack) > 0 {
			f := &stack[len(stack)-1]
			if f.next == len(f.kids) {
				r.state[f.key] = built
				stack = stack[:len(stack)-1]
				continue
			}
			child := f.kids[f.next].child
			f.next++
			switch r.state[child] {
			case onPath:
				return fmt.Errorf("loro: container %s is its own ancestor", child)
			case built:
				continue
			}
			if err := visit(child); err != nil {
				return err
			}
		}
	}
	for _, s := range r.slots {
		if s.m != nil {
			s.m[s.key] = r.vals[s.child]
		} else {
			s.list[s.index] = r.vals[s.child]
		}
	}
	return nil
}

// shape returns container k's value with a slot for each child container in
// it. A container without ops shows its kind's empty value, as loro shows a
// child that was created and never written to.
func (r *resolver) shape(k contKey) (any, []childSlot, error) {
	var ops []Op
	if ci := r.conts[k]; ci != nil {
		ops = ci.ops
	}
	var kids []childSlot
	switch k.kind {
	case change.CMap:
		// Ascending (lamport, peer) order, so the last write to a key wins.
		sorted := sortedOps(ops)
		win := make(map[string]int, len(sorted))
		for i, op := range sorted {
			if op.VKind == change.VKDeleteOnce {
				delete(win, op.MapKey)
				continue
			}
			win[op.MapKey] = i
		}
		m := make(map[string]any, len(win))
		var parent *mergeableParent
		for key, i := range win {
			op := &sorted[i]
			id := ID{Peer: op.Peer, Counter: op.Counter}
			switch v := op.Value.(type) {
			case change.ContainerType:
				kids = append(kids, childSlot{m: m, key: key, child: childKey(id, v)})
				continue
			case []byte:
				if parent == nil {
					parent = newMergeableParent(k)
				}
				if child, ok := parent.child(key, v); ok {
					kids = append(kids, childSlot{m: m, key: key, child: child})
					continue
				}
			}
			m[key] = plainValue(op.Value, id, true)
		}
		// Sorted, so the walk, and which container an error names, does not
		// depend on map iteration order.
		slices.SortFunc(kids, func(a, b childSlot) int { return strings.Compare(a.key, b.key) })
		return m, kids, nil
	case change.CText:
		if s := r.seqs[k]; s != nil {
			return s.text(), nil, nil
		}
		return "", nil, nil
	case change.CList, change.CMovableList:
		out := []any{}
		if s := r.seqs[k]; s != nil {
			s.each(func(v any, id ID, whole bool) {
				if ct, ok := v.(change.ContainerType); ok {
					kids = append(kids, childSlot{index: len(out), child: childKey(id, ct)})
					out = append(out, nil)
					return
				}
				out = append(out, plainValue(v, id, whole))
			})
		}
		for i := range kids {
			kids[i].list = out
		}
		return out, kids, nil
	case change.CCounter:
		return counterValue(ops), nil, nil
	case change.CTree:
		tree, err := buildTree(ops, func(id string, node map[string]any) {
			kids = append(kids, childSlot{m: node, key: "meta", child: contKey{name: id, kind: change.CMap}})
		})
		return tree, kids, err
	default:
		// loro shows a container of a kind it does not know as null.
		if len(ops) == 0 {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("loro: merge unsupported container kind %v", k.kind)
	}
}

// plainValue returns a value that is not itself a child container as toJSON
// shows it. A container kind nested inside a list or a map value is not a
// child: loro's import reads it as a container id and toJSON prints that id,
// "cid:counter@peer:Kind", rather than any container's contents. The id is
// the one loro's value reader passes down: the atom's id, plus the element's
// index for an element of a list that is a whole op value, which whole says
// (a map write or a MovableList set); an item of a list insert is already
// one such element. loro's own API cannot write one (its JSON import refuses
// "container values must not be nested"), so only a crafted blob holds one.
// The value is copied only when it holds one.
func plainValue(v any, id ID, whole bool) any {
	if !holdsContainer(v) {
		return v
	}
	switch x := v.(type) {
	case change.ContainerType:
		return childKey(id, x).String()
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			at := id
			if whole {
				at.Counter += int64(i)
			}
			out[i] = plainValue(e, at, false)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = plainValue(e, id, false)
		}
		return out
	}
	return v
}

func holdsContainer(v any) bool {
	switch x := v.(type) {
	case change.ContainerType:
		return true
	case []any:
		return slices.ContainsFunc(x, holdsContainer)
	case map[string]any:
		for _, e := range x {
			if holdsContainer(e) {
				return true
			}
		}
	}
	return false
}
