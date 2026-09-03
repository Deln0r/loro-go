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

// Decode unwraps event content back into a FastUpdates blob. Content that
// arrived from a remote peer is untrusted: the payload is checked for a valid
// header and checksum before it reaches the merge.
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

// Publish exports the document's current updates and sends them to the room as
// one event, returning the event id the homeserver assigned.
func Publish(ctx context.Context, cli *mautrix.Client, roomID id.RoomID, blob []byte) (id.EventID, error) {
	content, err := Encode(blob)
	if err != nil {
		return "", err
	}
	resp, err := cli.SendMessageEvent(ctx, roomID, LoroUpdate, content)
	if err != nil {
		return "", fmt.Errorf("loromatrix: send: %w", err)
	}
	return resp.EventID, nil
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
		var content UpdateContent
		if err := json.Unmarshal(evt.Content.VeryRaw, &content); err != nil {
			errs = append(errs, fmt.Errorf("event %s: %w", evt.ID, err))
			continue
		}
		blob, err := Decode(&content)
		if err != nil {
			if !errors.Is(err, ErrForeignFormat) {
				errs = append(errs, fmt.Errorf("event %s: %w", evt.ID, err))
			}
			continue
		}
		u, err := loro.DecodeUpdates(blob)
		if err != nil {
			errs = append(errs, fmt.Errorf("event %s: %w", evt.ID, err))
			continue
		}
		merged.Changes = append(merged.Changes, u.Changes...)
	}
	return merged, errs
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
// An earlier version started from an empty /messages token instead. That works
// against a permissive test double and is rejected by a real homeserver
// (Dendrite answers M_INVALID_PARAM, "malformed sync token"), which is why the
// anchor comes from /sync.
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
	seenEvents := map[id.EventID]bool{}
	fresh := func(events []*event.Event) []*event.Event {
		out := events[:0:0]
		for _, evt := range events {
			if evt == nil || seenEvents[evt.ID] {
				continue
			}
			seenEvents[evt.ID] = true
			out = append(out, evt)
		}
		return out
	}

	sync, err := cli.SyncRequest(ctx, 0, "", "", false, event.PresenceOnline)
	if err != nil {
		return merged, errs, fmt.Errorf("loromatrix: replay: initial sync: %w", err)
	}
	from := ""
	if joined := sync.Rooms.Join[roomID]; joined != nil {
		u, syncErrs := Collect(fresh(joined.Timeline.Events))
		merged.Changes = append(merged.Changes, u.Changes...)
		errs = append(errs, syncErrs...)
		from = joined.Timeline.PrevBatch
	}
	if from == "" {
		// Either the room is not in the sync response or the server gave no
		// pagination anchor. Whatever the sync produced is all there is.
		return merged, errs, nil
	}

	seen := map[string]bool{}
	for {
		resp, err := cli.Messages(ctx, roomID, from, "", mautrix.DirectionBackward, nil, pageSize)
		if err != nil {
			return merged, errs, fmt.Errorf("loromatrix: replay: %w", err)
		}
		if len(resp.Chunk) == 0 {
			return merged, errs, nil
		}
		u, pageErrs := Collect(fresh(resp.Chunk))
		merged.Changes = append(merged.Changes, u.Changes...)
		errs = append(errs, pageErrs...)
		// A server that keeps handing back the same token would otherwise spin
		// here forever.
		if resp.End == "" || resp.End == from || seen[resp.End] {
			return merged, errs, nil
		}
		seen[from] = true
		from = resp.End
	}
}

// State merges the collected updates into document state, the same map
// loro-crdt's toJSON() produces.
func State(u *loro.Updates) (map[string]any, error) {
	return loro.MergeState(u)
}
