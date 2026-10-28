package loromatrix

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/Deln0r/loro-go/loro"
)

// FuzzCollectRaw feeds one arbitrary wire event to the per-event decoder that
// Replay runs over every /sync and /messages page. Whatever a homeserver puts
// there, the decoder must return changes or an error, never panic or hang: a
// crash here would take the whole replay down, which is what decoding events
// one at a time exists to prevent.
func FuzzCollectRaw(f *testing.F) {
	d := loro.NewDocument(1)
	if err := d.Text("title").Insert(0, "hello"); err != nil {
		f.Fatal(err)
	}
	blob, err := d.ExportUpdates()
	if err != nil {
		f.Fatal(err)
	}
	payload := base64.StdEncoding.EncodeToString(blob)
	valid, _ := json.Marshal(map[string]any{
		"type": EventType, "event_id": "$ok", "content": map[string]any{"format": Format, "payload": payload},
	})
	for _, seed := range []string{
		string(valid),
		`{"type":"dev.loro.update","event_id":"$bad","content":"not an object"}`,
		`{"type":"dev.loro.update","content":{"format":"loro-fast-updates-v1","payload":"!!"}}`,
		`{"type":"dev.loro.update","content":{"format":"loro-fast-updates-v1","payload":"bG9ybw=="}}`,
		`{"type":"m.room.message","content":{"body":"hi"}}`,
		`"not an event"`,
		`null`,
		`[]`,
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		u, errs := CollectRaw([]json.RawMessage{data})
		if u == nil {
			t.Fatal("CollectRaw returned nil updates")
		}
		if len(u.Changes) > 0 && len(errs) > 0 {
			t.Fatalf("one event both decoded and failed: %v", errs)
		}
		if len(u.Changes) > 0 {
			if _, err := loro.MergeState(u); err != nil {
				return // a merge may refuse what decoding let through, with an error
			}
		}
	})
}
