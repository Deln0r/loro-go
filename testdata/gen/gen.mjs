// Cross-language fixture generator for loro-go. Drives the canonical
// loro-crdt npm package and emits, per scenario:
//   ../fixtures/<name>.update.bin    FastUpdates export (sync wire)
//   ../fixtures/<name>.snapshot.bin  FastSnapshot export (full doc)
//   ../fixtures/<name>.json          doc.toJSON() expected final state
// Peer IDs are pinned so the bytes are deterministic across runs.
import { LoroDoc } from "loro-crdt/nodejs";
import { writeFileSync, mkdirSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";

const here = dirname(fileURLToPath(import.meta.url));
const outDir = join(here, "..", "fixtures");
mkdirSync(outDir, { recursive: true });

function emit(name, build) {
  const doc = new LoroDoc();
  doc.setPeerId(1n);
  build(doc);
  doc.commit();
  const update = doc.export({ mode: "update" });
  const snapshot = doc.export({ mode: "snapshot" });
  writeFileSync(join(outDir, `${name}.update.bin`), Buffer.from(update));
  writeFileSync(join(outDir, `${name}.snapshot.bin`), Buffer.from(snapshot));
  writeFileSync(join(outDir, `${name}.json`), JSON.stringify(doc.toJSON(), null, 2) + "\n");
  let ops;
  try {
    ops = doc.exportJsonUpdates();
  } catch (e) {
    ops = { error: String(e) };
  }
  writeFileSync(
    join(outDir, `${name}.ops.json`),
    JSON.stringify(ops, (_k, v) => (typeof v === "bigint" ? v.toString() : v), 2) + "\n",
  );
  console.log(`${name}: update=${update.length}B snapshot=${snapshot.length}B`);
}

emit("text_hi", (doc) => {
  doc.getText("t").insert(0, "hi");
});

emit("map_kv", (doc) => {
  const m = doc.getMap("m");
  m.set("k", "v");
  m.set("n", 42);
});

emit("list_abc", (doc) => {
  const l = doc.getList("l");
  l.insert(0, "a");
  l.insert(1, "b");
  l.insert(2, "c");
});

// Exercises the f64 BIG-endian path in the change-block VALUES stream (the #1
// silent-corruption risk flagged in the wire reference, CONFLICT #3).
emit("map_float", (doc) => {
  const m = doc.getMap("m");
  m.set("pi", 3.14);
  m.set("big", 1e308);
  m.set("neg", -2.5);
});

// Edge cases for existing containers. unicode_text: multi-byte runes incl an
// astral emoji, insert-only round-trip (state must match byte-for-byte).
emit("unicode_text", (doc) => {
  doc.getText("t").insert(0, "héllo 世界 🦀");
});

// map_mixed: assorted scalar value kinds in one map (bool, negative int, empty
// string, string), exercising several VALUES-stream kinds together.
emit("map_mixed", (doc) => {
  const m = doc.getMap("m");
  m.set("yes", true);
  m.set("no", false);
  m.set("neg", -1234567);
  m.set("empty", "");
  m.set("s", "text");
});

// text_cjk_del: delete spanning multi-byte (BMP) runes. Positions are BMP so the
// utf-16 (loro) and rune (loro-go) indices agree; "abc世界def" delete(3,2) -> "abcdef".
emit("text_cjk_del", (doc) => {
  const t = doc.getText("t");
  t.insert(0, "abc世界def");
  t.delete(3, 2);
});

// Concurrent / multi-peer fixtures: two peers edit the same place, merge, then
// export the merged update + final toJSON. Exercises multi-change blocks and the
// CRDT merge (Fugue ordering for text/list, LWW for map).
function emitMerged(name, buildA, buildB) {
  const a = new LoroDoc();
  a.setPeerId(1n);
  buildA(a);
  a.commit();
  const b = new LoroDoc();
  b.setPeerId(2n);
  buildB(b);
  b.commit();
  a.import(b.export({ mode: "update" }));
  b.import(a.export({ mode: "update" }));
  const update = a.export({ mode: "update" });
  const snapshot = a.export({ mode: "snapshot" });
  writeFileSync(join(outDir, `${name}.update.bin`), Buffer.from(update));
  writeFileSync(join(outDir, `${name}.snapshot.bin`), Buffer.from(snapshot));
  writeFileSync(join(outDir, `${name}.json`), JSON.stringify(a.toJSON(), null, 2) + "\n");
  let ops;
  try {
    ops = a.exportJsonUpdates();
  } catch (e) {
    ops = { error: String(e) };
  }
  writeFileSync(
    join(outDir, `${name}.ops.json`),
    JSON.stringify(ops, (_k, v) => (typeof v === "bigint" ? v.toString() : v), 2) + "\n",
  );
  console.log(`${name}: update=${update.length}B merged=${JSON.stringify(a.toJSON())}`);
}

emitMerged("conc_text", (d) => d.getText("t").insert(0, "A"), (d) => d.getText("t").insert(0, "B"));
emitMerged("conc_map", (d) => d.getMap("m").set("k", "A"), (d) => d.getMap("m").set("k", "B"));
emitMerged("conc_list", (d) => d.getList("l").insert(0, "A"), (d) => d.getList("l").insert(0, "B"));
// Multi-char concurrent runs: probe Fugue non-interleaving (runs stay contiguous).
emitMerged("conc_text2", (d) => d.getText("t").insert(0, "AB"), (d) => d.getText("t").insert(0, "CD"));
// Delete ops: DeleteSeq for text/list (id-span tombstones), map key deletion.
emit("text_del", (doc) => {
  const t = doc.getText("t");
  t.insert(0, "hello");
  t.delete(1, 2);
});
emit("list_del", (doc) => {
  const l = doc.getList("l");
  l.insert(0, "a");
  l.insert(1, "b");
  l.insert(2, "c");
  l.delete(1, 1);
});
emit("map_del", (doc) => {
  const m = doc.getMap("m");
  m.set("k", "v");
  m.set("x", 1);
  m.delete("k");
});

// Counter container: increments accumulate (incl. a negative one). toJSON gives
// the summed numeric value. Probes how loro encodes counter ops in the VALUES
// stream (no dedicated ValueKind; observed empirically).
emit("counter", (doc) => {
  const c = doc.getCounter("c");
  c.increment(5);
  c.increment(3);
  c.increment(-2);
});

// Multi-change block: two commits with timestamps far enough apart that loro
// keeps them as separate changes (default merge interval is 1000s) packed into
// one block for the same peer.
{
  const doc = new LoroDoc();
  doc.setPeerId(1n);
  doc.getText("t").insert(0, "ab");
  doc.commit({ timestamp: 1000 });
  doc.getText("t").insert(2, "cd");
  doc.commit({ timestamp: 5000 });
  const update = doc.export({ mode: "update" });
  const snapshot = doc.export({ mode: "snapshot" });
  writeFileSync(join(outDir, "two_changes.update.bin"), Buffer.from(update));
  writeFileSync(join(outDir, "two_changes.snapshot.bin"), Buffer.from(snapshot));
  writeFileSync(join(outDir, "two_changes.json"), JSON.stringify(doc.toJSON(), null, 2) + "\n");
  writeFileSync(
    join(outDir, "two_changes.ops.json"),
    JSON.stringify(doc.exportJsonUpdates(), (_k, v) => (typeof v === "bigint" ? v.toString() : v), 2) + "\n",
  );
  console.log(`two_changes: update=${update.length}B`);
}

// Cross-peer delete: peer 2 deletes elements peer 1 inserted (real causal
// dependency, delete span targets another peer's ids).
{
  const a = new LoroDoc();
  a.setPeerId(1n);
  a.getText("t").insert(0, "abc");
  a.commit();
  const b = new LoroDoc();
  b.setPeerId(2n);
  b.import(a.export({ mode: "update" }));
  b.getText("t").delete(1, 1);
  b.commit();
  a.import(b.export({ mode: "update" }));
  const update = a.export({ mode: "update" });
  const snapshot = a.export({ mode: "snapshot" });
  writeFileSync(join(outDir, "cross_del.update.bin"), Buffer.from(update));
  writeFileSync(join(outDir, "cross_del.snapshot.bin"), Buffer.from(snapshot));
  writeFileSync(join(outDir, "cross_del.json"), JSON.stringify(a.toJSON(), null, 2) + "\n");
  writeFileSync(
    join(outDir, "cross_del.ops.json"),
    JSON.stringify(a.exportJsonUpdates(), (_k, v) => (typeof v === "bigint" ? v.toString() : v), 2) + "\n",
  );
  console.log(`cross_del: update=${update.length}B merged=${JSON.stringify(a.toJSON())}`);
}

// Chunk 3 probes: Tree, MovableList (fractional-index positions), rich text (Peritext marks).
emit("mlist", (doc) => {
  const l = doc.getMovableList("ml");
  l.insert(0, "a");
  l.insert(1, "b");
  l.insert(2, "c");
  l.move(2, 0);
});
emit("tree_simple", (doc) => {
  const tr = doc.getTree("tr");
  const root = tr.createNode();
  tr.createNode(root.id);
});
// Tree node meta: each node carries a Map sub-container (node.data) whose values
// appear under "meta" in toJSON. Probes non-root container resolution by node id.
emit("tree_meta", (doc) => {
  const tr = doc.getTree("tr");
  const root = tr.createNode();
  root.data.set("name", "root-node");
  root.data.set("n", 5);
  const child = tr.createNode(root.id);
  child.data.set("label", "child");
});
emit("richtext", (doc) => {
  const t = doc.getText("rt");
  t.insert(0, "hello");
  t.mark({ start: 0, end: 3 }, "bold", true);
});

// Wide tree: two children, then repeated insertions BETWEEN the same pair. That
// is what drives fractional indices to grow and share leading bytes (80,
// 817480, 817580, ...), so the positions blob actually exercises its
// prefix-compression column. Appending siblings would not: those indices differ
// in their first byte and every prefix length stays zero.
emit("tree_wide", (doc) => {
  const tr = doc.getTree("tr");
  const root = tr.createNode();
  tr.createNode(root.id, 0);
  tr.createNode(root.id, 1);
  for (let i = 0; i < 12; i++) {
    tr.createNode(root.id, 1);
  }
});

// Rich-text toDelta cases: capture the styled delta (toJSON only gives plain
// text). One mark, two disjoint marks, overlapping marks.
function emitDelta(name, build) {
  const doc = new LoroDoc();
  doc.setPeerId(1n);
  const t = doc.getText("rt");
  build(t);
  doc.commit();
  const update = doc.export({ mode: "update" });
  writeFileSync(join(outDir, `${name}.update.bin`), Buffer.from(update));
  writeFileSync(join(outDir, `${name}.snapshot.bin`), Buffer.from(doc.export({ mode: "snapshot" })));
  writeFileSync(join(outDir, `${name}.json`), JSON.stringify(doc.toJSON(), null, 2) + "\n");
  writeFileSync(join(outDir, `${name}.delta.json`), JSON.stringify(t.toDelta(), null, 2) + "\n");
  writeFileSync(
    join(outDir, `${name}.ops.json`),
    JSON.stringify(doc.exportJsonUpdates(), (_k, v) => (typeof v === "bigint" ? v.toString() : v), 2) + "\n",
  );
  console.log(`${name}: update=${update.length}B delta=${JSON.stringify(t.toDelta())}`);
}
emitDelta("rt_one", (t) => {
  t.insert(0, "hello");
  t.mark({ start: 0, end: 3 }, "bold", true);
});
emitDelta("rt_two", (t) => {
  t.insert(0, "hello world");
  t.mark({ start: 0, end: 5 }, "bold", true);
  t.mark({ start: 6, end: 11 }, "italic", true);
});
emitDelta("rt_overlap", (t) => {
  t.insert(0, "abcde");
  t.mark({ start: 0, end: 4 }, "bold", true);
  t.mark({ start: 2, end: 5 }, "italic", true);
});

emitMerged(
  "conc_list2",
  (d) => {
    const l = d.getList("l");
    l.insert(0, "A");
    l.insert(1, "B");
  },
  (d) => {
    const l = d.getList("l");
    l.insert(0, "C");
    l.insert(1, "D");
  },
);

// Overlapping exports from one peer. loro coalesces adjacent atoms into a
// single run, so two exports of the same document taken at different moments
// share a first counter while covering different id spans. A reader that
// deduplicates on the first counter alone silently loses the longer run's tail.
// Emitted as two separate update blobs, since the whole point is what happens
// when a merge sees both.
{
  const doc = new LoroDoc();
  doc.setPeerId(2n);
  doc.getText("t").insert(0, "ab");
  doc.commit();
  const early = doc.export({ mode: "update" });
  doc.getText("t").insert(2, "cd");
  doc.commit();
  const late = doc.export({ mode: "update" });
  writeFileSync(join(outDir, "span_overlap.early.bin"), Buffer.from(early));
  writeFileSync(join(outDir, "span_overlap.late.bin"), Buffer.from(late));
  writeFileSync(join(outDir, "span_overlap.json"), JSON.stringify(doc.toJSON(), null, 2) + "\n");
  console.log(`span_overlap: early=${early.length}B late=${late.length}B`);
}

// A full export alongside a from-version delta covering only the tail. The
// delta starts at a later counter, so a reader keying on the first counter
// misses the overlap entirely and applies the tail atoms a second time.
{
  const doc = new LoroDoc();
  doc.setPeerId(3n);
  doc.getText("t").insert(0, "hel");
  doc.commit();
  const v = doc.version();
  doc.getText("t").insert(3, "lo");
  doc.commit();
  const full = doc.export({ mode: "update" });
  const tail = doc.export({ mode: "update", from: v });
  writeFileSync(join(outDir, "span_tail.full.bin"), Buffer.from(full));
  writeFileSync(join(outDir, "span_tail.tail.bin"), Buffer.from(tail));
  writeFileSync(join(outDir, "span_tail.json"), JSON.stringify(doc.toJSON(), null, 2) + "\n");
  console.log(`span_tail: full=${full.length}B tail=${tail.length}B`);
}

// Random insert-anywhere histories, with loro-crdt's own toJSON as the answer.
//
// The fixed scenarios above are almost all appends, and an append is the one
// case a wrong sibling order still gets right. A merge that was wrong on 294 of
// these 300 histories passed every one of them. Kept as one file rather than
// 600, since each history is tiny and the point is breadth.
{
  const rng = (seed) => { let s = seed >>> 0; return () => (s = (s * 1664525 + 1013904223) >>> 0) / 4294967296; };
  const letters = "abcdefghijklmnopqrstuvwxyz";
  const corpus = [];
  for (let seed = 1; seed <= 300; seed++) {
    const r = rng(seed);
    const doc = new LoroDoc();
    doc.setPeerId(1n);
    const t = doc.getText("t");
    const steps = 1 + Math.floor(r() * 12);
    for (let i = 0; i < steps; i++) {
      const at = Math.floor(r() * (t.length + 1));
      const s = letters[Math.floor(r() * 26)].repeat(1 + Math.floor(r() * 3));
      t.insert(at, s);
      if (r() < 0.6) doc.commit();
    }
    doc.commit();
    corpus.push({
      seed,
      update: Buffer.from(doc.export({ mode: "update" })).toString("base64"),
      expected: doc.toJSON(),
    });
  }
  writeFileSync(join(outDir, "ordering_corpus.json"), JSON.stringify(corpus, null, 1) + "\n");
  console.log(`ordering_corpus: ${corpus.length} histories`);
}
