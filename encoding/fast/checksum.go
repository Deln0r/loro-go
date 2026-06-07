package fast

import (
	"encoding/binary"
	"errors"

	"github.com/Deln0r/loro-go/encoding/xxh32"
)

// ErrChecksum means the stored Fast-mode checksum does not match the recomputed
// xxh32 over the mode field plus body.
var ErrChecksum = errors.New("loro/fast: checksum mismatch")

// VerifyChecksum recomputes the Fast-mode checksum and compares it to the stored
// value. For Fast modes the checksum is xxh32(bytes[20:], seed) stored
// little-endian in checksum bytes [12..16] (absolute [16..20)). The hash covers
// the 2-byte mode field plus the entire body, not just the body.
func VerifyChecksum(b []byte) error {
	if len(b) < HeaderSize {
		return ErrShort
	}
	stored := binary.LittleEndian.Uint32(b[16:20])
	got := xxh32.Checksum(b[20:], xxh32.Seed)
	if got != stored {
		return ErrChecksum
	}
	return nil
}
