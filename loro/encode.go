package loro

import (
	"github.com/Deln0r/loro-go/encoding/change"
	"github.com/Deln0r/loro-go/encoding/fast"
)

// Doc builds a loro document from scratch and exports a FastUpdates blob that
// loro-crdt can import. It models a single change by one peer (the common case
// for a fresh local edit batch); concurrent/multi-change export is future work.
//
// Key-pool ordering matches loro: op keys (map keys, mark keys) are registered as
// ops are applied, and root-container names are registered at Export time, after
// the op keys. This is what makes the output byte-identical to loro's own export.
type Doc struct {
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

// NewDoc returns a builder for the given peer id.
func NewDoc(peer uint64) *Doc {
	return &Doc{peer: peer, keyIdx: map[string]int{}, contIdx: map[string]int{}}
}

func (d *Doc) opKey(s string) int {
	if i, ok := d.keyIdx[s]; ok {
		return i
	}
	i := len(d.keys)
	d.keys = append(d.keys, s)
	d.keyIdx[s] = i
	return i
}

func (d *Doc) container(name string, kind change.ContainerType) int {
	if i, ok := d.contIdx[name]; ok {
		return i
	}
	i := len(d.conts)
	d.conts = append(d.conts, contDef{name, kind})
	d.contIdx[name] = i
	return i
}

func (d *Doc) addOp(cidx int, prop int64, vk change.ValueKind, length int64) {
	d.ci = append(d.ci, int64(cidx))
	d.prop = append(d.prop, prop)
	d.vk = append(d.vk, vk)
	d.lens = append(d.lens, length)
	d.counter += length
}

// TextInsert inserts text at a rune position in a root Text container.
func (d *Doc) TextInsert(name string, pos int, text string) {
	c := d.container(name, change.CText)
	d.addOp(c, int64(pos), change.VKStr, int64(len([]rune(text))))
	_ = d.vw.OpContent(change.VKStr, text)
}

// MapSet sets a key in a root Map container. value must be a Go type the value
// writer understands (nil/bool/int64/float64/string/[]byte/[]any/map[string]any).
func (d *Doc) MapSet(name, key string, value any) {
	c := d.container(name, change.CMap)
	k := d.opKey(key)
	d.addOp(c, int64(k), change.VKLoroValue, 1)
	_ = d.vw.OpContent(change.VKLoroValue, value)
}

// ListInsert inserts items at an index in a root List container.
func (d *Doc) ListInsert(name string, pos int, items []any) {
	c := d.container(name, change.CList)
	d.addOp(c, int64(pos), change.VKLoroValue, int64(len(items)))
	_ = d.vw.OpContent(change.VKLoroValue, items)
}

// ExportUpdates encodes the accumulated ops as a FastUpdates blob.
func (d *Doc) ExportUpdates() []byte {
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
