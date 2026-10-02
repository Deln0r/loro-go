package loro

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// parseLoroID reads an id as loro's JSON export writes it: "counter@peerIndex",
// the index pointing into the export's peer table.
func parseLoroID(peers []string, s string) (ID, error) {
	counter, peerIdx, ok := strings.Cut(s, "@")
	if !ok {
		return ID{}, fmt.Errorf("id %q", s)
	}
	pi, err := strconv.Atoi(peerIdx)
	if err != nil || pi < 0 || pi >= len(peers) {
		return ID{}, fmt.Errorf("id %q", s)
	}
	peer, err := strconv.ParseUint(peers[pi], 10, 64)
	if err != nil {
		return ID{}, err
	}
	ctr, err := strconv.ParseInt(counter, 10, 64)
	if err != nil {
		return ID{}, err
	}
	return ID{Peer: peer, Counter: ctr}, nil
}

func sortIDs(ids []ID) {
	sort.Slice(ids, func(i, j int) bool {
		if ids[i].Peer != ids[j].Peer {
			return ids[i].Peer < ids[j].Peer
		}
		return ids[i].Counter < ids[j].Counter
	})
}

// TestChangeDepsMatchLoro checks the decoded dependencies of every change in
// every fixture against the deps loro-crdt itself recorded in the fixture's
// .ops.json. Sequence merge resolves an insert against what its author had
// seen, so a wrong dep puts text in the wrong place without any error.
func TestChangeDepsMatchLoro(t *testing.T) {
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

	compared, onSelf, foreign := 0, 0, 0
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
				ID   string   `json:"id"`
				Deps []string `json:"deps"`
			} `json:"changes"`
		}
		if err := json.Unmarshal(raw, &rec); err != nil {
			// A handful of old fixtures recorded an export error here instead.
			continue
		}
		want := map[ID][]ID{}
		for _, c := range rec.Changes {
			id, err := parseLoroID(rec.Peers, c.ID)
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			deps := []ID{}
			for _, d := range c.Deps {
				dep, err := parseLoroID(rec.Peers, d)
				if err != nil {
					t.Fatalf("%s: %v", name, err)
				}
				deps = append(deps, dep)
			}
			sortIDs(deps)
			want[id] = deps
		}

		u, err := DecodeUpdates(blob)
		if err != nil {
			t.Fatalf("%s decode: %v", name, err)
		}
		for _, c := range u.Changes {
			w, ok := want[c.ID]
			if !ok {
				t.Errorf("%s: decoded change %d@%d that loro did not record", name, c.ID.Counter, c.ID.Peer)
				continue
			}
			got := append([]ID{}, c.Deps...)
			sortIDs(got)
			if fmt.Sprint(got) != fmt.Sprint(w) {
				t.Errorf("%s: change %d@%d decoded deps %v, loro recorded %v", name, c.ID.Counter, c.ID.Peer, got, w)
			}
			compared++
			for _, d := range got {
				if d.Peer == c.ID.Peer {
					onSelf++
				} else {
					foreign++
				}
			}
		}
	}
	// Both encodings must be exercised: the own-peer flag and the peer-indexed
	// list. A fixture set without either would pass on any reading of it.
	if onSelf == 0 || foreign == 0 {
		t.Fatalf("deps on own peer: %d, on other peers: %d; one of the two encodings is not exercised", onSelf, foreign)
	}
	t.Logf("%d changes compared, %d own-peer deps, %d deps on other peers", compared, onSelf, foreign)
}
