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
	// emptyPageAt injects one empty chunk that still carries a live "end"
	// token, which a real server may do when per-user history visibility
	// removes every event on a page. Replay must keep walking.
	emptyPageAt int
	emptyServed bool
	// encrypted makes the room answer with an m.room.encryption state event,
	// as a room with E2EE turned on does.
	encrypted bool
	// stateBroken makes the state lookup fail with something other than a
	// clean "not found", so the fail-closed path is exercised.
	stateBroken bool
}

func (f *fakeHomeserver) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/_matrix/client/v3/rooms/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/send/"):
			f.send(w, r)
		case strings.HasSuffix(r.URL.Path, "/messages"):
			f.messages(w, r)
		case strings.Contains(r.URL.Path, "/state/m.room.encryption"):
			f.encryptionState(w, r)
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

// encryptionState answers the way a homeserver does: an encrypted room has the
// state event, an unencrypted one answers 404 M_NOT_FOUND.
func (f *fakeHomeserver) encryptionState(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch {
	case f.stateBroken:
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"errcode":"M_UNKNOWN","error":"nope"}`))
	case f.encrypted:
		_, _ = w.Write([]byte(`{"algorithm":"m.megolm.v1.aes-sha2"}`))
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"errcode":"M_NOT_FOUND","error":"Event not found."}`))
	}
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

// messages mirrors what Dendrite was measured to do, which is not what an
// earlier version of this double assumed.
//
// Dendrite does not care whether the token is empty; it cares about direction.
// A backward page is anchored at the live end of the timeline, so dir=b with
// the token absent or empty is served normally. A forward page has no such
// default and answers 400 M_INVALID_PARAM for an absent or empty token. A
// token that is present but unparseable is rejected either way.
//
// Getting this right matters in both directions: a double more permissive than
// the server hides real bugs (that is how the original forward-paging bug
// shipped), and a double stricter than the server fails code the server would
// have served.
func (f *fakeHomeserver) messages(w http.ResponseWriter, r *http.Request) {
	badToken := func() {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"errcode":"M_INVALID_PARAM","error":"Invalid from parameter: malformed sync token"}`))
	}

	from := r.URL.Query().Get("from")
	dir := r.URL.Query().Get("dir")
	if from == "" {
		if dir == "f" {
			badToken()
			return
		}
		// Backward with no anchor: serve from the live end of the timeline.
		f.mu.Lock()
		from = fmt.Sprintf("tok%d", len(f.log))
		f.mu.Unlock()
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
	if f.emptyPageAt > 0 && !f.emptyServed && end <= f.emptyPageAt {
		// Hand back nothing for this page while still pointing further back.
		f.emptyServed = true
		chunk = nil
	}

	// The walk ends when there is no "end" token, not when a chunk is empty.
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

// TestReplayWalksPastAnEmptyPage pins the pagination stop condition. The Matrix
// spec ends a walk at the absence of an "end" token, not at an empty chunk: a
// server may legitimately return an empty page that still points further back,
// for instance when per-user history visibility removed every event on it.
// Stopping at the empty page silently truncates the document and reports
// success, which is the worst shape a bug can take here.
func TestReplayWalksPastAnEmptyPage(t *testing.T) {
	f := &fakeHomeserver{pageSize: 1, emptyPageAt: 3}
	cli := newTestClient(t, f)
	ctx := context.Background()

	const n = 5
	for i := range n {
		blob := sampleBlob(t, uint64(i+1), fmt.Sprintf("p%d", i))
		if _, err := Publish(ctx, cli, testRoomID, blob); err != nil {
			t.Fatalf("publish %d: %v", i, err)
		}
	}

	u, errs, err := Replay(ctx, cli, testRoomID, 1)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if len(errs) != 0 {
		t.Errorf("unexpected decode errors: %v", errs)
	}
	// The injected page returns nothing, modelling a server that filtered every
	// event on it away, so that one change is legitimately not visible. What
	// must survive is the walk: everything BEHIND that page has to come back.
	// Stopping at the empty page yields only the pages in front of it.
	if len(u.Changes) != n-1 {
		t.Errorf("replayed %d changes, want %d: the walk stopped at the empty page", len(u.Changes), n-1)
	}
}

// TestDoubleMatchesDendriteOnTokens pins the double against what the real
// server was measured to do, so it can neither hide a bug by being lenient nor
// fail correct code by being strict.
func TestDoubleMatchesDendriteOnTokens(t *testing.T) {
	f := &fakeHomeserver{}
	cli := newTestClient(t, f)
	ctx := context.Background()
	if _, err := Publish(ctx, cli, testRoomID, sampleBlob(t, 1, "x")); err != nil {
		t.Fatal(err)
	}

	t.Run("backward without a token is served", func(t *testing.T) {
		resp, err := cli.Messages(ctx, testRoomID, "", "", mautrix.DirectionBackward, nil, 10)
		if err != nil {
			t.Fatalf("dir=b with no token should be served, got %v", err)
		}
		if len(resp.Chunk) == 0 {
			t.Error("dir=b with no token returned nothing")
		}
	})
	t.Run("forward without a token is refused", func(t *testing.T) {
		if _, err := cli.Messages(ctx, testRoomID, "", "", mautrix.DirectionForward, nil, 10); err == nil {
			t.Error("dir=f with no token should be refused, got nil")
		}
	})
	t.Run("unparseable token is refused", func(t *testing.T) {
		if _, err := cli.Messages(ctx, testRoomID, "garbage", "", mautrix.DirectionBackward, nil, 10); err == nil {
			t.Error("an unparseable token should be refused, got nil")
		}
	})
}

// TestPublishRefusesEncryptedRoom pins the guard that matters most to a user.
// A homeserver accepts a plaintext event into an encrypted room and returns a
// normal event id, so without this check the document would land readable in a
// room where the members were promised otherwise, and nothing in the return
// value would say so.
func TestPublishRefusesEncryptedRoom(t *testing.T) {
	f := &fakeHomeserver{encrypted: true}
	cli := newTestClient(t, f)

	_, err := Publish(context.Background(), cli, testRoomID, sampleBlob(t, 1, "secret"))
	if err == nil {
		t.Fatal("published into an encrypted room, want a refusal")
	}
	if !errors.Is(err, ErrEncryptedRoom) {
		t.Errorf("error = %v, want errors.Is ErrEncryptedRoom", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.log) != 0 {
		t.Errorf("refused, but %d event(s) still reached the room", len(f.log))
	}
}

// TestPublishFailsClosedOnUnknownState pins the direction of the failure: if
// the room's encryption state cannot be read at all, publishing is refused
// rather than risked.
func TestPublishFailsClosedOnUnknownState(t *testing.T) {
	f := &fakeHomeserver{stateBroken: true}
	cli := newTestClient(t, f)

	if _, err := Publish(context.Background(), cli, testRoomID, sampleBlob(t, 1, "x")); err == nil {
		t.Error("published without knowing whether the room is encrypted, want a refusal")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.log) != 0 {
		t.Errorf("refused, but %d event(s) still reached the room", len(f.log))
	}
}
