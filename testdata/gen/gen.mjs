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
