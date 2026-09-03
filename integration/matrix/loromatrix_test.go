package loromatrix

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/Deln0r/loro-go/loro"
)

// fakeHomeserver is enough of the client-server API to exercise the transport:
// it accepts sent events into an ordered room log and pages that log back out.
// It is not a Matrix implementation and does not pretend to be one; the
// end-to-end check against a real homeserver lives in the docker-compose demo.
const testRoomID = id.RoomID("!room:example.org")

type fakeHomeserver struct {
	mu     sync.Mutex
	log    []*event.Event
	nextID int
	// pageSize forces Replay to page when set, so the paging loop is covered.
	pageSize int
}

func (f *fakeHomeserver) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/_matrix/client/v3/rooms/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/send/"):
			f.send(w, r)
		case strings.HasSuffix(r.URL.Path, "/messages"):
			f.messages(w, r)
		default:
			http.Error(w, `{"errcode":"M_UNRECOGNIZED"}`, http.StatusNotFound)
		}
	})
	mux.HandleFunc("/_matrix/client/v3/sync", f.sync)
	return mux
}

// sync returns the newest slice of the room plus a prev_batch anchor, which is
// how Replay gets a token it can page backwards from.
func (f *fakeHomeserver) sync(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	size := f.pageSize
	if size <= 0 {
		size = len(f.log)
	}
	start := len(f.log) - size
	if start < 0 {
		start = 0
	}
	newest := append([]*event.Event(nil), f.log[start:]...)

	resp := map[string]any{
		"next_batch": "now",
		"rooms": map[string]any{
			"join": map[string]any{
				string(testRoomID): map[string]any{
					"timeline": map[string]any{
						"events":     newest,
						"prev_batch": fmt.Sprintf("tok%d", start),
						"limited":    start > 0,
					},
				},
			},
		},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (f *fakeHomeserver) send(w http.ResponseWriter, r *http.Request) {
	var raw json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		http.Error(w, `{"errcode":"M_BAD_JSON"}`, http.StatusBadRequest)
		return
	}
	// .../send/{eventType}/{txnID}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	evtType := parts[len(parts)-2]

	f.mu.Lock()
	f.nextID++
	evtID := id.EventID(fmt.Sprintf("$evt%d", f.nextID))
	f.log = append(f.log, &event.Event{
		ID:      evtID,
		Type:    event.NewEventType(evtType),
		Content: event.Content{VeryRaw: raw},
	})
	f.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"event_id": string(evtID)})
}

// messages pages backwards from a token, and rejects a missing one exactly as
// Dendrite does. That rejection is deliberate: the first version of Replay
// started from an empty token, this double happily accepted it, and only a real
// homeserver caught the bug. A double that is more permissive than the server
// it stands in for cannot catch that class of defect, so this one is not.
func (f *fakeHomeserver) messages(w http.ResponseWriter, r *http.Request) {
	from := r.URL.Query().Get("from")
	if from == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"errcode":"M_INVALID_PARAM","error":"Invalid from parameter: malformed sync token"}`))
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	end := 0
	if _, err := fmt.Sscanf(from, "tok%d", &end); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"errcode":"M_INVALID_PARAM","error":"malformed sync token"}`))
		return
	}
	if end > len(f.log) {
		end = len(f.log)
	}
	size := f.pageSize
	if size <= 0 {
		size = len(f.log)
	}
	start := end - size
	if start < 0 {
		start = 0
	}
	chunk := append([]*event.Event(nil), f.log[start:end]...)

	// An exhausted room returns an empty chunk, which is Replay's stop signal.
	resp := map[string]any{"start": from, "chunk": chunk}
	if start > 0 {
		resp["end"] = fmt.Sprintf("tok%d", start)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func newTestClient(t *testing.T, f *fakeHomeserver) *mautrix.Client {
	t.Helper()
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	cli, err := mautrix.NewClient(srv.URL, id.UserID("@peer:example.org"), "token")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return cli
}

// sampleBlob builds a real FastUpdates export from the library itself, so the
// transport is exercised against genuine bytes rather than a hand-made stub.
func sampleBlob(t *testing.T, peer uint64, text string) []byte {
	t.Helper()
	d := loro.NewDoc(peer)
	d.TextInsert("title", 0, text)
	blob := d.ExportUpdates()
	if len(blob) == 0 {
		t.Fatal("ExportUpdates returned no bytes")
	}
	return blob
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	blob := sampleBlob(t, 1, "hello")
	content, err := Encode(blob)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if content.Format != Format {
		t.Errorf("format = %q, want %q", content.Format, Format)
	}
	got, err := Decode(content)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if string(got) != string(blob) {
		t.Error("round-trip changed the blob bytes")
	}
}

func TestDecodeRejects(t *testing.T) {
	good := sampleBlob(t, 1, "hi")
	b64 := base64.StdEncoding.EncodeToString(good)

	corrupt := append([]byte(nil), good...)
	corrupt[len(corrupt)-1] ^= 0xFF // break the body, so the checksum no longer matches

	cases := []struct {
		name    string
		content *UpdateContent
		wantIs  error
	}{
		{"nil content", nil, nil},
		{"foreign format", &UpdateContent{Format: "something-else", Payload: b64}, ErrForeignFormat},
		{"not base64", &UpdateContent{Format: Format, Payload: "!!!not base64!!!"}, nil},
		{"empty payload", &UpdateContent{Format: Format, Payload: ""}, nil},
		{"corrupt blob", &UpdateContent{Format: Format, Payload: base64.StdEncoding.EncodeToString(corrupt)}, nil},
		{"truncated blob", &UpdateContent{Format: Format, Payload: base64.StdEncoding.EncodeToString(good[:10])}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Decode(c.content)
			if err == nil {
				t.Fatal("want an error, got nil")
			}
			if c.wantIs != nil && !errors.Is(err, c.wantIs) {
				t.Errorf("error = %v, want errors.Is %v", err, c.wantIs)
			}
		})
	}
}

