# loro-go over Matrix

Carries Loro CRDT updates as Matrix events, so Go services that already speak
Matrix can hold a shared local-first document without adding a second network
stack.

A room is the log. Each local edit is exported as a `FastUpdates` blob and
published as one `dev.loro.update` event; a peer coming back online replays the
room and merges whatever it finds. Nothing here depends on the events arriving
in order, exactly once, or before the merge runs, which is precisely why a CRDT
is the right payload for a federated transport that guarantees none of those.

This is a **transport**, not a sync protocol. There is no version-vector diff,
no server, and no session negotiation: peers exchange whole update blobs and let
the CRDT converge. That keeps the surface small enough to audit in one sitting.

## Separate module, on purpose

`github.com/Deln0r/loro-go` has an empty `require` block and stays that way.
Pulling a Matrix client into the library would put that dependency in front of
every consumer, so the integration is its own module and you opt into it:

```
go get github.com/Deln0r/loro-go/integration/matrix
```

## Run it against a real homeserver

The demo is a check, not a printout: it exits non-zero if the peers fail to
converge, or if they converge on a document that lost an edit.

```
cd integration/matrix
docker compose up -d          # Dendrite on :8008, with alice and bob created
go run ./cmd/loro-matrix-demo
docker compose down -v        # when finished
```

Dendrite is the Go homeserver, which is the honest thing to test a Go CRDT
against: with loro-go this half of the stack is Go end to end, no cgo and no
foreign toolchain anywhere in the build.

Expected output:

```
-- offline edits (peers cannot see each other) --
  alice: notes += "alice wrote this. ", meta.author = alice
  bob:   notes += "bob wrote this. ",   meta.reviewed = 1

-- reconnect and publish --
  bob   -> $Ax4hsZ... (dev.loro.update)
  alice -> $WP8DwT... (dev.loro.update)

-- replay the room and merge --
  alice replayed 2 change(s) from the room
  bob replayed 2 change(s) from the room

  alice sees: {"meta":{"author":"alice","reviewed":1},"notes":"alice wrote this. bob wrote this. "}
  bob sees:   {"meta":{"author":"alice","reviewed":1},"notes":"alice wrote this. bob wrote this. "}

OK: both peers converged, and both edits survived the merge.
```

Note the order: bob publishes first and alice second, but alice's text lands
first in the merged document. Order of arrival does not decide the result.

## Unit tests

```
go test ./...
```

These run against an in-process test double rather than Docker, so they work in
CI without a homeserver. The double is written to match what Dendrite was
measured to do, in both directions: a double more permissive than the real
server hides bugs, and a double stricter than it fails code the server would
have served. Both mistakes were made here before the current shape settled.

**The pagination token.** The first version of `Replay` paged forward with no
token, every unit test passed, and a real homeserver rejected it with
`M_INVALID_PARAM, "malformed sync token"`. The first fix blamed the emptiness of
the token, and that was wrong: measured against Dendrite, `dir=b` with the token
absent or empty is served normally, because a backward page is anchored at the
live end of the timeline, while `dir=f` with the token absent or empty is
refused, because a forward page has no such default. The fault was the
direction. The double now enforces exactly that rule rather than a stricter one
it invented.

**Where pagination ends.** A walk ends when the server stops returning an `end`
token, not when a page comes back empty. A server may hand back an empty chunk
with a live continuation token, for instance when per-user history visibility
removed every event on that page, and stopping there silently truncates the
document while reporting success.

**A duplicate-op bug in the library underneath, in two rounds.** Dendrite's sync
window and its first backfill page overlap, so a peer received one update twice
and the merge corrupted the document instead of absorbing it (`"hello"` came
back as `"hellooloolloolooellooloollooloo..."`). The first fix identified an op
by its peer and its first counter, which was not enough. loro coalesces adjacent
atoms into one run, so two exports of the same document taken at different
moments share a first counter while covering different id spans: `"ab"` is
counter 0 length 2 and `"abcd"` is counter 0 length 4. Merging `"abcd"` after
`"ab"` therefore dropped `"cd"` outright, and reading a room forwards produced a
different document from reading it backwards. The complement was as bad: a
from-version delta re-sends a tail at a later start counter, misses a
first-counter key entirely, and reproduces the original corruption.

`MergeState` now treats an op as the id **range** it occupies and clips an
incoming op to the sub-ranges it has not already consumed. `loro/idempotence_test.go`
pins both halves against fixtures generated by loro-crdt itself. No fixture had
covered any of this, because each merges a fixed set of distinct changes exactly
once. It took a real transport to produce the overlaps.

## What this does not do

- No sync protocol: no version-vector diff, no partial-history request, no
  awareness or presence channel.
- No end-to-end encryption. Updates are published as plaintext event content.
  `Publish` therefore refuses an encrypted room outright when the client has no
  crypto helper, and fails closed if it cannot tell: a homeserver will accept a
  plaintext event into an encrypted room and return a normal event id, so
  without that check the document would land readable in a room whose members
  were promised otherwise. Installing a mautrix crypto helper lifts the refusal,
  since mautrix then encrypts outgoing events itself.
- **Not hardened against a hostile homeserver.** mautrix parses a `/sync` or
  `/messages` response into one typed tree, so a single event whose `content` is
  a JSON string rather than an object fails the whole response, and with it
  every legitimate update on that page. Because the event stays in the room, the
  failure is permanent for that peer. Dendrite rejects such an event on `/send`
  (`M_BAD_JSON`), so this needs a hostile or non-conforming server in a
  federated room; the caller gets a loud error rather than a wrong document, so
  this is availability, not data loss. Fixing it means decoding each event from
  raw JSON instead of relying on the client library's typed parse.
- No compaction. Every update stays in the room, so replay cost grows with the
  room's history.

Each of those is a deliberate boundary, not an oversight.
