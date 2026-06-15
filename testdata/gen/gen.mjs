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
emit("richtext", (doc) => {
  const t = doc.getText("rt");
  t.insert(0, "hello");
  t.mark({ start: 0, end: 3 }, "bold", true);
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