// TestEncodeRefusesBadBlob pins that a broken local export is caught before it
// is published, rather than becoming every reader's problem.
func TestEncodeRefusesBadBlob(t *testing.T) {
	if _, err := Encode([]byte("not a loro blob")); err == nil {
		t.Error("want an error encoding a non-loro blob, got nil")
	}
}

func TestCollectSkipsAndReports(t *testing.T) {
	blob := sampleBlob(t, 1, "abc")
	good, err := Encode(blob)
	if err != nil {
		t.Fatal(err)
	}
	goodRaw, _ := json.Marshal(good)
	corruptRaw, _ := json.Marshal(&UpdateContent{Format: Format, Payload: "@@@"})
	foreignRaw, _ := json.Marshal(&UpdateContent{Format: "other-format", Payload: "irrelevant"})

	events := []*event.Event{
		nil,
		{ID: "$m", Type: event.NewEventType("m.room.message"), Content: event.Content{VeryRaw: []byte(`{"body":"hi"}`)}},
		{ID: "$good", Type: LoroUpdate, Content: event.Content{VeryRaw: goodRaw}},
		{ID: "$corrupt", Type: LoroUpdate, Content: event.Content{VeryRaw: corruptRaw}},
		{ID: "$foreign", Type: LoroUpdate, Content: event.Content{VeryRaw: foreignRaw}},
	}

	u, errs := Collect(events)
	if len(u.Changes) == 0 {
		t.Error("the one good event produced no changes")
	}
	// The corrupt event is reported; the room message and the foreign-format
	// event are skipped in silence.
	if len(errs) != 1 {
		t.Errorf("got %d errors %v, want exactly 1 (the corrupt event)", len(errs), errs)
	} else if !strings.Contains(errs[0].Error(), "$corrupt") {
		t.Errorf("error does not name the offending event: %v", errs[0])
	}
}

func TestPublishAndReplay(t *testing.T) {
	f := &fakeHomeserver{}
	cli := newTestClient(t, f)
	ctx := context.Background()
	room := testRoomID

	blob := sampleBlob(t, 7, "published")
	evtID, err := Publish(ctx, cli, room, blob)
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if evtID == "" {
		t.Error("Publish returned an empty event id")
	}

	u, errs, err := Replay(ctx, cli, room, 0)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if len(errs) != 0 {
		t.Errorf("unexpected decode errors: %v", errs)
	}
	state, err := State(u)
	if err != nil {
		t.Fatalf("State: %v", err)
	}
	if state["title"] != "published" {
		t.Errorf("state = %#v, want title \"published\"", state)
	}
}

