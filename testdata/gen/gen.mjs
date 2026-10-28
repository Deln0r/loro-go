// Cross-language fixture generator for loro-go. Drives the canonical
// loro-crdt npm package and emits, per scenario:
//   ../fixtures/<name>.update.bin    FastUpdates export (sync wire)
//   ../fixtures/<name>.snapshot.bin  FastSnapshot export (full doc)
//   ../fixtures/<name>.json          doc.toJSON() expected final state
// Peer IDs are pinned so the bytes are deterministic across runs.
import { LoroDoc, LoroText, LoroMap, LoroList, LoroMovableList, LoroCounter, LoroTree } from "loro-crdt/nodejs";
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

// Objects inside values. A map nested in a list item, a map value or a
// MovableList item names each of its keys by its index in the block's key arena.
emit("nested_values", (doc) => {
  doc.getList("l").insert(0, { k: 1, z: [1, { q: "s" }] });
  doc.getMap("m").set("v", { k: 2, b: "x", n: null });
  doc.getMap("m").set("w", [{ a: [] }, {}]);
  const ml = doc.getMovableList("ml");
  ml.insert(0, { id: 1 });
  ml.insert(1, "plain");
  ml.set(1, { id: 2, tags: ["t"] });
});

// Rich-text cases the corpora do not reach: marks of one key with different
// values, an unmark, and marks of one key made concurrently by two peers, where
// the mark with the later (lamport, peer) wins. Peer 1 marks first in
// rt_conc_color_lamport, so its red mark has the higher lamport and wins over
// peer 2's blue. The answer is a replay's, as for the corpora.
function emitDeltaPeers(name, peers, build) {
  const docs = [];
  for (let p = 1; p <= peers; p++) {
    const d = new LoroDoc();
    d.setPeerId(BigInt(p));
    d.configTextStyle({
      bold: { expand: "after" },
      italic: { expand: "after" },
      color: { expand: "none" },
      x: { expand: "none" },
      y: { expand: "none" },
    });
    docs.push(d);
  }
  build(docs);
  const all = new LoroDoc();
  for (const d of docs) {
    d.commit();
    all.import(d.export({ mode: "update" }));
  }
  const update = all.export({ mode: "update" });
  const replay = new LoroDoc();
  replay.import(update);
  const delta = replay.getText("rt").toDelta();
  writeFileSync(join(outDir, `${name}.update.bin`), Buffer.from(update));
  writeFileSync(join(outDir, `${name}.json`), JSON.stringify(replay.toJSON(), null, 2) + "\n");
  writeFileSync(join(outDir, `${name}.delta.json`), JSON.stringify(delta, null, 2) + "\n");
  console.log(`${name}: update=${update.length}B delta=${JSON.stringify(delta)}`);
}
// share copies peer 0's text to every other peer, so their marks are concurrent.
const share = (docs) => {
  docs[0].commit();
  for (const d of docs.slice(1)) d.import(docs[0].export({ mode: "update" }));
};
emitDeltaPeers("rt_same_key", 1, ([a]) => {
  const t = a.getText("rt");
  t.insert(0, "abcdef");
  t.mark({ start: 0, end: 4 }, "color", "red");
  t.mark({ start: 2, end: 6 }, "color", "blue");
});
emitDeltaPeers("rt_unmark", 1, ([a]) => {
  const t = a.getText("rt");
  t.insert(0, "abcdef");
  t.mark({ start: 0, end: 6 }, "bold", true);
  t.unmark({ start: 1, end: 3 }, "bold");
});
emitDeltaPeers("rt_conc_color", 2, (docs) => {
  docs[0].getText("rt").insert(0, "abcdef");
  share(docs);
  docs[0].getText("rt").mark({ start: 0, end: 4 }, "color", "red");
  docs[1].getText("rt").mark({ start: 2, end: 6 }, "color", "blue");
});
emitDeltaPeers("rt_conc_color_lamport", 2, (docs) => {
  docs[0].getText("rt").insert(0, "abcdef");
  share(docs);
  docs[0].getText("rt").mark({ start: 5, end: 6 }, "italic", true);
  docs[0].commit();
  docs[0].getText("rt").mark({ start: 0, end: 4 }, "color", "red");
  docs[1].getText("rt").mark({ start: 2, end: 6 }, "color", "blue");
});
// Style values that are objects and arrays: loro compares them by content, so
// "cd", marked in two pieces with equal arrays, is one run.
emitDeltaPeers("rt_struct", 1, ([a]) => {
  const t = a.getText("rt");
  t.insert(0, "abcd");
  t.mark({ start: 0, end: 2 }, "x", { k: 1 });
  t.mark({ start: 2, end: 3 }, "y", [1, 2]);
  t.mark({ start: 3, end: 4 }, "y", [1, 2]);
});

// A mark whose two anchors arrive in different exports. The first export stops
// between them, and a mark without its end styles nothing; merged with the
// full export, in either order, the mark is whole again.
{
  const doc = new LoroDoc();
  doc.setPeerId(1n);
  const t = doc.getText("rt");
  t.insert(0, "abc");
  t.mark({ start: 0, end: 1 }, "bold", true);
  doc.commit();
  const part = doc.export({ mode: "updates-in-range", spans: [{ id: { peer: "1", counter: 0 }, len: 4 }] });
  const full = doc.export({ mode: "update" });
  const partDoc = new LoroDoc();
  partDoc.import(part);
  const fullDoc = new LoroDoc();
  fullDoc.import(full);
  writeFileSync(join(outDir, "rt_split_mark.part.bin"), Buffer.from(part));
  writeFileSync(join(outDir, "rt_split_mark.part.delta.json"), JSON.stringify(partDoc.getText("rt").toDelta(), null, 2) + "\n");
  writeFileSync(join(outDir, "rt_split_mark.update.bin"), Buffer.from(full));
  writeFileSync(join(outDir, "rt_split_mark.json"), JSON.stringify(fullDoc.toJSON(), null, 2) + "\n");
  writeFileSync(join(outDir, "rt_split_mark.delta.json"), JSON.stringify(fullDoc.getText("rt").toDelta(), null, 2) + "\n");
  console.log(`rt_split_mark: part=${JSON.stringify(partDoc.getText("rt").toDelta())} full=${JSON.stringify(fullDoc.getText("rt").toDelta())}`);
}

