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
