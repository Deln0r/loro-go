// Edit scripts for loro-go's Document: loro-crdt runs each script and records
// every blob a peer exported and, at each checkpoint, every peer's toJSON and
// full export, so the Go test can run the same script with Documents and hold
// them to loro-crdt.
//
//   ../fixtures/script_corpus.json        300 random scripts, 1 to 3 peers
//   ../fixtures/script_crowd_corpus.json  60 random scripts, 4 to 12 peers
//   ../fixtures/script_named.json         small scripts, one or more per rule
//   ../fixtures/shallow_snapshot.bin      a shallow snapshot, which loro-go refuses
//
// A script names its peers by peer id and lists actions; "p" is the index of
// the peer acting. Text positions are Unicode code points ("pos", "n"), the
// UTF-16 positions loro-crdt's API took are kept beside them ("pos16",
// "n16"). Values that JSON cannot carry are tagged: a Uint8Array is
// {"$bin": base64}. Numbers reach loro-crdt as JavaScript numbers, which its
// binding stores as an integer when whole and within 2^53, and as a float
// otherwise; the Go side converts them the same way.
//
//   text_ins  {c, pos, pos16, s}      text_del  {c, pos, n, pos16, n16}
//   map_set   {c, k, v}               map_del   {c, k}
//   list_ins  {c, pos, v: [items]}    list_del  {c, pos, n}
//   inc       {c, v}                  acquire   {c, kind}
//   commit    {msg?, ts?}             merge_interval {v}
//   export    {}         the peer exported; the export commits its open edits
//   import    {blob, refused?}  the peer imported blobs[blob]; with refused,
//                        loro-crdt refused it with that error and nothing changed
//   check     {peers: [{json, export, vv?, fr?}]}  toJSON and the full export
//                        of every peer, and in the structure scripts its encoded
//                        version and frontiers; taking the exports commits every
//                        peer's edits
//
// A sync delivers the exact blob the source exported: its whole history, a
// tail from a version (the receiver's, or one the source saved earlier, so it
// overlaps or comes before its prefix and waits), the same blob again, or a
// snapshot. Counter increments are integers below 2^53: loro adds a peer's
// own increments one by one and imported ones as one sum, so fractional
// increments can leave two loro peers with one history at different values
// (review item 1), and no implementation can match both.
//
// Everything is seeded and peer ids are fixed, so a rerun writes the same
// bytes.
import { LoroDoc, LoroText, LoroMap, LoroList, LoroMovableList, LoroCounter, LoroTree, encodeFrontiers } from "loro-crdt/nodejs";
import { writeFileSync, mkdirSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";

const here = dirname(fileURLToPath(import.meta.url));
const outDir = join(here, "..", "fixtures");
mkdirSync(outDir, { recursive: true });

const rng = (seed) => { let s = seed >>> 0; return () => (s = (s * 1664525 + 1013904223) >>> 0) / 4294967296; };
const pickOf = (r, xs) => xs[Math.floor(r() * xs.length)];
const points = (s) => Array.from(s);
const u16 = (s, k) => points(s).slice(0, k).join("").length;
const b64 = (bytes) => Buffer.from(bytes).toString("base64");

const tag = (v) => {
  if (v instanceof Uint8Array) return { $bin: b64(v) };
  if (Array.isArray(v)) return v.map(tag);
  if (v !== null && typeof v === "object") return Object.fromEntries(Object.entries(v).map(([k, x]) => [k, tag(x)]));
  return v;
};

class Script {
  constructor(head, peers) {
    this.head = head;
    this.peers = peers;
    this.docs = peers.map((id) => {
      const d = new LoroDoc();
      d.setPeerId(BigInt(id));
      return d;
    });
    this.actions = [];
    this.blobs = [];
    this.blobIndex = new Map();
  }
  blob(bytes) {
    const s = b64(bytes);
    if (!this.blobIndex.has(s)) {
      this.blobIndex.set(s, this.blobs.length);
      this.blobs.push(s);
    }
    return this.blobIndex.get(s);
  }
  textIns(p, c, pos, s) {
    const t = this.docs[p].getText(c);
    const pos16 = u16(t.toString(), pos);
    t.insert(pos16, s);
    this.actions.push({ a: "text_ins", p, c, pos, pos16, s });
  }
  textDel(p, c, pos, n) {
    const t = this.docs[p].getText(c);
    const cur = t.toString();
    const pos16 = u16(cur, pos);
    const n16 = u16(cur, pos + n) - pos16;
    t.delete(pos16, n16);
    this.actions.push({ a: "text_del", p, c, pos, n, pos16, n16 });
  }
  mapSet(p, c, k, v) {
    this.docs[p].getMap(c).set(k, v);
    this.actions.push({ a: "map_set", p, c, k, v: tag(v) });
  }
  mapDel(p, c, k) {
    this.docs[p].getMap(c).delete(k);
    this.actions.push({ a: "map_del", p, c, k });
  }
  listIns(p, c, pos, items) {
    const l = this.docs[p].getList(c);
    items.forEach((v, j) => l.insert(pos + j, v));
    this.actions.push({ a: "list_ins", p, c, pos, v: tag(items) });
  }
  listDel(p, c, pos, n) {
    this.docs[p].getList(c).delete(pos, n);
    this.actions.push({ a: "list_del", p, c, pos, n });
  }
  inc(p, c, v) {
    this.docs[p].getCounter(c).increment(v);
    this.actions.push({ a: "inc", p, c, v });
  }
  acquire(p, kind, c) {
    const d = this.docs[p];
    ({ text: () => d.getText(c), map: () => d.getMap(c), list: () => d.getList(c), counter: () => d.getCounter(c) })[kind]();
    this.actions.push({ a: "acquire", p, c, kind });
  }
  commit(p, opts) {
    const a = { a: "commit", p };
    if (opts && (opts.message !== undefined || opts.timestamp !== undefined)) {
      this.docs[p].commit(opts);
      if (opts.message !== undefined) a.msg = opts.message;
      if (opts.timestamp !== undefined) a.ts = opts.timestamp;
    } else {
      this.docs[p].commit();
    }
    this.actions.push(a);
  }
  mergeInterval(p, v) {
    this.docs[p].setChangeMergeInterval(v);
    this.actions.push({ a: "merge_interval", p, v });
  }
  // export records that peer p exported, which commits its open edits, and
  // returns the blob's index.
  export(p, mode) {
    const bytes = this.docs[p].export(mode ?? { mode: "update" });
    this.actions.push({ a: "export", p });
    return this.blob(bytes);
  }
  // snapshot is a FastSnapshot of peer p's history. It is taken from a copy
  // that imported p's update export: a snapshot export flushes the
  // exporter's change store, which changes how its later edits are stored,
  // and the Go side has no snapshot export to follow it.
  snapshot(p) {
    const bytes = this.docs[p].export({ mode: "update" });
    this.actions.push({ a: "export", p });
    const copy = new LoroDoc();
    copy.import(bytes);
    return this.blob(copy.export({ mode: "snapshot" }));
  }
  // external records a blob made by a document the Go side does not run.
  external(bytes) {
    return this.blob(bytes);
  }
  import(p, blob) {
    this.docs[p].import(Buffer.from(this.blobs[blob], "base64"));
    this.actions.push({ a: "import", p, blob });
  }
  // importRefused imports a blob loro-crdt refuses, and records that it
  // did: the Go side must refuse it too and change nothing.
  importRefused(p, blob) {
    let err;
    try {
      this.docs[p].import(Buffer.from(this.blobs[blob], "base64"));
    } catch (e) {
      err = String(e);
    }
    if (err === undefined) throw new Error(`${this.head.name}: loro-crdt imported a blob expected to be refused`);
    this.actions.push({ a: "import", p, blob, refused: err.slice(0, 120) });
  }
  // configStyle replaces peer p's text style configuration: styles maps a
  // key to "before", "after", "both" or "none".
  configStyle(p, styles) {
    this.docs[p].configTextStyle(Object.fromEntries(Object.entries(styles).map(([k, e]) => [k, { expand: e }])));
    this.actions.push({ a: "config_style", p, styles });
  }
  // mark styles code points [start, end) of text c; a null value unmarks.
  // When loro-crdt refuses the mark, the action records its error.
  mark(p, c, start, end, k, v) {
    const t = this.docs[p].getText(c);
    const cur = t.toString();
    const start16 = u16(cur, start);
    const end16 = u16(cur, end);
    const a = { a: "mark", p, c, start, end, start16, end16, k, v: tag(v) };
    try {
      t.mark({ start: start16, end: end16 }, k, v);
    } catch (e) {
      a.refused = String(e).slice(0, 120);
    }
    this.actions.push(a);
  }
  unmark(p, c, start, end, k) {
    const t = this.docs[p].getText(c);
    const cur = t.toString();
    const start16 = u16(cur, start);
    const end16 = u16(cur, end);
    const a = { a: "unmark", p, c, start, end, start16, end16, k };
    try {
      t.unmark({ start: start16, end: end16 }, k);
    } catch (e) {
      a.refused = String(e).slice(0, 120);
    }
    this.actions.push(a);
  }
  // container is peer p's handle on container cid, or undefined when the
  // peer does not know it. A root is taken as getText and its siblings take
  // it, which makes it part of the document.
  container(p, cid) {
    const d = this.docs[p];
    const m = /^cid:root-(.*):(\w+)$/.exec(cid);
    if (m && !m[1].startsWith("🤝:")) {
      return { Text: () => d.getText(m[1]), Map: () => d.getMap(m[1]), List: () => d.getList(m[1]),
        MovableList: () => d.getMovableList(m[1]), Tree: () => d.getTree(m[1]), Counter: () => d.getCounter(m[1]) }[m[2]]();
    }
    return d.getContainerById(cid);
  }
  // x runs op on peer p's container cid and records it with args, and with
  // what it returned (out) or loro's error (refused). It records nothing
  // and returns undefined when the peer does not know the container.
  x(p, cid, op, args, fn) {
    const c = this.container(p, cid);
    if (c === undefined) return undefined;
    const a = { a: "x", p, cid, op, ...args };
    let out;
    try {
      out = fn(c);
    } catch (e) {
      a.refused = String(e).slice(0, 120);
    }
    if (out !== undefined && a.refused === undefined) a.out = out;
    this.actions.push(a);
    return a;
  }
  check() {
    const peers = this.docs.map((d) => {
      const json = tag(d.toJSON());
      const out = { json };
      // With deltas set, each of those texts the peer shows also gets its
      // toDelta, which carries the styles toJSON leaves out.
      if (this.deltas) {
        const delta = {};
        for (const c of this.deltas) {
          if (typeof json[c] === "string") delta[c] = tag(d.getText(c).toDelta());
        }
        out.delta = delta;
      }
      out.export = this.blob(d.export({ mode: "update" }));
      // With versions set, the peer's version vector and frontiers, as
      // oplogVersion().encode() and encodeFrontiers write them.
      if (this.versions) {
        out.vv = b64(d.oplogVersion().encode());
        out.fr = b64(encodeFrontiers(d.oplogFrontiers()));
      }
      return out;
    });
    this.actions.push({ a: "check", peers });
  }
  out() {
    return { ...this.head, peers: this.peers, actions: this.actions, blobs: this.blobs };
  }
}

// ---------------------------------------------------------------------------
// Random scripts.

const alphabet = ["a", "b", "c", "d", "😀", "é", "中", "🦀", "x"];
const word = (r, n) => { let w = ""; for (let j = n ?? 1 + Math.floor(r() * 3); j > 0; j--) w += pickOf(r, alphabet); return w; };
const mapKeys = ["a", "b", "c", "d"];
const scalar = (r) => {
  const pick = r();
  if (pick < 0.3) return Math.floor(r() * 2000) - 1000;
  if (pick < 0.42) return Math.floor(r() * 1000) / 8 + 0.5;
  if (pick < 0.6) return word(r);
  if (pick < 0.7) return r() < 0.5;
  if (pick < 0.76) return null;
  if (pick < 0.82) return new Uint8Array([Math.floor(r() * 256), Math.floor(r() * 256)]);
  if (pick < 0.92) return [Math.floor(r() * 10), word(r)];
  return { [pickOf(r, ["x", "y"])]: Math.floor(r() * 10) };
};
const counterSteps = [1, -1, 3, 7, -20, 134217727, 134217728, -134217728, 2 ** 40];
const peerSets = [[1, 2, 3], [7, 3, 1000], [2 ** 40 + 5, 9, 4]];

function editText(s, p, r) {
  const t = s.docs[p].getText("t");
  const n = points(t.toString()).length;
  const pick = r();
  if (pick < 0.35 || n === 0) {
    s.textIns(p, "t", Math.floor(r() * (n + 1)), word(r));
  } else if (pick < 0.5) {
    // Typing: one character after another.
    let k = Math.floor(r() * (n + 1));
    for (let j = 2 + Math.floor(r() * 4); j > 0; j--, k++) s.textIns(p, "t", k, word(r, 1));
  } else if (pick < 0.75) {
    const k = Math.floor(r() * n);
    s.textDel(p, "t", k, 1 + Math.floor(r() * Math.min(4, n - k)));
  } else {
    // A backspace run: one character at a time, leftwards.
    let k = 1 + Math.floor(r() * n);
    for (let j = 1 + Math.floor(r() * 4); j > 0 && k > 0; j--, k--) s.textDel(p, "t", k - 1, 1);
  }
}

function editMap(s, p, r) {
  const m = s.docs[p].getMap("m");
  const k = pickOf(r, mapKeys);
  const pick = r();
  if (pick < 0.25 && m.get(k) !== undefined) {
    // The value it has: loro makes no op.
    s.mapSet(p, "m", k, m.get(k));
  } else if (pick < 0.75) {
    s.mapSet(p, "m", k, scalar(r));
  } else {
    // Possibly absent: loro makes an op all the same.
    s.mapDel(p, "m", k);
  }
}

function editList(s, p, r) {
  const l = s.docs[p].getList("l");
  const n = l.length;
  if (r() < 0.6 || n === 0) {
    const items = [];
    for (let j = 1 + Math.floor(r() * 3); j > 0; j--) items.push(r() < 0.7 ? Math.floor(r() * 100) : scalar(r));
    s.listIns(p, "l", Math.floor(r() * (n + 1)), items);
  } else {
    const k = Math.floor(r() * n);
    s.listDel(p, "l", k, 1 + Math.floor(r() * Math.min(3, n - k)));
  }
}

function randomScript(seed) {
  const r = rng(seed * 2246822519);
  const n = 1 + (seed % 3);
  const s = new Script({ seed }, peerSets[Math.floor(seed / 3) % peerSets.length].slice(0, n));
  const saved = s.docs.map(() => []);
  const held = [];
  const delivered = [];
  const deliver = (p, blob) => {
    s.import(p, blob);
    delivered.push(blob);
    if (r() < 0.5) s.check();
  };
  if (r() < 0.2) s.acquire(Math.floor(r() * n), pickOf(r, ["text", "map", "list", "counter"]), "e");
  for (let step = 20 + Math.floor(r() * 30); step > 0; step--) {
    const p = Math.floor(r() * n);
    const pick = r();
    if (pick < 0.3) editText(s, p, r);
    else if (pick < 0.44) editMap(s, p, r);
    else if (pick < 0.58) editList(s, p, r);
    else if (pick < 0.65) s.inc(p, "n", r() < 0.85 ? Math.floor(r() * 21) - 10 : pickOf(r, counterSteps));
    else if (pick < 0.78) {
      const q = r();
      if (q < 0.6) s.commit(p);
      else if (q < 0.85) s.commit(p, { message: pickOf(r, ["m1", "m2", "fix"]) });
      else s.commit(p, { timestamp: Math.floor(r() * 3000) });
    } else {
      // A sync. With one peer, the peer takes back its own blobs.
      const src = p;
      const dst = n === 1 ? p : (p + 1 + Math.floor(r() * (n - 1))) % n;
      const q = r();
      if (q < 0.3) deliver(dst, s.export(src));
      else if (q < 0.5) deliver(dst, s.export(src, { mode: "update", from: s.docs[dst].version() }));
      else if (q < 0.62) saved[src].push(s.docs[src].version());
      else if (q < 0.77 && saved[src].length > 0) {
        // A tail from an older version of the source, delivered later: it
        // overlaps what the receiver has, or comes before its prefix.
        held.push([dst, s.export(src, { mode: "update", from: pickOf(r, saved[src]) })]);
      } else if (q < 0.9 && held.length > 0) {
        const [to, blob] = held.splice(Math.floor(r() * held.length), 1)[0];
        deliver(to, blob);
      } else if (q < 0.95 && delivered.length > 0) {
        deliver(dst, pickOf(r, delivered));
      } else {
        deliver(dst, s.snapshot(src));
      }
    }
  }
  for (const [to, blob] of held) s.import(to, blob);
  s.check();
  return s.out();
}

const corpus = [];
for (let seed = 1; seed <= 300; seed++) corpus.push(randomScript(seed));
writeFileSync(join(outDir, "script_corpus.json"), JSON.stringify(corpus) + "\n");
{
  let actions = 0, checks = 0, peers = 0, imports = 0;
  for (const c of corpus) {
    actions += c.actions.length;
    peers += c.peers.length;
    for (const a of c.actions) {
      if (a.a === "check") checks++;
      if (a.a === "import") imports++;
    }
  }
  console.log(`script_corpus.json: ${corpus.length} scripts, ${peers} peers, ${actions} actions, ${imports} imports, ${checks} checkpoints`);
}

// ---------------------------------------------------------------------------
// Named scripts, one or more per rule (review item numbers in brackets).

const named = [];
const add = (name, peers, build) => {
  const s = new Script({ name }, peers);
  build(s);
  s.check();
  named.push(s.out());
};

// [3] Import commits the open transaction: A's m.c is a change of its own,
// depending on what A imported, with lamport 1.
add("import_commits_txn", [1, 2], (s) => {
  s.mapSet(0, "m", "a", 1);
  s.mapSet(1, "m", "b", 2);
  s.import(0, s.export(1));
  s.mapSet(0, "m", "c", 3);
});

// [14] Pending: B gets A's tail before its prefix. B's next op has lamport
// 0 and no deps; once the prefix arrives and the tail is released, B's next
// op has lamport 4 and depends on A:3 and B:0. A partial overlap is trimmed.
add("pending_tail_before_prefix", [1, 2], (s) => {
  s.textIns(0, "t", 0, "ab");
  s.commit(0);
  const v = s.docs[0].version();
  const prefix = s.export(0);
  s.textIns(0, "t", 2, "cd");
  const tail = s.export(0, { mode: "update", from: v });
  s.import(1, tail);
  s.check();
  s.textIns(1, "t", 0, "X");
  s.import(1, prefix);
  s.textIns(1, "t", 0, "Y");
  s.check();
  // The same tail again, and the whole history: both already applied.
  s.import(1, tail);
  s.import(1, s.export(0));
});

// [14] A released change loses the part already applied. A's three
// commits merge into one change, so the tail from v1 is one change from
// counter 3 to 9. B's two tails wait for A's first ops, and the history up
// to v2 releases them: the tail from v1 is applied from 6 on. C has the
// history up to v2 when the tail from v1 arrives, and the import trims it at
// once.
add("pending_overlap_released", [1, 2, 3], (s) => {
  s.textIns(0, "t", 0, "abc");
  s.commit(0);
  const v1 = s.docs[0].version();
  s.textIns(0, "t", 3, "def");
  s.commit(0);
  const upToV2 = s.export(0);
  const v2 = s.docs[0].version();
  s.textIns(0, "t", 6, "ghi");
  s.commit(0);
  const fromV1 = s.export(0, { mode: "update", from: v1 });
  const fromV2 = s.export(0, { mode: "update", from: v2 });
  s.import(1, fromV2);
  s.import(1, fromV1);
  s.check();
  s.import(1, upToV2);
  s.check();
  s.import(2, upToV2);
  s.import(2, fromV1);
  s.textDel(1, "t", 2, 5);
});

// [5] Timestamps: a change takes the requested timestamp, or 0, raised to
// the latest timestamp of what it depends on; a commit merges into the
// change before it within 1000 s of that change's own timestamp.
add("timestamps_floor_and_interval", [1, 2], (s) => {
  s.mapSet(0, "m", "a", 1);
  s.commit(0, { timestamp: 42 });
  s.import(1, s.export(0));
  s.mapSet(1, "m", "b", 2);
  s.commit(1);
  s.mapSet(0, "m", "c", 3);
  s.commit(0, { timestamp: 100 });
  s.mapSet(0, "m", "d", 4);
  s.commit(0, { timestamp: 1042 });
  s.mapSet(0, "m", "e", 5);
  s.commit(0, { timestamp: 1043 });
  s.mapSet(0, "m", "f", 6);
  s.commit(0, { timestamp: 10 });
  s.mergeInterval(1, 5);
  s.mapSet(1, "m", "g", 7);
  s.commit(1, { timestamp: 50 });
  s.mapSet(1, "m", "h", 8);
  s.commit(1, { timestamp: 56 });
  s.mapSet(1, "m", "i", 9);
  s.commit(1, { timestamp: 61 });
});

// [4] Change merging: commits merge while Change::can_merge_right holds and
// the block has room. 1400 one-key commits make changes of 1362 and 38 ops;
// a commit message, or a different one, starts a new change.
add("change_merge_block_limit", [1], (s) => {
  for (let i = 0; i < 1400; i++) {
    s.mapSet(0, "m", "k" + i, i);
    s.commit(0);
  }
  s.mapSet(0, "m", "x", 1);
  s.commit(0, { message: "a" });
  s.mapSet(0, "m", "y", 1);
  s.commit(0, { message: "a" });
  s.mapSet(0, "m", "z", 1);
  s.commit(0, { message: "b" });
});

// [4] A change too big for one block is split as loro splits it; typing
// into a full block still folds into its last op.
add("change_split_and_fold", [1, 2], (s) => {
  s.textIns(0, "t", 0, "x".repeat(5000));
  s.commit(0);
  for (let i = 0; i < 40; i++) {
    s.textIns(0, "t", 5000 + i, "a");
    s.commit(0);
  }
  for (let i = 0; i < 1100; i++) s.listIns(0, "l", i, [i]);
  s.commit(0);
  s.import(1, s.export(0));
});

// [2] Ops fold only when they are adjacent local ops: B imports A's two
// exports and keeps two inserts where A has one; B's own typing folds only
// within one arena buffer.
add("fold_local_ops_only", [1, 2], (s) => {
  s.textIns(0, "t", 0, "ab");
  s.import(1, s.export(0));
  s.textIns(0, "t", 2, "cd");
  s.import(1, s.export(0));
  s.textIns(1, "t", 4, "e");
  s.textIns(1, "t", 5, "f");
  for (let i = 0; i < 40; i++) {
    s.textIns(1, "t", 6 + i, "g");
    s.commit(1);
  }
  s.listIns(0, "l", 0, [1, 2, 3]);
  s.import(1, s.export(0));
  s.listIns(1, "l", 3, [4, 5]);
});

// [7] Text deletes: split at id gaps and at style anchors, rightmost first;
// backspaces fold into one backwards delete when their ids continue.
add("text_delete_split", [1, 2], (s) => {
  s.textIns(0, "t", 0, "ab");
  s.textIns(0, "t", 1, "X");
  s.textDel(0, "t", 0, 3);
  s.textIns(0, "t", 0, "abc");
  s.textDel(0, "t", 2, 1);
  s.textDel(0, "t", 1, 1);
  s.textIns(0, "u", 0, "hello world");
  s.textDel(0, "u", 10, 1);
  s.textDel(0, "u", 9, 1);
  s.textDel(0, "u", 8, 1);
  s.textDel(0, "u", 3, 4);
  // Anchors: a document marked elsewhere, imported, then edited across and
  // at the edges of its marks.
  const ext = new LoroDoc();
  ext.setPeerId(77n);
  const et = ext.getText("r");
  et.insert(0, "abcdefgh");
  et.mark({ start: 1, end: 3 }, "bold", true);
  et.mark({ start: 4, end: 6 }, "link", "x");
  et.mark({ start: 2, end: 7 }, "underline", true);
  ext.commit();
  s.import(1, s.external(ext.export({ mode: "update" })));
  s.textIns(1, "r", 3, "B");
  s.textIns(1, "r", 1, "C");
  s.textIns(1, "r", 7, "L");
  s.textIns(1, "r", 0, "S");
  s.textIns(1, "r", 12, "E");
  s.check();
  s.textDel(1, "r", 1, 9);
});

// [11] Map: setting a key to the value it has makes no op, deleting an
// absent key makes one.
add("map_noop_rules", [1, 2], (s) => {
  s.mapSet(0, "m", "x", 1);
  s.mapSet(0, "m", "x", 1);
  s.mapSet(0, "m", "f", 1.5);
  s.mapSet(0, "m", "f", 1.5);
  s.mapSet(0, "m", "s", "v");
  s.mapSet(0, "m", "s", "v");
  s.mapSet(0, "m", "n", null);
  s.mapSet(0, "m", "n", null);
  s.mapSet(0, "m", "l", [1, "a"]);
  s.mapSet(0, "m", "l", [1, "a"]);
  s.mapSet(0, "m", "b", new Uint8Array([1, 2]));
  s.mapSet(0, "m", "b", new Uint8Array([1, 2]));
  s.mapDel(0, "m", "absent");
  s.mapDel(0, "m", "x");
  s.mapDel(0, "m", "x");
  s.commit(0);
  s.import(1, s.export(0));
  s.mapSet(1, "m", "s", "v");
  s.mapSet(1, "m", "f", 1.5);
  s.mapSet(1, "m", "s", "w");
});

// [12] Counter: an increment is an I64 when whole and below 2^27 in
// magnitude, an F64 otherwise.
add("counter_encoding", [1, 2], (s) => {
  for (const v of [1, -1, 0, 134217727, -134217727, 134217728, -134217728, 2 ** 40, -(2 ** 52)]) s.inc(0, "n", v);
  // Past the int64 range: toJSON shows a double. Each partial sum is exact,
  // so the order loro adds them in does not matter.
  for (const v of [2 ** 64, -(2 ** 60)]) s.inc(0, "big", v);
  s.commit(0);
  s.import(1, s.export(0));
  s.inc(1, "n", 5);
  s.inc(1, "big", 2 ** 62);
});

// [15] Typed roots: a Text and a Map of one name are two containers, and a
// root shows as soon as a handle to it is taken.
add("typed_roots_and_acquire", [1, 2], (s) => {
  s.acquire(0, "text", "empty");
  s.acquire(0, "list", "l0");
  s.acquire(0, "counter", "c0");
  s.textIns(0, "same", 0, "text");
  s.mapSet(0, "same", "k", 1);
  s.check();
  s.import(1, s.export(0));
  s.acquire(1, "map", "own");
  s.textIns(1, "same", 4, "!");
  s.check();
  s.import(1, s.snapshot(0));
});

add("snapshot_into_fresh_peer", [1, 2, 3], (s) => {
  s.acquire(0, "text", "empty");
  s.textIns(0, "t", 0, "hello");
  s.mapSet(0, "m", "k", "v");
  s.commit(0, { message: "first" });
  const snap = s.snapshot(0);
  s.import(1, snap);
  s.acquire(2, "map", "mine");
  s.import(2, snap);
  s.check();
  s.textIns(1, "t", 5, "!");
  s.import(2, snap);
});

// [16] A handle taken before an import edits the state the import made.
add("handle_survives_import", [1, 2], (s) => {
  s.textIns(1, "t", 0, "b");
  s.textIns(0, "t", 0, "a");
  s.import(1, s.export(0));
  s.textIns(1, "t", 2, "c");
  s.listIns(1, "l", 0, [1]);
  s.import(1, s.export(0));
  s.listIns(1, "l", 1, [2]);
});

// [import] Two writers that share a peer id: A's blob reuses B's op id 0@1
// for other content, and loro refuses it whole. A's state and its next
// counter stay as they were.
add("reused_op_id_refused", [1, 1], (s) => {
  s.textIns(0, "t", 0, "a");
  s.commit(0);
  s.textIns(1, "t", 0, "bc");
  s.commit(1);
  s.importRefused(0, s.export(1));
  s.check();
  s.textIns(0, "t", 1, "x");
  // The same id with the same content is no conflict.
  s.import(1, s.export(1));
});

// [frontiers] Many peers in one frontier: loro writes a change's deps in
// the order of its hash map, which here holds 9, 17 and 33 peers, filled
// in different orders and then cut back as changes on top of them arrive.
const frontierScript = (name, peers, order, rounds) => add(name, [1000, ...peers], (s) => {
  const blobs = peers.map((p, i) => {
    s.mapSet(i + 1, "m", String(p), 0);
    return s.export(i + 1);
  });
  for (const i of order) s.import(0, blobs[i]);
  s.mapSet(0, "m", "result", 1);
  s.commit(0);
  for (let round = 0; round < rounds; round++) {
    // Some peers take the hub's history and write on top of it; the hub
    // takes them back, so their ids replace the hub's own in its
    // frontiers, and the others' stay.
    const hub = s.export(0);
    const back = [];
    for (let k = round; k < peers.length; k += 2 + round) {
      s.import(k + 1, hub);
      s.mapSet(k + 1, "m", String(peers[k]), round + 1);
      back.push(s.export(k + 1));
    }
    for (const b of back.reverse()) s.import(0, b);
    s.mapSet(0, "m", "round", round + 1);
    s.commit(0);
  }
  s.check();
});
{
  const nine = [12, 29, 46, 47, 64, 81, 98, 115, 132];
  frontierScript("frontiers_9_peers", nine, nine.map((_, i) => i), 2);
  frontierScript("frontiers_9_peers_reversed", nine, nine.map((_, i) => 8 - i), 2);
  const seventeen = Array.from({ length: 17 }, (_, i) => 3 + 7 * i);
  frontierScript("frontiers_17_peers", seventeen, [16, 0, 15, 1, 14, 2, 13, 3, 12, 4, 11, 5, 10, 6, 9, 7, 8], 3);
  const r = rng(33);
  const thirtyThree = Array.from({ length: 33 }, () => 1 + Math.floor(r() * 2 ** 40));
  const order = thirtyThree.map((_, i) => i).sort(() => r() - 0.5);
  frontierScript("frontiers_33_peers", thirtyThree, order, 3);
}

writeFileSync(join(outDir, "script_named.json"), JSON.stringify(named) + "\n");
console.log(`script_named.json: ${named.length} scripts: ${named.map((n) => n.name).join(", ")}`);

// ---------------------------------------------------------------------------
// Crowd scripts: 4 to 12 peers, so that frontiers hold many peers and loro's
// hash map grows, rehashes and drops entries as random syncs move them. A
// sync here gathers the exports of several peers into one.

function crowdScript(seed) {
  const r = rng(seed * 3266489917);
  const n = 4 + (seed % 9);
  const ids = Array.from({ length: n }, () => (r() < 0.5 ? 1 + Math.floor(r() * 200) : 1 + Math.floor(r() * 2 ** 40)));
  const peers = [...new Set(ids)];
  while (peers.length < n) peers.push(1000 + peers.length);
  const s = new Script({ seed }, peers);
  const saved = s.docs.map(() => []);
  const held = [];
  for (let step = 30 + Math.floor(r() * 30); step > 0; step--) {
    const p = Math.floor(r() * n);
    const pick = r();
    if (pick < 0.25) editText(s, p, r);
    else if (pick < 0.45) editMap(s, p, r);
    else if (pick < 0.55) editList(s, p, r);
    else if (pick < 0.6) s.inc(p, "n", Math.floor(r() * 21) - 10);
    else if (pick < 0.68) s.commit(p, r() < 0.3 ? { message: pickOf(r, ["m1", "m2"]) } : undefined);
    else {
      const q = r();
      if (q < 0.55) {
        // Gather: p takes the exports of up to half the others.
        for (let k = 1 + Math.floor(r() * Math.max(1, n / 2)); k > 0; k--) {
          const src = (p + 1 + Math.floor(r() * (n - 1))) % n;
          s.import(p, s.export(src, { mode: "update", from: s.docs[p].version() }));
        }
        if (r() < 0.3) s.check();
      } else if (q < 0.7) {
        saved[p].push(s.docs[p].version());
      } else if (q < 0.85 && saved[p].length > 0) {
        held.push([(p + 1 + Math.floor(r() * (n - 1))) % n, s.export(p, { mode: "update", from: pickOf(r, saved[p]) })]);
      } else if (held.length > 0) {
        const [to, blob] = held.splice(Math.floor(r() * held.length), 1)[0];
        s.import(to, blob);
      } else {
        s.import((p + 1) % n, s.export(p));
      }
    }
  }
  for (const [to, blob] of held) s.import(to, blob);
  s.check();
  return s.out();
}

const crowd = [];
for (let seed = 1; seed <= 60; seed++) crowd.push(crowdScript(seed));
writeFileSync(join(outDir, "script_crowd_corpus.json"), JSON.stringify(crowd) + "\n");
{
  let actions = 0, checks = 0, peers = 0, imports = 0;
  for (const c of crowd) {
    actions += c.actions.length;
    peers += c.peers.length;
    for (const a of c.actions) {
      if (a.a === "check") checks++;
      if (a.a === "import") imports++;
    }
  }
  console.log(`script_crowd_corpus.json: ${crowd.length} scripts, ${peers} peers, ${actions} actions, ${imports} imports, ${checks} checkpoints`);
}

// A shallow snapshot, which loro-go refuses: its history starts past the
// beginning.
{
  const d = new LoroDoc();
  d.setPeerId(1n);
  d.getText("t").insert(0, "hello");
  d.commit();
  const f = d.frontiers();
  d.getText("t").insert(5, " world");
  d.commit();
  writeFileSync(join(outDir, "shallow_snapshot.bin"), d.export({ mode: "shallow-snapshot", frontiers: f }));
}

// ---------------------------------------------------------------------------
// Rich text: marks and unmarks over all four expand rules, keys configured
// per peer (custom keys, colon keys, keys left out), style values of every
// shape, concurrent marks, inserts at the edges of styled ranges and deletes
// across anchors. Checkpoints record each peer's toDelta of "t" (and of "r"
// in the named scripts) beside toJSON.
//
//   ../fixtures/script_rich_corpus.json  300 random scripts, 1 to 3 peers
//   ../fixtures/script_rich_named.json   one or more small scripts per mark rule
//   config_style {styles: {key: expand}}   mark {c, start, end, start16, end16, k, v, refused?}
//   unmark {c, start, end, start16, end16, k, refused?}   check peers also carry {delta: {c: toDelta}}

const expands = ["before", "after", "both", "none"];
const richKeys = ["bold", "italic", "link", "comment:1", "comment:2", "a", "b", "c", "d", "c:x", "u"];
const richValue = (r) => {
  const pick = r();
  if (pick < 0.25) return true;
  if (pick < 0.3) return false;
  if (pick < 0.45) return word(r);
  if (pick < 0.6) return Math.floor(r() * 100) - 50;
  if (pick < 0.7) return Math.floor(r() * 1000) / 8 + 0.5;
  if (pick < 0.85) return { k: Math.floor(r() * 5) };
  return [Math.floor(r() * 5), word(r)];
};
// richConfig is a peer's style configuration: the defaults it keeps, and
// custom keys with an expand rule of their own. Keys it leaves out ("u",
// and now and then a default) cannot be marked by that peer.
const richConfig = (r) => {
  const styles = {};
  for (const k of ["bold", "italic", "link", "comment"]) if (r() < 0.85) styles[k] = pickOf(r, expands);
  for (const k of ["a", "b", "c", "d"]) if (r() < 0.9) styles[k] = pickOf(r, expands);
  return styles;
};

function richScript(seed) {
  const r = rng(seed * 2654435761);
  const n = 1 + (seed % 3);
  const s = new Script({ seed }, peerSets[Math.floor(seed / 3) % peerSets.length].slice(0, n));
  s.deltas = ["t"];
  for (let p = 0; p < n; p++) if (r() < 0.8) s.configStyle(p, richConfig(r));
  s.textIns(0, "t", 0, word(r, 6 + Math.floor(r() * 6)));
  const edges = s.docs.map(() => []);
  const held = [];
  const textLen = (p) => points(s.docs[p].getText("t").toString()).length;
  for (let step = 25 + Math.floor(r() * 30); step > 0; step--) {
    const p = Math.floor(r() * n);
    const len = textLen(p);
    const pick = r();
    if (pick < 0.22 || len < 2) {
      // Inserts land at the edge of a range this peer marked now and then.
      let k = Math.floor(r() * (len + 1));
      if (edges[p].length > 0 && r() < 0.5) k = Math.min(len, pickOf(r, edges[p]));
      s.textIns(p, "t", k, word(r));
    } else if (pick < 0.32) {
      const k = Math.floor(r() * len);
      s.textDel(p, "t", k, 1 + Math.floor(r() * Math.min(4, len - k)));
    } else if (pick < 0.58) {
      const a = Math.floor(r() * (len - 1));
      const b = a + 1 + Math.floor(r() * Math.min(6, len - a));
      edges[p].push(a, b);
      s.mark(p, "t", a, Math.min(b, len), pickOf(r, richKeys), richValue(r));
    } else if (pick < 0.68) {
      const a = Math.floor(r() * (len - 1));
      const b = a + 1 + Math.floor(r() * Math.min(6, len - a));
      s.unmark(p, "t", a, Math.min(b, len), pickOf(r, richKeys));
    } else if (pick < 0.76) {
      if (r() < 0.3) s.commit(p, { message: pickOf(r, ["m1", "m2"]) });
      else s.commit(p);
    } else if (pick < 0.78) {
      s.configStyle(p, richConfig(r));
    } else {
      const src = p;
      const dst = n === 1 ? p : (p + 1 + Math.floor(r() * (n - 1))) % n;
      const q = r();
      if (q < 0.4) s.import(dst, s.export(src));
      else if (q < 0.7) s.import(dst, s.export(src, { mode: "update", from: s.docs[dst].version() }));
      else if (q < 0.85) held.push([dst, s.export(src, { mode: "update", from: s.docs[dst].version() })]);
      else if (held.length > 0) {
        const [to, blob] = held.splice(Math.floor(r() * held.length), 1)[0];
        s.import(to, blob);
      }
      if (r() < 0.5) s.check();
    }
  }
  for (const [to, blob] of held) s.import(to, blob);
  s.check();
  return s.out();
}

const rich = [];
for (let seed = 1; seed <= 300; seed++) rich.push(richScript(seed));
writeFileSync(join(outDir, "script_rich_corpus.json"), JSON.stringify(rich) + "\n");
{
  let actions = 0, checks = 0, peers = 0, marks = 0, unmarks = 0, refused = 0;
  for (const c of rich) {
    actions += c.actions.length;
    peers += c.peers.length;
    for (const a of c.actions) {
      if (a.a === "check") checks++;
      if (a.a === "mark") marks++;
      if (a.a === "unmark") unmarks++;
      if (a.refused) refused++;
    }
  }
  console.log(`script_rich_corpus.json: ${rich.length} scripts, ${peers} peers, ${actions} actions, ${marks} marks, ${unmarks} unmarks, ${refused} refused, ${checks} checkpoints`);
}

const richNamed = [];
const addRich = (name, peers, build) => {
  const s = new Script({ name }, peers);
  s.deltas = ["t", "r"];
  build(s);
  s.check();
  richNamed.push(s.out());
};

// [8] A mark the range already has makes no op: the same mark again, one
// inside it, and one over it and its anchors. Another value, or a range
// that reaches past it, makes one.
addRich("mark_skip_applied", [1, 2], (s) => {
  s.textIns(0, "t", 0, "hello world");
  s.mark(0, "t", 0, 5, "bold", true);
  s.mark(0, "t", 0, 5, "bold", true);
  s.mark(0, "t", 1, 3, "bold", true);
  s.mark(0, "t", 0, 6, "bold", true);
  s.mark(0, "t", 2, 4, "bold", "x");
  s.mark(0, "t", 2, 4, "bold", "x");
  s.mark(0, "t", 0, 3, "link", { href: 1 });
  s.mark(0, "t", 0, 3, "link", { href: 1 });
  s.mark(0, "t", 0, 3, "link", { href: 2 });
  s.mark(0, "t", 5, 8, "italic", 1);
  s.mark(0, "t", 5, 8, "italic", 1.5);
  s.check();
  s.import(1, s.export(0));
  s.mark(1, "t", 0, 6, "bold", true);
  s.mark(1, "t", 3, 4, "bold", "x");
});

// [8] Unmarking a key the range lacks makes no op, configured or not;
// unmarking one it has makes one, and a second time none. Unmarking a key
// the range has but the peer has not configured is refused.
addRich("unmark_absent_key", [1, 2], (s) => {
  s.textIns(0, "t", 0, "abcdefgh");
  s.unmark(0, "t", 0, 4, "bold");
  s.unmark(0, "t", 0, 4, "zzz");
  s.mark(0, "t", 2, 6, "link", "x");
  s.unmark(0, "t", 0, 2, "link");
  s.unmark(0, "t", 0, 4, "link");
  s.unmark(0, "t", 0, 4, "link");
  s.mark(0, "t", 6, 8, "italic", true);
  s.import(1, s.export(0));
  s.configStyle(1, { bold: "after" });
  s.unmark(1, "t", 5, 8, "italic");
  s.unmark(1, "t", 0, 8, "underline");
});

// [8] Marking a key the peer has not configured is refused and makes no
// op, until the key is configured. The check comes after the one for a
// mark the range already has: a peer that has not configured a key can
// repeat another peer's mark of it, which makes no op, but not change it.
addRich("mark_unconfigured_key", [1, 2], (s) => {
  s.textIns(0, "t", 0, "abcdef");
  s.mark(0, "t", 0, 3, "zzz", 1);
  s.mark(0, "t", 0, 3, "zzz:1", 1);
  s.configStyle(0, { zzz: "both", bold: "after" });
  s.mark(0, "t", 0, 3, "zzz", 1);
  s.mark(0, "t", 1, 4, "zzz:1", 2);
  s.mark(0, "t", 2, 5, "italic", true);
  s.import(1, s.export(0));
  s.mark(1, "t", 0, 3, "zzz", 1);
  s.mark(1, "t", 1, 2, "zzz", 1);
  s.mark(1, "t", 0, 3, "zzz", 2);
  s.unmark(1, "t", 0, 3, "zzz");
});

// [8] The info byte of a mark is its key's expand rule; an unmark's is the
// rule reversed: Before and After stay, Both and None swap. Text inserted
// at each edge of each mark lands where the rules put it.
addRich("expand_rules_and_flags", [1, 2], (s) => {
  s.configStyle(0, { a: "before", b: "after", c: "both", d: "none" });
  s.configStyle(1, { a: "before", b: "after", c: "both", d: "none" });
  s.textIns(0, "t", 0, "0123456789abcdefghij");
  s.mark(0, "t", 1, 3, "a", true);
  s.mark(0, "t", 5, 7, "b", true);
  s.mark(0, "t", 9, 11, "c", true);
  s.mark(0, "t", 13, 15, "d", true);
  for (const k of [1, 5, 9, 13]) s.textIns(0, "t", k, "<");
  s.check();
  for (const k of [5, 10, 15, 20]) s.textIns(0, "t", k, ">");
  s.check();
  s.import(1, s.export(0));
  s.unmark(1, "t", 0, 24, "a");
  s.unmark(1, "t", 0, 24, "b");
  s.unmark(1, "t", 0, 24, "c");
  s.unmark(1, "t", 0, 24, "d");
  s.textIns(1, "t", 0, "^");
  s.textIns(1, "t", 25, "$");
  s.mark(1, "t", 2, 4, "link", 1);
  s.unmark(1, "t", 2, 4, "link");
});

// [8] A key with a colon is marked with the configuration of the part
// before it: "comment:1" as "comment". Overlapping marks of one key decide
// by (lamport, peer); an unmark of one colon key leaves the other.
addRich("colon_keys", [1, 2], (s) => {
  s.configStyle(0, { comment: "before", x: "both" });
  s.configStyle(1, { comment: "none", x: "after" });
  s.textIns(0, "t", 0, "commentary here");
  s.mark(0, "t", 0, 6, "comment:1", { by: "a" });
  s.mark(0, "t", 3, 9, "comment:2", { by: "b" });
  s.mark(0, "t", 4, 8, "x:y", [1, 2]);
  s.mark(0, "t", 0, 2, "bold", true);
  s.import(1, s.export(0));
  s.mark(1, "t", 2, 10, "comment:1", "edited");
  s.unmark(1, "t", 0, 15, "comment:2");
  s.textIns(1, "t", 0, "+");
  s.textIns(1, "t", 7, "+");
});

// [8] ConfigTextStyle replaces the whole configuration: a peer that
// configures only x can no longer mark bold, until it configures bold.
addRich("config_replaces_defaults", [1], (s) => {
  s.textIns(0, "t", 0, "abcdef");
  s.mark(0, "t", 0, 2, "bold", true);
  s.configStyle(0, { x: "after" });
  s.mark(0, "t", 2, 4, "bold", true);
  s.mark(0, "t", 2, 4, "x", true);
  s.unmark(0, "t", 0, 1, "bold");
  s.configStyle(0, { bold: "none", x: "after" });
  s.mark(0, "t", 3, 6, "bold", true);
  s.unmark(0, "t", 0, 1, "bold");
});

// Marks by three peers at once on overlapping ranges, of one key and of
// several; after the sync, marks that the merged text already has make no
// op.
addRich("concurrent_marks", [1, 2, 3], (s) => {
  s.textIns(0, "t", 0, "shared text body");
  for (const p of [1, 2]) s.import(p, s.export(0));
  s.mark(0, "t", 0, 8, "bold", true);
  s.mark(1, "t", 4, 12, "bold", "two");
  s.mark(2, "t", 2, 6, "italic", { k: 3 });
  s.mark(2, "t", 6, 14, "bold", 3);
  s.textIns(1, "t", 4, "X");
  const blobs = [0, 1, 2].map((p) => s.export(p));
  for (const p of [0, 1, 2]) for (const b of blobs) s.import(p, b);
  s.check();
  s.mark(0, "t", 7, 9, "bold", 3);
  s.mark(1, "t", 0, 3, "bold", true);
  s.unmark(2, "t", 0, 17, "italic");
});

// A mark's ends go where text inserted there would go, among the anchors
// of the marks already there; a false or null start anchor keeps new text
// before it. Deletes across those anchors split around them.
addRich("mark_ends_among_anchors", [1, 2], (s) => {
  s.configStyle(0, { a: "before", b: "after", c: "both", d: "none" });
  s.textIns(0, "t", 0, "abcdefghij");
  s.mark(0, "t", 2, 5, "a", true);
  s.mark(0, "t", 2, 5, "b", 1);
  s.mark(0, "t", 5, 8, "c", "x");
  s.mark(0, "t", 5, 8, "d", false);
  s.mark(0, "t", 2, 8, "bold", true);
  s.mark(0, "t", 5, 5 + 1, "italic", true);
  s.unmark(0, "t", 4, 6, "a");
  s.textIns(0, "t", 5, "|");
  s.textIns(0, "t", 2, "|");
  s.check();
  s.import(1, s.export(0));
  s.textDel(1, "t", 1, 9);
  s.mark(1, "t", 0, 2, "underline", true);
  s.textIns(1, "t", 1, "*");
});

// A mark with a null value is an unmark.
addRich("mark_null_is_unmark", [1], (s) => {
  s.textIns(0, "t", 0, "abcdef");
  s.mark(0, "t", 0, 4, "link", "x");
  s.mark(0, "t", 1, 3, "link", null);
  s.mark(0, "t", 4, 6, "link", null);
});

writeFileSync(join(outDir, "script_rich_named.json"), JSON.stringify(richNamed) + "\n");
console.log(`script_rich_named.json: ${richNamed.length} scripts: ${richNamed.map((n) => n.name).join(", ")}`);

// ---------------------------------------------------------------------------
// Structures: MovableList and Tree, containers stored in containers (map
// values, list and MovableList items, MovableList sets, tree node meta
// maps) at any depth, mergeable children, and edits through handles whose
// container was deleted. Actions name their container by id (action "x").
//
//   ../fixtures/script_struct_corpus.json        300 random scripts, 1 to 3 peers
//   ../fixtures/script_struct_crowd_corpus.json  60 random scripts, 4 to 12 peers
//   ../fixtures/script_struct_named.json         one or more small scripts per rule
//   x {cid, op, ...args, out?, refused?}: op is text_ins {pos, pos16, s},
//     text_del {pos, n, pos16, n16}, map_set {k, v}, map_del {k},
//     list_ins {pos, v}, list_del {pos, n}, ml_ins {pos, v}, ml_del {pos, n},
//     ml_move {from, to}, ml_set {pos, v}, inc {v}, tree_create {parent,
//     index?} out the node, tree_move {node, parent, index}, tree_delete
//     {node}, meta {node} out its map, child {how: map|list|ml_ins|ml_set|
//     mergeable, k or pos, kind} out the child, read {} out its toJSON.
//     A parent of null is the top level.

const structKinds = ["Text", "Map", "List", "MovableList", "Tree", "Counter"];
const mapEdgeValues = ["", [], {}, "a longer value é😀", [1, "two", [3]], { k: "", l: [] }];
const ML = "cid:root-ml:MovableList", TR = "cid:root-tr:Tree", MP = "cid:root-m:Map", LS = "cid:root-l:List";
const newContainer = (k) =>
  ({ Text: () => new LoroText(), Map: () => new LoroMap(), List: () => new LoroList(),
    MovableList: () => new LoroMovableList(), Tree: () => new LoroTree(), Counter: () => new LoroCounter() })[k]();
const kindOfCid = (cid) => /:(\w+)$/.exec(cid)[1];
const treeNodes = (t) => {
  const out = [];
  const walk = (ns) => ns.forEach((n) => { out.push(n.id); walk(n.children); });
  walk(t.toJSON());
  return out;
};

// structEdit makes one random edit to a container peer p knows: pool holds
// every container id the script has seen, nodes every tree node created.
function structEdit(s, p, r, pool, nodes) {
  const cid = pickOf(r, pool);
  const c = s.container(p, cid);
  if (c === undefined) return;
  const kind = kindOfCid(cid);
  const add = (a) => { if (a && a.out !== undefined && /^cid:/.test(a.out)) pool.push(a.out); };
  const child = (how, args, fn) => {
    const k = pickOf(r, structKinds);
    add(s.x(p, cid, "child", { how, ...args, kind: k }, (h) => fn(h, k).id));
  };
  const q = r();
  switch (kind) {
    case "Text": {
      const n = points(c.toString()).length;
      if (q < 0.6 || n === 0) {
        const pos = Math.floor(r() * (n + 1));
        const str = word(r);
        s.x(p, cid, "text_ins", { pos, pos16: u16(c.toString(), pos), s: str }, (h) => { h.insert(u16(h.toString(), pos), str); });
      } else {
        const pos = Math.floor(r() * n);
        const k = 1 + Math.floor(r() * Math.min(3, n - pos));
        const cur = c.toString();
        s.x(p, cid, "text_del", { pos, n: k, pos16: u16(cur, pos), n16: u16(cur, pos + k) - u16(cur, pos) },
          (h) => { h.delete(u16(cur, pos), u16(cur, pos + k) - u16(cur, pos)); });
      }
      break;
    }
    case "Map": {
      const k = pickOf(r, ["a", "b", "c"]);
      if (q < 0.35) {
        // Every fifth action writes one of the values a map holds whole that
        // scalar() never makes: an empty string, list or map, and longer ones.
        const v = s.actions.length % 5 === 0 ? mapEdgeValues[(s.actions.length / 5) % mapEdgeValues.length] : scalar(r);
        s.x(p, cid, "map_set", { k, v: tag(v) }, (h) => { h.set(k, v); });
      } else if (q < 0.45) s.x(p, cid, "map_del", { k }, (h) => { h.delete(k); });
      else if (q < 0.75) child("map", { k }, (h, kind) => h.setContainer(k, newContainer(kind)));
      else child("mergeable", { k }, (h, kind) => h["ensureMergeable" + kind](k));
      break;
    }
    case "List": {
      const n = c.length;
      if (q < 0.45 || n === 0) {
        const pos = Math.floor(r() * (n + 1));
        const v = [Math.floor(r() * 50)];
        s.x(p, cid, "list_ins", { pos, v }, (h) => { h.insert(pos, v[0]); });
      } else if (q < 0.65) {
        const pos = Math.floor(r() * n);
        const k = 1 + Math.floor(r() * Math.min(2, n - pos));
        s.x(p, cid, "list_del", { pos, n: k }, (h) => { h.delete(pos, k); });
      } else {
        const pos = Math.floor(r() * (n + 1));
        child("list", { pos }, (h, kind) => h.insertContainer(pos, newContainer(kind)));
      }
      break;
    }
    case "MovableList": {
      const n = c.length;
      if (q < 0.3 || n === 0) {
        const pos = Math.floor(r() * (n + 1));
        const v = [Math.floor(r() * 50)];
        if (r() < 0.3) v.push(word(r));
        s.x(p, cid, "ml_ins", { pos, v: tag(v) }, (h) => { v.forEach((x, j) => h.insert(pos + j, x)); });
      } else if (q < 0.45) {
        const pos = Math.floor(r() * n);
        const k = 1 + Math.floor(r() * Math.min(2, n - pos));
        s.x(p, cid, "ml_del", { pos, n: k }, (h) => { h.delete(pos, k); });
      } else if (q < 0.65) {
        const from = Math.floor(r() * n);
        const to = r() < 0.1 ? from : Math.floor(r() * n);
        s.x(p, cid, "ml_move", { from, to }, (h) => { h.move(from, to); });
      } else if (q < 0.8) {
        const pos = Math.floor(r() * n);
        const v = r() < 0.2 ? h0(c, pos) : scalar(r);
        s.x(p, cid, "ml_set", { pos, v: tag(v) }, (h) => { h.set(pos, v); });
      } else if (q < 0.9) {
        const pos = Math.floor(r() * (n + 1));
        child("ml_ins", { pos }, (h, kind) => h.insertContainer(pos, newContainer(kind)));
      } else {
        const pos = Math.floor(r() * n);
        child("ml_set", { pos }, (h, kind) => h.setContainer(pos, newContainer(kind)));
      }
      break;
    }
    case "Tree": {
      const alive = treeNodes(c);
      const known = nodes.get(cid) ?? [];
      const anyNode = () => (known.length > 0 && r() < 0.15 ? pickOf(r, known) : alive.length > 0 ? pickOf(r, alive) : null);
      const kidsOf = (parent) => (parent === null ? c.roots() : c.getNodeByID(parent)?.children() ?? []);
      if (q < 0.4 || alive.length === 0) {
        const parent = r() < 0.4 ? null : anyNode();
        const withIndex = r() < 0.6;
        const kids = (kidsOf(parent) ?? []).length;
        const index = Math.floor(r() * (kids + 1));
        const a = s.x(p, cid, "tree_create", withIndex ? { parent, index } : { parent },
          (h) => (withIndex ? h.createNode(parent ?? undefined, index) : h.createNode(parent ?? undefined)).id);
        if (a && a.out) nodes.set(cid, [...known, a.out]);
      } else if (q < 0.7) {
        const node = anyNode();
        const parent = r() < 0.3 ? null : anyNode();
        const kids = (kidsOf(parent) ?? []).length;
        const index = Math.floor(r() * (kids + 1));
        s.x(p, cid, "tree_move", { node, parent, index }, (h) => { h.move(node, parent ?? undefined, index); });
      } else if (q < 0.8) {
        const node = anyNode();
        s.x(p, cid, "tree_delete", { node }, (h) => { h.delete(node); });
      } else {
        const node = anyNode();
        add(s.x(p, cid, "meta", { node }, (h) => h.getNodeByID(node).data.id));
      }
      break;
    }
    case "Counter": {
      const v = Math.floor(r() * 21) - 10;
      s.x(p, cid, "inc", { v }, (h) => { h.increment(v); });
      break;
    }
  }
  if (r() < 0.05) {
    const cid2 = pickOf(r, pool);
    if (s.container(p, cid2) !== undefined) s.x(p, cid2, "read", {}, (h) => tag(h.toJSON()));
  }
}
// structRace has every peer, before any of them syncs, move the same item
// of the root MovableList and add a node at one place of the root tree:
// concurrent moves of one item leave positions nobody points at, so user
// and op indexes part, and concurrent nodes at one place share an index,
// which a later node between them spreads out.
function structRace(s, r, nodes) {
  const from = Math.floor(r() * 4);
  const at = Math.floor(r() * 3);
  s.docs.forEach((d, p) => {
    // toJSON, not getMovableList: taking a root makes it part of the
    // document, and the peer takes nothing it does not edit.
    const json = d.toJSON();
    const n = (json.ml ?? []).length;
    if (from < n) {
      const to = Math.floor(r() * n);
      s.x(p, ML, "ml_move", { from, to }, (h) => { h.move(from, to); });
    }
    const kids = (json.tr ?? []).length;
    const index = Math.min(at, kids);
    const a = s.x(p, TR, "tree_create", { parent: null, index }, (h) => h.createNode(undefined, index).id);
    if (a && a.out) nodes.set(TR, [...(nodes.get(TR) ?? []), a.out]);
  });
}

function h0(c, pos) {
  const v = c.get(pos);
  return v !== null && typeof v === "object" && !(v instanceof Uint8Array) && !Array.isArray(v) ? 1 : v;
}

function structScript(seed, crowd) {
  const r = rng(seed * (crowd ? 2971215073 : 1836311903));
  const n = crowd ? 4 + (seed % 9) : 1 + (seed % 3);
  const peers = crowd
    ? (() => {
        const ids = [...new Set(Array.from({ length: n }, () => 1 + Math.floor(r() * 2 ** 40)))];
        while (ids.length < n) ids.push(1000 + ids.length);
        return ids;
      })()
    : peerSets[Math.floor(seed / 3) % peerSets.length].slice(0, n);
  const s = new Script({ seed }, peers);
  s.versions = true;
  const pool = [ML, TR, MP, LS];
  const nodes = new Map();
  const held = [];
  for (let step = 30 + Math.floor(r() * 30); step > 0; step--) {
    const p = Math.floor(r() * n);
    const pick = r();
    if (pick < 0.68 || (n === 1 && pick < 0.72)) structEdit(s, p, r, pool, nodes);
    else if (pick < 0.72) structRace(s, r, nodes);
    else if (pick < 0.8) s.commit(p);
    else {
      const q = r();
      if (crowd && q < 0.4) {
        for (let k = 1 + Math.floor(r() * Math.max(1, n / 2)); k > 0; k--) {
          const src = (p + 1 + Math.floor(r() * (n - 1))) % n;
          s.import(p, s.export(src, { mode: "update", from: s.docs[p].version() }));
        }
      } else {
        const src = p;
        const dst = n === 1 ? p : (p + 1 + Math.floor(r() * (n - 1))) % n;
        if (q < 0.6) s.import(dst, s.export(src));
        else if (q < 0.8) held.push([dst, s.export(src, { mode: "update", from: s.docs[dst].version() })]);
        else if (held.length > 0) {
          const [to, blob] = held.splice(Math.floor(r() * held.length), 1)[0];
          s.import(to, blob);
        }
      }
      if (r() < (crowd ? 0.25 : 0.5)) s.check();
    }
  }
  for (const [to, blob] of held) s.import(to, blob);
  s.check();
  return s.out();
}

for (const [file, count, crowd] of [["script_struct_corpus.json", 300, false], ["script_struct_crowd_corpus.json", 60, true]]) {
  const corpus = [];
  for (let seed = 1; seed <= count; seed++) corpus.push(structScript(seed, crowd));
  writeFileSync(join(outDir, file), JSON.stringify(corpus) + "\n");
  let actions = 0, checks = 0, peers = 0, xs = 0, refused = 0;
  const ops = {};
  for (const c of corpus) {
    actions += c.actions.length;
    peers += c.peers.length;
    for (const a of c.actions) {
      if (a.a === "check") checks++;
      if (a.a === "x") {
        xs++;
        ops[a.op + (a.how ? "/" + a.how : "")] = (ops[a.op + (a.how ? "/" + a.how : "")] ?? 0) + 1;
        if (a.refused) refused++;
      }
    }
  }
  console.log(`${file}: ${corpus.length} scripts, ${peers} peers, ${actions} actions, ${xs} container edits, ${refused} refused, ${checks} checkpoints`);
  console.log(`  ${Object.entries(ops).map(([k, v]) => k + " " + v).join(", ")}`);
}

// Named structure scripts, one or more per rule; [n] is the review item.

const structNamed = [];
const addStruct = (name, peers, build) => {
  const s = new Script({ name }, peers);
  s.versions = true;
  build(s);
  s.check();
  structNamed.push(s.out());
};
const X = {
  mlIns: (s, p, cid, pos, v) => s.x(p, cid, "ml_ins", { pos, v: tag(v) }, (h) => { v.forEach((x, j) => h.insert(pos + j, x)); }),
  mlDel: (s, p, cid, pos, n) => s.x(p, cid, "ml_del", { pos, n }, (h) => { h.delete(pos, n); }),
  mlMove: (s, p, cid, from, to) => s.x(p, cid, "ml_move", { from, to }, (h) => { h.move(from, to); }),
  mlSet: (s, p, cid, pos, v) => s.x(p, cid, "ml_set", { pos, v: tag(v) }, (h) => { h.set(pos, v); }),
  create: (s, p, cid, parent, index) =>
    s.x(p, cid, "tree_create", index === undefined ? { parent } : { parent, index },
      (h) => (index === undefined ? h.createNode(parent ?? undefined) : h.createNode(parent ?? undefined, index)).id).out,
  move: (s, p, cid, node, parent, index) => s.x(p, cid, "tree_move", { node, parent, index }, (h) => { h.move(node, parent ?? undefined, index); }),
  del: (s, p, cid, node) => s.x(p, cid, "tree_delete", { node }, (h) => { h.delete(node); }),
  meta: (s, p, cid, node) => s.x(p, cid, "meta", { node }, (h) => h.getNodeByID(node).data.id).out,
  child: (s, p, cid, how, at, kind) =>
    s.x(p, cid, "child", { how, ...(how === "map" || how === "mergeable" ? { k: at } : { pos: at }), kind }, (h) =>
      ({ map: () => h.setContainer(at, newContainer(kind)), mergeable: () => h["ensureMergeable" + kind](at),
        list: () => h.insertContainer(at, newContainer(kind)), ml_ins: () => h.insertContainer(at, newContainer(kind)),
        ml_set: () => h.setContainer(at, newContainer(kind)) })[how]().id).out,
  text: (s, p, cid, pos, str) => s.x(p, cid, "text_ins", { pos, pos16: pos, s: str }, (h) => { h.insert(pos, str); }),
  set: (s, p, cid, k, v) => s.x(p, cid, "map_set", { k, v: tag(v) }, (h) => { h.set(k, v); }),
  unset: (s, p, cid, k) => s.x(p, cid, "map_del", { k }, (h) => { h.delete(k); }),
  lins: (s, p, cid, pos, v) => s.x(p, cid, "list_ins", { pos, v: tag([v]) }, (h) => { h.insert(pos, v); }),
  ldel: (s, p, cid, pos, n) => s.x(p, cid, "list_del", { pos, n }, (h) => { h.delete(pos, n); }),
  inc: (s, p, cid, v) => s.x(p, cid, "inc", { v }, (h) => { h.increment(v); }),
  read: (s, p, cid) => s.x(p, cid, "read", {}, (h) => tag(h.toJSON())),
};
const sync = (s, a, b) => {
  s.import(b, s.export(a));
  s.import(a, s.export(b));
};

// [9] After concurrent moves an element has dead positions, so a user index
// and the op index of the same place differ: inserts, deletes, moves and
// sets made then carry op indexes and name elements by their creation.
addStruct("movable_op_index_after_moves", [1, 2], (s) => {
  X.mlIns(s, 0, ML, 0, ["a", "b", "c", "d"]);
  sync(s, 0, 1);
  X.mlMove(s, 0, ML, 0, 2);
  X.mlMove(s, 1, ML, 0, 3);
  X.mlMove(s, 1, ML, 1, 0);
  sync(s, 0, 1);
  s.check();
  X.mlIns(s, 0, ML, 2, ["x", "y"]);
  X.mlDel(s, 0, ML, 1, 3);
  X.mlMove(s, 0, ML, 2, 0);
  X.mlSet(s, 0, ML, 1, "z");
  X.mlIns(s, 1, ML, 4, ["w"]);
  X.mlDel(s, 1, ML, 0, 2);
  s.check();
  sync(s, 0, 1);
  X.mlIns(s, 1, ML, 1, ["v"]);
  X.mlMove(s, 1, ML, 0, 2);
});

// [9] Concurrent moves and sets of one element, and a delete racing a move:
// the move with the greater (lamport, peer) places it, the set with the
// greater one gives its value, and a delete of any of its positions
// removes it.
addStruct("movable_concurrent_move_set_delete", [1, 2, 3], (s) => {
  X.mlIns(s, 0, ML, 0, [1, 2, 3, 4, 5]);
  sync(s, 0, 1);
  sync(s, 0, 2);
  X.mlMove(s, 0, ML, 4, 0);
  X.mlSet(s, 0, ML, 4, "p0");
  X.mlMove(s, 1, ML, 4, 2);
  X.mlSet(s, 1, ML, 4, "p1");
  X.mlDel(s, 2, ML, 4, 1);
  X.mlMove(s, 2, ML, 0, 3);
  sync(s, 0, 1);
  s.check();
  sync(s, 1, 2);
  sync(s, 0, 1);
  X.mlIns(s, 0, ML, 3, ["n"]);
  X.mlMove(s, 2, ML, 3, 0);
});

// [11] Setting an item to the value it has is still an op.
addStruct("movable_set_equal_value", [1, 2], (s) => {
  X.mlIns(s, 0, ML, 0, [7, "s", true]);
  X.mlSet(s, 0, ML, 0, 7);
  X.mlSet(s, 0, ML, 1, "s");
  s.commit(0);
  X.mlSet(s, 0, ML, 2, true);
  X.mlSet(s, 0, ML, 2, false);
  sync(s, 0, 1);
  X.mlSet(s, 1, ML, 2, false);
});

// [11] Moving an item to where it is makes no op, and is no error even
// past the end; a move elsewhere is one op.
addStruct("movable_move_in_place", [1, 2], (s) => {
  X.mlIns(s, 0, ML, 0, ["a", "b", "c"]);
  X.mlMove(s, 0, ML, 1, 1);
  X.mlMove(s, 0, ML, 0, 0);
  X.mlMove(s, 0, ML, 9, 9);
  X.mlMove(s, 0, ML, 9, 8);
  s.commit(0);
  X.mlMove(s, 0, ML, 2, 2);
  X.mlMove(s, 0, ML, 2, 0);
  sync(s, 0, 1);
  X.mlMove(s, 1, ML, 0, 0);
});

// [10] Concurrent first children all get index 80. A node placed between
// two equal indexes is spread out with the siblings that share them: the
// new node and each of those siblings get fresh indexes, the siblings by a
// move op each, which takes a counter.
addStruct("tree_rearrange_equal_indexes", [1, 2, 3], (s) => {
  const a = X.create(s, 0, TR, null);
  const b = X.create(s, 1, TR, null);
  const c = X.create(s, 2, TR, null, 0);
  sync(s, 0, 1);
  sync(s, 0, 2);
  sync(s, 1, 2);
  s.check();
  X.create(s, 0, TR, null);
  const n = X.create(s, 0, TR, null, 1);
  X.create(s, 0, TR, null, 2);
  s.check();
  sync(s, 0, 1);
  sync(s, 0, 2);
  X.create(s, 1, TR, n, 0);
  X.create(s, 1, TR, n, 0);
  X.create(s, 2, TR, n, 0);
  sync(s, 1, 2);
  X.create(s, 1, TR, n, 1);
  X.move(s, 2, TR, a, null, 1);
  X.move(s, 2, TR, b, a, 0);
  X.move(s, 2, TR, c, null, 0);
  sync(s, 0, 2);
});

// [11] A tree move to where the node is makes no op, under a parent or at
// the top; a move to another index of the same parent is an op.
addStruct("tree_move_in_place", [1, 2], (s) => {
  const a = X.create(s, 0, TR, null);
  const b = X.create(s, 0, TR, null);
  const c = X.create(s, 0, TR, a);
  X.move(s, 0, TR, a, null, 0);
  X.move(s, 0, TR, b, null, 1);
  X.move(s, 0, TR, c, a, 0);
  s.commit(0);
  X.move(s, 0, TR, b, null, 0);
  X.move(s, 0, TR, b, null, 0);
  sync(s, 0, 1);
  X.move(s, 1, TR, c, a, 0);
  X.move(s, 1, TR, c, b, 0);
});

// [11] Moving a node under itself or a descendant is refused and records
// nothing: the next op takes the counter it would have. Moving under a
// deleted node is no cycle and is made.
addStruct("tree_cycle_refused", [1, 2], (s) => {
  const a = X.create(s, 0, TR, null);
  const b = X.create(s, 0, TR, a);
  const c = X.create(s, 0, TR, b);
  X.move(s, 0, TR, a, c, 0);
  X.move(s, 0, TR, a, a, 0);
  X.move(s, 0, TR, b, c, 0);
  X.create(s, 0, TR, null);
  const d = X.create(s, 0, TR, null);
  X.del(s, 0, TR, d);
  X.move(s, 0, TR, c, d, 0);
  X.move(s, 0, TR, d, null, 0);
  X.create(s, 0, TR, d);
  sync(s, 0, 1);
  X.move(s, 1, TR, b, b, 0);
  X.move(s, 1, TR, a, b, 0);
  X.move(s, 1, TR, b, null, 0);
});

// [16] A container stored in another is deleted with the value holding it:
// an overwritten map key, a deleted list item, a MovableList item set to
// something else, a deleted tree node's meta map, and every container
// inside those. Its contents stay readable, edits through it are refused,
// and an edit that would make no op is no error.
addStruct("deleted_container_handles", [1, 2], (s) => {
  const t = X.child(s, 0, MP, "map", "a", "Text");
  X.text(s, 0, t, 0, "hi");
  const inner = X.child(s, 0, MP, "map", "b", "Map");
  const deep = X.child(s, 0, inner, "map", "k", "List");
  X.lins(s, 0, deep, 0, 5);
  X.set(s, 0, inner, "v", 1);
  const lm = X.child(s, 0, LS, "list", 0, "Map");
  X.set(s, 0, lm, "q", "r");
  const mc = X.child(s, 0, ML, "ml_ins", 0, "Counter");
  X.inc(s, 0, mc, 3);
  const node = X.create(s, 0, TR, null);
  const m = X.meta(s, 0, TR, node);
  X.set(s, 0, m, "name", "n");
  const kid = X.create(s, 0, TR, node);
  const km = X.meta(s, 0, TR, kid);
  X.set(s, 0, km, "x", 1);
  sync(s, 0, 1);
  X.set(s, 0, MP, "a", 1);
  X.unset(s, 0, MP, "b");
  X.ldel(s, 0, LS, 0, 1);
  X.mlSet(s, 0, ML, 0, "gone");
  X.del(s, 0, TR, node);
  s.check();
  X.text(s, 0, t, 0, "x");
  X.read(s, 0, t);
  X.set(s, 0, inner, "v", 2);
  X.set(s, 0, inner, "v", 1);
  X.unset(s, 0, inner, "zz");
  X.lins(s, 0, deep, 0, 6);
  X.read(s, 0, inner);
  X.set(s, 0, lm, "q", "s");
  X.inc(s, 0, mc, 1);
  X.read(s, 0, mc);
  X.set(s, 0, m, "name", "o");
  X.set(s, 0, km, "x", 2);
  X.read(s, 0, km);
  X.text(s, 1, t, 2, "!");
  X.lins(s, 1, deep, 1, 7);
  X.set(s, 1, km, "y", 2);
  sync(s, 0, 1);
  X.text(s, 1, t, 0, "?");
  X.read(s, 1, t);
  X.read(s, 1, deep);
});

// [16] Edits through a deleted container that make no op succeed: a tree
// move to where the node is, a MovableList move to the same index, a map
// write of the value there; the ones that make an op are refused.
addStruct("deleted_container_no_op_edits", [1], (s) => {
  const tr = X.child(s, 0, MP, "map", "t", "Tree");
  const n = X.create(s, 0, tr, null);
  const ml = X.child(s, 0, MP, "map", "l", "MovableList");
  X.mlIns(s, 0, ml, 0, [1, 2]);
  const mm = X.child(s, 0, MP, "map", "m", "Map");
  X.set(s, 0, mm, "k", 1);
  X.set(s, 0, MP, "t", null);
  X.set(s, 0, MP, "l", null);
  X.set(s, 0, MP, "m", null);
  X.move(s, 0, tr, n, null, 0);
  X.move(s, 0, tr, n, null, 1);
  X.create(s, 0, tr, null);
  X.mlMove(s, 0, ml, 1, 1);
  X.mlMove(s, 0, ml, 0, 1);
  X.mlSet(s, 0, ml, 0, 1);
  X.set(s, 0, mm, "k", 1);
  X.set(s, 0, mm, "k", 2);
  X.unset(s, 0, mm, "none");
});

// [16] A mergeable child is named by its parent and key, so concurrent
// ensures meet in one container. Deleting the key deletes it; ensuring it
// again brings back the same container with its contents. Another kind at
// the key is another container; a key with a value that is no marker
// refuses one.
addStruct("mergeable_reactivation", [1, 2], (s) => {
  const t = X.child(s, 0, MP, "mergeable", "a", "Text");
  X.text(s, 0, t, 0, "hi");
  const t1 = X.child(s, 1, MP, "mergeable", "a", "Text");
  X.text(s, 1, t1, 0, "yo");
  sync(s, 0, 1);
  s.check();
  X.child(s, 0, MP, "mergeable", "a", "Text");
  X.unset(s, 0, MP, "a");
  X.text(s, 0, t, 0, "x");
  X.read(s, 0, t);
  X.child(s, 0, MP, "mergeable", "a", "Text");
  X.text(s, 0, t, 0, "!");
  const l = X.child(s, 0, MP, "mergeable", "a", "List");
  X.lins(s, 0, l, 0, 1);
  X.text(s, 0, t, 0, "x");
  X.child(s, 0, MP, "mergeable", "a", "Text");
  X.set(s, 0, MP, "b", "plain");
  X.child(s, 0, MP, "mergeable", "b", "Map");
  X.set(s, 0, MP, "c", new Uint8Array([0, 76, 77, 1, 0, 0, 0, 0]));
  X.child(s, 0, MP, "mergeable", "c", "Map");
  sync(s, 0, 1);
  X.set(s, 1, MP, "a", 5);
  X.child(s, 1, MP, "mergeable", "a", "Text");
  X.read(s, 1, t);
});

// [16] Mergeable children of mergeable and stored maps, at keys that need
// escaping, and every kind of them.
addStruct("mergeable_nested_paths", [1, 2], (s) => {
  const mm = X.child(s, 0, MP, "mergeable", "a>b/c\\d", "Map");
  const mmm = X.child(s, 0, mm, "mergeable", "k", "Map");
  const tx = X.child(s, 0, mmm, "mergeable", "", "Text");
  X.text(s, 0, tx, 0, "deep");
  const stored = X.child(s, 0, MP, "map", "s", "Map");
  for (const kind of structKinds) X.child(s, 0, stored, "mergeable", kind, kind);
  const c = X.child(s, 1, MP, "mergeable", "a>b/c\\d", "Map");
  const c2 = X.child(s, 1, c, "mergeable", "k", "Map");
  const t2 = X.child(s, 1, c2, "mergeable", "", "Text");
  X.text(s, 1, t2, 0, "er");
  sync(s, 0, 1);
  X.unset(s, 1, mm, "k");
  X.text(s, 1, t2, 0, "no");
  X.child(s, 1, mm, "mergeable", "k", "Map");
  X.text(s, 1, t2, 0, "yes");
});

// [16] Containers stored in containers at depth, of every kind, made and
// edited through their handles by two peers; a new Counter child takes an
// op of its own, the increment by its detached value.
addStruct("nested_children", [1, 2], (s) => {
  const m = X.child(s, 0, MP, "map", "m", "Map");
  const l = X.child(s, 0, m, "map", "l", "List");
  const ml = X.child(s, 0, l, "list", 0, "MovableList");
  const tr = X.child(s, 0, ml, "ml_ins", 0, "Tree");
  const n = X.create(s, 0, tr, null);
  const nm = X.meta(s, 0, tr, n);
  const c = X.child(s, 0, nm, "map", "c", "Counter");
  X.inc(s, 0, c, 2);
  X.child(s, 0, ml, "ml_ins", 1, "Map");
  const t = X.child(s, 0, ml, "ml_set", 1, "Text");
  X.text(s, 0, t, 0, "set");
  for (const kind of structKinds) X.child(s, 0, l, "list", 1, kind);
  for (const kind of structKinds) X.child(s, 0, ml, "ml_ins", 1, kind);
  sync(s, 0, 1);
  X.text(s, 1, t, 3, "!");
  X.inc(s, 1, c, -1);
  X.mlMove(s, 1, ml, 0, 3);
  X.child(s, 0, m, "map", "l", "Counter");
  sync(s, 0, 1);
  X.read(s, 1, l);
});

writeFileSync(join(outDir, "script_struct_named.json"), JSON.stringify(structNamed) + "\n");
console.log(`script_struct_named.json: ${structNamed.length} scripts: ${structNamed.map((n) => n.name).join(", ")}`);
