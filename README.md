# loro-go

A pure-Go library for the [Loro](https://github.com/loro-dev/loro) CRDT wire format.

[![CI](https://github.com/Deln0r/loro-go/actions/workflows/ci.yml/badge.svg)](https://github.com/Deln0r/loro-go/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/Deln0r/loro-go.svg)](https://pkg.go.dev/github.com/Deln0r/loro-go)
[![Listed on loro.dev](https://img.shields.io/badge/loro.dev%20docs-listed-7c3aed.svg)](https://loro.dev/docs/tutorial/get_started)
[![Go](https://img.shields.io/badge/go-1.26+-00ADD8.svg)](go.mod)
[![License: MIT](https://img.shields.io/badge/license-MIT-green.svg)](LICENSE)
[![Status](https://img.shields.io/badge/status-alpha-orange)]()
[![Byte-compat vs loro-crdt](https://img.shields.io/badge/byte--compat-loro--crdt%201.16.4-success)]()
[![Codeberg mirror](https://img.shields.io/badge/mirror-codeberg.org-2185d0)](https://codeberg.org/Deln0r/loro-go)

loro-go reads and writes the Loro **Fast** wire format (`FastUpdates` and `FastSnapshot`) byte-for-byte, and reconstructs document state for Map, List, Text, MovableList, Tree and Counter containers. No cgo, single Go toolchain build.

Bytes are verified against two independent ground truths: real `loro-crdt@1.16.4` exports, and the `serde_columnar@0.3.14` crate (golden column vectors emitted by a small Rust harness). A blob produced by loro-go imports cleanly into the canonical `loro-crdt` JavaScript package with matching `toJSON()`, checked on every CI run. Upstream minors are re-checked against the committed fixtures; see [COMPAT.md](COMPAT.md) (1.16.4: byte-identical).

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
- Change blocks: all eight blobs decode and re-encode byte-identically
- Containers: Map (LWW), List and Text (each insert's position is resolved against what its author had seen, its causal past from the change deps minus the deletes in that past, with rich-text mark anchors counted, so edits made after a sync, a delete or a mark land where loro puts them; concurrent inserts at the same place are ordered differently, see "Not yet"), MovableList (moves; see "Not yet" for edits made after a move), Tree (moves and deletes applied in (lamport, peer) order with cycle-closing moves skipped as loro does, siblings ordered by fractional index then (lamport, peer), per-node `meta` maps), Counter (summed increments)
- Deletes: text/list id-span tombstones (DeleteSeq), map key deletion (DeleteOnce), including deletes targeting another peer's elements
- Blocks carrying multiple changes (per-change ids, lamports, timestamps recovered; every decoded change lamport is checked against the lamport loro-crdt itself recorded)
- LZ4-compressed SSTable blocks (decompression, checksums verified)
- Rich-text `toDelta`: styled runs reconstructed via the mark anchor model (`loro.TextDelta`)
- Encode from scratch: build a document and export `FastUpdates` byte-identical to loro-crdt
- KV/SSTable reader for the snapshot oplog section, with block and meta checksum verification
- Malformed-input hardening: the decoders are fuzzed and bound every attacker-controlled length and RLE run count, so a hostile blob errors out instead of panicking, hanging, or allocating without bound
- Merge is order-independent and idempotent: the same changes in any order give the same state, and a duplicate or partially overlapping op is absorbed rather than applied twice, so a transport that retries or replays does not corrupt the document
- Sequence merge checked against loro-crdt on random histories: 300 of 300 one-author histories with inserts anywhere (astral characters included), deletes, backspace runs, marks and list edits match; so do 300 of 300 insert-anywhere histories and named histories with syncs, deletes, backwards deletes and marks. The merge also matches a brute-force reference, itself held to loro-crdt, on 300 random multi-peer histories with real deps

## Matrix transport

[`integration/matrix`](integration/matrix/) carries Loro updates as Matrix
events, so a Go service that already speaks Matrix can hold a shared local-first
document without a second network stack. Two peers edit while disconnected,
publish into a room, replay it, and converge; the demo exits non-zero if they do
not, and CI runs it against a real [Dendrite](https://github.com/element-hq/dendrite)
homeserver on every push. It is a separate Go module, so this library's own
`go.mod` stays dependency-free.

## Not yet

- Concurrent inserts at the same place in the order loro gives them. Two inserts made at one spot without either author seeing the other come out newest first here; loro orders them by Fugue's rule, which looks at peer ids and at the element to the right of the insertion point. Three peers that each put one item into an empty list give `[56, 29, 83]` in loro and `[29, 56, 83]` here. Of 300 random histories in which two or three peers edit concurrently and sync (`testdata/fixtures/seq_concurrent_corpus.json`), 185 come out as loro has them.
- Text deletes written by loro-crdt's WASM build up to 1.16.3 with wrong ids (loro-dev/loro#1149). Such a delete names characters next to the ones deleted when an emoji or other astral character ends one of the inserts it spans. loro applies a delete by its position and is unaffected; this library applies it by its ids and removes the wrong characters. Reading by position needs the concurrent order above to match first.
- Fast merging of long concurrent branches. An edit made with everything already merged in view (typing, or editing after a sync) resolves at once; one made concurrently with edits already merged is resolved by scanning the merged sequence. Two peers that each made thousands of changes offline therefore cost time proportional to the product of the two, and past 2^28 steps `MergeState` returns an error rather than run on.
- MovableList edits made after a move. Positions are resolved without the moves, so from `[a,b,c]`, moving `c` to the front and then inserting `X` at 1 gives `[b,a,X,c]` where loro gives `[c,X,a,b]`, and moving `c` to the front and then deleting the first element removes nothing: `[c,a,b]` where loro gives `[a,b]`.
- Mark anchoring under concurrent edits (expand rules), and marks interacting with deletes in the same range
- Nested containers other than tree-node meta maps (a container stored as a map or list value)

## Cross-language fixtures

`testdata/gen` drives `loro-crdt` (npm) to emit binary + JSON fixtures, `testdata/rustgen` emits `serde_columnar` golden column vectors, and `testdata/gen/validate_go.mjs` imports a loro-go-produced blob back into `loro-crdt`. The Go tests assert byte-identity and state equality against these. Regenerate with:

```
cd testdata/gen && npm install && node gen.mjs
cd ../rustgen && cargo run > ../columnar_golden.txt
```

## Contributing

Issues and pull requests are welcome. See [CONTRIBUTING.md](CONTRIBUTING.md) for scope, the byte-compatibility rule for codec changes, fixture regeneration, and fuzzing.

## License

MIT. See [LICENSE](LICENSE).
