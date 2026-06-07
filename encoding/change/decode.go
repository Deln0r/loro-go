package change

import (
	"github.com/Deln0r/loro-go/encoding/columnar"
	"github.com/Deln0r/loro-go/encoding/postcard"
)

// ContainerType is Loro's container kind. Verified empirically against real
// fixtures: §4.3 of the wire reference lists the order wrong (Text=0); the real
// loro ContainerType enum is Map=0, List=1, Text=2, ...
type ContainerType uint8

const (
	CMap         ContainerType = 0
	CList        ContainerType = 1
	CText        ContainerType = 2
	CTree        ContainerType = 3
	CMovableList ContainerType = 4
	CCounter     ContainerType = 5
)

func (c ContainerType) String() string {
	switch c {
	case CMap:
		return "Map"
	case CList:
		return "List"
	case CText:
		return "Text"
	case CTree:
		return "Tree"
	case CMovableList:
		return "MovableList"
	case CCounter:
		return "Counter"
	default:
		return "Unknown"
	}
}

// Container is one entry of the container arena (cids blob). For a root
// container KeyOrCounter is the index into the keys pool; for a normal
// container it is the op counter.
type Container struct {
	IsRoot       bool
	Kind         ContainerType
	PeerIdx      uint64
	KeyOrCounter int64
}

// DecodeContainers decodes the cids blob. It is row-wise plain postcard (the
// columnar RLE attrs are not applied), so each row is varint(4) + 4 fields:
// is_root u8, kind u8, peer_idx uvarint, key_or_counter zigzag-varint.
func DecodeContainers(cids []byte) ([]Container, error) {
	r := postcard.NewReader(cids)
	if r.Empty() {
		return nil, nil
	}
	n, err := r.Uvarint()
	if err != nil {
		return nil, err
	}
	out := make([]Container, n)
	for i := range out {
		if _, err = r.Uvarint(); err != nil { // field count, always 4
			return nil, err
		}
		isRoot, err := r.Byte()
		if err != nil {
			return nil, err
		}
		kind, err := r.Byte()
		if err != nil {
			return nil, err
		}
		peerIdx, err := r.Uvarint()
		if err != nil {
			return nil, err
		}
		koc, err := r.Varint() // postcard zigzag
		if err != nil {
			return nil, err
		}
		out[i] = Container{IsRoot: isRoot != 0, Kind: ContainerType(kind), PeerIdx: peerIdx, KeyOrCounter: koc}
	}
	return out, nil
}

// DecodeKeys decodes the keys blob: a bare sequence of length-prefixed UTF-8
// strings with no count, read until exhausted.
func DecodeKeys(keys []byte) ([]string, error) {
	r := postcard.NewReader(keys)
	var out []string
	for !r.Empty() {
		s, err := r.String()
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

// Ops is the decoded ops blob: four parallel columns, one row per op.
type Ops struct {
	ContainerIdx []int64
	Prop         []int64
	ValueKind    []ValueKind
	Len          []int64
}

// N returns the number of ops.
func (o *Ops) N() int { return len(o.ContainerIdx) }

// DecodeOps decodes the ops blob (a serde_columnar 4-column vec): container_index
// DeltaRle<u32>, prop DeltaRle<i32>, value_type AnyRle<u8>, len AnyRle<u32>.
func DecodeOps(ops []byte) (*Ops, error) {
	cols, err := columnar.Columns(ops)
	if err != nil {
		return nil, err
	}
	if len(cols) != 4 {
		return nil, ErrBlock
	}
	ci, err := columnar.DeltaRleI64(cols[0])
	if err != nil {
		return nil, err
	}
	pr, err := columnar.DeltaRleI64(cols[1])
	if err != nil {
		return nil, err
	}
	vkU8, err := columnar.AnyRleU8(cols[2])
	if err != nil {
		return nil, err
	}
	ln, err := columnar.AnyRleU64(cols[3])
	if err != nil {
		return nil, err
	}
	vk := make([]ValueKind, len(vkU8))
	for i, b := range vkU8 {
		vk[i] = ValueKind(b)
	}
	lens := make([]int64, len(ln))
	for i, x := range ln {
		lens[i] = int64(x)
	}
	return &Ops{ContainerIdx: ci, Prop: pr, ValueKind: vk, Len: lens}, nil
}
