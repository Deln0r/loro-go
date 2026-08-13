package fast

import (
	"bytes"
	"testing"
)

func TestEncodeRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		mode Mode
		body []byte
	}{
		{"updates", ModeFastUpdates, []byte("some body bytes")},
		{"snapshot", ModeFastSnapshot, []byte{0x00, 0xFF, 0x10}},
		{"empty body", ModeFastUpdates, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			blob := Encode(c.mode, c.body)
			if len(blob) != HeaderSize+len(c.body) {
				t.Fatalf("len = %d, want %d", len(blob), HeaderSize+len(c.body))
			}
			h, err := ParseHeader(blob)
			if err != nil {
				t.Fatalf("ParseHeader: %v", err)
			}
			if h.Mode != c.mode {
				t.Errorf("mode = %d, want %d", h.Mode, c.mode)
			}
			if !bytes.Equal(h.Body, c.body) {
				t.Errorf("body = % x, want % x", h.Body, c.body)
			}
			if err := VerifyChecksum(blob); err != nil {
				t.Errorf("VerifyChecksum on self-encoded blob: %v", err)
			}
		})
	}
}

// TestEncodeReproducesFixtureBytes re-encodes real loro-crdt blobs from their own
// parsed mode and body, and asserts the result is byte-identical. This pins
// Encode as the exact inverse of upstream's framing, checksum placement included.
func TestEncodeReproducesFixtureBytes(t *testing.T) {
	for _, name := range []string{
		"text_hi.update.bin", "text_hi.snapshot.bin",
		"map_kv.update.bin", "cross_del.snapshot.bin",
	} {
		t.Run(name, func(t *testing.T) {
			want := fixture(t, name)
			h, err := ParseHeader(want)
			if err != nil {
				t.Fatalf("ParseHeader: %v", err)
			}
			if got := Encode(h.Mode, h.Body); !bytes.Equal(got, want) {
				t.Errorf("re-encoded blob differs from upstream (%d vs %d bytes)", len(got), len(want))
			}
		})
	}
}

func TestVerifyChecksumRejects(t *testing.T) {
	good := Encode(ModeFastUpdates, []byte("payload"))

	t.Run("short buffer", func(t *testing.T) {
		if err := VerifyChecksum(good[:HeaderSize-1]); err != ErrShort {
			t.Errorf("got %v, want ErrShort", err)
		}
	})

	t.Run("tampered body", func(t *testing.T) {
		bad := append([]byte{}, good...)
		bad[len(bad)-1] ^= 0xFF
		if err := VerifyChecksum(bad); err != ErrChecksum {
			t.Errorf("got %v, want ErrChecksum", err)
		}
	})

	t.Run("tampered stored checksum", func(t *testing.T) {
		bad := append([]byte{}, good...)
		bad[16] ^= 0xFF
		if err := VerifyChecksum(bad); err != ErrChecksum {
			t.Errorf("got %v, want ErrChecksum", err)
		}
	})

	// The hash covers the mode field as well as the body, so swapping a valid
	// mode for another valid one must still fail.
	t.Run("tampered mode", func(t *testing.T) {
		bad := append([]byte{}, good...)
		bad[21] = byte(ModeFastSnapshot)
		if err := VerifyChecksum(bad); err != ErrChecksum {
			t.Errorf("got %v, want ErrChecksum", err)
		}
	})
}
