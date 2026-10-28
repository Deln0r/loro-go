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

	// Write the decoded changes back: the same bytes loro-crdt exported.
	again, _ := loro.EncodeUpdates(u)
	_ = again

	// Or keep a live document: import, edit, commit, export.
	d := loro.NewDocument(1)
	_ = d.Import(blob)
	_ = d.Text("title").Insert(0, "hello ") // positions in Unicode code points
	_ = d.Map("meta").Set("n", int64(7))
	d.CommitWith(loro.CommitOptions{Message: "edit from Go"})
	out, _ := d.ExportUpdates() // loro-crdt imports it
	_ = out

	// Sync: send your version, get back only what you lack.
	mine := d.Version().Encode() // the bytes of doc.oplogVersion().encode()
	theirs, _ := loro.DecodeVersionVector(mine)
	missing, _ := d.ExportUpdatesFrom(theirs) // export({mode:"update", from})
	_ = missing
}
```

`loro.DecodeSnapshot` reads the `FastSnapshot` format (oplog SSTable) the same way.

## What works

- Header + checksum (xxh32), `FastUpdates` and `FastSnapshot` framing
- `serde_columnar` strategies: Rle, BoolRle, DeltaRle, DeltaOfDelta (decode and encode, byte-verified)
- Change blocks: all eight blobs decode and re-encode byte-identically, blob by blob, for 28 named fixtures and every history of the nine corpora. A map of two or more keys nested in a value lists its keys in the order of loro's hash map, which a decoded Go map does not keep: the value reader can record that order and the writer replay it, and without it the writer sorts the keys, which decodes to the same value
- Encode decoded changes: `loro.EncodeUpdates` writes changes back as `FastUpdates`, following loro's block encoder register by register (peers, containers, keys, tree positions, dependencies), with commit messages, timestamps and the block grouping the decoder recorded. Of 2781 loro-crdt exports, every history of the nine corpora and the 81 `FastUpdates` fixture files, nested and mergeable containers included, `DecodeUpdates` then `EncodeUpdates` gives back 2764 byte for byte. The other 17 hold a map of two or more keys nested in a value: they decode to the same changes, come back byte for byte once loro's key order is supplied, and loro-crdt imports them to the same state, version and history, checked on every CI run. Changes without a recorded grouping, such as ones collected from several exports, are grouped into blocks as loro's export groups them, which reproduces 2780 of the 2781; the other is an export of id ranges, whose blocks loro writes in layers. It refuses, with an error, changes loro would read back otherwise than written or refuse to import: ops out of sequence, one-atom ops of another length, value kinds a container does not take, ids, counters and lamports past loro's 32-bit fields, positions loro's decoder rejects, strings that are not UTF-8, and a peer's changes overlapping or out of order
- Containers: Map (LWW), List and Text (each insert's position is resolved against what its author had seen, its causal past from the change deps minus the deletes in that past, with rich-text mark anchors counted, so edits made after a sync, a delete or a mark land where loro puts them; inserts made concurrently at the same place are ordered by Fugue from their left and right origins, as loro orders them), MovableList (positions and elements kept apart as loro keeps them: a move adds a position for its element and deletes the old one, the element sits at its position with the latest writer and shows the value of its latest `set`), Tree (moves and deletes applied in (lamport, peer) order with cycle-closing moves skipped as loro does, siblings ordered by fractional index then (lamport, peer), per-node `meta` maps), Counter (summed increments)
- Deletes: a text or list delete removes the elements at its position in its author's view, as loro's import does, so a delete that loro-crdt's WASM build up to 1.16.3 recorded with wrong ids (loro-dev/loro#1149) still removes the right characters; when part of the history is missing it falls back to the ids it recorded. Map key deletion (DeleteOnce), and deletes of other peers' elements
- Blocks carrying multiple changes (per-change ids, lamports, timestamps recovered; every decoded change lamport is checked against the lamport loro-crdt itself recorded)
- LZ4-compressed SSTable blocks (decompression, checksums verified)
- Rich-text `toDelta` (`loro.TextDelta`): the merged text split into styled runs, with overlapping marks of one key decided by (lamport, peer), unmarks, marks whose two anchors arrive in different exports, and style values of any shape. It matches loro-crdt's `toDelta` on 300 of 300 one-author histories, 300 of 300 concurrent ones, and 300 of 300 rich-text histories by one to three peers with marks of all four expand rules, inserts at the edges of styled runs and deletes of the whole text. A Text stored in another container is read by its id, `counter@peer`, and a mergeable one by its root name
- Values of any shape: objects and arrays nested in list items, map values and style values, with a nested map's keys read and written as indexes into the block's key arena, as loro does
- Nested containers: a container stored as a map value, a List or MovableList item, or a value in a tree node's meta map shows where its parent holds it, at any depth, as loro's `toJSON` shows it. A child is named by the id of the atom that placed it (the map write, the k-th item of a list insert, the MovableList `set`), so one under a key written over, in a deleted item or on the losing side of two concurrent writes to one key shows nowhere, no nested container shows at the top level, and one never written to shows its kind's empty value. Mergeable children (`ensureMergeableText` and its siblings, loro 1.13 and later) show under their parent's key while its marker is there, never at the top level, and two peers' ensures under one key write into one container. Checked against loro-crdt on 300 of 300 random histories by one to three peers with all six kinds nested up to three levels below a root, and on 14 named fixtures. A crafted container that is its own ancestor is an error when a root reaches it; loro-crdt refuses to import such a document at all, and loro-go shows the rest of a document whose cycle no root reaches
- A live document, `loro.Document`: import `FastUpdates` and `FastSnapshot` blobs, edit root Text, Map, List and Counter containers through handles, style text with `Mark` and `Unmark`, commit with a message or a timestamp, export the whole history. It records what loro-crdt 1.17.1 records for the same edits: ids, lamports, dependencies, timestamps (never below those of the changes depended on), text deletes split where loro splits them, the map writes loro skips and the deletes it keeps, counter steps as I64 or F64 by loro's rule, changes waiting for missing dependencies kept out of the version, the state and the export until those arrive, changes merged and ops folded as loro's change store merges and folds them, and dependencies listed in the order of loro's hash map. Held to loro-crdt on 300 random edit scripts by one to three peers and 60 by four to twelve, with commits, messages, timestamps and syncs that deliver whole histories, tails from a version, tails before their prefix, duplicates and snapshots: at all 2554 and 1777 checks of a peer, its JSON equals loro's `toJSON`, its history holds the same atoms as loro's export of that peer (ids, lamports, containers, positions, values, delete targets, causal pasts), and its export is loro's export byte for byte. 18 named scripts pin loro's rules one by one, and each of 14 rules switched off makes its script fail. loro-crdt imports every final export to the Go peer's state, checked on every CI run. An import is all or nothing, and refuses what loro refuses: an op id reused for another op, a change that skips counters of its peer, an op whose position lies past what its author saw. Rich text follows loro's marks: the style keys `ConfigTextStyle` configures (bold, italic and underline expanding after, link, highlight, comment and code to neither side, until a configuration replaces them), a colon key such as `comment:1` taking its prefix's configuration, an unmark's expand rule reversed from its key's, a mark's ends placed among other marks' anchors as inserted text would be, and no op for a mark the range already has or an unmark of a key it lacks. Held to loro-crdt on 300 random rich-text scripts by one to three peers, with marks and unmarks over all four expand rules, keys configured per peer, style values of every shape, concurrent marks, inserts at style edges and deletes across anchors: at all 3114 checks, JSON, `toDelta`, atoms and bytes are loro's, and so are refusals of unconfigured keys; 9 named scripts pin the mark rules, and each of 7 switched off makes its script fail. One mutex guards each document, so it can be shared between goroutines. MovableList, Tree and containers stored in other containers are imported, shown and exported, but not yet edited
- Versions for sync: `Document.Version` and `Document.Frontiers` are loro-crdt's `oplogVersion()` and `oplogFrontiers()`, edits not yet committed included and changes waiting for a dependency left out, with their peers in the order of loro's hash maps, so `VersionVector.Encode` and `EncodeFrontiers` write loro's bytes, and `DecodeVersionVector` and `DecodeFrontiers` read them as loro does. `Document.ExportUpdatesFrom` is `export({mode: "update", from})`: what a version vector lacks, a change it holds in part cut where it ends (the part one lamport later for each op cut, and depending on the op before it), in blocks as loro's export regroups them; `Document.ExportUpdatesInRange` is `export({mode: "updates-in-range"})`. Held to loro-crdt on 400 random histories: 320 by one to twelve peers editing every kind of container, marks and nested containers included, and 80 by eight to twelve peers of more than 20 changes tied on lamport, which an import applies in the order Rust's unstable sort gives them. The version's bytes and the frontiers, order and bytes, are loro's in 400 of 400, and in 100 of 100 imported as snapshots; exports from 3600 versions (empty, prefixes of one or several peers that cut inside changes, causally closed ones, the full one, counters of 0 or below, peers never seen, counters past the end) are loro's bytes in 3600, 925 of them cutting a change, and exports of 800 sets of id ranges in 800. loro-crdt imports each export into a document at its version and reaches the whole history's version and state, 235 times releasing changes that waited, checked on every CI run. 9 named scripts hold the versions and exports of Documents that edit, sync and import snapshots to loro's bytes, and each of 12 rules switched off makes the corpus or its script fail. A counter in `from` so far below 0 that loro's 32-bit subtraction wraps makes loro-crdt panic; loro-go counts it as 0
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
- Writing nested containers: `Doc` builds root Text, Map and List containers only, and `Document` edits root Text, Map, List and Counter containers only; MovableList, Tree and child containers come next
- Shallow snapshots (`export({mode: "shallow-snapshot"})`): their history starts past the beginning, and `Document.Import` refuses them with an error
- Converting between a version vector and frontiers (`frontiersToVV`, `vvToFrontiers`): loro builds the result while it walks the history, and the order its hash map ends up in is not followed yet
- Two root containers of different kinds under one name, such as a Text and a List both called `t`. `TextDelta` keeps them apart, but `MergeState` returns one value per name and can show the other one than loro's `toJSON` does: in the one case checked, loro shows the List and `MergeState` the Text. `Document.JSON` shows the one loro shows: of the roots of that name in the order loro registered them, the last that is not empty

## Cross-language fixtures

`testdata/gen` drives `loro-crdt` (npm) to emit binary + JSON fixtures, `testdata/rustgen` emits `serde_columnar` golden column vectors, and `testdata/gen/validate_go.mjs` imports a loro-go-produced blob back into `loro-crdt`. `testdata/gen/gen_encode.mjs` writes the exports the encoder is held to, and `testdata/gen/validate_encode.mjs` imports every re-encoded blob that is not byte-identical next to loro's own. `testdata/gen/gen_scripts.mjs` writes the edit scripts `Document` is held to, with every blob and checkpoint loro-crdt produced running them, and `testdata/gen/validate_document.mjs` imports each Document's final export into loro-crdt. `testdata/gen/gen_versions.mjs` writes the histories and scripts versions and exports from them are held to, and `testdata/gen/validate_versions.mjs` imports each export from a version into a loro-crdt document at that version. The Go tests assert byte-identity and state equality against these. Regenerate with:

```
cd testdata/gen && npm install && node gen.mjs && node gen_encode.mjs && node gen_scripts.mjs && node gen_versions.mjs
cd ../rustgen && cargo run > ../columnar_golden.txt
```

`testdata/gen/lorojs_check.mjs` runs the same fixtures and corpora through loro.js, the TypeScript implementation of Loro, as a second oracle, comparing each document's `toJSON` and, where one was recorded, its `toDelta`. Two tree fixtures with crossing concurrent moves came out as an empty tree in loro.js 0.3.0; that was reported as [loro-dev/loro#1182](https://github.com/loro-dev/loro/issues/1182), fixed in [loro-dev/loro#1205](https://github.com/loro-dev/loro/pull/1205), and loro.js 0.4.0 agrees on them, as it does on every nested-container fixture and history, mergeable children included. It differs from loro-crdt in two ways that remain. In 20 of the 300 rich-text histories and two fixtures, loro.js gives the same text and styles but breaks the runs at different places: it joins text under an unmark with unstyled text beside it, and keeps apart runs whose style values are equal objects. And a mark whose end anchor was deleted, which only a hand-written history does, still styles its text in loro.js.

## Contributing

Issues and pull requests are welcome. See [CONTRIBUTING.md](CONTRIBUTING.md) for scope, the byte-compatibility rule for codec changes, fixture regeneration, and fuzzing.

## License

MIT. See [LICENSE](LICENSE).
