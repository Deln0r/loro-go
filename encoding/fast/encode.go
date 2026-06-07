package fast

import (
	"encoding/binary"

	"github.com/Deln0r/loro-go/encoding/xxh32"
)

// Encode builds a Fast-format blob: magic + 16-byte checksum + mode (big-endian)
// + body. For Fast modes the checksum is xxh32 over (mode bytes ++ body) stored
// little-endian in checksum bytes [12..16]. Inverse of ParseHeader/VerifyChecksum.
func Encode(mode Mode, body []byte) []byte {
	out := make([]byte, 0, HeaderSize+len(body))
	out = append(out, magic...)
	var cs [16]byte
	var mb [2]byte
	binary.BigEndian.PutUint16(mb[:], uint16(mode))
	hashInput := make([]byte, 0, 2+len(body))
	hashInput = append(hashInput, mb[:]...)
	hashInput = append(hashInput, body...)
	binary.LittleEndian.PutUint32(cs[12:16], xxh32.Checksum(hashInput, xxh32.Seed))
	out = append(out, cs[:]...)
	out = append(out, mb[:]...)
	out = append(out, body...)
	return out
}
