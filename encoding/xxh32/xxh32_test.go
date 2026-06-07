package xxh32

import "testing"

// Canonical XXH32 reference vectors (seed 0).
func TestKnownVectors(t *testing.T) {
	cases := []struct {
		in   string
		seed uint32
		want uint32
	}{
		{"", 0, 0x02CC5D05},
		{"", 1, 0x0B2CB792},
		{"abc", 0, 0x32D153FF},
	}
	for _, c := range cases {
		if got := Checksum([]byte(c.in), c.seed); got != c.want {
			t.Errorf("Checksum(%q, %d) = %#08x, want %#08x", c.in, c.seed, got, c.want)
		}
	}
}