// A delete of a mark's end anchor. loro's own text.delete() never removes an
// anchor, so this history is written as JSON updates: "abc", bold over "ab"
// (anchors at counters 3 and 4), then a delete at entity position 3, the end
// anchor. loro then styles nothing.
{
  const doc = new LoroDoc();
  doc.setPeerId(1n);
  const t = doc.getText("rt");
  t.insert(0, "abc");
  t.mark({ start: 0, end: 2 }, "bold", true);
  doc.commit();
  const json = doc.exportJsonUpdates();
  json.changes.push({
    id: "5@0",
    timestamp: 0,
    deps: ["4@0"],
    lamport: 5,
    msg: null,
    ops: [{ container: "cid:root-rt:Text", content: { type: "delete", pos: 3, len: 1, start_id: "4@0" }, counter: 5 }],
  });
  const crafted = new LoroDoc();
  crafted.importJsonUpdates(json);
  const update = crafted.export({ mode: "update" });
  const replay = new LoroDoc();
  replay.import(update);
  writeFileSync(join(outDir, "rt_end_deleted.update.bin"), Buffer.from(update));
  writeFileSync(join(outDir, "rt_end_deleted.json"), JSON.stringify(replay.toJSON(), null, 2) + "\n");
  writeFileSync(join(outDir, "rt_end_deleted.delta.json"), JSON.stringify(replay.getText("rt").toDelta(), null, 2) + "\n");
  console.log(`rt_end_deleted: delta=${JSON.stringify(replay.getText("rt").toDelta())}`);
}

// An unmark over bold text leaves it unstyled, yet loro keeps it a run of its
// own, apart from the text beside it that was never bold.
emitDeltaPeers("rt_unmark_alone", 1, ([a]) => {
  const t = a.getText("rt");
  t.insert(0, "abcd");
  t.mark({ start: 1, end: 3 }, "bold", true);
  t.unmark({ start: 1, end: 3 }, "bold");
});

// A Text and a List with the same root name are two containers. No .json:
// loro's toJSON shows only one of them under the name.
{
  const doc = new LoroDoc();
  doc.setPeerId(1n);
  const t = doc.getText("rt");
  t.insert(0, "hi");
  doc.getList("rt").insert(0, 1);
  t.insert(2, "!");
  doc.commit();
  const update = doc.export({ mode: "update" });
  const replay = new LoroDoc();
  replay.import(update);
  writeFileSync(join(outDir, "rt_same_name.update.bin"), Buffer.from(update));
  writeFileSync(join(outDir, "rt_same_name.delta.json"), JSON.stringify(replay.getText("rt").toDelta(), null, 2) + "\n");
  console.log(`rt_same_name: delta=${JSON.stringify(replay.getText("rt").toDelta())} json=${JSON.stringify(replay.toJSON())}`);
}

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

// A peer that authored nothing deleting everything. Deletes in this CRDT are
// id-addressed and carry no authorisation: any peer holding the ids may
// tombstone them. Emitted so the transport can demonstrate, rather than assert,
// that validating a blob says nothing about whether its author was entitled to
// send it.
{
  const author = new LoroDoc();
  author.setPeerId(1n);
  author.getText("t").insert(0, "confidential minutes");
  author.commit();
  const authored = author.export({ mode: "update" });

  const other = new LoroDoc();
  other.setPeerId(99n);
  other.import(authored);
  other.getText("t").delete(0, 20);
  other.commit();
  const wipe = other.export({ mode: "update", from: author.version() });

  writeFileSync(join(outDir, "foreign_delete.authored.bin"), Buffer.from(authored));
  writeFileSync(join(outDir, "foreign_delete.wipe.bin"), Buffer.from(wipe));
  writeFileSync(join(outDir, "foreign_delete.json"), JSON.stringify(other.toJSON(), null, 2) + "\n");
  console.log(`foreign_delete: authored=${authored.length}B wipe=${wipe.length}B`);
}

// Guard, not a fixture. `new LoroDoc({peerId})` silently ignores the option and
// leaves a random peer id, which would make every blob above non-deterministic
// while looking correct. Only setPeerId works; this fails the generator loudly
// if that ever changes or if someone reaches for the constructor form.
{
  const explicit = new LoroDoc();
  explicit.setPeerId(1n);
  if (explicit.peerId !== 1n) {
    throw new Error(`setPeerId did not take: peerId=${explicit.peerId}`);
  }
  const viaOption = new LoroDoc({ peerId: 1n });
  if (viaOption.peerId === 1n) {
    throw new Error("new LoroDoc({peerId}) now works; update this guard and the generator may use it");
  }
  console.log("peer-id guard: setPeerId works, constructor option still ignored");
}

// Tree moves and deletes. Every tree op is a move: creating a node moves a new
// node in, and deleting one moves it under loro's deleted-root sentinel. The
// tree fixtures above only ever create nodes, so none of this was covered, and
// state reconstruction turned out to show a moved node under every parent it
// had ever had, keep deleted nodes, and recurse forever on the crossing-moves
// case below.
emit("tree_move_once", (doc) => {
  const t = doc.getTree("tr");
  const a = t.createNode();
  const b = t.createNode();
  doc.commit();
  t.move(b.id, a.id);
});

emit("tree_move_twice", (doc) => {
  const t = doc.getTree("tr");
  const a = t.createNode();
  const b = t.createNode();
  const c = t.createNode();
  doc.commit();
  t.move(c.id, a.id);
  doc.commit();
  t.move(c.id, b.id);
});

// Deleting a node takes its subtree with it.
emit("tree_delete_subtree", (doc) => {
  const t = doc.getTree("tr");
  t.createNode();
  const b = t.createNode();
  const c = t.createNode();
  doc.commit();
  t.move(c.id, b.id);
  doc.commit();
  t.delete(b.id);
});

