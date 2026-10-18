// Package loromatrix carries Loro CRDT updates over the Matrix protocol.
//
// A room is the log. Every local edit is exported as a FastUpdates blob and
// published as one custom timeline event; a peer coming back online replays the
// room and merges what it finds. Nothing here assumes the events arrive in
// order, exactly once, or at all before the merge: that is what makes a CRDT
// the right payload for a federated transport that guarantees none of those.
//
// This package deliberately stops at transport. It does not implement a sync
// protocol, a version-vector diff, or a server. Peers exchange whole update
// blobs and let the CRDT converge.
//
// It lives in its own Go module so that the loro-go library itself keeps a
// dependency-free go.mod.
package loromatrix

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/Deln0r/loro-go/loro"
)

// EventType is the custom Matrix event type carrying one Loro update blob.
const EventType = "dev.loro.update"

// Format is the value of UpdateContent.Format this package writes and accepts.
// It names the payload encoding so a future format can be introduced without
// making old events ambiguous.
const Format = "loro-fast-updates-v1"

// LoroUpdate is the mautrix event type for EventType. Content is read straight
// off Content.VeryRaw rather than through mautrix's parsed-content registry, so
// this package needs no global registration to be usable from another program.
var LoroUpdate = event.NewEventType(EventType)

// UpdateContent is the JSON body of a dev.loro.update event.
type UpdateContent struct {
	Format string `json:"format"`
	// Payload is the standard-base64 encoding of a loro FastUpdates blob,
	// exactly as loro-crdt's doc.export({mode:"update"}) produces it.
	Payload string `json:"payload"`
}

// ErrForeignFormat is returned for an event whose format field is not Format.
// It is not a failure: a room may legitimately carry other event types, and a
// reader skips what it does not understand.
var ErrForeignFormat = errors.New("loromatrix: unrecognised update format")

// Encode wraps a FastUpdates blob as event content. The blob is parsed first,
// so a malformed local export fails here rather than being published into a
// room where every reader would have to reject it.
func Encode(blob []byte) (*UpdateContent, error) {
	if _, err := loro.DecodeUpdates(blob); err != nil {
		return nil, fmt.Errorf("loromatrix: refusing to publish an unparseable blob: %w", err)
	}
	return &UpdateContent{
		Format:  Format,
		Payload: base64.StdEncoding.EncodeToString(blob),
	}, nil
}

// Decode unwraps event content back into a FastUpdates blob, checking the
// header and checksum before the payload reaches the merge.
//
// Be clear about what that buys. It buys availability of reading: a malformed
// or hostile blob errors out instead of taking the reader down with it. It buys
// nothing about whether the sender was entitled to send what it sent. Deletes
// here are id-addressed and carry no authorisation, so any peer holding the ids
// may tombstone them, and in a Matrix room the right to write is the right to
// delete everyone else's content. Room membership is full authority over the
// document, not a reader role. TestValidationIsNotAuthorisation demonstrates it.
func Decode(c *UpdateContent) ([]byte, error) {
	if c == nil {
		return nil, errors.New("loromatrix: nil content")
	}
	if c.Format != Format {
		return nil, fmt.Errorf("%w: %q", ErrForeignFormat, c.Format)
	}
	blob, err := base64.StdEncoding.DecodeString(c.Payload)
	if err != nil {
		return nil, fmt.Errorf("loromatrix: payload is not valid base64: %w", err)
	}
	if _, err := loro.DecodeUpdates(blob); err != nil {
		return nil, fmt.Errorf("loromatrix: payload is not a valid FastUpdates blob: %w", err)
	}
	return blob, nil
}

// ErrEncryptedRoom is returned when the target room is encrypted and this
// client has no crypto helper, so publishing would put the document into the
// room in the clear.
var ErrEncryptedRoom = errors.New("loromatrix: room is encrypted and this client cannot encrypt")

// Publish exports the document's current updates and sends them to the room as
// one event, returning the event id the homeserver assigned.
//
// If the room is encrypted and the client cannot encrypt, Publish refuses.
// A homeserver will happily accept a plaintext event into an encrypted room and
// return a normal event id, so without this check the document would land
// readable in a room where every member was told otherwise, with nothing in the
// return value to say so. When the caller has installed a crypto helper,
// mautrix encrypts outgoing events itself and the check is skipped.
//
// The check costs one state request per publish. That is deliberate: caching it
// would mean trusting a snapshot of a room setting that can change at any time,
// and the failure it guards against is disclosure.
func Publish(ctx context.Context, cli *mautrix.Client, roomID id.RoomID, blob []byte) (id.EventID, error) {
	content, err := Encode(blob)
	if err != nil {
		return "", err
	}
	if err := checkPlaintextAllowed(ctx, cli, roomID); err != nil {
		return "", err
	}
	resp, err := cli.SendMessageEvent(ctx, roomID, LoroUpdate, content)
	if err != nil {
		return "", fmt.Errorf("loromatrix: send: %w", err)
	}
	return resp.EventID, nil
}

