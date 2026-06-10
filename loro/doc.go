package loro

import (
	"fmt"

	"github.com/Deln0r/loro-go/encoding/change"
)

// BuildState reconstructs the document state from decoded updates, mirroring
// loro's doc.toJSON(): a map of root-container name to its value (Text -> string,
// Map -> map, List -> []any). This applies ops in change/op order; it does NOT
// yet implement the full CRDT merge (Fugue ordering, LWW conflict resolution),
// so it is correct only for non-concurrent histories such as a single change.
func BuildState(u *Updates) (map[string]any, error) {
	state := map[string]any{}
	for _, ch := range u.Changes {
		for _, op := range ch.Ops {
			switch op.Kind {
			case change.CText:
				cur, _ := state[op.Container].(string)
				if op.VKind == change.VKDeleteSeq {
					state[op.Container] = deleteString(cur, int(op.Pos), int(op.Len))
					continue
				}
				ins, ok := op.Value.(string)
				if !ok {
					return nil, fmt.Errorf("loro: text op value is %T, want string", op.Value)
				}
				state[op.Container] = insertString(cur, int(op.Pos), ins)
			case change.CMap:
				m, ok := state[op.Container].(map[string]any)
				if !ok {
					m = map[string]any{}
					state[op.Container] = m
				}
				if op.VKind == change.VKDeleteOnce {
					delete(m, op.MapKey)
					continue
				}
				m[op.MapKey] = op.Value
			case change.CList:
				cur, _ := state[op.Container].([]any)
				if op.VKind == change.VKDeleteSeq {
					state[op.Container] = deleteList(cur, int(op.Pos), int(op.Len))
					continue
				}
				elems, ok := op.Value.([]any)
				if !ok {
					return nil, fmt.Errorf("loro: list op value is %T, want []any", op.Value)
				}
				state[op.Container] = insertList(cur, int(op.Pos), elems)
			default:
				return nil, fmt.Errorf("loro: unsupported container kind %v", op.Kind)
			}
		}
	}
	return state, nil
}

// insertString inserts ins at rune position pos.
func insertString(s string, pos int, ins string) string {
	r := []rune(s)
	if pos < 0 {
		pos = 0
	}
	if pos > len(r) {
		pos = len(r)
	}
	return string(r[:pos]) + ins + string(r[pos:])
}

// deleteString removes n runes starting at rune position pos.
func deleteString(s string, pos, n int) string {
	r := []rune(s)
	if pos < 0 {
		pos = 0
	}
	if pos > len(r) {
		pos = len(r)
	}
	end := pos + n
	if end > len(r) {
		end = len(r)
	}
	return string(r[:pos]) + string(r[end:])
}

// deleteList removes n elements starting at index pos.
func deleteList(lst []any, pos, n int) []any {
	if pos < 0 {
		pos = 0
	}
	if pos > len(lst) {
		pos = len(lst)
	}
	end := pos + n
	if end > len(lst) {
		end = len(lst)
	}
	return append(lst[:pos], lst[end:]...)
}

// insertList inserts elems at index pos.
func insertList(lst []any, pos int, elems []any) []any {
	if pos < 0 {
		pos = 0
	}
	if pos > len(lst) {
		pos = len(lst)
	}
	out := make([]any, 0, len(lst)+len(elems))
	out = append(out, lst[:pos]...)
	out = append(out, elems...)
	out = append(out, lst[pos:]...)
	return out
}
