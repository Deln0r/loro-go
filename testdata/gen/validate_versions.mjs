// Cross-validation of loro-go's Document.ExportUpdatesFrom: loro-crdt
// imports what a Document exported from a version, into a document that
// holds the history up to that version. The Go test
// TestEmitVersionExportsForJS writes testdata/go_out/versions.json: for
// every history of versions_corpus.json and every named script's export,
// the whole history, and for each version the Go export from it. Here the
// history is loaded, the part of it the version names is taken with
// export({mode: "updates-in-range"}) and imported into a fresh document,
// and then the Go export: that document must reach the whole history's
// version and state. A version that names ops whose dependencies it does
// not name leaves changes waiting after the first import; the Go export has
// to bring what they wait for. Exits non-zero on any mismatch.
import { LoroDoc, VersionVector } from "loro-crdt/nodejs";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";

const here = dirname(fileURLToPath(import.meta.url));
const file = join(here, "..", "go_out", "versions.json");

let entries;
try {
  entries = JSON.parse(readFileSync(file, "utf8"));
} catch (e) {
  console.error(`missing ${file}: run "go test ./loro/ -run TestEmitVersionExportsForJS" first`);
  process.exit(1);
}
if (entries.length === 0) {
  console.error(`${file} holds no exports`);
  process.exit(1);
}

const bytes = (b64) => new Uint8Array(Buffer.from(b64, "base64"));
const stable = (v) =>
  JSON.stringify(v, (_k, x) => {
    if (typeof x === "bigint") return x.toString();
    if (x instanceof Uint8Array) return Array.from(x);
    if (x instanceof Map) x = Object.fromEntries(x);
    if (x && typeof x === "object" && !Array.isArray(x)) {
      return Object.fromEntries(Object.entries(x).sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0)));
    }
    return x;
  });

let failed = 0;
let checked = 0;
let waited = 0;
for (const e of entries) {
  const whole = new LoroDoc();
  whole.import(bytes(e.update));
  const end = whole.oplogVersion().toJSON();
  const wantVersion = stable(end);
  const wantState = stable(whole.toJSON());
  for (const [i, x] of e.exports.entries()) {
    checked++;
    const name = `${e.name} export ${i}`;
    const spans = [];
    for (const [peer, counter] of VersionVector.decode(bytes(x.from)).toJSON()) {
      const len = Math.min(counter, end.get(peer) ?? 0);
      if (len > 0) spans.push({ id: { peer, counter: 0 }, len });
    }
    const doc = new LoroDoc();
    if (spans.length > 0) {
      const status = doc.import(whole.export({ mode: "updates-in-range", spans }));
      if (status.pending) waited++;
    }
    try {
      doc.import(bytes(x.go));
    } catch (err) {
      console.error(`${name}: loro-crdt refuses the Go export: ${err}`);
      failed++;
      doc.free();
      continue;
    }
    if (stable(doc.oplogVersion().toJSON()) !== wantVersion) {
      console.error(`${name}: the Go export leaves version ${stable(doc.oplogVersion().toJSON())}, want ${wantVersion}`);
      failed++;
    } else if (stable(doc.toJSON()) !== wantState) {
      console.error(`${name}: the Go export leaves another state than the whole history's`);
      failed++;
    }
    doc.free();
  }
  whole.free();
}
if (failed > 0) {
  console.error(`${failed} mismatches in ${checked} exports`);
  process.exit(1);
}
console.log(`ok: loro-crdt brings all ${checked} Go exports from a version, ${waited} of them onto a version with changes waiting, to the whole history's version and state`);
