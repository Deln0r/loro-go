// Package lz4 decompresses LZ4 frame data (the compression loro's KV store
// applies to large SSTable blocks via lz4_flex). Decompression only; loro-go
// never needs to produce compressed blocks to stay byte-compatible, because
// compression is a per-block storage choice, not part of the logical format.
package lz4

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/Deln0r/loro-go/encoding/xxh32"
)

var ErrFrame = errors.New("loro/lz4: malformed frame")

const frameMagic = 0x184D2204

// maxDecompressed bounds a single frame's decompressed output. LZ4 matches let a
// small block expand many-fold, so without a ceiling a tiny hostile block could
// drive a huge allocation. Real Loro SSTable blocks are a few MB at most.
const maxDecompressed = 64 << 20

// DecompressFrame decodes one LZ4 frame and returns the decompressed content.
// Block and content checksums are verified when the frame carries them.
func DecompressFrame(b []byte) ([]byte, error) {
	if len(b) < 7 {
		return nil, ErrFrame
	}
	if binary.LittleEndian.Uint32(b[:4]) != frameMagic {
		return nil, fmt.Errorf("%w: bad magic", ErrFrame)
	}
	p := 4
	flg := b[p]
	bd := b[p+1]
	p += 2
	if flg>>6 != 0b01 {
		return nil, fmt.Errorf("%w: unsupported version %d", ErrFrame, flg>>6)
	}
	blockChecksum := flg&(1<<4) != 0
	contentSize := flg&(1<<3) != 0
	contentChecksum := flg&(1<<2) != 0
	dictID := flg&1 != 0
	_ = bd // block max size only bounds allocations; we trust declared sizes

	headerStart := 4
	if contentSize {
		p += 8
	}
	if dictID {
		p += 4
	}
	if p >= len(b) {
		return nil, ErrFrame
	}
	// HC byte: second byte of xxh32 (seed 0) over the descriptor.
	hc := b[p]
	if byte(xxh32.Checksum(b[headerStart:p], 0)>>8) != hc {
		return nil, fmt.Errorf("%w: header checksum", ErrFrame)
	}
	p++

	var out []byte
	for {
		if p+4 > len(b) {
			return nil, fmt.Errorf("%w: truncated block size", ErrFrame)
		}
		size := binary.LittleEndian.Uint32(b[p:])
		p += 4
		if size == 0 { // EndMark
			break
		}
		uncompressed := size&(1<<31) != 0
		n := int(size &^ (1 << 31))
		if n > len(b)-p { // subtraction form so a large n cannot overflow the add
			return nil, fmt.Errorf("%w: truncated block", ErrFrame)
		}
		data := b[p : p+n]
		p += n
		if blockChecksum {
			if p+4 > len(b) {
				return nil, fmt.Errorf("%w: truncated block checksum", ErrFrame)
			}
			if binary.LittleEndian.Uint32(b[p:]) != xxh32.Checksum(data, 0) {
				return nil, fmt.Errorf("%w: block checksum", ErrFrame)
			}
			p += 4
		}
		if uncompressed {
			if len(out)+len(data) > maxDecompressed {
				return nil, fmt.Errorf("%w: decompressed size exceeds limit", ErrFrame)
			}
			out = append(out, data...)
			continue
		}
		dec, err := decompressBlock(data, out, maxDecompressed)
		if err != nil {
			return nil, err
		}
		out = dec
	}
	if contentChecksum {
		if p+4 > len(b) {
			return nil, fmt.Errorf("%w: truncated content checksum", ErrFrame)
		}
		if binary.LittleEndian.Uint32(b[p:]) != xxh32.Checksum(out, 0) {
			return nil, fmt.Errorf("%w: content checksum", ErrFrame)
		}
	}
	return out, nil
}

// decompressBlock decodes one raw LZ4 block, appending to dst (matches may
// reference earlier dst bytes only within this frame's already-decoded output).
func decompressBlock(src, dst []byte, maxOut int) ([]byte, error) {
	base := 0 // matches may not reach before the frame's own output start
	i := 0
	for i < len(src) {
		token := src[i]
		i++
		// literals
		litLen := int(token >> 4)
		if litLen == 15 {
			for {
				if i >= len(src) {
					return nil, fmt.Errorf("%w: literal length", ErrFrame)
				}
				c := src[i]
				i++
				litLen += int(c)
				if c != 255 {
					break
				}
			}
		}
		if litLen > len(src)-i {
			return nil, fmt.Errorf("%w: literals overrun", ErrFrame)
		}
		if len(dst)+litLen > maxOut {
			return nil, fmt.Errorf("%w: decompressed size exceeds limit", ErrFrame)
		}
		dst = append(dst, src[i:i+litLen]...)
		i += litLen
		if i == len(src) {
			break // last sequence carries literals only
		}
		// match
		if i+2 > len(src) {
			return nil, fmt.Errorf("%w: match offset", ErrFrame)
		}
		offset := int(binary.LittleEndian.Uint16(src[i:]))
		i += 2
		if offset == 0 || offset > len(dst)-base {
			return nil, fmt.Errorf("%w: bad match offset %d", ErrFrame, offset)
		}
		matchLen := int(token&0x0F) + 4
		if token&0x0F == 15 {
			for {
				if i >= len(src) {
					return nil, fmt.Errorf("%w: match length", ErrFrame)
				}
				c := src[i]
				i++
				matchLen += int(c)
				if c != 255 {
					break
				}
			}
		}
		if len(dst)+matchLen > maxOut {
			return nil, fmt.Errorf("%w: decompressed size exceeds limit", ErrFrame)
		}
		// byte-by-byte copy: overlapping matches replicate runs by design
		pos := len(dst) - offset
		for k := 0; k < matchLen; k++ {
			dst = append(dst, dst[pos+k])
		}
	}
	return dst, nil
}
