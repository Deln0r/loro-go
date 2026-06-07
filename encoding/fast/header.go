// Package fast decodes Loro's Fast wire format (FastUpdates + FastSnapshot),
// the only live format in the loro-crdt 1.x line. The legacy V1/RLE "Outdated"
// modes are intentionally unsupported (the upstream decoder rejects them too).
//
// Header layout (MIN_HEADER_SIZE = 22), confirmed byte-for-byte against
// loro-crdt@1.12.5 output:
//
//	[0..4)   magic "loro"
//	[4..20)  16-byte checksum region; for Fast modes only the last 4 bytes
//	         carry the xxhash32 (seed "LORO"), the first 12 are zero
//	[20..22) mode, big-endian u16
package fast

import (
	"encoding/binary"
	"errors"
)

// Mode is the 2-byte encode mode stored at header offset [20..22).
type Mode uint16

const (
	ModeOutdatedRle      Mode = 1 // dead pre-1.0 RLE format
	ModeOutdatedSnapshot Mode = 2 // dead pre-1.0 snapshot format
	ModeFastSnapshot     Mode = 3 // full-doc: oplog + DocState + gc/shallow
	ModeFastUpdates      Mode = 4 // sync wire: oplog only
)

const (
	magic       = "loro"
	checksumLen = 16
	modeLen     = 2
	// HeaderSize is the fixed Loro header length (MIN_HEADER_SIZE).
	HeaderSize = len(magic) + checksumLen + modeLen
)

var (
	ErrShort       = errors.New("loro/fast: buffer shorter than header")
	ErrMagic       = errors.New("loro/fast: bad magic")
	ErrOutdated    = errors.New("loro/fast: outdated (V1/RLE) mode not supported")
	ErrUnknownMode = errors.New("loro/fast: unknown encode mode")
)

// Header is a parsed Loro Fast-format header plus the body slice that follows.
type Header struct {
	Mode     Mode
	Checksum [16]byte // [4..20); xxhash32 in the last 4 bytes for Fast modes
	Body     []byte   // bytes after the 22-byte header (aliases the input)
}

// ParseHeader validates the 22-byte Loro header and returns it with the body
// slice. It rejects the legacy Outdated modes and unknown modes.
func ParseHeader(b []byte) (Header, error) {
	if len(b) < HeaderSize {
		return Header{}, ErrShort
	}
	if string(b[:4]) != magic {
		return Header{}, ErrMagic
	}
	var h Header
	copy(h.Checksum[:], b[4:20])
	h.Mode = Mode(binary.BigEndian.Uint16(b[20:22]))
	switch h.Mode {
	case ModeFastSnapshot, ModeFastUpdates:
		// supported
	case ModeOutdatedRle, ModeOutdatedSnapshot:
		return Header{}, ErrOutdated
	default:
		return Header{}, ErrUnknownMode
	}
	h.Body = b[HeaderSize:]
	return h, nil
}
