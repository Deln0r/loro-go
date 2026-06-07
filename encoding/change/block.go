// Package change decodes Loro change blocks: the outer integer frame, the 8
// length-prefixed blobs, and (incrementally) their contents. A change block is
// the value of a 12-byte KV key in FastSnapshot, and the LEB128-framed record
// payload in FastUpdates.
package change

import (
	"errors"

	"github.com/Deln0r/loro-go/encoding/postcard"
)

// ErrBlock marks a malformed change block.
var ErrBlock = errors.New("loro/change: malformed block")

// SplitBlocks splits a FastUpdates body into raw change-block byte slices. The
// body is a bare stream of LEB128-length-prefixed blocks until exhausted.
func SplitBlocks(body []byte) ([][]byte, error) {
	r := postcard.NewReader(body)
	var blocks [][]byte
	for !r.Empty() {
		n, err := r.Uvarint()
		if err != nil {
			return nil, err
		}
		blk, err := r.Bytes(int(n))
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, blk)
	}
	return blocks, nil
}

// Block is a parsed change block: the outer integer frame plus the 8 raw blobs
// in their fixed order.
type Block struct {
	CounterStart uint64
	CounterLen   uint64
	LamportStart uint64
	LamportLen   uint64
	NChanges     uint64

	Header     []byte // 1: peer table, atom lens, deps, lamports
	ChangeMeta []byte // 2: timestamps, commit-message lens + bytes
	CIDs       []byte // 3: container arena (row-wise)
	Keys       []byte // 4: key strings
	Positions  []byte // 5: fractional-index positions
	Ops        []byte // 6: op columns
	DeleteIDs  []byte // 7: delete-start ids
	Values     []byte // 8: value stream
}

// ParseBlock parses the outer frame (5 varints) and the 8 length-prefixed blobs.
func ParseBlock(b []byte) (*Block, error) {
	r := postcard.NewReader(b)
	var blk Block
	for _, p := range []*uint64{
		&blk.CounterStart, &blk.CounterLen, &blk.LamportStart, &blk.LamportLen, &blk.NChanges,
	} {
		v, err := r.Uvarint()
		if err != nil {
			return nil, err
		}
		*p = v
	}
	blobs := make([][]byte, 8)
	for i := range blobs {
		n, err := r.Uvarint()
		if err != nil {
			return nil, err
		}
		data, err := r.Bytes(int(n))
		if err != nil {
			return nil, err
		}
		blobs[i] = data
	}
	blk.Header, blk.ChangeMeta, blk.CIDs, blk.Keys = blobs[0], blobs[1], blobs[2], blobs[3]
	blk.Positions, blk.Ops, blk.DeleteIDs, blk.Values = blobs[4], blobs[5], blobs[6], blobs[7]
	if !r.Empty() {
		return nil, ErrBlock // trailing bytes after the 8 blobs
	}
	return &blk, nil
}
