package loro

import (
	"slices"
	"testing"
)

// TestDecodeKeepsMessagesAndBlocks reads an export of two ranges of peer 1's
// history and one of peer 2's (testdata/gen/gen_encode.mjs). Each of peer 1's
// commits carries a message, so no two of its changes merge, and loro writes
// the second range of peer 1 into a second layer of blocks, after peer 2's.
// Both are in the bytes and both must survive decoding: the messages, and how
// the changes were grouped into blocks, which EncodeUpdates needs to write the
// blob back as it was.
func TestDecodeKeepsMessagesAndBlocks(t *testing.T) {
	u, err := DecodeUpdates(readFixture(t, "encode_in_range.update.bin"))
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		id  ID
		msg string
	}
	var got []row
	for _, ch := range u.Changes {
		got = append(got, row{ch.ID, ch.CommitMsg})
	}
	want := []row{
		{ID{1, 0}, "line 0"}, {ID{1, 1}, "line 1"}, {ID{1, 2}, "line 2"},
		{ID{2, 0}, ""},
		{ID{1, 10}, "line 10"}, {ID{1, 11}, "line 11"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("changes and messages %v, want %v", got, want)
	}
	if want := []int{3, 1, 2}; !slices.Equal(u.Blocks, want) {
		t.Errorf("blocks %v, want %v", u.Blocks, want)
	}
}
