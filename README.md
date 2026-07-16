# loro-go

A pure-Go library for the [Loro](https://github.com/loro-dev/loro) CRDT wire format.

[![CI](https://github.com/Deln0r/loro-go/actions/workflows/ci.yml/badge.svg)](https://github.com/Deln0r/loro-go/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/Deln0r/loro-go.svg)](https://pkg.go.dev/github.com/Deln0r/loro-go)
[![Listed on loro.dev](https://img.shields.io/badge/loro.dev%20docs-listed-7c3aed.svg)](https://loro.dev/docs/tutorial/get_started)
[![Go](https://img.shields.io/badge/go-1.26+-00ADD8.svg)](go.mod)
[![License: MIT](https://img.shields.io/badge/license-MIT-green.svg)](LICENSE)
[![Status](https://img.shields.io/badge/status-alpha-orange)]()
[![Byte-compat vs loro-crdt](https://img.shields.io/badge/byte--compat-loro--crdt%201.13.6-success)]()
[![Codeberg mirror](https://img.shields.io/badge/mirror-codeberg.org-2185d0)](https://codeberg.org/Deln0r/loro-go)

loro-go reads and writes the Loro **Fast** wire format (`FastUpdates` and `FastSnapshot`) byte-for-byte, and reconstructs document state for Map, List, Text, MovableList, Tree and Counter containers. No cgo, single Go toolchain build.

Bytes are verified against two independent ground truths: real `loro-crdt@1.13.6` exports, and the `serde_columnar@0.3.14` crate (golden column vectors emitted by a small Rust harness). A blob produced by loro-go imports cleanly into the canonical `loro-crdt` JavaScript package with matching `toJSON()`. Upstream minors are re-checked against the committed fixtures; see [COMPAT.md](COMPAT.md) (1.13.6: byte-identical).

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
- Containers: Map (LWW), List and Text (Fugue ordering, concurrent + multi-peer merge), MovableList (moves), Tree (nested state with fractional index and per-node `meta` maps), Counter (summed increments)
- Deletes: text/list id-span tombstones (DeleteSeq), map key deletion (DeleteOnce), including deletes targeting another peer's elements
- Blocks carrying multiple changes (per-change ids, lamports, timestamps recovered)
- LZ4-compressed SSTable blocks (decompression, checksums verified)
- Rich-text `toDelta`: styled runs reconstructed via the mark anchor model (`loro.TextDelta`)
- Encode from scratch: build a document and export `FastUpdates` byte-identical to loro-crdt
- KV/SSTable reader for the snapshot oplog section, with block and meta checksum verification
- Malformed-input hardening: the decoders are fuzzed and bound every attacker-controlled length and RLE run count, so a hostile blob errors out instead of panicking, hanging, or allocating without bound

## Not yet

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