// Two honest peers from a shared base make crossing moves at the same lamport:
// one puts A under B, the other B under A. Applying both would make each the
// other's ancestor. loro orders the moves by (lamport, peer) and drops the one
// that would close the cycle, so peer 1's move stands and peer 2's is skipped.
{
  const base = new LoroDoc();
  base.setPeerId(1n);
  const bt = base.getTree("tr");
  const a = bt.createNode();
  const b = bt.createNode();
  base.commit();
  const p1 = new LoroDoc();
  p1.setPeerId(1n);
  p1.import(base.export({ mode: "update" }));
  const p2 = new LoroDoc();
  p2.setPeerId(2n);
  p2.import(base.export({ mode: "update" }));
  p1.getTree("tr").move(a.id, b.id);
  p1.commit();
  p2.getTree("tr").move(b.id, a.id);
  p2.commit();
  p1.import(p2.export({ mode: "update" }));
  const update = p1.export({ mode: "update" });
  const snapshot = p1.export({ mode: "snapshot" });
  writeFileSync(join(outDir, "tree_crossing_moves.update.bin"), Buffer.from(update));
  writeFileSync(join(outDir, "tree_crossing_moves.snapshot.bin"), Buffer.from(snapshot));
  writeFileSync(join(outDir, "tree_crossing_moves.json"), JSON.stringify(p1.toJSON(), null, 2) + "\n");
  console.log(`tree_crossing_moves: update=${update.length}B merged=${JSON.stringify(p1.toJSON()).slice(0, 120)}`);
}

// A rejected move stays rejected. From a shared base, A under B and B under A
// cross; B under A loses. Then A moves back to the root, which breaks the old
// cycle. The dropped move must not come back to life: both nodes end at root.
{
  const base = new LoroDoc();
  base.setPeerId(1n);
  const bt = base.getTree("tr");
  const a = bt.createNode();
  const b = bt.createNode();
  base.commit();
  const p1 = new LoroDoc();
  p1.setPeerId(1n);
  p1.import(base.export({ mode: "update" }));
  const p2 = new LoroDoc();
  p2.setPeerId(2n);
  p2.import(base.export({ mode: "update" }));
  p1.getTree("tr").move(a.id, b.id);
  p1.commit();
  p2.getTree("tr").move(b.id, a.id);
  p2.commit();
  p1.import(p2.export({ mode: "update" }));
  p1.getTree("tr").move(a.id, undefined);
  p1.commit();
  const update = p1.export({ mode: "update" });
  writeFileSync(join(outDir, "tree_rejected_stays.update.bin"), Buffer.from(update));
  writeFileSync(join(outDir, "tree_rejected_stays.snapshot.bin"), Buffer.from(p1.export({ mode: "snapshot" })));
  writeFileSync(join(outDir, "tree_rejected_stays.json"), JSON.stringify(p1.toJSON(), null, 2) + "\n");
  console.log(`tree_rejected_stays: update=${update.length}B`);
}

// Sibling order on equal fractional indices. Peers 2 and 10 each create a child
// of the same parent at the same position without seeing each other, so both
// children get the same index. The tie is broken by the numeric (lamport, peer)
// of the move that placed them, not by the id string, where "0@10" < "0@2".
{
  const base = new LoroDoc();
  base.setPeerId(1n);
  const root = base.getTree("tr").createNode();
  base.commit();
  const p2 = new LoroDoc();
  p2.setPeerId(2n);
  p2.import(base.export({ mode: "update" }));
  const p10 = new LoroDoc();
  p10.setPeerId(10n);
  p10.import(base.export({ mode: "update" }));
  p2.getTree("tr").createNode(root.id, 0);
  p2.commit();
  p10.getTree("tr").createNode(root.id, 0);
  p10.commit();
  p2.import(p10.export({ mode: "update" }));
  const update = p2.export({ mode: "update" });
  writeFileSync(join(outDir, "tree_sibling_tie.update.bin"), Buffer.from(update));
  writeFileSync(join(outDir, "tree_sibling_tie.snapshot.bin"), Buffer.from(p2.export({ mode: "snapshot" })));
  writeFileSync(join(outDir, "tree_sibling_tie.json"), JSON.stringify(p2.toJSON(), null, 2) + "\n");
  console.log(`tree_sibling_tie: update=${update.length}B merged=${JSON.stringify(p2.toJSON()).slice(0, 160)}`);
}

// Delete against a concurrent move of the same node. A delete is a move too,
// so whichever sorts later in (lamport, peer) order decides where A ends up.
{
  const base = new LoroDoc();
  base.setPeerId(1n);
  const bt = base.getTree("tr");
  const a = bt.createNode();
  const b = bt.createNode();
  base.commit();
  const p1 = new LoroDoc();
  p1.setPeerId(1n);
  p1.import(base.export({ mode: "update" }));
  const p2 = new LoroDoc();
  p2.setPeerId(2n);
  p2.import(base.export({ mode: "update" }));
  p1.getTree("tr").delete(a.id);
  p1.commit();
  p2.getTree("tr").move(a.id, b.id);
  p2.commit();
  p1.import(p2.export({ mode: "update" }));
  const update = p1.export({ mode: "update" });
  writeFileSync(join(outDir, "tree_delete_vs_move.update.bin"), Buffer.from(update));
  writeFileSync(join(outDir, "tree_delete_vs_move.snapshot.bin"), Buffer.from(p1.export({ mode: "snapshot" })));
  writeFileSync(join(outDir, "tree_delete_vs_move.json"), JSON.stringify(p1.toJSON(), null, 2) + "\n");
  console.log(`tree_delete_vs_move: update=${update.length}B merged=${JSON.stringify(p1.toJSON()).slice(0, 160)}`);
}

// Creation and moves inside one change, so every op shares the change's start
// lamport plus its own offset.
emit("tree_one_change", (doc) => {
  const t = doc.getTree("tr");
  const a = t.createNode();
  const b = t.createNode();
  const c = t.createNode();
  t.move(c.id, a.id);
  t.move(b.id, c.id);
});

// emitDoc writes the same four files as emit for a document built elsewhere,
// for histories that need more than one peer and more than one round of sync.
function emitDoc(name, doc) {
  const update = doc.export({ mode: "update" });
  const snapshot = doc.export({ mode: "snapshot" });
  writeFileSync(join(outDir, `${name}.update.bin`), Buffer.from(update));
  writeFileSync(join(outDir, `${name}.snapshot.bin`), Buffer.from(snapshot));
  writeFileSync(join(outDir, `${name}.json`), JSON.stringify(doc.toJSON(), null, 2) + "\n");
  writeFileSync(
    join(outDir, `${name}.ops.json`),
    JSON.stringify(doc.exportJsonUpdates(), (_k, v) => (typeof v === "bigint" ? v.toString() : v), 2) + "\n",
  );
  console.log(`${name}: update=${update.length}B merged=${JSON.stringify(doc.toJSON()).slice(0, 140)}`);
}