// checkPlaintextAllowed reports whether an unencrypted event may be sent to the
// room. It fails closed: if the room's encryption state cannot be read at all,
// publishing is refused rather than risked.
func checkPlaintextAllowed(ctx context.Context, cli *mautrix.Client, roomID id.RoomID) error {
	if cli.Crypto != nil {
		return nil // mautrix encrypts outgoing events for this client
	}
	var enc event.EncryptionEventContent
	err := cli.StateEvent(ctx, roomID, event.StateEncryption, "", &enc)
	if err == nil {
		if enc.Algorithm != "" {
			return fmt.Errorf("%w (%s)", ErrEncryptedRoom, enc.Algorithm)
		}
		return nil
	}
	// An unencrypted room simply has no such state event.
	var httpErr mautrix.HTTPError
	if errors.As(err, &httpErr) && httpErr.RespError != nil && httpErr.RespError.ErrCode == "M_NOT_FOUND" {
		return nil
	}
	return fmt.Errorf("loromatrix: cannot determine whether room is encrypted, refusing to publish: %w", err)
}

// Collect merges a batch of timeline events into a single *loro.Updates.
//
// Events that are not Loro updates are skipped silently, since a real room
// carries membership, messages and everything else. Events that claim to be
// Loro updates but do not decode are reported: a peer publishing corrupt blobs
// is a real fault worth surfacing, not noise to swallow. The returned Updates
// holds every change that did decode, so one bad event does not lose the rest.
func Collect(events []*event.Event) (*loro.Updates, []error) {
	merged := &loro.Updates{}
	var errs []error
	for _, evt := range events {
		if evt == nil || evt.Type.Type != EventType {
			continue
		}
		changes, err := decodeContent(evt.ID, evt.Content.VeryRaw)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		merged.Changes = append(merged.Changes, changes...)
	}
	return merged, errs
}

// CollectRaw is Collect for events still in their wire form, one JSON value
// each, as a /sync timeline or a /messages chunk holds them. Each event is
// decoded on its own, so an event that is not an object, or whose content is
// not one, is reported and the rest still merge. That is the difference from
// handing the whole response to a typed parse, where one such event fails the
// response and every update on it.
func CollectRaw(events []json.RawMessage) (*loro.Updates, []error) {
	merged := &loro.Updates{}
	errs := collectRaw(events, merged, nil)
	return merged, errs
}

// rawEvent is the part of a timeline event this package reads.
type rawEvent struct {
	ID      id.EventID      `json:"event_id"`
	Type    string          `json:"type"`
	Content json.RawMessage `json:"content"`
}

// collectRaw decodes each event, appends the changes of the Loro updates among
// them to merged, and returns what failed. seen, when not nil, skips an event
// id already handled, since a sync window and the first backfill page can
// overlap.
func collectRaw(events []json.RawMessage, merged *loro.Updates, seen map[id.EventID]bool) []error {
	var errs []error
	for _, raw := range events {
		var evt rawEvent
		if err := json.Unmarshal(raw, &evt); err != nil {
			errs = append(errs, fmt.Errorf("loromatrix: unreadable event: %w", err))
			continue
		}
		if seen != nil && evt.ID != "" {
			if seen[evt.ID] {
				continue
			}
			seen[evt.ID] = true
		}
		if evt.Type != EventType {
			continue
		}
		changes, err := decodeContent(evt.ID, evt.Content)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		merged.Changes = append(merged.Changes, changes...)
	}
	return errs
}

// decodeContent turns the content of one dev.loro.update event into changes.
// An event in a format this package does not know yields none and no error.
func decodeContent(evtID id.EventID, raw json.RawMessage) ([]loro.Change, error) {
	var content UpdateContent
	if err := json.Unmarshal(raw, &content); err != nil {
		return nil, fmt.Errorf("event %s: %w", evtID, err)
	}
	blob, err := Decode(&content)
	if err != nil {
		if errors.Is(err, ErrForeignFormat) {
			return nil, nil
		}
		return nil, fmt.Errorf("event %s: %w", evtID, err)
	}
	u, err := loro.DecodeUpdates(blob)
	if err != nil {
		return nil, fmt.Errorf("event %s: %w", evtID, err)
	}
	return u.Changes, nil
}

