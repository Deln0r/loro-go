// Cross-validation: import a blob PRODUCED BY THE GO PORT into the canonical
// loro-crdt (JS) and assert the document it reconstructs. This is the Go -> JS
// half of the byte-compatibility claim, so it exits non-zero on any mismatch and
// is wired into CI. The blob comes from the Go test TestEmitForJSImport.
import { LoroDoc } from "loro-crdt/nodejs";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";

const here = dirname(fileURLToPath(import.meta.url));
const blobPath = join(here, "..", "go_out", "from_go.update.bin");

let blob;
try {
  blob = readFileSync(blobPath);
} catch (e) {
  console.error(`missing ${blobPath}: run "go test ./loro/ -run TestEmitForJSImport" first`);
  process.exit(1);
}
if (blob.length === 0) {
  console.error(`${blobPath} is empty`);
  process.exit(1);
}

// Mirrors what TestEmitForJSImport builds on the Go side.
const expected = { title: "from-go", meta: { n: 7, ok: true }, xs: ["a", 2] };

const doc = new LoroDoc();
doc.import(new Uint8Array(blob));
const got = doc.toJSON();

// Object key order is not part of the contract, so compare with sorted keys.
const stable = (v) =>
  JSON.stringify(v, (_k, x) =>
    x && typeof x === "object" && !Array.isArray(x)
      ? Object.fromEntries(Object.entries(x).sort(([a], [b]) => (a < b ? -1 : 1)))
      : x,
  );

if (stable(got) !== stable(expected)) {
  console.error(`Go blob imported into loro-crdt as ${stable(got)}, want ${stable(expected)}`);
  process.exit(1);
}
console.log(`ok: loro-crdt imported the Go-produced blob as ${stable(got)}`);
