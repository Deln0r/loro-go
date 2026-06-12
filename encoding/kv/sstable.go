// Package kv decodes Loro's immutable SSTable (the KV store used by FastSnapshot
// oplog/state sections). Layout (crates/kv-store/src/{sstable.rs,block.rs}):
//
//	[0..4)   magic "LORO" (uppercase)
//	[4]      schema version 0
//	[5..M)   block chunks
//	[M..E-4) BlockMeta index (num_blocks u32-LE + per-block meta + meta checksum)
//	[E-4..E) meta_offset u32-LE (= M), the very last 4 bytes
//
// All multi-byte ints here are little-endian. Block + meta integrity is checked
// with xxh32 (seed "LORO").
package kv

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/Deln0r/loro-go/encoding/lz4"
	"github.com/Deln0r/loro-go/encoding/xxh32"
)

var ErrSSTable = errors.New("loro/kv: malformed SSTable")

// Entry is one key/value pair from the SSTable.
type Entry struct {
	Key   []byte
	Value []byte
}

type blockMeta struct {
	offset      uint32
	firstKey    []byte
	isLarge     bool
	compression uint8
}

// ParseSSTable decodes an SSTable into its key/value entries (block checksums
// verified). Compression other than None is not yet supported.
func ParseSSTable(b []byte) ([]Entry, error) {
	if len(b) < 5+4 {
		return nil, ErrSSTable
	}
	if string(b[:4]) != "LORO" {
		return nil, fmt.Errorf("%w: bad magic", ErrSSTable)
	}
	if b[4] != 0 {
		return nil, fmt.Errorf("%w: schema version %d", ErrSSTable, b[4])
	}
	metaOffset := binary.LittleEndian.Uint32(b[len(b)-4:])
	if int(metaOffset) < 5 || int(metaOffset) > len(b)-4 {
		return nil, fmt.Errorf("%w: meta_offset %d", ErrSSTable, metaOffset)
	}
	metas, err := decodeMeta(b[metaOffset : len(b)-4])
	if err != nil {
		return nil, err
	}
	var entries []Entry
	for i, m := range metas {
		blockEnd := metaOffset
		if i+1 < len(metas) {
			blockEnd = metas[i+1].offset
		}
		if int(m.offset) > int(blockEnd) || int(blockEnd) > len(b) {
			return nil, fmt.Errorf("%w: block bounds", ErrSSTable)
		}
		es, err := decodeBlock(b[m.offset:blockEnd], m)
		if err != nil {
			return nil, err
		}
		entries = append(entries, es...)
	}
	return entries, nil
}

func decodeMeta(meta []byte) ([]blockMeta, error) {
	if len(meta) < 4+4 {
		return nil, fmt.Errorf("%w: meta too short", ErrSSTable)
	}
	num := binary.LittleEndian.Uint32(meta[:4])
	if num > 10_000_000 {
		return nil, fmt.Errorf("%w: num_blocks %d", ErrSSTable, num)
	}
	// meta checksum is the last 4 bytes, over everything after num_blocks.
	body := meta[4 : len(meta)-4]
	want := binary.LittleEndian.Uint32(meta[len(meta)-4:])
	if xxh32.Checksum(body, xxh32.Seed) != want {
		return nil, fmt.Errorf("%w: meta checksum", ErrSSTable)
	}
	out := make([]blockMeta, 0, num)
	p := 0
	for i := uint32(0); i < num; i++ {
		var m blockMeta
		if p+4+2 > len(body) {
			return nil, ErrSSTable
		}
		m.offset = binary.LittleEndian.Uint32(body[p:])
		p += 4
		fkLen := int(binary.LittleEndian.Uint16(body[p:]))
		p += 2
		if p+fkLen+1 > len(body) {
			return nil, ErrSSTable
		}
		m.firstKey = body[p : p+fkLen]
		p += fkLen
		lac := body[p]
		p++
		m.isLarge = lac&0x80 != 0
		m.compression = lac & 0x7f
		if !m.isLarge {
			if p+2 > len(body) {
				return nil, ErrSSTable
			}
			lkLen := int(binary.LittleEndian.Uint16(body[p:]))
			p += 2 + lkLen // last_key skipped (not needed for read)
			if p > len(body) {
				return nil, ErrSSTable
			}
		}
		out = append(out, m)
	}
	return out, nil
}

func decodeBlock(block []byte, m blockMeta) ([]Entry, error) {
	if len(block) < 4 {
		return nil, ErrSSTable
	}
	data := block[:len(block)-4]
	want := binary.LittleEndian.Uint32(block[len(block)-4:])
	if xxh32.Checksum(data, xxh32.Seed) != want {
		return nil, fmt.Errorf("%w: block checksum", ErrSSTable)
	}
	switch m.compression {
	case 0:
		// raw
	case 1:
		dec, err := lz4.DecompressFrame(data)
		if err != nil {
			return nil, err
		}
		data = dec
	default:
		return nil, fmt.Errorf("%w: compression %d not supported", ErrSSTable, m.compression)
	}
	if m.isLarge {
		// single oversized value; no offsets/key headers.
		return []Entry{{Key: m.firstKey, Value: data}}, nil
	}
	if len(data) < 2 {
		return nil, ErrSSTable
	}
	n := int(binary.LittleEndian.Uint16(data[len(data)-2:]))
	if n == 0 {
		return nil, ErrSSTable
	}
	offBytes := 2 * (n + 1)
	if len(data) < offBytes {
		return nil, ErrSSTable
	}
	dataEnd := len(data) - offBytes
	offs := make([]int, n)
	for i := 0; i < n; i++ {
		offs[i] = int(binary.LittleEndian.Uint16(data[dataEnd+2*i:]))
	}
	kv := data[:dataEnd]
	out := make([]Entry, 0, n)
	for i := 0; i < n; i++ {
		start := offs[i]
		end := dataEnd
		if i+1 < n {
			end = offs[i+1]
		}
		if start > end || end > len(kv) {
			return nil, ErrSSTable
		}
		var key []byte
		valStart := start
		if i == 0 {
			key = m.firstKey
		} else {
			if start+3 > end {
				return nil, ErrSSTable
			}
			prefixLen := int(kv[start])
			suffixLen := int(binary.LittleEndian.Uint16(kv[start+1:]))
			ks := start + 3
			if ks+suffixLen > end || prefixLen > len(m.firstKey) {
				return nil, ErrSSTable
			}
			key = append(append([]byte{}, m.firstKey[:prefixLen]...), kv[ks:ks+suffixLen]...)
			valStart = ks + suffixLen
		}
		out = append(out, Entry{Key: key, Value: kv[valStart:end]})
	}
	return out, nil
}
