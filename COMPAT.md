# Upstream compatibility checks

loro-go pins its fixture generator to an exact `loro-crdt` version (see
`testdata/gen/package.json`). When upstream ships a new minor, we regenerate the
fixtures with it and byte-compare against the committed ones. This file records
those checks.

## loro-crdt 1.13.1 vs 1.12.5 (checked 2026-06-10)

**Result: Fast wire format unchanged. All 15 fixture scenarios regenerate
byte-identical under 1.13.1.**

Method:

```
cd testdata/gen
npm install loro-crdt@1.13.1
node gen.mjs
git diff --exit-code ../fixtures   # empty: 30 binary blobs identical
cd ../.. && go test ./...          # full suite green, incl byte round-trips
```

Source-level cross-check between tags `loro-crdt@1.12.5` and `loro-crdt@1.13.1`
(61 files changed upstream):

- Core codec files untouched: `loro-internal/src/encoding.rs`,
  `encoding/value.rs`, `oplog/change_store/block_encode.rs`,
  `oplog/change_store.rs`, `kv-store/src/*`. `EncodeMode` values unchanged.
- The 1.13.0 feature (mergeable child containers) encodes its deterministic
  container id inside the existing `Root` variant: the name string carries a
  reserved namespace prefix plus an escaped `(parent, key)` payload
  (`crates/loro-common/src/lib.rs`). No new `ContainerID` wire variant.
  Visibility is an ordinary `Binary` map value resolved by the map's LWW.
- 1.13.1 is a packaging fix (Node/Vitest bare imports), no codec code.

Scope note: the byte comparison covers the 15 committed fixture scenarios
(text, map, list, float values, deletes, movable list, tree, rich text, and
concurrent multi-peer merges). Documents using the new mergeable-container API
are not in the fixture set; for those, the claim rests on the source diff above.

## loro-crdt 1.13.6 vs 1.12.5 (checked 2026-06-28)

**Result: Fast wire format still unchanged. Every committed fixture regenerates
byte-identical under 1.13.6, and all `toJSON()` states match.** The generator pin
was moved from `1.12.5` to `1.13.6` (Dependabot) on the strength of this check.

Method:

```
cd testdata/gen
npm install loro-crdt@1.13.6
node gen.mjs
git status --porcelain ../fixtures   # empty: all binary blobs + JSON identical
cd ../.. && go test ./...            # full suite green
```

## loro-crdt 1.13.7 + serde 1.0.229 (checked 2026-07-21)

