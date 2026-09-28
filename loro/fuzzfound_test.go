package loro

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// corpusInput reads the single []byte argument out of a go fuzz corpus file.
func corpusInput(t *testing.T, target, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "fuzz", target, name))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if rest, ok := strings.CutPrefix(line, "[]byte("); ok {
			s, err := strconv.Unquote(strings.TrimSuffix(rest, ")"))
			if err != nil {
				t.Fatal(err)
			}
			return []byte(s)
		}
	}
	t.Fatalf("%s/%s: no []byte argument", target, name)
	return nil
}

// TestInsertLengthMismatchIsRejected replays the second input the resealed
// fuzzer found. It holds two text inserts of length 1 that each carry two
// characters, so the merge created elements whose ids belonged to the next op
// and one insert became its own left neighbour; the flatten then recursed until
// the process died. Such an op is malformed and must be refused at decode.
func TestInsertLengthMismatchIsRejected(t *testing.T) {
	data := reseal(corpusInput(t, "FuzzDecodeUpdates", "6708d8421908ebbe"))
	_, err := DecodeUpdates(data)
	if err == nil {
		t.Fatal("inserts whose length disagrees with their value decoded without error")
	}
	if !strings.Contains(err.Error(), "carries") {
		t.Fatalf("expected the insert-length error, got: %v", err)
	}
}

// TestFlattenSurvivesCollidingIDs checks the guard beneath that rejection.
// Two elements share the id 1@1, and the second names 1@1 as its left origin,
// which is the shape the malformed inserts produced. The walk must emit each
// id once and stop, not recurse through the collision forever.
func TestFlattenSurvivesCollidingIDs(t *testing.T) {
	all := []elem{
		{peer: 1, counter: 0, lamport: 0, value: "a"},
		{peer: 1, counter: 1, lamport: 1, value: "b", hasLeft: true, leftPeer: 1, leftCounter: 0},
		{peer: 1, counter: 1, lamport: 2, value: "c", hasLeft: true, leftPeer: 1, leftCounter: 1},
	}
	out := flatten(all)
	if len(out) != 2 {
		t.Fatalf("flatten emitted %d elements, want the 2 distinct ids", len(out))
	}
}
