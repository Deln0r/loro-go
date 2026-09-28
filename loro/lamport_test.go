package loro

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// TestChangeLamportsMatchLoro checks the decoded lamport of every change in
// every fixture against the lamport loro-crdt itself recorded for that change
// in the fixture's .ops.json.
//
// The state tests could not catch a wrong lamport. They compare the final
// toJSON, and a peer's own sequential edits come out the same whatever clock
// they carry, so two_changes decoded its second change with the first change's
// lamport for months and still passed. A lamport only decides anything once it
// is compared with another peer's, which is exactly where an error does damage.
// This test compares the lamports themselves.
func TestChangeLamportsMatchLoro(t *testing.T) {
	dir := filepath.Join("..", "testdata", "fixtures")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if name, ok := strings.CutSuffix(e.Name(), ".ops.json"); ok {
			names = append(names, name)
		}
	}
	sort.Strings(names)

	type changeKey struct {
		peer    uint64
		counter int64
	}
	compared, multiChange := 0, 0
	for _, name := range names {
		blob, err := os.ReadFile(filepath.Join(dir, name+".update.bin"))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		raw, err := os.ReadFile(filepath.Join(dir, name+".ops.json"))
		if err != nil {
			t.Fatal(err)
		}
		var rec struct {
			Peers   []string `json:"peers"`
			Changes []struct {
				ID      string `json:"id"` // "counter@peerIndex"
				Lamport int64  `json:"lamport"`
			} `json:"changes"`
		}
		if err := json.Unmarshal(raw, &rec); err != nil {
			// A handful of old fixtures recorded an export error here instead.
			continue
		}
		want := map[changeKey]int64{}
		for _, c := range rec.Changes {
			counter, peerIdx, ok := strings.Cut(c.ID, "@")
			if !ok {
				t.Fatalf("%s: change id %q", name, c.ID)
			}
			pi, err := strconv.Atoi(peerIdx)
			if err != nil || pi < 0 || pi >= len(rec.Peers) {
				t.Fatalf("%s: change id %q", name, c.ID)
			}
			peer, err := strconv.ParseUint(rec.Peers[pi], 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			ctr, err := strconv.ParseInt(counter, 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			want[changeKey{peer, ctr}] = c.Lamport
		}

		u, err := DecodeUpdates(blob)
		if err != nil {
			t.Fatalf("%s decode: %v", name, err)
		}
		perPeer := map[uint64]int{}
		for _, c := range u.Changes {
			perPeer[c.ID.Peer]++
			w, ok := want[changeKey{c.ID.Peer, c.ID.Counter}]
			if !ok {
				t.Errorf("%s: decoded change %d@%d that loro did not record", name, c.ID.Counter, c.ID.Peer)
				continue
			}
			compared++
			if c.Lamport != w {
				t.Errorf("%s: change %d@%d decoded lamport %d, loro recorded %d", name, c.ID.Counter, c.ID.Peer, c.Lamport, w)
			}
		}
		for _, n := range perPeer {
			if n > 1 {
				multiChange++
				break
			}
		}
	}
	// Without a fixture whose block holds several changes, the header lamport
	// column is never read and this test would pass on any reading of it.
	if multiChange < 2 {
		t.Fatalf("only %d fixtures put several changes in one peer's block; the header lamport column is not exercised", multiChange)
	}
	t.Logf("%d change lamports compared, %d fixtures with several changes in one block", compared, multiChange)
}