// Edits made after a sync. loro cannot fold them into the peer's earlier change
// because they depend on the other peer's ops, so each peer's block carries
// several changes and the lamport column in the block header is actually read.
// Every other fixture edits before merging, which is why a decoder that read
// that column one change off went unnoticed. Here it decides the result: map key
// "k" is written three times, and only the true lamports make "a2" the winner.
{
  const a = new LoroDoc();
  a.setPeerId(1n);
  const b = new LoroDoc();
  b.setPeerId(2n);
  a.getMap("m").set("k", "a1");
  a.getText("t").insert(0, "A");
  a.getList("l").insert(0, "a");
  const tr = a.getTree("tr");
  const n1 = tr.createNode();
  const n2 = tr.createNode();
  a.commit();
  b.import(a.export({ mode: "update" }));
  b.getMap("m").set("k", "b1");
  b.getText("t").insert(1, "B");
  b.getList("l").insert(1, "b");
  b.getTree("tr").move(n2.id, n1.id);
  b.commit();
  a.import(b.export({ mode: "update" }));
  a.getMap("m").set("k", "a2");
  a.getText("t").insert(0, "C");
  a.getList("l").insert(0, "c");
  a.getTree("tr").move(n2.id, undefined);
  a.commit();
  b.import(a.export({ mode: "update" }));
  b.getMap("m").set("k2", "b2");
  b.getText("t").insert(0, "D");
  b.commit();
  a.import(b.export({ mode: "update" }));
  emitDoc("post_merge_edits", a);
}

// Inserts placed after a delete. A position counts only what its author could
// see, so a delete the author had seen shifts it and a delete it had not seen
// must not. Every earlier fixture deleted last, which let a merge that never
// applied deletes to the author's view pass all of them.
{
  // One peer deletes "bc", then types "X" after "e": "adeXf".
  const doc = new LoroDoc();
  doc.setPeerId(1n);
  doc.getText("t").insert(0, "abcdef");
  doc.getList("l").insert(0, "a");
  doc.getList("l").insert(1, "b");
  doc.getList("l").insert(2, "c");
  doc.getList("l").insert(3, "d");
  doc.commit();
  doc.getText("t").delete(1, 2);
  doc.getList("l").delete(1, 2);
  doc.commit();
  doc.getText("t").insert(3, "X");
  doc.getList("l").insert(2, "X");
  doc.commit();
  emitDoc("insert_after_delete", doc);
}
{
  // Peer 2 deletes "bc"; peer 1 imports that and types "X" after "e": "adeXf".
  const a = new LoroDoc();
  a.setPeerId(1n);
  const b = new LoroDoc();
  b.setPeerId(2n);
  a.getText("t").insert(0, "abcdef");
  a.getList("l").insert(0, "a");
  a.getList("l").insert(1, "b");
  a.getList("l").insert(2, "c");
  a.getList("l").insert(3, "d");
  a.commit();
  b.import(a.export({ mode: "update" }));
  b.getText("t").delete(1, 2);
  b.getList("l").delete(1, 2);
  b.commit();
  a.import(b.export({ mode: "update" }));
  a.getText("t").insert(3, "X");
  a.getList("l").insert(2, "X");
  a.commit();
  b.import(a.export({ mode: "update" }));
  emitDoc("insert_after_foreign_delete", b);
}
{
  // Peer 2 deletes "bc" while peer 1, not yet aware of it, types "X" between
  // "c" and "d". X belongs after the deleted pair: "aXdef".
  const a = new LoroDoc();
  a.setPeerId(1n);
  const b = new LoroDoc();
  b.setPeerId(2n);
  a.getText("t").insert(0, "abcdef");
  a.getList("l").insert(0, "a");
  a.getList("l").insert(1, "b");
  a.getList("l").insert(2, "c");
  a.getList("l").insert(3, "d");
  a.commit();
  b.import(a.export({ mode: "update" }));
  b.getText("t").delete(1, 2);
  b.getList("l").delete(1, 2);
  b.commit();
  a.getText("t").insert(3, "X");
  a.getList("l").insert(3, "X");
  a.commit();
  a.import(b.export({ mode: "update" }));
  b.import(a.export({ mode: "update" }));
  emitDoc("insert_concurrent_with_delete", a);
}

// Two backspaces in a row. loro folds them into one delete whose span starts at
// the LOWEST id it removes and runs backwards: start 1@peer, length -2, removing
// "b" and "c". Reading the span downwards from its start removes "a" and "b".
{
  const doc = new LoroDoc();
  doc.setPeerId(1n);
  doc.getText("t").insert(0, "abcd");
  for (const [i, v] of ["a", "b", "c", "d"].entries()) doc.getList("l").insert(i, v);
  doc.commit();
  doc.getText("t").delete(2, 1);
  doc.commit();
  doc.getText("t").delete(1, 1);
  doc.commit();
  doc.getList("l").delete(2, 1);
  doc.commit();
  doc.getList("l").delete(1, 1);
  doc.commit();
  emitDoc("delete_backwards", doc);
}

// One delete op whose atoms the other peer saw only in part. Peer 1 deletes
// "b", peer 2 imports that, peer 1 deletes "c", and loro folds both deletes
// into one op. Peer 2 types "X" after "c" having seen only the first atom, so a
// merge that treats the whole op as seen puts "X" after "d".
{
  const a = new LoroDoc();
  a.setPeerId(1n);
  const b = new LoroDoc();
  b.setPeerId(2n);
  a.getText("t").insert(0, "abcd");
  a.commit();
  a.getText("t").delete(1, 1);
  a.commit();
  b.import(a.export({ mode: "update" }));
  a.getText("t").delete(1, 1);
  a.commit();
  b.getText("t").insert(2, "X");
  b.commit();
  a.import(b.export({ mode: "update" }));
  b.import(a.export({ mode: "update" }));
  emitDoc("delete_split_by_dep", a);
}

