// Edit scripts for loro-go's Document: loro-crdt runs each script and records
// every blob a peer exported and, at each checkpoint, every peer's toJSON and
// full export, so the Go test can run the same script with Documents and hold
// them to loro-crdt.
//
//   ../fixtures/script_corpus.json   300 random scripts
//   ../fixtures/script_named.json    one small script per rule of loro's
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
//   import    {blob}     the peer imported blobs[blob]
//   check     {peers: [{json, export}]}  toJSON and the full export of every
//                        peer; taking the exports commits every peer's edits
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
import { LoroDoc } from "loro-crdt/nodejs";
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
  check() {
    const peers = this.docs.map((d) => {
      const json = tag(d.toJSON());
      const bytes = d.export({ mode: "update" });
      return { json, export: this.blob(bytes) };
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

writeFileSync(join(outDir, "script_named.json"), JSON.stringify(named) + "\n");
console.log(`script_named.json: ${named.length} scripts: ${named.map((n) => n.name).join(", ")}`);
