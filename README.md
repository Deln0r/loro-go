# loro-go

A pure-Go library for the [Loro](https://github.com/loro-dev/loro) CRDT wire format.

[![CI](https://github.com/Deln0r/loro-go/actions/workflows/ci.yml/badge.svg)](https://github.com/Deln0r/loro-go/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/Deln0r/loro-go.svg)](https://pkg.go.dev/github.com/Deln0r/loro-go)
[![Listed on loro.dev](https://img.shields.io/badge/loro.dev%20docs-listed-7c3aed.svg)](https://loro.dev/docs/tutorial/get_started)
[![Go](https://img.shields.io/badge/go-1.26+-00ADD8.svg)](go.mod)
[![License: MIT](https://img.shields.io/badge/license-MIT-green.svg)](LICENSE)
[![Status](https://img.shields.io/badge/status-alpha-orange)]()
[![Byte-compat vs loro-crdt](https://img.shields.io/badge/byte--compat-loro--crdt%201.17.1-success)]()
[![Codeberg mirror](https://img.shields.io/badge/mirror-codeberg.org-2185d0)](https://codeberg.org/Deln0r/loro-go)

loro-go reads and writes the Loro **Fast** wire format (`FastUpdates` and `FastSnapshot`) byte-for-byte, and reconstructs document state for Map, List, Text, MovableList, Tree and Counter containers. No cgo, single Go toolchain build.

Bytes are verified against two independent ground truths: real `loro-crdt@1.17.1` exports, and the `serde_columnar@0.3.14` crate (golden column vectors emitted by a small Rust harness). A blob produced by loro-go imports cleanly into the canonical `loro-crdt` JavaScript package with matching `toJSON()`, checked on every CI run. Upstream minors are re-checked against the committed fixtures; see [COMPAT.md](COMPAT.md) (1.17.1: same format).

loro-go is listed in the official [Loro documentation](https://loro.dev/docs/tutorial/get_started) as the pure-Go community implementation.

> Not affiliated with loro-dev. Loro is a separate project; this is an independent Go reading of its wire format.

> **Mirror.** Primary repository is [github.com/Deln0r/loro-go](https://github.com/Deln0r/loro-go); an EU-hosted mirror auto-synced on every push lives at [codeberg.org/Deln0r/loro-go](https://codeberg.org/Deln0r/loro-go).

## Install

```
go get github.com/Deln0r/loro-go
```

## Quick start

```go
package main

import (
	"fmt"

	"github.com/Deln0r/loro-go/loro"
)

func main() {
	// Decode a FastUpdates blob exported by loro-crdt and reconstruct state.
	u, err := loro.DecodeUpdates(blob) // blob = doc.export({mode:"update"})
	if err != nil {
		panic(err)
	}
	state, _ := loro.MergeState(u)
	fmt.Println(state) // map[string]any matching doc.toJSON()

	// Or build a document in Go and export bytes loro-crdt can import.
	d := loro.NewDoc(1)
	d.TextInsert("title", 0, "hello")
	d.MapSet("meta", "n", int64(7))
	out := d.ExportUpdates()
	_ = out
}
```

`loro.DecodeSnapshot` reads the `FastSnapshot` format (oplog SSTable) the same way.

## What works

- Header + checksum (xxh32), `FastUpdates` and `FastSnapshot` framing
- `serde_columnar` strategies: Rle, BoolRle, DeltaRle, DeltaOfDelta (decode and encode, byte-verified)
- Change blocks: all eight blobs decode and re-encode byte-identically. The one exception is a map of two or more keys nested in a value: loro writes its keys in the order of its hash map and loro-go in sorted order, so those bytes can differ while they decode to the same value
- Containers: Map (LWW), List and Text (each insert's position is resolved against what its author had seen, its causal past from the change deps minus the deletes in that past, with rich-text mark anchors counted, so edits made after a sync, a delete or a mark land where loro puts them; inserts made concurrently at the same place are ordered by Fugue from their left and right origins, as loro orders them), MovableList (positions and elements kept apart as loro keeps them: a move adds a position for its element and deletes the old one, the element sits at its position with the latest writer and shows the value of its latest `set`), Tree (moves and deletes applied in (lamport, peer) order with cycle-closing moves skipped as loro does, siblings ordered by fractional index then (lamport, peer), per-node `meta` maps), Counter (summed increments)
- Deletes: a text or list delete removes the elements at its position in its author's view, as loro's import does, so a delete that loro-crdt's WASM build up to 1.16.3 recorded with wrong ids (loro-dev/loro#1149) still removes the right characters; when part of the history is missing it falls back to the ids it recorded. Map key deletion (DeleteOnce), and deletes of other peers' elements
- Blocks carrying multiple changes (per-change ids, lamports, timestamps recovered; every decoded change lamport is checked against the lamport loro-crdt itself recorded)
- LZ4-compressed SSTable blocks (decompression, checksums verified)
- Rich-text `toDelta` (`loro.TextDelta`): the merged text split into styled runs, with overlapping marks of one key decided by (lamport, peer), unmarks, marks whose two anchors arrive in different exports, and style values of any shape. It matches loro-crdt's `toDelta` on 300 of 300 one-author histories, 300 of 300 concurrent ones, and 300 of 300 rich-text histories by one to three peers with marks of all four expand rules, inserts at the edges of styled runs and deletes of the whole text
- Values of any shape: objects and arrays nested in list items, map values and style values, with a nested map's keys read and written as indexes into the block's key arena, as loro does
- Encode from scratch: build a document and export `FastUpdates` byte-identical to loro-crdt, except for the key order of a map of two or more keys nested in a value (see Change blocks); loro-crdt imports it to the same document
- KV/SSTable reader for the snapshot oplog section, with block and meta checksum verification
- Malformed-input hardening: the decoders are fuzzed and bound every attacker-controlled length and RLE run count, and the key bytes that ops and nested maps reference by index, so a hostile blob errors out instead of panicking, hanging, or allocating without bound
- Merge is order-independent and idempotent: the same changes in any order give the same state, and a duplicate or partially overlapping op is absorbed rather than applied twice, so a transport that retries or replays does not corrupt the document
- Sequence merge checked against loro-crdt on random histories: 300 of 300 one-author histories with inserts anywhere (astral characters included), deletes, backspace runs, marks and list edits match, as do 300 of 300 histories in which two or three peers edit concurrently and sync, 300 of 300 insert-anywhere histories, and named histories with syncs, deletes, backwards deletes and marks. MovableList (inserts, deletes, moves, sets; one author and several), Tree (creates, moves, deletes, meta writes) and rich text (styled by one to three peers) each match on 300 of 300 random histories; every block of every corpus history re-encodes byte-identically. The merge also matches a brute-force reference, itself held to loro-crdt, on 300 random multi-peer histories with real deps

## Matrix transport

[`integration/matrix`](integration/matrix/) carries Loro updates as Matrix
events, so a Go service that already speaks Matrix can hold a shared local-first
document without a second network stack. Two peers edit while disconnected,
publish into a room, replay it, and converge; the demo exits non-zero if they do
not, and CI runs it against a real [Dendrite](https://github.com/element-hq/dendrite)
homeserver on every push. It is a separate Go module, so this library's own
`go.mod` stays dependency-free.

## Not yet

- Fast merging of long concurrent branches. An edit made with everything already merged in view (typing, or editing after a sync) resolves at once; one made concurrently with edits already merged is resolved by scanning the merged sequence. Two peers that each made thousands of changes offline therefore cost time proportional to the product of the two, and past 2^28 steps `MergeState` returns an error rather than run on.
- Nested containers other than tree-node meta maps (a container stored as a map or list value)
- Two root containers of different kinds under one name, such as a Text and a List both called `t`. `TextDelta` keeps them apart, but `MergeState` returns one value per name and can show the other one than loro's `toJSON` does: in the one case checked, loro shows the List and `MergeState` the Text

## Cross-language fixtures

`testdata/gen` drives `loro-crdt` (npm) to emit binary + JSON fixtures, `testdata/rustgen` emits `serde_columnar` golden column vectors, and `testdata/gen/validate_go.mjs` imports a loro-go-produced blob back into `loro-crdt`. The Go tests assert byte-identity and state equality against these. Regenerate with:

```
cd testdata/gen && npm install && node gen.mjs
cd ../rustgen && cargo run > ../columnar_golden.txt
```

`testdata/gen/lorojs_check.mjs` runs the same fixtures and corpora through loro.js, the TypeScript implementation of Loro, as a second oracle, comparing each document's `toJSON` and, where one was recorded, its `toDelta`. loro.js 0.3.0 agrees with loro-crdt on all of them except in three ways. Two tree fixtures with crossing concurrent moves came out as an empty tree; that was reported as [loro-dev/loro#1182](https://github.com/loro-dev/loro/issues/1182) and fixed upstream in [loro-dev/loro#1205](https://github.com/loro-dev/loro/pull/1205), after 0.3.0. In 20 of the 300 rich-text histories and two fixtures, loro.js gives the same text and styles but breaks the runs at different places: it joins text under an unmark with unstyled text beside it, and keeps apart runs whose style values are equal objects. And a mark whose end anchor was deleted, which only a hand-written history does, still styles its text in loro.js.

## Contributing

Issues and pull requests are welcome. See [CONTRIBUTING.md](CONTRIBUTING.md) for scope, the byte-compatibility rule for codec changes, fixture regeneration, and fuzzing.

## License

MIT. See [LICENSE](LICENSE).