// Replay reads the whole room and merges every Loro update in it. This is what
// a peer does when it comes back online: it has no idea what it missed, so it
// reads the log and lets the CRDT sort out the result.
//
// It anchors on an initial /sync, which yields the newest slice of the timeline
// plus a prev_batch token, then pages backwards from there until the room is
// exhausted. Paging backwards is not a detail that leaks into the result: the
// merge is order-independent, which is the property that makes a CRDT the right
// payload for a transport that promises no ordering.
//
// An earlier version paged FORWARD with no token at all, which a real
// homeserver rejects. Measured against Dendrite: a backward page needs no
// token, because the server anchors it at the live end of the timeline, and
// dir=b with the token absent or empty both return the newest page. A forward
// page has no such default, and dir=f with the token absent or empty both
// answer 400 M_INVALID_PARAM "Invalid from parameter: malformed sync token".
// So the fault was the direction, not the emptiness of the token; anchoring on
// /sync and walking backwards sidesteps it and makes the walk deterministic.
//
// Pagination ends when the server stops handing back an "end" token, not when a
// page comes back empty. A spec-compliant server may return an empty chunk with
// a live continuation token, for instance when every event on that page was
// removed by per-user history visibility, and stopping there would silently
// truncate the document.
//
// pageSize bounds one homeserver request; Replay pages until the room ends.
func Replay(ctx context.Context, cli *mautrix.Client, roomID id.RoomID, pageSize int) (*loro.Updates, []error, error) {
	if pageSize <= 0 {
		pageSize = 100
	}
	merged := &loro.Updates{}
	var errs []error

	// The sync window and the first backfill page overlap on some servers, so
	// the same event can be handed out twice. The merge tolerates a duplicate
	// op, but there is no reason to decode and carry one, and counting events
	// is how a caller can tell what it actually read.
	seen := map[id.EventID]bool{}

	// Both responses are read only as far as this package needs and the
	// events are kept raw, to be decoded one at a time by collectRaw. A typed
	// parse of the whole response fails on a single event whose content is not
	// an object, which a hostile or non-conforming server can serve, and takes
	// every legitimate update on that page with it.
	var sync struct {
		Rooms struct {
			Join map[id.RoomID]json.RawMessage `json:"join"`
		} `json:"rooms"`
	}
	syncURL := cli.BuildURLWithQuery(mautrix.ClientURLPath{"v3", "sync"}, map[string]string{
		"timeout":      "0",
		"set_presence": string(event.PresenceOnline),
	})
	if _, err := cli.MakeRequest(ctx, http.MethodGet, syncURL, nil, &sync); err != nil {
		return merged, errs, fmt.Errorf("loromatrix: replay: initial sync: %w", err)
	}
	from := ""
	if raw, ok := sync.Rooms.Join[roomID]; ok {
		var joined struct {
			Timeline struct {
				Events    []json.RawMessage `json:"events"`
				PrevBatch string            `json:"prev_batch"`
			} `json:"timeline"`
		}
		if err := json.Unmarshal(raw, &joined); err != nil {
			return merged, errs, fmt.Errorf("loromatrix: replay: initial sync: room %s: %w", roomID, err)
		}
		errs = append(errs, collectRaw(joined.Timeline.Events, merged, seen)...)
		from = joined.Timeline.PrevBatch
	}
	if from == "" {
		// Either the room is not in the sync response or the server gave no
		// pagination anchor. Whatever the sync produced is all there is.
		return merged, errs, nil
	}

	tokens := map[string]bool{}
	for {
		var page struct {
			Chunk []json.RawMessage `json:"chunk"`
			End   string            `json:"end"`
		}
		pageURL := cli.BuildURLWithQuery(mautrix.ClientURLPath{"v3", "rooms", roomID, "messages"}, map[string]string{
			"dir":   string(mautrix.DirectionBackward),
			"from":  from,
			"limit": strconv.Itoa(pageSize),
		})
		if _, err := cli.MakeRequest(ctx, http.MethodGet, pageURL, nil, &page); err != nil {
			return merged, errs, fmt.Errorf("loromatrix: replay: %w", err)
		}
		errs = append(errs, collectRaw(page.Chunk, merged, seen)...)
		// A server that keeps handing back the same token would otherwise spin
		// here forever.
		if page.End == "" || page.End == from || tokens[page.End] {
			return merged, errs, nil
		}
		tokens[from] = true
		from = page.End
	}
}

// State merges the collected updates into document state, the same map
// loro-crdt's toJSON() produces.
func State(u *loro.Updates) (map[string]any, error) {
	return loro.MergeState(u)
}