// Inserts after a mark. A mark adds two anchors to the text's coordinate
// space, so typing at visible position 4 after a mark over [0,3) is recorded
// as position 6. A merge that leaves the anchors out loses the neighbour.
{
  const doc = new LoroDoc();
  doc.setPeerId(1n);
  const t = doc.getText("t");
  t.insert(0, "hello");
  doc.commit();
  t.mark({ start: 0, end: 3 }, "bold", true);
  doc.commit();
  t.insert(4, "X");
  doc.commit();
  t.insert(1, "Y");
  doc.commit();
  emitDoc("insert_after_mark", doc);
}
{
  const a = new LoroDoc();
  a.setPeerId(1n);
  const b = new LoroDoc();
  b.setPeerId(2n);
  a.getText("t").insert(0, "hello");
  a.commit();
  b.import(a.export({ mode: "update" }));
  b.getText("t").mark({ start: 0, end: 3 }, "bold", true);
  b.commit();
  a.import(b.export({ mode: "update" }));
  a.getText("t").insert(4, "X");
  a.commit();
  b.import(a.export({ mode: "update" }));
  emitDoc("insert_after_foreign_mark", a);
}

// The same backwards delete in two overlapping exports. Two backspaces, an
// export, two more backspaces: loro folds all four into one delete op, so the
// later export repeats the first two atoms inside a longer op. Merging both
// has to slice that op down to its new atoms, or apply nothing twice.
{
  const doc = new LoroDoc();
  doc.setPeerId(4n);
  doc.getText("t").insert(0, "abcdef");
  doc.commit();
  doc.getText("t").delete(5, 1);
  doc.getText("t").delete(4, 1);
  doc.commit();
  const early = doc.export({ mode: "update" });
  doc.getText("t").delete(3, 1);
  doc.getText("t").delete(2, 1);
  doc.commit();
  const late = doc.export({ mode: "update" });
  writeFileSync(join(outDir, "span_delete_overlap.early.bin"), Buffer.from(early));
  writeFileSync(join(outDir, "span_delete_overlap.late.bin"), Buffer.from(late));
  writeFileSync(join(outDir, "span_delete_overlap.json"), JSON.stringify(doc.toJSON(), null, 2) + "\n");
  const ops = (b) => { const d = new LoroDoc(); d.import(b); return d.exportJsonUpdates().changes.flatMap((c) => c.ops.map((o) => `${o.counter}:${JSON.stringify(o.content)}`)); };
  console.log(`span_delete_overlap: early=${ops(early).join(" ")} late=${ops(late).join(" ")}`);
}

// A full export alongside a delta from the middle of it. "ab", a saved
// version, "cd", a delta from that version, then "ef" and a full export. loro
// folds the whole history into one change, so after deduplication the full
// copy keeps only "ab" and "ef", with "cd" coming from the delta. "ef" was
// typed after "cd" and can only be placed once "cd" is in.
{
  const doc = new LoroDoc();
  doc.setPeerId(5n);
  doc.getText("t").insert(0, "ab");
  doc.commit();
  const v = doc.version();
  doc.getText("t").insert(2, "cd");
  doc.commit();
  const delta = doc.export({ mode: "update", from: v });
  doc.getText("t").insert(4, "ef");
  doc.commit();
  const full = doc.export({ mode: "update" });
  writeFileSync(join(outDir, "span_gap.delta.bin"), Buffer.from(delta));
  writeFileSync(join(outDir, "span_gap.full.bin"), Buffer.from(full));
  writeFileSync(join(outDir, "span_gap.json"), JSON.stringify(doc.toJSON(), null, 2) + "\n");
  console.log(`span_gap: delta=${delta.length}B full=${full.length}B merged=${JSON.stringify(doc.toJSON())}`);
}

// Acceptance corpora for the sequence merge. Random histories with loro-crdt's
// own toJSON as the answer: inserts anywhere (astral and other multi-byte
// characters included), deletes forwards and backwards (backspace runs that
// loro folds into one op), marks over text, and list edits. The single-peer
// corpus has one author; in the concurrent one, two or three peers edit their
// own replicas and now and then import another's, so edits land after syncs
// and alongside each other.
{
  const rng = (seed) => { let s = seed >>> 0; return () => (s = (s * 1664525 + 1013904223) >>> 0) / 4294967296; };
  const alphabet = ["a", "b", "c", "d", "e", "😀", "é", "中"];
  const points = (s) => Array.from(s);
  // The JS API counts positions in UTF-16 units; pick them on character boundaries.
  const u16 = (s, k) => points(s).slice(0, k).join("").length;
  const editOnce = (doc, r) => {
    const t = doc.getText("t");
    const l = doc.getList("l");
    const s = t.toString();
    const n = points(s).length;
    const pick = r();
    if (pick < 0.4 || n === 0) {
      const k = Math.floor(r() * (n + 1));
      let ins = "";
      for (let j = 1 + Math.floor(r() * 3); j > 0; j--) ins += alphabet[Math.floor(r() * alphabet.length)];
      t.insert(u16(s, k), ins);
    } else if (pick < 0.6) {
      const k = Math.floor(r() * n);
      const m = 1 + Math.floor(r() * Math.min(3, n - k));
      t.delete(u16(s, k), u16(s, k + m) - u16(s, k));
    } else if (pick < 0.72) {
      // A backspace run: one character at a time, leftwards.
      let k = 1 + Math.floor(r() * n);
      for (let j = 1 + Math.floor(r() * 3); j > 0 && k > 0; j--, k--) {
        const cur = t.toString();
        t.delete(u16(cur, k - 1), u16(cur, k) - u16(cur, k - 1));
      }
    } else if (pick < 0.82) {
      const a = Math.floor(r() * n);
      const b = a + 1 + Math.floor(r() * (n - a));
      t.mark({ start: u16(s, a), end: u16(s, b) }, r() < 0.5 ? "bold" : "italic", true);
    } else if (pick < 0.92 || l.length === 0) {
      l.insert(Math.floor(r() * (l.length + 1)), Math.floor(r() * 100));
    } else {
      const k = Math.floor(r() * l.length);
      l.delete(k, 1 + Math.floor(r() * Math.min(2, l.length - k)));
    }
  };
  // The answer is what a replay of the exported bytes shows, which is what a
  // reader of those bytes sees. The writing replica can differ: it lists a
  // root container it merely opened, as "l": [], where a replay has nothing.
  // delta is the replay's toDelta() for the text, styles included, which
  // toJSON leaves out.
  const record = (seed, doc) => {
    const update = doc.export({ mode: "update" });
    const replay = new LoroDoc();
    replay.import(update);
    const expected = replay.toJSON();
    const delta = "t" in expected ? replay.getText("t").toDelta() : undefined;
    return { seed, update: Buffer.from(update).toString("base64"), expected, delta };
  };

  const single = [];
  for (let seed = 1; seed <= 300; seed++) {
    const r = rng(seed * 7919);
    const doc = new LoroDoc();
    doc.setPeerId(1n);
    for (let i = 3 + Math.floor(r() * 20); i > 0; i--) {
      editOnce(doc, r);
      if (r() < 0.5) doc.commit();
    }
    doc.commit();
    single.push(record(seed, doc));
  }
  writeFileSync(join(outDir, "seq_single_corpus.json"), JSON.stringify(single, null, 1) + "\n");
  console.log(`seq_single_corpus: ${single.length} histories`);

  const concurrent = [];
  for (let seed = 1; seed <= 300; seed++) {
    const r = rng(seed * 104729);
    const peers = [];
    for (let p = 0; p < 2 + (seed % 2); p++) {
      const doc = new LoroDoc();
      doc.setPeerId(BigInt(p + 1));
      peers.push(doc);
    }
    for (let i = 4 + Math.floor(r() * 24); i > 0; i--) {
      const doc = peers[Math.floor(r() * peers.length)];
      if (r() < 0.25) {
        const other = peers[Math.floor(r() * peers.length)];
        if (other !== doc) doc.import(other.export({ mode: "update" }));
        continue;
      }
      editOnce(doc, r);
      doc.commit();
    }
    const all = new LoroDoc();
    for (const doc of peers) all.import(doc.export({ mode: "update" }));
    concurrent.push(record(seed, all));
  }
  writeFileSync(join(outDir, "seq_concurrent_corpus.json"), JSON.stringify(concurrent, null, 1) + "\n");
  console.log(`seq_concurrent_corpus: ${concurrent.length} histories`);
}

