package change

import (
	"github.com/Deln0r/loro-go/encoding/columnar"
	"github.com/Deln0r/loro-go/encoding/postcard"
)

// EncodeFastUpdates frames change blocks into a FastUpdates body: each block is
// LEB128-length-prefixed, concatenated.
func EncodeFastUpdates(blocks [][]byte) []byte {
	var out []byte
	for _, b := range blocks {
		out = postcard.AppendUvarint(out, uint64(len(b)))
		out = append(out, b...)
	}
	return out
}

// EncodeBlock re-emits the outer frame (5 varints) and the 8 length-prefixed
// blobs. Inverse of ParseBlock.
func EncodeBlock(blk *Block) []byte {
	var out []byte
	for _, v := range []uint64{blk.CounterStart, blk.CounterLen, blk.LamportStart, blk.LamportLen, blk.NChanges} {
		out = postcard.AppendUvarint(out, v)
	}
	for _, blob := range [][]byte{
		blk.Header, blk.ChangeMeta, blk.CIDs, blk.Keys,
		blk.Positions, blk.Ops, blk.DeleteIDs, blk.Values,
	} {
		out = postcard.AppendUvarint(out, uint64(len(blob)))
		out = append(out, blob...)
	}
	return out
}

// EncodeOps re-emits the ops blob from decoded columns (inverse of DecodeOps).
func EncodeOps(o *Ops) []byte {
	vk := make([]uint8, len(o.ValueKind))
	for i, k := range o.ValueKind {
		vk[i] = uint8(k)
	}
	ln := make([]uint64, len(o.Len))
	for i, x := range o.Len {
		ln[i] = uint64(x)
	}
	return columnar.EncodeColumns([][]byte{
		columnar.EncodeDeltaRleI64(o.ContainerIdx),
		columnar.EncodeDeltaRleI64(o.Prop),
		columnar.EncodeAnyRleU8(vk),
		columnar.EncodeAnyRleU64(ln),
	})
}

// EncodeContainers re-emits the cids blob (inverse of DecodeContainers).
func EncodeContainers(cs []Container) []byte {
	out := postcard.AppendUvarint(nil, uint64(len(cs)))
	for _, c := range cs {
		out = postcard.AppendUvarint(out, 4) // field count
		isRoot := byte(0)
		if c.IsRoot {
			isRoot = 1
		}
		out = append(out, isRoot, byte(c.Kind))
		out = postcard.AppendUvarint(out, c.PeerIdx)
		out = postcard.AppendVarint(out, c.KeyOrCounter)
	}
	return out
}

// EncodeKeys re-emits the keys blob (inverse of DecodeKeys).
func EncodeKeys(keys []string) []byte {
	var out []byte
	for _, k := range keys {
		out = postcard.AppendUvarint(out, uint64(len(k)))
		out = append(out, k...)
	}
	return out
}
