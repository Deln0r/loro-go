package loro

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"math"
	"strconv"
	"strings"

	"github.com/Deln0r/loro-go/encoding/change"
)

// Mergeable children (loro 1.13 and later, map.ensureMergeableText(key) and
// its siblings) are root containers named after the path to them, so that two
// peers that each create one under the same key write to one container. The
// parent map holds a binary marker at the key instead of a container value;
// the child shows there while the marker is the key's current value, and
// never at the top level. The rules below follow loro-common's lib.rs
// (new_mergeable, mergeable_marker, parse_mergeable_marker).
const mergeablePrefix = "🤝:"

// The marker is magic, kind, then a CRC-24 binding it to (parent, key, kind),
// so a marker copied to another key or map is an ordinary binary value.
var mergeableMagic = [4]byte{0x00, 'L', 'M', 0x01}

const mergeableCRCDomain = "loro.mergeable.marker.v1"

// mergeableParent is a map that may hold mergeable markers: its id as loro
// encodes it, which the marker's digest covers, and the path prefix its
// mergeable children are named with. ok is false for a map that cannot have
// one, such as a nested map whose counter is outside loro's i32 range.
type mergeableParent struct {
	cid  []byte
	path string
	ok   bool
}

func newMergeableParent(k contKey) *mergeableParent {
	p := &mergeableParent{}
	if k.root {
		// ContainerID::to_bytes: kind | 0x80, the name's length, the name.
		p.cid = append([]byte{byte(k.kind) | 0x80}, binary.AppendUvarint(nil, uint64(len(k.name)))...)
		p.cid = append(p.cid, k.name...)
		// A mergeable map's own children extend its path; any other root map
		// starts one from its escaped name.
		if payload, ok := strings.CutPrefix(k.name, mergeablePrefix); ok && validMergeablePayload(payload) {
			p.path = payload
		} else {
			p.path = "$" + escapeMergeable(k.name)
		}
		p.ok = true
		return p
	}
	counterStr, peerStr, found := strings.Cut(k.name, "@")
	peer, perr := strconv.ParseUint(peerStr, 10, 64)
	counter, cerr := strconv.ParseInt(counterStr, 10, 64)
	if !found || perr != nil || cerr != nil || counter < math.MinInt32 || counter > math.MaxInt32 {
		return p
	}
	// kind, then the peer and the i32 counter, little-endian.
	p.cid = binary.LittleEndian.AppendUint64([]byte{byte(k.kind)}, peer)
	p.cid = binary.LittleEndian.AppendUint32(p.cid, uint32(int32(counter)))
	p.path = "@" + strconv.FormatUint(peer, 36) + ":" + strconv.FormatInt(counter, 36)
	p.ok = true
	return p
}

// child returns the mergeable child that marker v activates at key, if v is
// the marker for that key of this map and a kind loro knows.
func (p *mergeableParent) child(key string, v []byte) (contKey, bool) {
	if !p.ok || len(v) != 8 || !bytes.HasPrefix(v, mergeableMagic[:]) {
		return contKey{}, false
	}
	kind := change.ContainerType(v[4])
	if kind > change.CCounter {
		return contKey{}, false
	}
	crc := mergeableCRC(p, key, kind)
	if v[5] != byte(crc>>16) || v[6] != byte(crc>>8) || v[7] != byte(crc) {
		return contKey{}, false
	}
	return contKey{root: true, name: mergeablePrefix + p.path + ">" + escapeMergeable(key), kind: kind}, true
}

// mergeableCRC is the CRC-24 a marker binds (parent, key, kind) with.
func mergeableCRC(p *mergeableParent, key string, kind change.ContainerType) uint32 {
	in := []byte(mergeableCRCDomain)
	in = binary.AppendUvarint(in, uint64(len(p.cid)))
	in = append(in, p.cid...)
	in = binary.AppendUvarint(in, uint64(len(key)))
	in = append(in, key...)
	in = append(in, byte(kind))
	return crc32.ChecksumIEEE(in) & 0xffffff
}

// isMergeableName reports whether a root container name is a mergeable
// child's, which loro keeps out of the top level. A name that merely starts
// with the prefix but does not parse is an ordinary root.
func isMergeableName(name string) bool {
	payload, ok := strings.CutPrefix(name, mergeablePrefix)
	return ok && validMergeablePayload(payload)
}

func escapeMergeable(s string) string {
	var b strings.Builder
	for _, c := range s {
		switch c {
		case '\\':
			b.WriteString(`\\`)
		case '>':
			b.WriteString(`\>`)
		case '/':
			b.WriteString(`\s`)
		case 0:
			b.WriteString(`\0`)
		default:
			b.WriteRune(c)
		}
	}
	return b.String()
}

// validMergeablePayload is loro's validate_mergeable_payload: a base parent,
// "$" and an escaped root name or "@" and a base-36 peer:counter, then ">"
// and the escaped key path.
func validMergeablePayload(payload string) bool {
	sep, escaped := -1, false
	for i, c := range payload {
		if escaped {
			escaped = false
			continue
		}
		if c == '\\' {
			escaped = true
		} else if c == '>' {
			sep = i
			break
		}
	}
	if sep < 0 {
		return false
	}
	base, path := payload[:sep], payload[sep+1:]
	switch {
	case strings.HasPrefix(base, "$"):
		name := base[1:]
		if name == "" || strings.HasPrefix(name, mergeablePrefix) || !validEscapes(name, `\>`) {
			return false
		}
	case strings.HasPrefix(base, "@"):
		peer, counter, ok := strings.Cut(base[1:], ":")
		if !ok || !validBase36(peer, false) || !validBase36(counter, true) {
			return false
		}
	default:
		return false
	}
	return validEscapes(path, `\>s0`)
}

// validEscapes reports whether s has no raw '/' or NUL and every backslash
// escapes one of allowed.
func validEscapes(s, allowed string) bool {
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		switch rs[i] {
		case '\\':
			i++
			if i == len(rs) || !strings.ContainsRune(allowed, rs[i]) {
				return false
			}
		case '/', 0:
			return false
		}
	}
	return true
}

// validBase36 is loro's parse_u64_base36, or with signed its
// parse_i32_base36: lowercase digits without a leading zero, in range.
func validBase36(s string, signed bool) bool {
	neg := false
	if signed {
		s, neg = strings.CutPrefix(s, "-")
	}
	if s == "" || (len(s) > 1 && s[0] == '0') {
		return false
	}
	for i := 0; i < len(s); i++ {
		if (s[i] < '0' || s[i] > '9') && (s[i] < 'a' || s[i] > 'z') {
			return false
		}
	}
	v, err := strconv.ParseUint(s, 36, 64)
	switch {
	case err != nil:
		return false
	case !signed:
		return true
	case neg:
		return v != 0 && v <= -math.MinInt32
	}
	return v <= math.MaxInt32
}
