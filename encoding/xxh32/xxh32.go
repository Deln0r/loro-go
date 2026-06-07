// Package xxh32 is a pure-Go (no CGo) implementation of the XXH32 hash, used by
// Loro's Fast format for the header checksum and every KV/SSTable block and meta
// checksum. Loro's seed is 0x4F524F4C (little-endian bytes of "LORO").
package xxh32

import "math/bits"

const (
	prime1 uint32 = 2654435761
	prime2 uint32 = 2246822519
	prime3 uint32 = 3266489917
	prime4 uint32 = 668265263
	prime5 uint32 = 374761393
)

// Seed is the constant seed Loro uses everywhere: u32::from_le_bytes(*b"LORO").
const Seed uint32 = 0x4F524F4C

func round(acc, input uint32) uint32 {
	acc += input * prime2
	acc = bits.RotateLeft32(acc, 13)
	return acc * prime1
}

func le32(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}

// Checksum returns the XXH32 of data with the given seed.
func Checksum(data []byte, seed uint32) uint32 {
	n := len(data)
	var h uint32
	i := 0
	if n >= 16 {
		v1 := seed + prime1 + prime2
		v2 := seed + prime2
		v3 := seed
		v4 := seed - prime1
		for ; i <= n-16; i += 16 {
			v1 = round(v1, le32(data[i:]))
			v2 = round(v2, le32(data[i+4:]))
			v3 = round(v3, le32(data[i+8:]))
			v4 = round(v4, le32(data[i+12:]))
		}
		h = bits.RotateLeft32(v1, 1) + bits.RotateLeft32(v2, 7) +
			bits.RotateLeft32(v3, 12) + bits.RotateLeft32(v4, 18)
	} else {
		h = seed + prime5
	}
	h += uint32(n)
	for ; i <= n-4; i += 4 {
		h += le32(data[i:]) * prime3
		h = bits.RotateLeft32(h, 17) * prime4
	}
	for ; i < n; i++ {
		h += uint32(data[i]) * prime5
		h = bits.RotateLeft32(h, 11) * prime1
	}
	h ^= h >> 15
	h *= prime2
	h ^= h >> 13
	h *= prime3
	h ^= h >> 16
	return h
}
