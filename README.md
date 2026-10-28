# loro-go

A pure-Go implementation of the [Loro](https://github.com/loro-dev/loro) CRDT: read, edit, write and sync Loro documents from Go, byte-compatible with `loro-crdt`.

[![CI](https://github.com/Deln0r/loro-go/actions/workflows/ci.yml/badge.svg)](https://github.com/Deln0r/loro-go/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/Deln0r/loro-go/loro.svg)](https://pkg.go.dev/github.com/Deln0r/loro-go/loro)
[![Release](https://img.shields.io/github/v/release/Deln0r/loro-go)](https://github.com/Deln0r/loro-go/releases)
[![Listed on loro.dev](https://img.shields.io/badge/loro.dev%20docs-listed-7c3aed.svg)](https://loro.dev/docs/tutorial/get_started)
[![Byte-compat vs loro-crdt](https://img.shields.io/badge/byte--compat-loro--crdt%201.17.1-success)](VERIFICATION.md)
[![License: MIT](https://img.shields.io/badge/license-MIT-green.svg)](LICENSE)

loro-go is a Loro peer written in Go. A Go service can import the updates and snapshots that a `loro-crdt` application exports, edit the document, and send back updates that `loro-crdt` imports and converges with. It speaks Loro's **Fast** wire format (`FastUpdates` and `FastSnapshot`) and writes the bytes `loro-crdt` 1.17.1 writes for the same edits. No cgo and no dependencies outside the standard library.

loro-go is listed in the [Loro documentation](https://loro.dev/docs/tutorial/get_started) as the pure-Go community implementation. It is an independent project, not affiliated with loro-dev. The primary repository is on GitHub, with a mirror at [codeberg.org/Deln0r/loro-go](https://codeberg.org/Deln0r/loro-go).

## Install

```
go get github.com/Deln0r/loro-go@latest
```

Requires Go 1.26 or later.

## Quick start

```go
package main

import (
	"fmt"
	"log"

	"github.com/Deln0r/loro-go/loro"
)

func main() {
	// A peer with its own peer id, as every loro replica has.
	doc := loro.NewDocument(1)

	// Edit. Each call returns an error instead of panicking.
	if err := doc.Text("title").Insert(0, "Shopping"); err != nil {
		log.Fatal(err)
	}
	items := doc.List("items")
	if err := items.Insert(0, "milk", "eggs"); err != nil {
		log.Fatal(err)
	}
	doc.CommitWith(loro.CommitOptions{Message: "first list"})

	// Export the history: loro-crdt's doc.import(blob) takes these bytes.
	blob, err := doc.ExportUpdates()
	if err != nil {
		log.Fatal(err)
	}

	// Another peer imports it, edits, and sends back only what is new.
	other := loro.NewDocument(2)
	if err := other.Import(blob); err != nil {
		log.Fatal(err)
	}
	if err := other.List("items").Insert(1, "bread"); err != nil {
		log.Fatal(err)
	}
	reply, err := other.ExportUpdatesFrom(doc.Version())
	if err != nil {
		log.Fatal(err)
	}
	if err := doc.Import(reply); err != nil {
		log.Fatal(err)
	}

	state, err := doc.JSON()
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(state) // map[items:[milk bread eggs] title:Shopping]
}
```

On the JavaScript side these are plain `loro-crdt` calls:

```js
const blob = doc.export({ mode: "update" });   // bytes for Document.Import
doc.import(bytesFromGo);                         // what ExportUpdates returned
const mine = doc.oplogVersion().encode();        // for loro.DecodeVersionVector
const theirs = VersionVector.decode(goVersion);  // Document.Version().Encode()
const missing = doc.export({ mode: "update", from: theirs });
```

More runnable examples are on [pkg.go.dev](https://pkg.go.dev/github.com/Deln0r/loro-go/loro#pkg-examples).

## What it covers

| Need | API in package `loro` |
| --- | --- |
| A live peer | `NewDocument`, `Import` (updates and snapshots), `Commit`, `CommitWith`, `ExportUpdates`, `JSON` |
| Containers | `Text` (`Insert`, `Delete`, `Mark`, `Unmark`, `Delta`), `Map`, `List`, `MovableList` (`Move`, `Set`), `Tree` (`Create`, `Move`, `Delete`, `Meta`), `Counter` |
| Containers inside containers | `Map.SetContainer`, `List.InsertContainer`, `MovableList.InsertContainer` and `SetContainer`, `Map.EnsureMergeable`, `Document.GetContainer` |
| Sync | `Version`, `Frontiers`, `ExportUpdatesFrom`, `ExportUpdatesInRange`, `VersionVector.Encode`, `DecodeVersionVector`, `EncodeFrontiers`, `DecodeFrontiers` |
| Blobs without a document | `DecodeUpdates`, `DecodeSnapshot`, `MergeState` (state as `toJSON` shows it), `TextDelta` (as `toDelta`), `EncodeUpdates` |

Worth knowing before you start:

- Text positions count Unicode code points. loro-crdt's JavaScript API counts UTF-16 code units, so convert a position that comes from JavaScript. List and MovableList positions count items in both.
- Map values and list items are `nil`, `bool`, `int64`, `float64`, `string`, `[]byte`, `[]any` and `map[string]any`; other integer types are stored as `int64`. loro-crdt's JavaScript binding turns a whole number within ±2^53 into an integer, while a Go `float64` stays a float.
- Give every replica its own peer id. Two writers sharing one produce ops with the same ids, and an import that meets such an op refuses it, as loro does.
- A `Document` is safe to share between goroutines: one mutex guards it, and calls run one at a time.
- Malformed or hostile input is an error: the decoders and `Import` bound every length and count they read, so a bad blob returns an error instead of panicking, hanging or allocating without bound. An import is all or nothing.

## How it is checked

The ground truth is `loro-crdt` itself, run on the same histories:

- Random edit scripts by one to twelve peers run side by side in `loro-crdt` and in Go. At all 12,589 checkpoints the Go document's JSON, the atoms of its history (ids, lamports, positions, values, causal pasts) and its export bytes are `loro-crdt`'s.
- `loro-crdt` imports every Go export, 2,902 whole histories and 3,623 exports from a version, and reaches the same state, on every CI run.
- Decoding and re-encoding 2,781 `loro-crdt` exports gives back 2,764 byte for byte. The other 17 hold a map of two or more keys nested in a value, whose key order loro takes from its hash map; they decode to the same changes, and `loro-crdt` imports them to the same document.
- The decoders, the encoder and `Document.Import` are fuzzed briefly on every push and for four minutes per target each night.
- Each new `loro-crdt` release is checked against the committed fixtures before the pin moves; [COMPAT.md](COMPAT.md) records every check.

[VERIFICATION.md](VERIFICATION.md) lists everything that is checked, corpus by corpus.

## Known limitations

- Merging long concurrent branches is slow. An edit made with everything already merged in view (typing, or editing after a sync) resolves at once; one made concurrently with edits already merged is resolved by scanning the merged sequence. Two peers that each made thousands of changes offline therefore cost time proportional to the product of the two, and past 2^28 steps `MergeState` returns an error rather than run on.
- Edits that jump between far-apart places of a long MovableList, List or Text are slower than in loro. An edit finds its place by walking the merged sequence from where the last one was, over the positions deletes and moves left behind, so such edits cost time in proportion to those positions: 8000 moves of a MovableList's first item to its end take 53 ms (loro-crdt 36 ms), 16000 take 215 ms (62 ms).
- Two roots of one name and different kinds after a snapshot import of a history by several peers: loro decides which one shows by the order its change store loads lazily, and the Document matches it in 178 of 200 random three-peer histories checked, and in every one-peer history.
- Export bytes after a list insert of several items arrived in one import and the next insert in a later one. loro keeps the two as separate ops (`[1, 2]` then `[3, 4]`), the Document folds them into one (`[1, 2, 3, 4]`); state, version and history agree, and loro-crdt imports either form. An insert of one item folds in both.
- Shallow snapshots (`export({mode: "shallow-snapshot"})`) are not supported: their history starts past the beginning, and `Document.Import` refuses them with an error.
- There is no conversion between a version vector and frontiers (`frontiersToVV`, `vvToFrontiers`): loro builds the result while it walks the history, and the order its hash map ends up in is not followed yet.
- Two root containers of different kinds under one name, such as a Text and a List both called `t`. `TextDelta` keeps them apart, but `MergeState` returns one value per name and can show the other one than loro's `toJSON` does: in the one case checked, loro shows the List and `MergeState` the Text. `Document.JSON` shows the one loro shows: of the roots of that name in the order loro registered them, the last that is not empty.

## Status and versioning

v1.0 completes the planned scope: reading, editing, writing and syncing Loro documents. From here the project is maintained rather than extended: bug fixes, security fixes, and new `loro-crdt` releases checked and followed. The API follows semantic versioning, so v1 releases do not break code that builds against v1.0.

Out of scope: a sync server or sync protocol, persistence, mobile bindings, and time travel (`checkout`). [CONTRIBUTING.md](CONTRIBUTING.md) explains what a pull request can change.

## Matrix transport

[`integration/matrix`](integration/matrix/) carries Loro updates as Matrix
events, so a Go service that already speaks Matrix can hold a shared local-first
document without a second network stack. Two peers edit while disconnected,
publish into a room, replay it, and converge; the demo exits non-zero if they do
not, and CI runs it against a real [Dendrite](https://github.com/element-hq/dendrite)
homeserver on every push. It is a separate Go module, so this library's own
`go.mod` stays dependency-free.

## Package layout

- `loro`: the API above.
- `encoding/...`: the wire codec underneath it: the header and checksum (`fast`, `xxh32`), the `serde_columnar` strategies (`columnar`), change blocks and values (`change`), the snapshot's SSTable and LZ4 blocks (`kv`, `lz4`), and varints (`postcard`).
- `integration/matrix`: the Matrix transport, a separate module.
- `testdata`: the fixtures and corpora `loro-crdt` generated, and the scripts that generate them; see [CONTRIBUTING.md](CONTRIBUTING.md#cross-language-fixtures).

## Contributing

Issues and pull requests are welcome. See [CONTRIBUTING.md](CONTRIBUTING.md) for scope, the byte-compatibility rule for codec changes, fixture regeneration and fuzzing, and [CHANGELOG.md](CHANGELOG.md) for what changed between releases.

## License

MIT. See [LICENSE](LICENSE).
