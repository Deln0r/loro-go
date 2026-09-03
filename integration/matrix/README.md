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
CI without a homeserver. The double refuses a `/messages` request with an empty
`from` token, exactly as Dendrite does. That refusal is deliberate and is worth
explaining, because it is the reason this package has the shape it does:

The first version of `Replay` paged forward from an empty token. The test double
accepted it, every unit test passed, and the code was wrong: a real homeserver
answers `M_INVALID_PARAM, "malformed sync token"`. Only running against Dendrite
found it. A double more permissive than the server it stands in for cannot catch
that class of bug, so this one no longer is.

Running against a real server also turned up a genuine bug in the library
underneath. Dendrite's sync window and its first backfill page overlap, so a
peer received one update twice, and merging a duplicate op corrupted the
document instead of being absorbed (`"hello"` came back as
`"hellooloolloolooellooloollooloo..."`). `MergeState` now identifies an op by
the peer and counter it was issued with and ignores one it has already applied;
`loro/idempotence_test.go` pins it. No fixture had covered this, because each
fixture merges a fixed set of distinct changes exactly once. It took a real
transport to produce a duplicate.

## What this does not do

- No sync protocol: no version-vector diff, no partial-history request, no
  awareness or presence channel.
- No end-to-end encryption. Updates are published as plaintext event content;
  a room with E2EE enabled would need the client to encrypt them like any other
  event.
- No compaction. Every update stays in the room, so replay cost grows with the
  room's history.

Each of those is a deliberate boundary, not an oversight.