**Result: still byte-identical.** Regenerating the fixtures under loro-crdt
1.13.7 leaves every binary blob and JSON state unchanged, and regenerating the
serde_columnar golden vectors under serde 1.0.229 leaves
`testdata/columnar_golden.txt` unchanged. Both pins were bumped (Dependabot #8
and #7) on the strength of these checks.

Method:

```
cd testdata/gen && npm install loro-crdt@1.13.7 && node gen.mjs
cd ../rustgen && cargo update -p serde --precise 1.0.229 && cargo run > ../columnar_golden.txt
cd ../.. && git status --porcelain testdata/   # empty: fixtures + golden vectors identical
go test ./...                                   # full suite green
```

## loro-crdt 1.13.8 (checked 2026-07-29)

**Result: still byte-identical.** Regenerating the fixtures under loro-crdt
1.13.8 leaves every binary blob and JSON state unchanged, including the unicode,
mixed-map and CJK-delete edge cases added since the previous check. The pin was
bumped from `1.13.7` (Dependabot #10) on the strength of this check.

Method:

```
cd testdata/gen && npm install loro-crdt@1.13.8 && node gen.mjs
cd ../.. && git status --porcelain testdata/fixtures/   # empty: all blobs + JSON identical
go test ./...                                            # full suite green
```

**1.13.9 (checked 2026-08-10):** same method, same result, fixtures byte-identical;
the pin moved on to `1.13.9` (Dependabot #12).

## loro-crdt 1.14.1 vs 1.13.9 (checked 2026-08-17)

**Result: Fast wire format unchanged across the first minor bump since 1.12.5.**
Every fixture regenerates byte-identical under 1.14.1, and the release notes for
1.14.0 and 1.14.1 describe no format work:

- 1.14.0 is WASM-binding error handling (catchable JS errors instead of
  `RuntimeError: unreachable` traps, a panic hook that records diagnostics, OOM
  reporting) plus a core fix to how changes whose dependencies are not yet in the
  DAG are parked and re-classified on import.
- 1.14.1 makes `importBatch` atomic: the batch runs in an oplog rollback scope so
  a blob rejected by state validation returns an error with the document still
  attached, instead of trapping and leaving it detached.

Both are import/apply and binding-level behaviour, not encoding. The pin moved to
`1.14.1` (Dependabot #13).

Note for future work: 1.14.0 mentions handling "an unknown container (created by a
newer version of loro-crdt)", so upstream anticipates new container types. New
container kinds would be a format addition to watch for, not a change to the
existing ones.

## loro-crdt 1.15.0 (checked 2026-08-31)

**Result: Fast wire format unchanged.** Fixtures regenerate byte-identical. The
release notes describe no work on this format:

- The minor change adds `pause()` / `resume()` to the JS `UndoManager`.
- One patch stops shallow-snapshot export from shipping the values of rich-text
  marks whose range was already deleted. That is the shallow/redaction section,
  which this library does not read.
- One patch skips recording a mark op when it re-asserts an identical mark. That
  changes which ops loro emits, not how ops encode.

Worth remembering if shallow snapshots are ever supported: from 1.15.0 the values
of dead style pairs are nulled during export, and re-exporting an older shallow
snapshot cleans it too.

## loro-crdt 1.16.1 (checked 2026-09-17)

**Result: Fast wire format unchanged across 1.15.1, 1.16.0 and 1.16.1.** Every
fixture regenerates byte-identical under 1.16.1, including the corpora added
since the last check: the 300 random insert-anywhere histories in
`ordering_corpus.json` (whose expected states are loro-crdt's own `toJSON()`),
the overlapping-export and tail-resend pairs, and the foreign-delete pair. The
generator's peer-id guard still holds. The pin moved to `1.16.1` (Dependabot #16).

The release notes describe no work on this format:

- 1.15.1 caches container ids per JavaScript wrapper and stops
  `toJsonWithReplacer` from throwing on malformed `cid:` strings. Binding-level.
- 1.16.0 adds JavaScript deep-read APIs (`getDeepValueWithID()` on containers,
  `toContainerTree()`), bounds a WASM decoded-value cache, and speeds up
  shallow-snapshot export. That last one changes the bytes of a shallow
  snapshot ("logically equivalent", about 17% smaller on their fixture), but
  shallow snapshots are a mode this library does not read.
- 1.16.1 speeds up concurrent imports and fixes a style table that grew a
  duplicate entry per re-replayed style op. Import and apply side, not encoding.

Two things worth knowing that are not format changes. The `cid` field in
`getDeepValueWithID()` results changed shape in 1.16.0, which matters only to
JavaScript consumers parsing it. And the pure-TypeScript runtime that was merged
upstream in July now ships on npm as `loro.js` (0.2.0 on 2026-08-27), so there
are two independent reference implementations to check against, not one.

## loro-crdt 1.16.3 (checked 2026-09-28)

**Result: Fast wire format unchanged across 1.16.2 and 1.16.3.** All 117
fixture files were rewritten by the generator under 1.16.3 and none differ,
including the ordering corpus and the overlap, tail-resend and foreign-delete
pairs. The pin moved to `1.16.3` (Dependabot #18).

1.16.2 does touch encoding, but not what this library reads:

- It fixes the decoding of **tree snapshot state** whose siblings are not in
  fractional-index order, and now rejects state with duplicate node ids or
  duplicate sibling positions. That is the state section of a snapshot. This
  library reads a snapshot's oplog section and rebuilds state from the ops, so
  the change is outside what it decodes. Worth knowing for anyone comparing
  against `loro.js`: its 0.1.0 and 0.2.0 releases wrote tree snapshot state with
  siblings out of order after a `move()` (loro-dev/loro#1088).
- It retains deleted-container state in `forkAt` and snapshot-at exports, and
  fixes shallow snapshots that exported but would not import. Neither mode is
  read here.
- The rest is runtime behaviour: subscribing during an emit, and
  `LoroText.getCursor` at UTF-16 boundaries.

1.16.3 makes inserting a container that belongs to another document a
recoverable error in the JavaScript binding.


## loro-crdt 1.16.4 (checked 2026-10-06)

**Result: Fast wire format unchanged.** All 183 fixture files were rewritten
by the generator under 1.16.4 and none differ. The pin moved to `1.16.4`
(Dependabot #19).

What in 1.16.4 touches a reader of these bytes:

- **Deletes with wrong ids in existing histories (loro-dev/loro#1149).** The
  WASM build up to 1.16.3 could record a wrong `start_id` for a text delete
  that spans text from separate inserts when an astral character ends one of
  them. 1.16.4 writes correct ids for new ops; histories written before keep
  the wrong ones. loro's own import applies a delete by its position in the
  author's version (`diff_calc.rs`), so it is unaffected. This library applies
  a delete by its id span, so it removes the wrong characters:
  `testdata/fixtures/frozen/delete_astral_ids_1163`, written by 1.16.3, gives
  `"x!"` here where loro gives `"b!"`. Reading deletes by position needs the
  merged order of concurrent inserts to match loro's first; until then the id
  span is the more reliable of the two, and the README lists the gap.
- **Hard limit on sequence positions.** loro now rejects insert, delete and
  move positions of 1 073 741 822 or more in a List, MovableList or Text op at
  decode. This library does not reject them; such a position resolves to no
  element.
- **Reused op ids.** loro now refuses an import that reuses op ids for different
  content (two clients sharing a peer id), and one whose change skips counters
  of its peer. This library keeps the first copy of an id range it sees and
  merges a change that skips counters as a partial history.
- **Empty root containers.** A root container touched by a history now stays
  in the value after a replay even when its ops cancel out (`{"t": ""}`), as it
  already did for the writing peer. `MergeState` lists every root container an
  op touches, so it agrees.
- The rest is runtime behaviour outside the Fast format: `applyDiff`,
  `revertTo` and undo made all-or-nothing, unknown container types, shallow
  snapshot re-export, movable-list `move`/`set` validation, and two rich-text
  fixes that change where new ops are recorded, not how old ones read.
