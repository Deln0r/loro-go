// Versions for loro-go's Document: what a sync protocol reads and sends.
// loro-crdt runs random histories and small named scripts and records the
// document's version vector and frontiers, their encodings, and its export
// from many versions, so the Go test can hold Document.Version,
// Document.Frontiers, EncodeFrontiers and ExportUpdatesFrom to them byte for
// byte.
//
//   ../fixtures/versions_corpus.json  320 random histories, 1 to 12 peers
//   ../fixtures/versions_named.json   named scripts, one or more per rule
//
// A corpus history is made by one to twelve peers with peer ids small,
// 32-bit or 64-bit, editing one kind of container (text, list, map, counter,
// movable list, tree, containers nested in containers, rich text marks) or
// all of them, committing with and without a message or a timestamp, and
// importing one another's exports, whole or from their own version. All of
// it is gathered into one document, whose export is the history; a fresh
// document imports it, and that replay is what is recorded:
//
//   { seed, shape, peers, update,          the history, base64
//     vv,                                  oplogVersion().encode()
//     frontiers: [[peer, counter]],        oplogFrontiers(), in its order
//     frontiersBytes,                      encodeFrontiers(oplogFrontiers())
//     versions: [{ kind, entries, setEnd, bytes, redecoded, export }],
//     ranges: [{ spans, export }],
//     blobs }
//
// Each version is a VersionVector the replay exports from:
// export({mode: "update", from}). kind says how it was made: "empty", a
// prefix of one peer ("peer"), prefixes of several peers that cut inside
// changes ("prefixes"), the version of the causal past of a few ids
// ("closed", frontiersToVV), the replay's own version ("full"), that one
// with a peer the history never saw and a counter past the end ("beyond"),
// only peers the history never saw ("unknown"), and counters of 0 or below
// ("nonpositive"). entries are the [peer, counter] pairs in the order the
// version was built; setEnd says it was built by VersionVector.parseJSON
// from entries that are all positive, so that setEnd in the same order
// builds the same map. bytes is vv.encode(), redecoded is
// VersionVector.decode(bytes).encode() where that differs from bytes, and
// export indexes blobs, or is -1 for the history's own bytes.
//
// ranges are exports of id ranges, export({mode: "updates-in-range",
// spans}), each span a [peer, counter, len] triple.
//
// A named script runs peers through edits, commits and imports, as the
// scripts of gen_scripts.mjs do, and records at "version" the peer's
// version and frontiers, edits not yet committed included, and at
// "export_from" its export from a version given by its bytes. Positions are
// in Unicode code points, and the texts are ASCII, so they are UTF-16
// positions too.
//
// Everything is seeded and peer ids are fixed, so a rerun writes the same
// bytes.
import {
  LoroDoc, LoroMap, LoroList, LoroText, LoroTree, LoroMovableList, LoroCounter,
  VersionVector, encodeFrontiers, isContainer,
} from "loro-crdt/nodejs";
import { writeFileSync, mkdirSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";

const here = dirname(fileURLToPath(import.meta.url));
const outDir = join(here, "..", "fixtures");
mkdirSync(outDir, { recursive: true });

const rng = (seed) => { let s = seed >>> 0; return () => (s = (s * 1664525 + 1013904223) >>> 0) / 4294967296; };
const pickOf = (r, xs) => xs[Math.floor(r() * xs.length)];
const b64 = (bytes) => Buffer.from(bytes).toString("base64");
const u32 = (r) => Math.floor(r() * 4294967296);

class Blobs {
  constructor() {
    this.list = [];
    this.index = new Map();
  }
  add(bytes) {
    const s = b64(bytes);
    if (!this.index.has(s)) {
      this.index.set(s, this.list.length);
      this.list.push(s);
    }
    return this.index.get(s);
  }
}

// ---------------------------------------------------------------------------
// Editors, one per shape, after the corpora of gen.mjs.

const alphabet = ["a", "b", "c", "d", "😀", "é", "中", "x"];
const word = (r) => { let w = ""; for (let j = 1 + Math.floor(r() * 3); j > 0; j--) w += pickOf(r, alphabet); return w; };
const points = (s) => Array.from(s);
const u16 = (s, k) => points(s).slice(0, k).join("").length;

function editText(doc, r) {
  const t = doc.getText("t");
  const s = t.toString();
  const n = points(s).length;
  const pick = r();
  if (pick < 0.5 || n === 0) t.insert(u16(s, Math.floor(r() * (n + 1))), word(r));
  else if (pick < 0.7) {
    // Typing, one character after another, which loro folds into one op.
    let k = Math.floor(r() * (n + 1));
    for (let j = 2 + Math.floor(r() * 4); j > 0; j--, k++) t.insert(u16(t.toString(), k), pickOf(r, alphabet));
  } else {
    const k = Math.floor(r() * n);
    const m = 1 + Math.floor(r() * Math.min(3, n - k));
    t.delete(u16(s, k), u16(s, k + m) - u16(s, k));
  }
}

const marks = { bold: { expand: "after" }, link: { expand: "none" }, cmt: { expand: "before" }, hl: { expand: "both" } };
function editMarks(doc, r) {
  const t = doc.getText("rt");
  const s = t.toString();
  const n = points(s).length;
  const pick = r();
  if (pick < 0.45 || n === 0) t.insert(u16(s, Math.floor(r() * (n + 1))), word(r));
  else if (pick < 0.6) {
    const k = Math.floor(r() * n);
    t.delete(u16(s, k), u16(s, k + 1) - u16(s, k));
  } else {
    const a = Math.floor(r() * n);
    const b = a + 1 + Math.floor(r() * (n - a));
    const key = pickOf(r, Object.keys(marks));
    if (pick < 0.9) t.mark({ start: u16(s, a), end: u16(s, b) }, key, pickOf(r, [true, "red", 3]));
    else t.unmark({ start: u16(s, a), end: u16(s, b) }, key);
  }
}

// Scalars only, or objects of one key: a map of two keys nested in a value
// is written in hash map order loro-go does not keep (README, Change blocks).
const scalar = (r) => {
  const pick = r();
  if (pick < 0.35) return Math.floor(r() * 2000) - 1000;
  if (pick < 0.5) return Math.floor(r() * 1000) / 8 + 0.5;
  if (pick < 0.7) return word(r);
  if (pick < 0.8) return r() < 0.5;
  if (pick < 0.85) return null;
  if (pick < 0.92) return [Math.floor(r() * 10), word(r)];
  return { [pickOf(r, ["x", "y"])]: Math.floor(r() * 10) };
};

function editMap(doc, r) {
  const m = doc.getMap("m");
  if (r() < 0.75) m.set(pickOf(r, ["a", "b", "c", "d"]), scalar(r));
  else m.delete(pickOf(r, ["a", "b", "c", "d"]));
}

function editList(doc, r) {
  const l = doc.getList("l");
  const n = l.length;
  if (r() < 0.6 || n === 0) {
    const at = Math.floor(r() * (n + 1));
    for (let j = 0, k = 1 + Math.floor(r() * 3); j < k; j++) l.insert(at + j, r() < 0.7 ? Math.floor(r() * 100) : scalar(r));
  } else {
    const k = Math.floor(r() * n);
    l.delete(k, 1 + Math.floor(r() * Math.min(3, n - k)));
  }
}

function editCounter(doc, r) {
  doc.getCounter("n").increment(r() < 0.8 ? Math.floor(r() * 21) - 10 : pickOf(r, [0.5, 134217728, -3.25]));
}

function editMovable(doc, r) {
  const l = doc.getMovableList("ml");
  const n = l.length;
  const pick = r();
  if (pick < 0.35 || n === 0) l.insert(Math.floor(r() * (n + 1)), Math.floor(r() * 100));
  else if (pick < 0.55) {
    const k = Math.floor(r() * n);
    l.delete(k, 1 + Math.floor(r() * Math.min(2, n - k)));
  } else if (pick < 0.8) l.move(Math.floor(r() * n), Math.floor(r() * n));
  else l.set(Math.floor(r() * n), 100 + Math.floor(r() * 100));
}

function editTree(doc, r) {
  const t = doc.getTree("tr");
  const alive = t.nodes().filter((x) => !x.isDeleted());
  const pick = r();
  try {
    if (pick < 0.4 || alive.length === 0) {
      const parent = alive.length > 0 && r() < 0.6 ? pickOf(r, alive) : undefined;
      const count = parent ? parent.children()?.length ?? 0 : t.roots().length;
      if (parent) parent.createNode(Math.floor(r() * (count + 1)));
      else t.createNode(undefined, Math.floor(r() * (count + 1)));
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
}

const kinds = [LoroMap, LoroList, LoroText, LoroTree, LoroMovableList, LoroCounter];
function editNested(doc, r) {
  const reach = [];
  const walk = (c, depth) => {
    reach.push({ c, depth });
    const kind = c.kind();
    if (kind === "Map") {
      for (const k of c.keys()) if (isContainer(c.get(k))) walk(c.get(k), depth + 1);
    } else if (kind === "List" || kind === "MovableList") {
      for (let i = 0; i < c.length; i++) if (isContainer(c.get(i))) walk(c.get(i), depth + 1);
    } else if (kind === "Tree") {
      for (const n of c.nodes()) if (!n.isDeleted()) walk(n.data, depth + 1);
    }
  };
  walk(doc.getMap("nm"), 0);
  walk(doc.getList("nl"), 0);
  const { c, depth } = pickOf(r, reach);
  const child = () => new (pickOf(r, kinds))();
  try {
    switch (c.kind()) {
      case "Map":
        if (depth < 3 && r() < 0.5) c.setContainer(pickOf(r, ["a", "b"]), child());
        else if (r() < 0.8) c.set(pickOf(r, ["a", "b", "c"]), Math.floor(r() * 100));
        else c.delete(pickOf(r, ["a", "b", "c"]));
        break;
      case "List":
      case "MovableList":
        if (depth < 3 && r() < 0.4) c.insertContainer(Math.floor(r() * (c.length + 1)), child());
        else if (c.length > 0 && r() < 0.3) c.delete(Math.floor(r() * c.length), 1);
        else c.insert(Math.floor(r() * (c.length + 1)), Math.floor(r() * 100));
        break;
      case "Text":
        c.insert(Math.floor(r() * (c.length + 1)), pickOf(r, ["a", "bc"]));
        break;
      case "Counter":
        c.increment(Math.floor(r() * 10) - 3);
        break;
      case "Tree":
        c.createNode(undefined, 0);
        break;
    }
  } catch {
    // loro refused the step.
  }
}

const shapes = {
  text: editText, list: editList, map: editMap, counter: editCounter, movable: editMovable,
  tree: editTree, nested: editNested, marks: editMarks,
};
const shapeNames = [...Object.keys(shapes), "mixed"];

// ---------------------------------------------------------------------------
// Random histories.

function peerIDs(r, n, style) {
  const ids = new Set();
  let k = 1;
  while (ids.size < n) {
    if (style === 0) ids.add(BigInt(k++));
    else if (style === 1) ids.add(BigInt(u32(r)));
    else {
      const id = (BigInt(u32(r)) << 32n) | BigInt(u32(r));
      if (id !== 0xffffffffffffffffn) ids.add(id);
    }
  }
  return [...ids];
}

const vvEntries = (vv) => [...vv.toJSON()].map(([p, c]) => [p, c]);
const fromEntries = (entries) => VersionVector.parseJSON(new Map(entries));

// A crowd history is many peers' changes made apart: eight to twelve peers
// commit two to four times each with alternating messages, so that every
// commit is a change of its own, and seldom sync. Their first changes share
// lamport 0 and later ones tie too, so the order Rust's unstable sort puts
// tied changes in, which decides where each peer lands in the version
// vector's hash map, is tested on blobs of more than 20 changes.
function history(seed, crowd) {
  const r = rng(seed * 2654435761);
  const n = crowd ? 8 + (seed % 5) : 1 + (seed % 12);
  const shape = crowd ? "crowd" : shapeNames[Math.floor(seed / 12) % shapeNames.length];
  const ids = peerIDs(r, n, seed % 3);
  const docs = ids.map((id) => {
    const d = new LoroDoc();
    d.setPeerId(id);
    d.configTextStyle(marks);
    return d;
  });
  const commits = ids.map(() => 0);
  for (let step = crowd ? n * (2 + Math.floor(r() * 3)) : 6 + Math.floor(r() * 24); step > 0; step--) {
    const p = Math.floor(r() * n);
    const d = docs[p];
    if (n > 1 && r() < (crowd ? 0.08 : 0.3)) {
      const q = (p + 1 + Math.floor(r() * (n - 1))) % n;
      const from = r() < 0.5 ? undefined : d.oplogVersion();
      d.import(docs[q].export(from ? { mode: "update", from } : { mode: "update" }));
      continue;
    }
    const edit = shape === "mixed" || crowd ? shapes[pickOf(r, Object.keys(shapes))] : shapes[shape];
    edit(d, r);
    const q = r();
    if (crowd) d.commit({ message: ["a", "b"][commits[p]++ % 2] });
    else if (q < 0.55) d.commit();
    else if (q < 0.85) d.commit({ message: pickOf(r, ["a", "b"]) });
    else d.commit({ timestamp: Math.floor(r() * 5000) });
  }
  const all = new LoroDoc();
  for (const d of docs) all.import(d.export({ mode: "update" }));
  const update = all.export({ mode: "update" });
  const replay = new LoroDoc();
  replay.import(update);

  const blobs = new Blobs();
  const full = vvEntries(replay.oplogVersion());
  const known = full.filter(([, c]) => c > 0);
  const unknownPeer = () => String(BigInt(u32(r)) * 7919n + 13n);
  const versions = [];
  const updateB64 = b64(update);
  const add = (kind, vv, entries) => {
    const bytes = vv.encode();
    const exported = replay.export({ mode: "update", from: vv });
    const v = {
      kind,
      entries,
      setEnd: entries !== null && entries.every(([, c]) => c > 0),
      bytes: b64(bytes),
      export: b64(exported) === updateB64 ? -1 : blobs.add(exported),
    };
    const redecoded = b64(VersionVector.decode(bytes).encode());
    if (redecoded !== v.bytes) v.redecoded = redecoded;
    versions.push(v);
  };
  const fromJSON = (kind, entries) => add(kind, fromEntries(entries), entries);
  fromJSON("empty", []);
  if (known.length > 0) {
    const [p, c] = pickOf(r, known);
    fromJSON("peer", [[p, 1 + Math.floor(r() * c)]]);
    const mixed = [];
    for (const [q, e] of known) if (r() < 0.7) mixed.push([q, 1 + Math.floor(r() * e)]);
    fromJSON("prefixes", mixed.length > 0 ? mixed : [[p, c]]);
    for (let j = 0; j < 2; j++) {
      const ids = [];
      for (let k = 1 + Math.floor(r() * 3); k > 0; k--) {
        const [q, e] = pickOf(r, known);
        ids.push({ peer: q, counter: Math.floor(r() * e) });
      }
      add("closed", replay.frontiersToVV(ids), null);
    }
    const nonpositive = known.map(([q, e]) => [q, r() < 0.5 ? 1 + Math.floor(r() * e) : pickOf(r, [0, -1, -7, -1000])]);
    fromJSON("nonpositive", nonpositive);
  }
  add("full", replay.oplogVersion(), null);
  const beyond = full.map(([q, e]) => [q, e]);
  if (beyond.length > 0) beyond[Math.floor(r() * beyond.length)][1] += 3;
  beyond.push([unknownPeer(), 4]);
  fromJSON("beyond", beyond);
  fromJSON("unknown", [[unknownPeer(), 2], [unknownPeer(), 9]]);

  // Exports of id ranges: spans of a few peers that overlap, touch, run
  // past the end or name nothing, and one of a peer never seen.
  const ranges = [];
  if (known.length > 0) {
    for (let j = 0; j < 2; j++) {
      const spans = [];
      for (let k = 1 + Math.floor(r() * 4); k > 0; k--) {
        const [q, e] = pickOf(r, known);
        spans.push([q, Math.floor(r() * (e + 2)), Math.floor(r() * (e + 1))]);
      }
      if (j === 1) spans.push([unknownPeer(), 0, 3]);
      const exported = replay.export({ mode: "updates-in-range", spans: spans.map(([peer, counter, len]) => ({ id: { peer, counter }, len })) });
      ranges.push({ spans, export: b64(exported) === updateB64 ? -1 : blobs.add(exported) });
    }
  }

  const out = {
    seed,
    shape,
    peers: n,
    changes: [...replay.getAllChanges().values()].reduce((n, cs) => n + cs.length, 0),
    update: updateB64,
    vv: b64(replay.oplogVersion().encode()),
    frontiers: replay.oplogFrontiers().map((id) => [id.peer, id.counter]),
    frontiersBytes: b64(encodeFrontiers(replay.oplogFrontiers())),
    versions,
    ranges,
    blobs: blobs.list,
  };
  // Every fourth history also as a snapshot, imported into an empty
  // document, which takes the version vector and frontiers the snapshot
  // stores. It is exported last: a snapshot export flushes the exporter's
  // change store, which changes how it exports updates.
  if (seed % 4 === 0) {
    const snap = replay.export({ mode: "snapshot" });
    const fromSnap = new LoroDoc();
    fromSnap.import(snap);
    out.snapshot = b64(snap);
    out.snapshotVV = b64(fromSnap.oplogVersion().encode());
    out.snapshotFrontiers = fromSnap.oplogFrontiers().map((id) => [id.peer, id.counter]);
    fromSnap.free();
  }
  // WebAssembly memory is freed when a document is, not when JavaScript
  // collects it; 400 histories run out of it otherwise.
  for (const d of [...docs, all, replay]) d.free();
  return out;
}

const corpus = [];
for (let seed = 1; seed <= 320; seed++) corpus.push(history(seed, false));
for (let seed = 321; seed <= 400; seed++) corpus.push(history(seed, true));
writeFileSync(join(outDir, "versions_corpus.json"), JSON.stringify(corpus) + "\n");
{
  const byShape = {};
  let versions = 0;
  for (const h of corpus) {
    byShape[h.shape] = (byShape[h.shape] ?? 0) + 1;
    versions += h.versions.length;
  }
  console.log(`versions_corpus.json: ${corpus.length} histories, ${versions} versions; shapes ${JSON.stringify(byShape)}`);
}

// ---------------------------------------------------------------------------
// Named scripts. A Go Document runs the same actions and is held at every
// "version" and "export_from" to what loro-crdt recorded.
//
//   text_ins {p, c, pos, s}   text_del {p, c, pos, n}   mark {p, c, start, end, k, v}
//   map_set  {p, c, k, v}     list_ins {p, c, pos, v: [items]}   inc {p, c, v}
//   commit   {p, msg?}        import {p, blob, refused?}
//   version  {p, vv, frontiers, fb}   vv and fb index blobs: the encoded
//            oplogVersion() and encodeFrontiers(oplogFrontiers()), taken
//            without a commit
//   export_range {p, spans, blob}  export({mode: "updates-in-range"}) with
//            spans as [peer, counter, len]
//   export_from {p, from, blob}   from indexes the encoded version vector
//            exported from, blob the export; with panicked, loro-crdt
//            panicked instead, and the Go export must equal the export from
//            the version with that peer at 0

class Script {
  constructor(name, peers) {
    this.name = name;
    this.peers = peers;
    this.docs = peers.map((id) => {
      const d = new LoroDoc();
      d.setPeerId(BigInt(id));
      return d;
    });
    this.actions = [];
    this.blobs = new Blobs();
  }
  textIns(p, c, pos, s) {
    this.docs[p].getText(c).insert(pos, s);
    this.actions.push({ a: "text_ins", p, c, pos, s });
  }
  textDel(p, c, pos, n) {
    this.docs[p].getText(c).delete(pos, n);
    this.actions.push({ a: "text_del", p, c, pos, n });
  }
  mark(p, c, start, end, k, v) {
    this.docs[p].getText(c).mark({ start, end }, k, v);
    this.actions.push({ a: "mark", p, c, start, end, k, v });
  }
  mapSet(p, c, k, v) {
    this.docs[p].getMap(c).set(k, v);
    this.actions.push({ a: "map_set", p, c, k, v });
  }
  listIns(p, c, pos, items) {
    const l = this.docs[p].getList(c);
    items.forEach((v, j) => l.insert(pos + j, v));
    this.actions.push({ a: "list_ins", p, c, pos, v: items });
  }
  inc(p, c, v) {
    this.docs[p].getCounter(c).increment(v);
    this.actions.push({ a: "inc", p, c, v });
  }
  commit(p, msg) {
    this.docs[p].commit(msg === undefined ? undefined : { message: msg });
    this.actions.push(msg === undefined ? { a: "commit", p } : { a: "commit", p, msg });
  }
  import(p, bytes) {
    this.docs[p].import(bytes);
    this.actions.push({ a: "import", p, blob: this.blobs.add(bytes) });
  }
  // importRefused records a blob loro-crdt refuses: the Go peer must refuse
  // it too.
  importRefused(p, bytes) {
    let err;
    try {
      this.docs[p].import(bytes);
    } catch (e) {
      err = String(e);
    }
    if (err === undefined) throw new Error(`${this.name}: loro-crdt imported a blob expected to be refused`);
    this.actions.push({ a: "import", p, blob: this.blobs.add(bytes), refused: err.slice(0, 120) });
  }
  // version records peer p's version and frontiers and returns the
  // version vector.
  version(p) {
    const d = this.docs[p];
    const vv = d.oplogVersion();
    this.actions.push({
      a: "version",
      p,
      vv: this.blobs.add(vv.encode()),
      frontiers: d.oplogFrontiers().map((id) => [id.peer, id.counter]),
      fb: this.blobs.add(encodeFrontiers(d.oplogFrontiers())),
    });
    return vv;
  }
  exportFrom(p, from) {
    const a = { a: "export_from", p, from: this.blobs.add(from.encode()) };
    const report = console.error;
    try {
      // loro-crdt prints its panics; this one is expected.
      console.error = () => {};
      a.blob = this.blobs.add(this.docs[p].export({ mode: "update", from }));
    } catch {
      // A panic leaves the document's lock held: nothing more can be done
      // with it, so this is the peer's last action.
      a.panicked = true;
    } finally {
      console.error = report;
    }
    this.actions.push(a);
  }
  exportRange(p, spans) {
    const bytes = this.docs[p].export({ mode: "updates-in-range", spans: spans.map(([peer, counter, len]) => ({ id: { peer, counter }, len })) });
    this.actions.push({ a: "export_range", p, spans, blob: this.blobs.add(bytes) });
  }
  out() {
    return { name: this.name, peers: this.peers, actions: this.actions, blobs: this.blobs.list };
  }
}

const named = [];
const add = (name, peers, build) => {
  const s = new Script(name, peers);
  build(s);
  named.push(s.out());
};
const vvOf = (entries) => fromEntries(entries.map(([p, c]) => [String(p), c]));

// A peer's own history, exported from versions it had and from versions
// that cut inside its changes: the part exported starts one lamport later
// for each op cut and depends on the op before it. Changes with other
// messages do not merge, so the history has several.
add("local_history_from_versions", [1, 2], (s) => {
  s.textIns(0, "t", 0, "hello");
  s.mapSet(0, "m", "k", 1);
  s.commit(0, "a");
  const v1 = s.version(0);
  s.textIns(0, "t", 5, " world");
  s.mark(0, "t", 0, 5, "bold", true);
  s.listIns(0, "l", 0, [1, 2, 3]);
  s.commit(0, "b");
  const v2 = s.version(0);
  s.import(1, s.docs[0].export({ mode: "update", from: v1 }));
  s.textIns(0, "t", 0, ">");
  s.inc(0, "n", 5);
  s.commit(0, "a");
  s.version(0);
  for (const from of [new VersionVector(), v1, v2, vvOf([[1, 3]]), vvOf([[1, 9]]), vvOf([[1, 14], [2, 4]]), s.docs[0].oplogVersion()]) s.exportFrom(0, from);
});

// Ops not committed are in the version and the frontiers already, and an
// export from a version commits them first.
add("uncommitted_ops_counted", [1], (s) => {
  s.textIns(0, "t", 0, "ab");
  s.commit(0);
  const v = s.version(0);
  s.textIns(0, "t", 2, "c");
  s.textIns(0, "t", 0, "d");
  s.version(0);
  s.exportFrom(0, v);
  s.version(0);
});

// Two peers edit and sync, one exports from what the other has: their
// changes depend on each other's, and the cut one depends only on the op
// before it.
add("cut_change_with_deps", [1, 2], (s) => {
  s.textIns(0, "t", 0, "abc");
  s.commit(0);
  s.import(1, s.docs[0].export({ mode: "update" }));
  s.textIns(1, "t", 3, "XYZ");
  s.commit(1);
  s.import(0, s.docs[1].export({ mode: "update" }));
  s.textIns(0, "t", 0, "0123");
  s.mapSet(0, "m", "x", "y");
  s.commit(0);
  s.version(0);
  s.exportFrom(0, vvOf([[1, 5], [2, 1]]));
  s.exportFrom(0, vvOf([[1, 3], [2, 3]]));
  s.exportFrom(0, s.docs[1].oplogVersion());
});

// A change waiting for its dependencies is in neither the version nor an
// export until they arrive.
add("pending_not_in_version", [1, 2], (s) => {
  s.textIns(0, "t", 0, "ab");
  s.commit(0);
  const v = s.docs[0].oplogVersion();
  const prefix = s.docs[0].export({ mode: "update" });
  s.textIns(0, "t", 2, "cd");
  s.commit(0, "m");
  const tail = s.docs[0].export({ mode: "update", from: v });
  s.textIns(1, "u", 0, "x");
  s.import(1, tail);
  s.version(1);
  s.exportFrom(1, new VersionVector());
  s.import(1, prefix);
  s.version(1);
  s.exportFrom(1, vvOf([[1, 1]]));
});

// Changes too big to share a block: the document keeps two blocks, and an
// export that cuts the first change makes room, so the export's fresh store
// puts the next block's changes into the first one.
add("export_regroups_blocks", [1], (s) => {
  s.textIns(0, "t", 0, "a".repeat(3000));
  s.commit(0, "a");
  s.textIns(0, "t", 3000, "b".repeat(1000));
  s.commit(0, "b");
  s.textIns(0, "t", 4000, "c".repeat(500));
  s.commit(0, "a");
  s.textIns(0, "t", 4500, "d".repeat(500));
  s.commit(0, "b");
  s.version(0);
  s.exportFrom(0, vvOf([[1, 2500]]));
  s.exportFrom(0, vvOf([[1, 3500]]));
  s.exportFrom(0, new VersionVector());
});

// loro sets a local op's end in the version vector with HashMap::insert,
// which makes room for one more entry even when the peer is there. A peer
// whose first op fills the map grows it with its second op, and a peer in a
// full map grows it with its next op: the map's order changes, and the
// version's bytes with it. The peer ids are searched for so that it shows.
{
  const encodeAfter = (ids, ops, before) => {
    const d = new LoroDoc();
    d.setPeerId(ids[0]);
    if (before) d.getText("t").insert(0, "s");
    for (const id of ids.slice(1)) {
      const o = new LoroDoc();
      o.setPeerId(id);
      o.getText("o").insert(0, "x");
      o.commit();
      d.import(o.export({ mode: "update" }));
      o.free();
    }
    for (let i = 0; i < ops; i++) d.getText("t").insert(0, "y");
    const out = b64(d.oplogVersion().encode());
    d.free();
    return out;
  };
  const r = rng(77);
  const search = (others, before, pick) => {
    for (let tries = 0; ; tries++) {
      if (tries === 5000) throw new Error(`no peer ids found for ${others} others`);
      const ids = [BigInt(u32(r))];
      for (let i = 0; i < others; i++) ids.push(BigInt(u32(r)));
      if (pick(ids)) return ids.map(String);
    }
  };
  // The peer's first op makes the third entry of a 4-bucket map; its second
  // grows the map to 8 buckets.
  const fills = search(2, false, (ids) => encodeAfter(ids, 1, false) !== encodeAfter(ids, 2, false));
  add("local_op_grows_full_map", fills.map(Number), (s) => {
    for (const p of [1, 2]) {
      s.textIns(p, "o", 0, "x");
      s.commit(p);
      s.import(0, s.docs[p].export({ mode: "update" }));
    }
    s.textIns(0, "t", 0, "y");
    s.version(0);
    s.textIns(0, "t", 0, "y");
    s.version(0);
    s.commit(0);
    s.version(0);
  });
  // The peer is in a full map before its next transaction: its first op
  // grows it.
  const full = search(2, true, (ids) => encodeAfter(ids, 0, true) !== encodeAfter(ids, 1, true));
  add("local_op_in_full_map", full.map(Number), (s) => {
    s.textIns(0, "t", 0, "s");
    s.commit(0);
    for (const p of [1, 2]) {
      s.textIns(p, "o", 0, "x");
      s.commit(p);
      s.import(0, s.docs[p].export({ mode: "update" }));
    }
    s.version(0);
    s.textIns(0, "t", 0, "y");
    s.version(0);
  });
}

// A snapshot imported into an empty document brings the version vector and
// frontiers it stores, decoded into maps sized for them, where an update
// builds them change by change. The peer ids are searched for so that the
// version vectors' orders differ.
{
  const r = rng(91);
  const build = (ids) => {
    const docs = ids.map((id) => {
      const d = new LoroDoc();
      d.setPeerId(id);
      d.getText("t").insert(0, "a");
      d.commit();
      return d;
    });
    const all = new LoroDoc();
    for (const d of docs) {
      all.import(d.export({ mode: "update" }));
      d.free();
    }
    return all;
  };
  let ids;
  for (let tries = 0; ; tries++) {
    if (tries === 2000) throw new Error("no peer ids found for snapshot_brings_version");
    ids = [];
    for (let i = 3 + (tries % 6); i > 0; i--) ids.push(BigInt(u32(r)));
    const all = build(ids);
    const viaSnap = new LoroDoc();
    viaSnap.import(all.export({ mode: "snapshot" }));
    const viaUpdate = new LoroDoc();
    const again = build(ids);
    viaUpdate.import(again.export({ mode: "update" }));
    again.free();
    const differ = b64(viaSnap.oplogVersion().encode()) !== b64(viaUpdate.oplogVersion().encode());
    for (const d of [all, viaSnap, viaUpdate]) d.free();
    if (differ) break;
  }
  add("snapshot_brings_version", [7, ...ids.map(Number)], (s) => {
    for (let p = 1; p < s.peers.length; p++) {
      s.textIns(p, "t", 0, "a");
      s.commit(p);
    }
    const all = new LoroDoc();
    for (let p = 1; p < s.peers.length; p++) all.import(s.docs[p].export({ mode: "update" }));
    s.import(0, all.export({ mode: "snapshot" }));
    s.version(0);
    s.textIns(0, "t", 0, "z");
    s.commit(0);
    s.version(0);
    s.exportFrom(0, vvOf([[s.peers[1], 1]]));
  });
}

// A list insert that owns its buffer appends what folds into it; one whose
// items others hold too does not, and folds only with the slice that
// follows it in the same buffer (op.rs ValueSlice::is_mergable,
// can_append, slice).
//
// The blob, written by loro-go's Doc, holds three list inserts in one
// change, [1, 2], [3, 4] and [5], which loro-crdt only writes when it put
// the change together from parts. Reading it, loro folds the second into
// the first, which follow each other in the block's shared buffer; the
// first then holds that buffer alone, owns it, and appends the third.
add("decoded_list_inserts_fold", [9], (s) => {
  s.import(0, Buffer.from("bG9ybwAAAAAAAAAAAAAAAOjdLQQABEwABQAFARABAQAAAAAAAAABAQAAAAAABQEAAAEABgEEAQEAAAIBbAASAQQCBgAEAQAEBAIGCwQEAgEBABAHAgMBAwIHAgMDAwQHAQMF", "base64"));
  s.version(0);
  s.exportFrom(0, new VersionVector());
});

// An export slices the changes it takes, and a slice of a list insert
// holds the insert's items beside the document's op: the first change cut
// at 1020 keeps two items it cannot append to, so the next change's insert
// stays an op of its own. Cutting a big insert to fit the change store's
// blocks makes pieces that hold one buffer: they fold with each other
// again, and no later insert appends to them.
add("list_slices_share_their_items", [1], (s) => {
  for (let i = 0; i < 1022; i++) s.listIns(0, "l", i, [i]);
  s.commit(0);
  s.listIns(0, "l", 1022, [1022]);
  s.mapSet(0, "m", "x", 1);
  s.commit(0);
  s.exportFrom(0, vvOf([[1, 1020]]));
  s.exportRange(0, [["1", 1020, 4]]);
  for (let i = 0; i < 1100; i++) s.listIns(0, "big", i, [i]);
  s.commit(0);
  s.listIns(0, "big", 1100, [1100]);
  s.commit(0);
  s.version(0);
  s.exportFrom(0, new VersionVector());
  s.exportFrom(0, vvOf([[1, 1500]]));
});

// loro checks each op's positions as it reads it, before it folds the op
// into the one before it (outdated_encode_reordered.rs:546). Two deletes at
// position 1073741822, the last loro reads, fold into one that ends past
// it; loro takes the change, as pending since its dependency is missing.
add("ops_checked_before_folding", [9], (s) => {
  s.import(0, Buffer.from("bG9ybwAAAAAAAAAAAAAAABRRc8kABEsCAgICAREBAQAAAAAAAAAAAQEAAAAAAAUBAAABAAYBBAEBAAACAWwAEwEEAgQABwP8////BwACBAkCBAENAQMCBAADAwACAwMCAAA=", "base64"));
  s.version(0);
});

// A snapshot whose stored version vector and frontiers are not what its
// changes make: loro installs them as they are decoded (loro_dag.rs:643),
// asking only that each frontier lie in some change (change_store.rs:1244).
// The snapshot is rebuilt with its oplog's "vv" and "fr" entries replaced:
// a version vector with an entry of counter 0, a frontier on an op that is
// not its change's last, and one past every change, which loro refuses.
{
  const xxh32 = (data) => {
    const P1 = 2654435761, P2 = 2246822519, P3 = 3266489917, P4 = 668265263, P5 = 374761393;
    const rotl = (x, r) => ((x << r) | (x >>> (32 - r))) >>> 0;
    const le = (i) => (data[i] | (data[i + 1] << 8) | (data[i + 2] << 16) | (data[i + 3] << 24)) >>> 0;
    const seed = 0x4f524f4c;
    const n = data.length;
    let h, i = 0;
    if (n >= 16) {
      const round = (acc, x) => Math.imul(rotl((acc + Math.imul(x, P2)) >>> 0, 13), P1) >>> 0;
      let v1 = (seed + P1 + P2) >>> 0, v2 = (seed + P2) >>> 0, v3 = seed, v4 = (seed - P1) >>> 0;
      for (; i <= n - 16; i += 16) {
        v1 = round(v1, le(i)); v2 = round(v2, le(i + 4)); v3 = round(v3, le(i + 8)); v4 = round(v4, le(i + 12));
      }
      h = (rotl(v1, 1) + rotl(v2, 7) + rotl(v3, 12) + rotl(v4, 18)) >>> 0;
    } else h = (seed + P5) >>> 0;
    h = (h + n) >>> 0;
    for (; i <= n - 4; i += 4) h = Math.imul(rotl((h + Math.imul(le(i), P3)) >>> 0, 17), P4) >>> 0;
    for (; i < n; i++) h = Math.imul(rotl((h + Math.imul(data[i], P5)) >>> 0, 11), P1) >>> 0;
    h = Math.imul(h ^ (h >>> 15), P2) >>> 0;
    h = Math.imul(h ^ (h >>> 13), P3) >>> 0;
    return (h ^ (h >>> 16)) >>> 0;
  };
  const u32le = (b, i) => b.readUInt32LE(i);
  // The entries of an SSTable whose blocks are not compressed
  // (crates/kv-store/src/sstable.rs, block.rs).
  const readSSTable = (b) => {
    const metaOff = u32le(b, b.length - 4);
    const meta = b.subarray(metaOff, b.length - 4);
    const num = u32le(meta, 0);
    const metas = [];
    let p = 4;
    for (let i = 0; i < num; i++) {
      const offset = u32le(meta, p); p += 4;
      const fk = meta.readUInt16LE(p); p += 2;
      const firstKey = meta.subarray(p, p + fk); p += fk;
      const flags = meta[p]; p += 1;
      if ((flags & 0x7f) !== 0) throw new Error("compressed SSTable block");
      const large = (flags & 0x80) !== 0;
      if (!large) { const lk = meta.readUInt16LE(p); p += 2 + lk; }
      metas.push({ offset, firstKey, large });
    }
    const entries = [];
    metas.forEach((m, i) => {
      const end = i + 1 < metas.length ? metas[i + 1].offset : metaOff;
      const data = b.subarray(m.offset, end - 4);
      if (m.large) { entries.push([Buffer.from(m.firstKey), Buffer.from(data)]); return; }
      const n = data.readUInt16LE(data.length - 2);
      const dataEnd = data.length - 2 * (n + 1);
      const offs = [];
      for (let k = 0; k < n; k++) offs.push(data.readUInt16LE(dataEnd + 2 * k));
      for (let k = 0; k < n; k++) {
        const st = offs[k], en = k + 1 < n ? offs[k + 1] : dataEnd;
        if (k === 0) { entries.push([Buffer.from(m.firstKey), Buffer.from(data.subarray(st, en))]); continue; }
        const prefix = data[st], suffix = data.readUInt16LE(st + 1);
        const key = Buffer.concat([m.firstKey.subarray(0, prefix), data.subarray(st + 3, st + 3 + suffix)]);
        entries.push([key, Buffer.from(data.subarray(st + 3 + suffix, en))]);
      }
    });
    return entries;
  };
  // An SSTable of the entries, sorted, each in a large block of its own.
  const writeSSTable = (entries) => {
    const parts = [Buffer.from("LORO"), Buffer.from([0])];
    let off = 5;
    const meta = [];
    for (const [key, value] of entries) {
      const sum = Buffer.alloc(4); sum.writeUInt32LE(xxh32(value));
      parts.push(value, sum);
      meta.push({ off, key });
      off += value.length + 4;
    }
    const m = [];
    for (const { off: o, key } of meta) {
      const h = Buffer.alloc(6); h.writeUInt32LE(o); h.writeUInt16LE(key.length, 4);
      m.push(h, key, Buffer.from([0x80]));
    }
    const body = Buffer.concat(m);
    const head = Buffer.alloc(4); head.writeUInt32LE(meta.length);
    const sum = Buffer.alloc(4); sum.writeUInt32LE(xxh32(body));
    const tail = Buffer.alloc(4); tail.writeUInt32LE(off);
    return Buffer.concat([...parts, head, body, sum, tail]);
  };
  // The snapshot with the oplog entries named in replace changed.
  const rewrite = (snap, replace) => {
    const s = Buffer.from(snap);
    const body = s.subarray(22);
    const oplogLen = u32le(body, 0);
    const entries = readSSTable(body.subarray(4, 4 + oplogLen)).map(([k, v]) => [k, replace[k.toString("latin1")] ?? v]);
    const oplog = writeSSTable(entries);
    const len = Buffer.alloc(4); len.writeUInt32LE(oplog.length);
    const out = Buffer.concat([s.subarray(0, 22), len, oplog, body.subarray(4 + oplogLen)]);
    out.writeUInt32LE(xxh32(out.subarray(20)), 16);
    return out;
  };
  add("snapshot_stored_version_installed", [9, 1], (s) => {
    s.textIns(1, "t", 0, "ab");
    s.commit(1);
    const snap = s.docs[1].export({ mode: "snapshot" });
    // Version vector {1: 2, 2: 0}, frontiers [0@1].
    s.import(0, rewrite(snap, { vv: Buffer.from([2, 1, 4, 2, 0]), fr: Buffer.from([1, 1, 0]) }));
    s.version(0);
    s.textIns(0, "t", 0, "z");
    s.commit(0);
    s.version(0);
    s.exportFrom(0, new VersionVector());
    // A frontier past every change: loro refuses the snapshot.
    s.importRefused(0, rewrite(snap, { fr: Buffer.from([1, 1, 14]) }));
  });
}

// A from counter so low that loro's i32 subtraction wraps: loro-crdt panics
// (change_store.rs:1117 assert_ne), loro-go counts it as 0. It runs last: a
// panic leaves loro-crdt's WebAssembly instance unusable.
add("from_counter_wraps", [1], (s) => {
  s.textIns(0, "t", 0, "abc");
  s.commit(0, "a");
  s.textIns(0, "t", 0, "def");
  s.commit(0, "b");
  s.exportFrom(0, vvOf([[1, -1000]]));
  s.exportFrom(0, vvOf([[1, -2147483648]]));
});

writeFileSync(join(outDir, "versions_named.json"), JSON.stringify(named) + "\n");
console.log(`versions_named.json: ${named.length} scripts: ${named.map((s) => s.name).join(", ")}`);