// Rich-text corpus. Marks of keys with each of loro's four expand rules (bold
// "after", link "none", cmt "before", hl "both"), valued true, a string, an
// object or an array, and unmarks; inserts that land on a run's edge half the
// time, where the expand rule decides which side of the anchor they go;
// deletes up to the whole text, then typing into what is left. One to three
// peers, syncing now and then. The answer is a replay's toJSON and toDelta.
{
  const rng = (seed) => { let s = seed >>> 0; return () => (s = (s * 1664525 + 1013904223) >>> 0) / 4294967296; };
  const points = (s) => Array.from(s);
  const u16 = (s, k) => points(s).slice(0, k).join("").length;
  const styles = { bold: { expand: "after" }, link: { expand: "none" }, cmt: { expand: "before" }, hl: { expand: "both" } };
  const keys = Object.keys(styles);
  const values = [true, "red", "blue", { k: 1 }, [1, 2]];
  const alphabet = ["a", "b", "c", "d", "😀", "中"];
  const edges = (t) => {
    const out = [0];
    for (const run of t.toDelta()) out.push(out[out.length - 1] + points(run.insert).length);
    return out;
  };
  const edit = (doc, r) => {
    const t = doc.getText("t");
    const s = t.toString();
    const n = points(s).length;
    const pick = r();
    if (pick < 0.4 || n === 0) {
      const e = edges(t);
      const k = r() < 0.5 ? e[Math.floor(r() * e.length)] : Math.floor(r() * (n + 1));
      let ins = "";
      for (let j = 1 + Math.floor(r() * 3); j > 0; j--) ins += alphabet[Math.floor(r() * alphabet.length)];
      t.insert(u16(s, k), ins);
    } else if (pick < 0.55) {
      const k = Math.floor(r() * n);
      const m = 1 + Math.floor(r() * Math.min(3, n - k));
      t.delete(u16(s, k), u16(s, k + m) - u16(s, k));
    } else if (pick < 0.6) {
      t.delete(0, s.length);
    } else {
      const a = Math.floor(r() * n);
      const b = a + 1 + Math.floor(r() * (n - a));
      const key = keys[Math.floor(r() * keys.length)];
      if (pick < 0.88) t.mark({ start: u16(s, a), end: u16(s, b) }, key, values[Math.floor(r() * values.length)]);
      else t.unmark({ start: u16(s, a), end: u16(s, b) }, key);
    }
  };
  const rich = [];
  for (let seed = 1; seed <= 300; seed++) {
    const r = rng(seed * 15485867);
    const peers = [];
    for (let p = 0; p < 1 + (seed % 3); p++) {
      const doc = new LoroDoc();
      doc.setPeerId(BigInt(p + 1));
      doc.configTextStyle(styles);
      peers.push(doc);
    }
    for (let i = 4 + Math.floor(r() * 24); i > 0; i--) {
      const doc = peers[Math.floor(r() * peers.length)];
      if (peers.length > 1 && r() < 0.25) {
        const other = peers[Math.floor(r() * peers.length)];
        if (other !== doc) doc.import(other.export({ mode: "update" }));
        continue;
      }
      edit(doc, r);
      doc.commit();
    }
    const all = new LoroDoc();
    for (const doc of peers) all.import(doc.export({ mode: "update" }));
    const update = all.export({ mode: "update" });
    const replay = new LoroDoc();
    replay.import(update);
    const expected = replay.toJSON();
    const delta = "t" in expected ? replay.getText("t").toDelta() : undefined;
    rich.push({ seed, update: Buffer.from(update).toString("base64"), expected, delta });
  }
  writeFileSync(join(outDir, "rich_corpus.json"), JSON.stringify(rich, null, 1) + "\n");
  console.log(`rich_corpus: ${rich.length} histories, ${rich.filter((c) => c.delta?.some((d) => d.attributes)).length} styled`);
}

// A backwards delete another peer saw only in part. Peer 1 backspaces over "c"
// and then "b" of "abcd", which loro folds into one backwards op whose first
// atom removed "c". Peer 3 had put "Y" between "b" and "c" concurrently. Peer 2
// sees the first backspace and "Y", so its view is "abYd", and types "X" at 2,
// after "b". A merge that reads the first atom as removing "b" (the lowest id)
// sees "aYcd" instead and puts "X" after "Y".
{
  const a = new LoroDoc();
  a.setPeerId(1n);
  const b = new LoroDoc();
  b.setPeerId(2n);
  const c = new LoroDoc();
  c.setPeerId(3n);
  a.getText("t").insert(0, "abcd");
  a.commit();
  b.import(a.export({ mode: "update" }));
  c.import(a.export({ mode: "update" }));
  c.getText("t").insert(2, "Y");
  c.commit();
  b.import(c.export({ mode: "update" }));
  a.getText("t").delete(2, 1);
  a.commit();
  b.import(a.export({ mode: "update" }));
  a.getText("t").delete(1, 1);
  a.commit();
  b.getText("t").insert(2, "X");
  b.commit();
  for (const p of [b, c]) a.import(p.export({ mode: "update" }));
  emitDoc("delete_backwards_split_by_dep", a);
  const dels = a.exportJsonUpdates().changes.flatMap((ch) => ch.ops.map((o) => `${ch.id}/${o.counter}:${JSON.stringify(o.content)}`)).filter((x) => x.includes("delete"));
  console.log(`delete_backwards_split_by_dep deletes: ${dels.join(" ")}`);
}