// TestReplayPages covers the paging loop: with a page size of one the fake
// homeserver hands back a single event per request and Replay must walk them
// all. A non-paging implementation passes the previous test and fails this one.
func TestReplayPages(t *testing.T) {
	f := &fakeHomeserver{pageSize: 1}
	cli := newTestClient(t, f)
	ctx := context.Background()
	room := testRoomID

	const n = 5
	for i := range n {
		blob := sampleBlob(t, uint64(i+1), fmt.Sprintf("p%d", i))
		if _, err := Publish(ctx, cli, room, blob); err != nil {
			t.Fatalf("Publish %d: %v", i, err)
		}
	}

	u, errs, err := Replay(ctx, cli, room, 1)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if len(errs) != 0 {
		t.Errorf("unexpected decode errors: %v", errs)
	}
	if len(u.Changes) != n {
		t.Errorf("replayed %d changes, want %d (paging lost events)", len(u.Changes), n)
	}
}

// TestTwoPeersConvergeOffline is the point of the whole package: two peers edit
// while neither can see the other, both publish when they reconnect, and both
// arrive at the same document. The room is the only thing they share, and it
// gives no ordering guarantee they rely on.
func TestTwoPeersConvergeOffline(t *testing.T) {
	f := &fakeHomeserver{}
	ctx := context.Background()
	room := testRoomID

	alice := newTestClient(t, f)
	bob := newTestClient(t, f)

	// Offline: each peer edits its own copy, neither has seen the other.
	aliceDoc := loro.NewDoc(1)
	aliceDoc.TextInsert("notes", 0, "alice was here")
	aliceDoc.MapSet("meta", "author", "alice")

	bobDoc := loro.NewDoc(2)
	bobDoc.TextInsert("notes", 0, "bob was here")
	bobDoc.MapSet("meta", "reviewed", int64(1))

	// Reconnect, in an order chosen to be awkward: bob publishes first.
	if _, err := Publish(ctx, bob, room, bobDoc.ExportUpdates()); err != nil {
		t.Fatalf("bob publish: %v", err)
	}
	if _, err := Publish(ctx, alice, room, aliceDoc.ExportUpdates()); err != nil {
		t.Fatalf("alice publish: %v", err)
	}

	aliceView, errs, err := Replay(ctx, alice, room, 0)
	if err != nil || len(errs) != 0 {
		t.Fatalf("alice replay: %v %v", err, errs)
	}
	bobView, errs, err := Replay(ctx, bob, room, 0)
	if err != nil || len(errs) != 0 {
		t.Fatalf("bob replay: %v %v", err, errs)
	}

	aliceState, err := State(aliceView)
	if err != nil {
		t.Fatalf("alice state: %v", err)
	}
	bobState, err := State(bobView)
	if err != nil {
		t.Fatalf("bob state: %v", err)
	}

	aliceJSON, _ := json.Marshal(aliceState)
	bobJSON, _ := json.Marshal(bobState)
	if string(aliceJSON) != string(bobJSON) {
		t.Fatalf("peers diverged:\n alice = %s\n bob   = %s", aliceJSON, bobJSON)
	}

	// Convergence to an empty document would satisfy the comparison above and
	// prove nothing, so assert both edits actually survived the merge.
	notes, _ := aliceState["notes"].(string)
	if !strings.Contains(notes, "alice") || !strings.Contains(notes, "bob") {
		t.Errorf("converged text lost an edit: %q", notes)
	}
	meta, _ := aliceState["meta"].(map[string]any)
	if meta["author"] != "alice" {
		t.Errorf("alice's map entry missing: %#v", meta)
	}
	if meta["reviewed"] == nil {
		t.Errorf("bob's map entry missing: %#v", meta)
	}
}

// TestConvergenceIsOrderIndependent replays the same two updates in both
// orders. A transport that leaked ordering into the result would differ here.
func TestConvergenceIsOrderIndependent(t *testing.T) {
	a := sampleBlob(t, 1, "aaa")
	b := sampleBlob(t, 2, "bbb")

	build := func(blobs ...[]byte) string {
		t.Helper()
		merged := &loro.Updates{}
		for _, blob := range blobs {
			u, err := loro.DecodeUpdates(blob)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			merged.Changes = append(merged.Changes, u.Changes...)
		}
		state, err := State(merged)
		if err != nil {
			t.Fatalf("state: %v", err)
		}
		out, _ := json.Marshal(state)
		return string(out)
	}

	if forward, reverse := build(a, b), build(b, a); forward != reverse {
		t.Errorf("order changed the result:\n a,b = %s\n b,a = %s", forward, reverse)
	}
}
