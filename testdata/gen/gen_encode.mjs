// Acceptance fixtures for loro-go's EncodeUpdates: exports that use every
// kind of op and every field the Fast format writes, so that decoding an
// export and encoding it again can be held to loro-crdt's own bytes.
//
//   ../fixtures/encode_corpus.json        [{ seed, update, expected }]
//   ../fixtures/encode_in_range.update.bin
//   ../fixtures/encode_block_edge.update.bin
//
// Each corpus history has one to three peers editing a Text (inserts,
// deletes, backspace runs, marks of each expand rule, unmarks), a Map
// (scalars, binary, nested objects and arrays, deletes, child containers), a
// List and a MovableList (inserts, deletes, moves, sets, child containers), a
// Tree (creates, moves, deletes, meta writes) and a Counter (integer and
// fractional steps), and the child containers they create. Commits carry a
// message now and then, and an explicit timestamp now and then, which later
// changes inherit. Peers sync now and then, which gives changes dependencies
// on other peers. Every 25th history is long, 600 to 1500 steps, so one peer's
// changes fill several 4 KiB change blocks. Every 75th is one peer typing at
// the end of the text or pushing onto a list 3000 times with plain commits,
// which loro merges into a few long changes whose op and block boundaries
// come from its in-memory buffers.
//
// Even seeds, and seed 75 of the two typing histories, export the authoring
// document of the first peer after it imported every other peer, so its own
// changes come straight from its memory; the other odd seeds export a fresh
// document that imported all of them.
// expected is what a replay of the export shows.
//
// encode_in_range.update.bin is an export of two ranges of one peer and one
// of another (mode "updates-in-range"). loro writes each peer's k-th range
// into its k-th layer of blocks, so the blocks are not sorted by peer.
//
// encode_block_edge.update.bin is one peer's 820 changes whose estimated
// sizes (Change::estimate_storage_size: 4, plus the text's bytes) add up to
// exactly 4096 over the first 819: "ab" and then "c" 818 times, each commit
// with its own message so that none merge. loro fills a block up to and
// including 4096, so the last change opens a second block.
//
// Everything is seeded, so a rerun writes the same bytes.
import { LoroDoc, LoroText, LoroMap, LoroList, LoroMovableList, LoroCounter } from "loro-crdt/nodejs";
import { writeFileSync, mkdirSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";

const here = dirname(fileURLToPath(import.meta.url));
const outDir = join(here, "..", "fixtures");
mkdirSync(outDir, { recursive: true });

const rng = (seed) => { let s = seed >>> 0; return () => (s = (s * 1664525 + 1013904223) >>> 0) / 4294967296; };
const pickOf = (r, xs) => xs[Math.floor(r() * xs.length)];
const points = (s) => Array.from(s);
// The JS API counts text positions in UTF-16 units; pick them on character boundaries.
const u16 = (s, k) => points(s).slice(0, k).join("").length;
const alphabet = ["a", "b", "c", "d", "😀", "é", "中"];
const word = (r) => { let w = ""; for (let j = 1 + Math.floor(r() * 3); j > 0; j--) w += pickOf(r, alphabet); return w; };
const styles = { bold: { expand: "after" }, link: { expand: "none" }, cmt: { expand: "before" }, hl: { expand: "both" } };
const styleKeys = Object.keys(styles);

const scalar = (r) => {
  const pick = r();
  if (pick < 0.2) return Math.floor(r() * 2000) - 1000;
  if (pick < 0.35) return Math.floor(r() * 1000) / 8;
  if (pick < 0.55) return word(r);
  if (pick < 0.65) return r() < 0.5;
  if (pick < 0.72) return null;
  if (pick < 0.8) return new Uint8Array([Math.floor(r() * 256), Math.floor(r() * 256)]);
  if (pick < 0.92) return [scalar(r), word(r)];
  // An object, of one key mostly. One of two keys or more is the class whose
  // bytes loro-go cannot reproduce, as loro lists its keys in hash order, so
  // it is kept rare enough to leave most histories byte for byte.
  const obj = { [pickOf(r, ["x", "y", "z", "k"])]: scalar(r) };
  if (r() < 0.12) {
    for (let j = 1 + Math.floor(r() * 2); j > 0; j--) obj[pickOf(r, ["w", "id", "name", "tags", "q"])] = Math.floor(r() * 10);
  }
  return obj;
};

const newChild = (r) => pickOf(r, [() => new LoroText(), () => new LoroMap(), () => new LoroList(), () => new LoroMovableList(), () => new LoroCounter()])();

// editChild makes one edit to a container this peer created inside another.
const editChild = (r, c) => {
  if (c instanceof LoroText) c.insert(Math.floor(r() * (c.length + 1)), word(r));
  else if (c instanceof LoroMap) c.set(pickOf(r, ["p", "q", "k"]), scalar(r));
  else if (c instanceof LoroMovableList) {
    if (c.length > 1 && r() < 0.4) c.move(Math.floor(r() * c.length), Math.floor(r() * c.length));
    else c.insert(Math.floor(r() * (c.length + 1)), scalar(r));
  } else if (c instanceof LoroList) c.insert(Math.floor(r() * (c.length + 1)), scalar(r));
  else if (c instanceof LoroCounter) c.increment(Math.floor(r() * 5) + 1);
};

const editText = (doc, r) => {
  const t = doc.getText("t");
  const s = t.toString();
  const n = points(s).length;
  const pick = r();
  if (pick < 0.35 || n === 0) t.insert(u16(s, Math.floor(r() * (n + 1))), word(r));
  else if (pick < 0.5) {
    const k = Math.floor(r() * n);
    const m = 1 + Math.floor(r() * Math.min(3, n - k));
    t.delete(u16(s, k), u16(s, k + m) - u16(s, k));
  } else if (pick < 0.6) {
    let k = 1 + Math.floor(r() * n);
    for (let j = 1 + Math.floor(r() * 3); j > 0 && k > 0; j--, k--) {
      const cur = t.toString();
      t.delete(u16(cur, k - 1), u16(cur, k) - u16(cur, k - 1));
    }
  } else {
    const a = Math.floor(r() * n);
    const b = a + 1 + Math.floor(r() * (n - a));
    const key = pickOf(r, styleKeys);
    if (pick < 0.9) t.mark({ start: u16(s, a), end: u16(s, b) }, key, pickOf(r, [true, "red", 3, { c: 1 }]));
    else t.unmark({ start: u16(s, a), end: u16(s, b) }, key);
  }
};

const editMap = (doc, r, kids) => {
  const m = doc.getMap("m");
  const pick = r();
  const key = pickOf(r, ["a", "b", "c", "d", "e"]);
  if (pick < 0.65) m.set(key, scalar(r));
  else if (pick < 0.8) { if (m.get(key) !== undefined) m.delete(key); else m.set(key, scalar(r)); }
  else kids.push(m.setContainer("kid" + key, newChild(r)));
};

const editList = (doc, r, kids) => {
  const l = doc.getList("l");
  const pick = r();
  if (pick < 0.6 || l.length === 0) l.insert(Math.floor(r() * (l.length + 1)), scalar(r));
  else if (pick < 0.85) {
    const k = Math.floor(r() * l.length);
    l.delete(k, 1 + Math.floor(r() * Math.min(2, l.length - k)));
  } else kids.push(l.insertContainer(Math.floor(r() * (l.length + 1)), newChild(r)));
};

const editMovable = (doc, r, kids) => {
  const l = doc.getMovableList("ml");
  const n = l.length;
  const pick = r();
  if (pick < 0.35 || n === 0) l.insert(Math.floor(r() * (n + 1)), scalar(r));
  else if (pick < 0.5) {
    const k = Math.floor(r() * n);
    l.delete(k, 1 + Math.floor(r() * Math.min(2, n - k)));
  } else if (pick < 0.75) l.move(Math.floor(r() * n), Math.floor(r() * n));
  else if (pick < 0.92) l.set(Math.floor(r() * n), scalar(r));
  else kids.push(l.setContainer(Math.floor(r() * n), newChild(r)));
};

const editTree = (doc, r) => {
  const t = doc.getTree("tr");
  const alive = t.nodes().filter((n) => !n.isDeleted());
  const pick = r();
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
  else pickOf(r, alive).data.set("k" + Math.floor(r() * 3), scalar(r));
};

const editCounter = (doc, r) => {
  const c = doc.getCounter("c");
  if (r() < 0.7) c.increment(Math.floor(r() * 10) - 3);
  else c.decrement(Math.floor(r() * 100) / 16);
};

const edit = (doc, r, kids) => {
  const pick = r();
  // A step loro refuses (a tree move under the node's own descendant, an edit
  // of a child container that was overwritten) is skipped.
  try {
    if (pick < 0.3) editText(doc, r);
    else if (pick < 0.45) editMap(doc, r, kids);
    else if (pick < 0.55) editList(doc, r, kids);
    else if (pick < 0.68) editMovable(doc, r, kids);
    else if (pick < 0.8) editTree(doc, r);
    else if (pick < 0.88) editCounter(doc, r);
    else if (kids.length > 0) editChild(r, pickOf(r, kids));
    else editText(doc, r);
  } catch {
    // skipped
  }
};

// typing and pushing are the two long one-peer patterns: one character at
// the end of the text, or one value into the middle of a list, per commit.
const typing = (doc, r) => { const t = doc.getText("t"); t.insert(t.length, pickOf(r, alphabet)); };
const pushing = (doc, r) => { const l = doc.getList("l"); l.insert(Math.floor(l.length / 2), Math.floor(r() * 100)); };

// commit attaches a message to some commits and an explicit timestamp, in
// seconds and rising through the history, to others.
const commit = (doc, r, clock) => {
  const opts = {};
  const pick = r();
  if (pick < 0.15) opts.message = "edit " + word(r);
  if (pick > 0.85) {
    clock.now += Math.floor(r() * 5000);
    opts.timestamp = clock.now;
  }
  doc.commit(opts);
};

const corpus = [];
for (let seed = 1; seed <= 300; seed++) {
  const r = rng(seed * 104723);
  const long = seed % 25 === 0;
  const pattern = seed % 75 === 0 ? (seed % 150 === 0 ? pushing : typing) : null;
  const nPeers = pattern ? 1 : long ? 1 + (seed % 2) : 1 + (seed % 3);
  // Large peer ids exercise all eight bytes of the peer table.
  const base = seed % 4 === 0 ? 0xfedcba9876543210n : 1n;
  const peers = [];
  for (let p = 0; p < nPeers; p++) {
    const doc = new LoroDoc();
    doc.setPeerId(base + BigInt(p));
    doc.configTextStyle(styles);
    peers.push({ doc, kids: [] });
  }
  const clock = { now: 1_700_000_000 };
  for (let i = pattern ? 3000 : long ? 600 + Math.floor(r() * 900) : 4 + Math.floor(r() * 36); i > 0; i--) {
    const peer = pickOf(r, peers);
    if (peers.length > 1 && r() < 0.15) {
      const other = pickOf(r, peers);
      if (other !== peer) peer.doc.import(other.doc.export({ mode: "update" }));
      continue;
    }
    if (pattern) {
      pattern(peer.doc, r);
      peer.doc.commit();
      continue;
    }
    edit(peer.doc, r, peer.kids);
    if (r() < 0.7) commit(peer.doc, r, clock);
  }
  for (const p of peers) p.doc.commit();
  let out;
  if (seed % 2 === 0 || seed === 75) {
    out = peers[0].doc;
    for (const p of peers.slice(1)) out.import(p.doc.export({ mode: "update" }));
  } else {
    out = new LoroDoc();
    for (const p of peers) out.import(p.doc.export({ mode: "update" }));
  }
  const update = out.export({ mode: "update" });
  const replay = new LoroDoc();
  replay.import(update);
  corpus.push({ seed, update: Buffer.from(update).toString("base64"), expected: replay.toJSON() });
}
writeFileSync(join(outDir, "encode_corpus.json"), JSON.stringify(corpus, null, 1) + "\n");
console.log(`encode_corpus: ${corpus.length} histories`);

{
  const a = new LoroDoc();
  a.setPeerId(1n);
  const b = new LoroDoc();
  b.setPeerId(2n);
  const t = a.getText("t");
  for (let i = 0; i < 20; i++) {
    t.insert(t.length, "x");
    a.commit({ message: "line " + i });
  }
  b.getMap("m").set("k", 1);
  b.commit();
  a.import(b.export({ mode: "update" }));
  const update = a.export({
    mode: "updates-in-range",
    spans: [
      { id: { peer: "1", counter: 0 }, len: 3 },
      { id: { peer: "1", counter: 10 }, len: 2 },
      { id: { peer: "2", counter: 0 }, len: 1 },
    ],
  });
  writeFileSync(join(outDir, "encode_in_range.update.bin"), Buffer.from(update));
  console.log(`encode_in_range: ${update.length}B`);
}

{
  const doc = new LoroDoc();
  doc.setPeerId(1n);
  const t = doc.getText("t");
  t.insert(0, "ab");
  doc.commit({ message: "m0" });
  for (let i = 1; i <= 819; i++) {
    t.insert(t.length, "c");
    doc.commit({ message: "m" + i });
  }
  const update = doc.export({ mode: "update" });
  writeFileSync(join(outDir, "encode_block_edge.update.bin"), Buffer.from(update));
  console.log(`encode_block_edge: ${update.length}B`);
}
