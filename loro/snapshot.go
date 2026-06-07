package loro

import (
	"encoding/binary"
	"fmt"

	"github.com/Deln0r/loro-go/encoding/change"
	"github.com/Deln0r/loro-go/encoding/fast"
	"github.com/Deln0r/loro-go/encoding/kv"
)

// DecodeSnapshot decodes a loro-crdt FastSnapshot export. It reads the oplog
// section (an SSTable), enumerates the 12-byte change-block keys, and decodes
// each as a change. The DocState and shallow sections are not read (the same
// state is reconstructed from the oplog via BuildState). Reserved keys
// vv/fr/sv/sf are skipped. Compressed blocks are not yet supported.
func DecodeSnapshot(blob []byte) (*Updates, error) {
	h, err := fast.ParseHeader(blob)
	if err != nil {
		return nil, err
	}
	if h.Mode != fast.ModeFastSnapshot {
		return nil, fmt.Errorf("loro: expected FastSnapshot (mode 3), got mode %d", h.Mode)
	}
	if err := fast.VerifyChecksum(blob); err != nil {
		return nil, err
	}
	body := h.Body
	if len(body) < 4 {
		return nil, fmt.Errorf("loro: snapshot body too short")
	}
	oplogLen := int(binary.LittleEndian.Uint32(body[:4]))
	if 4+oplogLen > len(body) {
		return nil, fmt.Errorf("loro: oplog section length %d exceeds body", oplogLen)
	}
	oplog := body[4 : 4+oplogLen]

	entries, err := kv.ParseSSTable(oplog)
	if err != nil {
		return nil, err
	}
	u := &Updates{}
	for _, e := range entries {
		if len(e.Key) != 12 { // reserved keys (vv/fr/sv/sf) are 2 bytes
			continue
		}
		blk, err := change.ParseBlock(e.Value)
		if err != nil {
			return nil, err
		}
		chs, err := decodeBlock(blk)
		if err != nil {
			return nil, err
		}
		u.Changes = append(u.Changes, chs...)
	}
	return u, nil
}
