// Cross-validation of loro-go's Document: loro-crdt imports what a Document
// exported. The Go test TestEmitDocumentExportsForJS writes, for every peer
// of every edit script, the Document's export at the script's last
// checkpoint, its JSON, and loro-crdt's export of the same peer, to
// testdata/go_out/document.json. Each Go export is imported into a fresh
// loro-crdt document, which must show the Go peer's JSON (less the root
// containers the peer only opened: an export carries no op for them, so an
// importer cannot show them), and must agree with a fresh document that
// imported loro's export on state, version and whole history. The
// histories a Document refuses, written to testdata/go_out/document_refused
// (an op past what its author saw, a change that skips counters), must be
// refused by loro-crdt too. Exits non-zero on any mismatch.
import { LoroDoc } from "loro-crdt/nodejs";
import { readFileSync, readdirSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";

const here = dirname(fileURLToPath(import.meta.url));
const file = join(here, "..", "go_out", "document.json");

let entries;
try {
  entries = JSON.parse(readFileSync(file, "utf8"));
} catch (e) {
  console.error(`missing ${file}: run "go test ./loro/ -run TestEmitDocumentExportsForJS" first`);
  process.exit(1);
}
if (entries.length === 0) {
  console.error(`${file} holds no exports`);
  process.exit(1);
}

const tag = (v) => {
  if (v instanceof Uint8Array) return { $bin: Buffer.from(v).toString("base64") };
  if (Array.isArray(v)) return v.map(tag);
  if (v !== null && typeof v === "object") return Object.fromEntries(Object.entries(v).map(([k, x]) => [k, tag(x)]));
  return v;
};
const stable = (v) =>
  JSON.stringify(v, (_k, x) => {
    if (typeof x === "bigint") return x.toString();
    if (x instanceof Uint8Array) return Array.from(x);
    if (x && typeof x === "object" && !Array.isArray(x)) {
      return Object.fromEntries(Object.entries(x).sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0)));
    }
    return x;
  });
const emptyRoot = (v) => v === "" || v === 0 || (Array.isArray(v) && v.length === 0) || (v && typeof v === "object" && Object.keys(v).length === 0);
const load = (b64) => {
  const doc = new LoroDoc();
  doc.import(new Uint8Array(Buffer.from(b64, "base64")));
  return doc;
};

let failed = 0;
let identical = 0;
for (const e of entries) {
  const name = `${e.script} peer ${e.peer}`;
  if (e.go === e.loro) identical++;
  const got = load(e.go);
  const want = load(e.loro);
  const json = tag(got.toJSON());
  const peerJSON = Object.fromEntries(Object.entries(e.json).filter(([k, v]) => k in json || !emptyRoot(v)));
  if (stable(json) !== stable(peerJSON)) {
    console.error(`${name}: loro-crdt reads the Go export as\n ${stable(json).slice(0, 400)}\n the Go peer shows\n ${stable(peerJSON).slice(0, 400)}`);
    failed++;
  }
  for (const [what, f] of [
    ["state", (d) => d.toJSON()],
    ["version", (d) => d.oplogVersion().toJSON()],
    ["history", (d) => d.exportJsonUpdates()],
  ]) {
    if (stable(f(got)) !== stable(f(want))) {
      console.error(`${name}: the Go export imports with another ${what} than loro's`);
      failed++;
    }
  }
}
// The histories a Document refuses: loro-crdt must refuse each too.
const refusedDir = join(here, "..", "go_out", "document_refused");
let refusedNames = [];
try {
  refusedNames = readdirSync(refusedDir).filter((f) => f.endsWith(".bin"));
} catch (e) {}
if (refusedNames.length === 0) {
  console.error(`${refusedDir} holds no refused histories`);
  failed++;
}
for (const name of refusedNames) {
  const doc = new LoroDoc();
  try {
    doc.import(new Uint8Array(readFileSync(join(refusedDir, name))));
    console.error(`${name}: loro-crdt imports a history a Document refuses: ${stable(doc.toJSON())}`);
    failed++;
  } catch (e) {}
}
if (failed > 0) {
  console.error(`${failed} mismatches in ${entries.length} exports and ${refusedNames.length} refused histories`);
  process.exit(1);
}
console.log(`ok: loro-crdt imports all ${entries.length} Document exports to the Go peer's state and to loro's own state, version and history; ${identical} are loro's bytes; it refuses all ${refusedNames.length} histories a Document refuses`);
