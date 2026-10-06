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
  const record = (seed, doc) => {
    const update = doc.export({ mode: "update" });
    const replay = new LoroDoc();
    replay.import(update);
    return { seed, update: Buffer.from(update).toString("base64"), expected: replay.toJSON() };
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
