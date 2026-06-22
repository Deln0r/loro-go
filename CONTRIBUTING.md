# Contributing to loro-go

Thanks for considering a contribution. loro-go is an independent pure-Go reading of the [Loro](https://github.com/loro-dev/loro) CRDT wire format. It is early-stage and the maintainer is open to issues and pull requests, but capacity is limited. Reading this whole document before opening anything saves everyone time.

## Scope

loro-go implements the Loro **Fast** wire format (`FastUpdates` and `FastSnapshot`) in pure Go, with no cgo.

- **In scope:** byte-compatible decode and encode of the Fast format, the `serde_columnar` strategies, change-block parsing, CRDT state reconstruction for Map / List / Text / MovableList / Tree / Counter, rich-text `toDelta`, the KV/SSTable reader, LZ4 block decompression, the cross-language fixture suite, and robustness hardening of the decoders against malformed input.
- **Out of scope until a future dedicated phase (planned work):** a real-time sync server, native persistence (sqlite or otherwise), mobile bindings (gomobile), peer-to-peer transport, the full concurrent Peritext expand-rule semantics, and a time-travel / checkout API. PRs that try to start any of these will be politely closed. They are reserved for a future development phase, and partial implementations would create maintenance debt.
- **Out of scope permanently:** anything outside the Loro wire format and its CRDT semantics.

If you are unsure whether something is in scope, open an issue first.

## Bug reports

The most useful bug reports include:

1. The version (commit SHA or release tag).
2. Minimal Go code that reproduces the issue.
3. Expected vs observed behavior.
4. If it is a byte-encoding bug: hex dumps of the input, the expected bytes, and the actual bytes, plus the `loro-crdt` equivalent if you have it.

Cross-implementation interop bugs are the highest priority. If you find a byte divergence against `loro-crdt` that is not already covered by a fixture in `testdata/fixtures/`, please attach the discrepancy.

Robustness bugs (a panic, unbounded allocation, or hang on untrusted input) are treated as security-relevant; see the disclosure note at the end.

## Pull requests

Before opening a PR:

- Run `go test ./...` (all packages must stay green).
- Run `go vet ./...`.
- Run `gofmt -l .` on changed files and fix anything it lists.
- Add a test that fails without the change and passes with it.

Any PR that adds or modifies a byte decoder or encoder MUST extend the fixture corpus. Code-only codec changes without fixture coverage will not be merged, because the entire value of this library is staying byte-identical to `loro-crdt`.

Commit message format: one short subject line under 70 characters, then a blank line, then a body explaining the *why*. Example:

```
counter container: reconstruct state as summed increments

Loro encodes counter increments in the VALUES stream as I64 (or F64 for
fractional ones) with no dedicated container in BuildState/MergeState.
The state is the order-independent sum of all increments (fixture
counter: 5 + 3 - 2 = 6 reproduces).
```

### Authorship

This project uses standard git authorship: commits are attributed to the human author of the change. Please do not include tool-generated attribution trailers or co-author lines in commit messages. PRs containing such trailers will be asked to amend.

## Cross-language fixtures

The fixture suite is the ground truth. Two harnesses produce it:

- `testdata/gen/` drives the `loro-crdt` npm package (pinned exactly to a single version) to emit binary blobs, `toJSON()` state, and op dumps.
- `testdata/rustgen/` emits golden column vectors from the `serde_columnar` crate.

To regenerate:

```
cd testdata/gen && npm install && node gen.mjs
cd ../rustgen && cargo run > ../columnar_golden.txt
```

After regeneration, commit the updated fixtures and any code needed to consume them, and keep the pinned `loro-crdt` and `serde_columnar` versions exact (a caret range would let the bytes drift silently). Do not commit `testdata/gen/node_modules/` or `testdata/rustgen/target/` (both are gitignored).

## Fuzzing

The decoders parse fully untrusted bytes, so they are fuzzed below the checksum layer (a malicious peer can recompute a valid checksum, so the deep decoders must be robust on their own). To run a target:

```
go test ./loro/ -run='^$' -fuzz=FuzzDecodeBlock -fuzztime=30s
go test ./encoding/columnar/ -run='^$' -fuzz=FuzzStrategies -fuzztime=30s
```

A new decoder path should never panic, allocate without bound, or hang on hostile input; it should return an error. If you add a decoder, add a fuzz target or extend an existing one, and bound any length or count taken from the input against the bytes that remain.

## Code style

- Standard `gofmt`. `go vet ./...` must pass.
- Idiomatic Go. Generics only where the standard library already uses them.
- No cgo. The whole project builds with `go build` and a couple of pure-Go test-time deps; please keep it that way.
- Public API stability is not yet a priority; breaking changes are accepted until v1.0.
- Comments: a `doc.go`-style package comment and godoc on exported symbols. Short, not essays.

## Security disclosure

For robustness or security-relevant issues (panics, unbounded allocation, or hangs on untrusted input), please report privately first via GitHub's "Report a vulnerability" (the Security tab) rather than opening a public issue. Once a fix is ready we will publish the fix and a short note together.
