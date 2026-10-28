package loro

import (
	"github.com/Deln0r/loro-go/encoding/change"
	"github.com/Deln0r/loro-go/encoding/fast"
)

// testDoc builds a document of one change by one peer and exports it as a
// FastUpdates blob that loro-crdt can import, the way the tests make small
// blobs. It checks nothing: a position past the end writes a blob that loro
// refuses. Document is the API for edits.
//
// Key-pool ordering matches loro: op keys (map keys, mark keys) are registered as
// ops are applied, each followed by the keys of any map nested in its value, and
// root-container names are registered at Export time, after the op keys. This is
// what makes the output byte-identical to loro's own export. The one exception
// is a map of two or more keys nested in a value: loro lists its keys in the
// order of its hash map and this builder in sorted order, so the bytes can
// differ while loro-crdt imports the same document.
type testDoc struct {
	peer    uint64
	keys    []string
	keyIdx  map[string]int
	conts   []contDef
	contIdx map[string]int

	ci      []int64
	prop    []int64
	vk      []change.ValueKind
	lens    []int64
	vw      change.ValueWriter
	counter int64
}

type contDef struct {
	name string
	kind change.ContainerType
}

// newTestDoc returns a builder for the given peer id.
func newTestDoc(peer uint64) *testDoc {
	d := &testDoc{peer: peer, keyIdx: map[string]int{}, contIdx: map[string]int{}}
	d.vw.Key = func(s string) uint64 { return uint64(d.opKey(s)) }
	return d
}

func (d *testDoc) opKey(s string) int {
	if i, ok := d.keyIdx[s]; ok {
		return i
	}
	i := len(d.keys)
	d.keys = append(d.keys, s)
	d.keyIdx[s] = i
	return i
}

func (d *testDoc) container(name string, kind change.ContainerType) int {
	if i, ok := d.contIdx[name]; ok {
		return i
	}
	i := len(d.conts)
	d.conts = append(d.conts, contDef{name, kind})
	d.contIdx[name] = i
	return i
}

func (d *testDoc) addOp(cidx int, prop int64, vk change.ValueKind, length int64) {
	d.ci = append(d.ci, int64(cidx))
	d.prop = append(d.prop, prop)
	d.vk = append(d.vk, vk)
	d.lens = append(d.lens, length)
	d.counter += length
}

// TextInsert inserts text at a rune position in a root Text container.
func (d *testDoc) TextInsert(name string, pos int, text string) {
	c := d.container(name, change.CText)
	d.addOp(c, int64(pos), change.VKStr, int64(len([]rune(text))))
	_ = d.vw.OpContent(change.VKStr, text)
}

// MapSet sets a key in a root Map container. value must be a Go type the value
// writer understands (nil/bool/int64/float64/string/[]byte/[]any/map[string]any).
func (d *testDoc) MapSet(name, key string, value any) {
	c := d.container(name, change.CMap)
	k := d.opKey(key)
	d.addOp(c, int64(k), change.VKLoroValue, 1)
	_ = d.vw.OpContent(change.VKLoroValue, value)
}

// ListInsert inserts items at an index in a root List container.
func (d *testDoc) ListInsert(name string, pos int, items []any) {
	c := d.container(name, change.CList)
	d.addOp(c, int64(pos), change.VKLoroValue, int64(len(items)))
	_ = d.vw.OpContent(change.VKLoroValue, items)
}

// ExportUpdates encodes the accumulated ops as a FastUpdates blob.
func (d *testDoc) ExportUpdates() []byte {
	conts := make([]change.Container, len(d.conts))
	for i, cd := range d.conts {
		ki := d.opKey(cd.name) // container names registered after op keys
		conts[i] = change.Container{IsRoot: true, Kind: cd.kind, PeerIdx: 0, KeyOrCounter: int64(ki)}
	}
	hdr := &change.ChangeHeader{Peers: []uint64{d.peer}, DepOnSelf: []bool{false}, DepLens: []uint64{0}}
	cm := &change.ChangeMeta{Timestamps: []int64{0}, CommitMsgs: []string{""}}
	ops := &change.Ops{ContainerIdx: d.ci, Prop: d.prop, ValueKind: d.vk, Len: d.lens}
	blk := &change.Block{
		CounterStart: 0, CounterLen: uint64(d.counter),
		LamportStart: 0, LamportLen: uint64(d.counter),
		NChanges:   1,
		Header:     change.EncodeHeader(hdr),
		ChangeMeta: change.EncodeChangeMeta(cm),
		CIDs:       change.EncodeContainers(conts),
		Keys:       change.EncodeKeys(d.keys),
		Ops:        change.EncodeOps(ops),
		Values:     d.vw.Bytes(),
	}
	body := change.EncodeFastUpdates([][]byte{change.EncodeBlock(blk)})
	return fast.Encode(fast.ModeFastUpdates, body)
}