// Acceptance corpora for MovableList and Tree: random histories with
// loro-crdt's own replay as the answer. A MovableList history inserts,
// deletes, moves and sets values; a Tree history creates nodes under random
// parents at random indexes, moves them, deletes them and writes their meta
// maps. A step loro refuses (a move under the node's own descendant, an index
// out of range) is skipped. The concurrent corpora spread the steps over two or
// three peers that now and then import one another's updates.
{
  const rng = (seed) => { let s = seed >>> 0; return () => (s = (s * 1664525 + 1013904223) >>> 0) / 4294967296; };
  const pickOf = (r, xs) => xs[Math.floor(r() * xs.length)];
  const editList = (doc, r) => {
    const l = doc.getMovableList("ml");
    const n = l.length;
    const pick = r();
    if (pick < 0.35 || n === 0) l.insert(Math.floor(r() * (n + 1)), Math.floor(r() * 100));
    else if (pick < 0.55) {
      const k = Math.floor(r() * n);
      l.delete(k, 1 + Math.floor(r() * Math.min(2, n - k)));
    } else if (pick < 0.8) l.move(Math.floor(r() * n), Math.floor(r() * n));
    else l.set(Math.floor(r() * n), 100 + Math.floor(r() * 100));
  };
  const editTree = (doc, r) => {
    const t = doc.getTree("tr");
    const alive = t.nodes().filter((n) => !n.isDeleted());
    const pick = r();
    try {
      if (pick < 0.35 || alive.length === 0) {
        const parent = alive.length > 0 && r() < 0.6 ? pickOf(r, alive) : undefined;
        const count = parent ? parent.children()?.length ?? 0 : t.roots().length;
        const index = Math.floor(r() * (count + 1));
        if (parent) parent.createNode(index);
        else t.createNode(undefined, index);
      } else if (pick < 0.65) {
        const node = pickOf(r, alive);
        const parent = r() < 0.3 ? undefined : pickOf(r, alive);
        const count = parent ? parent.children()?.length ?? 0 : t.roots().length;
        node.move(parent, Math.floor(r() * (count + 1)));
      } else if (pick < 0.8) t.delete(pickOf(r, alive).id);
      else pickOf(r, alive).data.set("k" + Math.floor(r() * 3), Math.floor(r() * 100));
    } catch {
      // loro refused the step: a cycle, or an index that no longer exists.
    }
  };
  const record = (seed, doc) => {
    const update = doc.export({ mode: "update" });
    const replay = new LoroDoc();
    replay.import(update);
    return { seed, update: Buffer.from(update).toString("base64"), expected: replay.toJSON() };
  };
  const corpus = (name, edit, peersFor, salt) => {
    const out = [];
    for (let seed = 1; seed <= 300; seed++) {
      const r = rng(seed * salt);
      const peers = [];
      for (let p = 0; p < peersFor(seed); p++) {
        const doc = new LoroDoc();
        doc.setPeerId(BigInt(p + 1));
        peers.push(doc);
      }
      for (let i = 4 + Math.floor(r() * 24); i > 0; i--) {
        const doc = pickOf(r, peers);
        if (peers.length > 1 && r() < 0.25) {
          const other = pickOf(r, peers);
          if (other !== doc) doc.import(other.export({ mode: "update" }));
          continue;
        }
        edit(doc, r);
        doc.commit();
      }
      const all = new LoroDoc();
      for (const doc of peers) all.import(doc.export({ mode: "update" }));
      out.push(record(seed, all));
    }
    writeFileSync(join(outDir, name), JSON.stringify(out, null, 1) + "\n");
    console.log(`${name}: ${out.length} histories`);
  };
  corpus("movable_single_corpus.json", editList, () => 1, 15485863);
  corpus("movable_concurrent_corpus.json", editList, (seed) => 2 + (seed % 2), 32452843);
  corpus("tree_corpus.json", editTree, (seed) => 1 + (seed % 3), 49979687);
}

// Containers stored as values in other containers. A child container's id is
// the id of the atom that placed it: the map write, the k-th item of a list
// insert, or the MovableList set, and its own ops name it by that id. The
// parent's value is a container kind with no id of its own, so a reader that
// does not look the child up shows the bare kind code.
emit("nested_note", (doc) => {
  const note = doc.getMap("note");
  note.set("title", "Groceries");
  const items = note.setContainer("items", new LoroList());
  items.insert(0, "milk");
  items.insert(1, "eggs");
  note.setContainer("body", new LoroText()).insert(0, "milk, eggs");
});

// A plain item and a Map inserted in a row are one insert op, so the Map is
// its second item and takes the op's id plus one.
emit("nested_in_list", (doc) => {
  const l = doc.getList("l");
  l.insert(0, "a");
  const inner = l.insertContainer(1, new LoroMap());
  inner.set("k", 1);
  l.insertContainer(2, new LoroText()).insert(0, "t");
  inner.setContainer("deep", new LoroList()).insert(0, 7);
});

// A MovableList element's container is named by the op that wrote the value
// it shows: the insert, or a later set that put another container there. The
// counter the set replaced keeps its ops and is no longer shown.
emit("nested_in_movable", (doc) => {
  const ml = doc.getMovableList("ml");
  ml.insertContainer(0, new LoroCounter()).increment(3);
  ml.insert(1, "plain");
  ml.insertContainer(2, new LoroMap()).set("k", "v");
  doc.commit();
  ml.setContainer(0, new LoroText()).insert(0, "x");
  ml.move(0, 2);
});

// Children created and never written to show their kind's empty value.
emit("nested_empty_children", (doc) => {
  const e = doc.getMap("e");
  e.setContainer("map", new LoroMap());
  e.setContainer("list", new LoroList());
  e.setContainer("text", new LoroText());
  e.setContainer("tree", new LoroTree());
  e.setContainer("ml", new LoroMovableList());
  e.setContainer("counter", new LoroCounter());
});

// Four levels: a map in a list in a MovableList in a map, edited at each one.
emit("nested_deep", (doc) => {
  const root = doc.getMap("m");
  root.set("n", 0);
  const ml = root.setContainer("ml", new LoroMovableList());
  ml.insert(0, 1);
  const l = ml.insertContainer(1, new LoroList());
  l.insert(0, "x");
  const m = l.insertContainer(1, new LoroMap());
  m.set("k", true);
  m.setContainer("t", new LoroText()).insert(0, "deep");
  doc.commit();
  ml.move(1, 0);
});

// Containers no longer reachable: a key overwritten with a scalar while
// another peer goes on writing into the container it held, a key overwritten
// with another container, a key deleted, a list item deleted, a MovableList
// element set to a scalar. None of them may show anywhere.
{
  const a = new LoroDoc();
  a.setPeerId(1n);
  const b = new LoroDoc();
  b.setPeerId(2n);
  const m = a.getMap("m");
  m.setContainer("a", new LoroText()).insert(0, "old");
  const replaced = m.setContainer("c", new LoroMap());
  replaced.set("x", 1);
  a.commit();
  b.import(a.export({ mode: "update" }));
  m.set("a", 1);
  m.setContainer("c", new LoroCounter()).increment(2);
  m.setContainer("b", new LoroList()).insert(0, 1);
  m.delete("b");
  const l = a.getList("l");
  l.insertContainer(0, new LoroMap()).set("x", 1);
  l.insert(1, "kept");
  l.delete(0, 1);
  const ml = a.getMovableList("ml");
  ml.insertContainer(0, new LoroText()).insert(0, "gone");
  ml.set(0, 5);
  a.commit();
  b.getMap("m").get("a").insert(3, "er");
  b.getMap("m").get("c").set("y", 2);
  b.commit();
  a.import(b.export({ mode: "update" }));
  emitDoc("nested_overwritten", a);
}

// Two peers put a container under one key concurrently: the later (lamport,
// peer) write wins and the other container, edits and all, is not shown. The
// two Maps they each insert into the list are both kept.
emitMerged(
  "nested_concurrent_set",
  (d) => {
    d.getMap("m").setContainer("k", new LoroText()).insert(0, "A");
    d.getList("l").insertContainer(0, new LoroMap()).set("who", "A");
  },
  (d) => {
    d.getMap("m").setContainer("k", new LoroList()).insert(0, "B");
    d.getList("l").insertContainer(0, new LoroMap()).set("who", "B");
  },
);

// Tree node meta maps holding containers, in a root tree and in a tree that is
// itself a map value.
emit("nested_tree_meta", (doc) => {
  const tr = doc.getTree("tr");
  const a = tr.createNode();
  a.data.set("name", "a");
  a.data.setContainer("notes", new LoroText()).insert(0, "note");
  const b = tr.createNode(a.id);
  b.data.setContainer("tags", new LoroList()).insert(0, "x");
  const inner = doc.getMap("m").setContainer("tree", new LoroTree());
  const n = inner.createNode();
  n.data.setContainer("c", new LoroCounter()).increment(4);
  inner.createNode(n.id);
});

// Mergeable children (loro 1.13 and later). The child is a root container
// named after its path, "🤝:$m>body" here, and the parent map holds a binary
// marker at the key. toJSON shows the child under the key and not at the top.
emit("mergeable_text", (doc) => {
  doc.getMap("m").set("title", "t");
  doc.getMap("m").ensureMergeableText("body").insert(0, "hi");
});

// Two peers that each ensure a mergeable child under one key, without seeing
// each other, write into the same container, where a setContainer would have
// given each its own and kept one.
{
  const a = new LoroDoc();
  a.setPeerId(1n);
  const b = new LoroDoc();
  b.setPeerId(2n);
  a.getMap("m").ensureMergeableText("body").insert(0, "hi ");
  a.getMap("m").ensureMergeableList("items").insert(0, "a");
  a.commit();
  b.getMap("m").ensureMergeableText("body").insert(0, "yo ");
  b.getMap("m").ensureMergeableList("items").insert(0, "b");
  b.getMap("m").ensureMergeableCounter("n").increment(1);
  b.commit();
  a.getMap("m").ensureMergeableCounter("n").increment(2);
  a.commit();
  a.import(b.export({ mode: "update" }));
  b.import(a.export({ mode: "update" }));
  emitDoc("mergeable_concurrent", a);
}

// Mergeable paths: a mergeable map's own mergeable child extends its path, a
// nested map's starts from its id in base 36 ("@peer:counter"), a tree node's
// meta map is such a nested map, and keys and root names escape '>', '/', '\'
// and NUL.
emit("mergeable_nested", (doc) => {
  const sub = doc.getMap("m").ensureMergeableMap("sub");
  sub.set("x", 1);
  sub.ensureMergeableText("deep>/\\").insert(0, "z");
  doc.getMap("m").setContainer("inner", new LoroMap()).ensureMergeableList("li").insert(0, 5);
  const node = doc.getTree("tr").createNode();
  node.data.ensureMergeableText("mt").insert(0, "y");
  doc.getMap("we>i\\rd").ensureMergeableCounter("k\0").increment(2);
  doc.getMap("we>i\\rd").ensureMergeableMovableList("ml").insert(0, "m");
});

// Mergeable children ensured and never written to show their kind's empty value.
emit("mergeable_empty", (doc) => {
  const m = doc.getMap("m");
  m.ensureMergeableMap("map");
  m.ensureMergeableList("list");
  m.ensureMergeableText("text");
  m.ensureMergeableTree("tree");
  m.ensureMergeableMovableList("ml");
  m.ensureMergeableCounter("counter");
});

// The marker decides what shows. Ensuring another kind under a key replaces
// the marker, and the first child is hidden with its state kept; deleting the
// key hides the child; ensuring it again shows the child as it was.
emit("mergeable_marker", (doc) => {
  const m = doc.getMap("m");
  m.ensureMergeableText("x").insert(0, "text");
  m.ensureMergeableList("x").insert(0, 1);
  m.ensureMergeableText("y").insert(0, "gone");
  m.delete("y");
  m.ensureMergeableText("z").insert(0, "back");
  m.delete("z");
  doc.commit();
  m.ensureMergeableText("z");
});
