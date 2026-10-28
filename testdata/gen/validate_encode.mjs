// Cross-validation of loro-go's EncodeUpdates where its bytes are not
// loro-crdt's own. The Go test TestEmitEncodedForJS writes, for every export
// in the fixtures that loro-go does not re-encode byte for byte, loro-crdt's
// blob and loro-go's next to each other in testdata/go_out/encode:
// <name>.loro.bin and <name>.go.bin. Each of the two is imported into a fresh
// document here, and the documents must agree on their state (toJSON), their
// version, and their whole history (exportJsonUpdates: every change with its
// deps, lamport, timestamp, message and ops). Exits non-zero on any mismatch,
// and when there is no pair to check.
import { LoroDoc } from "loro-crdt/nodejs";
import { readFileSync, readdirSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";

const here = dirname(fileURLToPath(import.meta.url));
const dir = join(here, "..", "go_out", "encode");

let names;
try {
  names = readdirSync(dir).filter((f) => f.endsWith(".loro.bin")).map((f) => f.slice(0, -".loro.bin".length));
} catch (e) {
  console.error(`missing ${dir}: run "go test ./loro/ -run TestEmitEncodedForJS" first`);
  process.exit(1);
}
if (names.length === 0) {
  console.error(`${dir} holds no pairs`);
  process.exit(1);
}

// Object key order is not part of the contract, so compare with sorted keys.
const stable = (v) =>
  JSON.stringify(v, (_k, x) => {
    if (typeof x === "bigint") return x.toString();
    if (x instanceof Uint8Array) return Array.from(x);
    if (x && typeof x === "object" && !Array.isArray(x)) {
      return Object.fromEntries(Object.entries(x).sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0)));
    }
    return x;
  });

const load = (file) => {
  const doc = new LoroDoc();
  doc.import(new Uint8Array(readFileSync(join(dir, file))));
  return doc;
};

let failed = 0;
for (const name of names) {
  const want = load(`${name}.loro.bin`);
  const got = load(`${name}.go.bin`);
  for (const [what, f] of [
    ["state", (d) => d.toJSON()],
    ["version", (d) => d.oplogVersion().toJSON()],
    ["history", (d) => d.exportJsonUpdates()],
  ]) {
    const w = stable(f(want));
    const g = stable(f(got));
    if (w !== g) {
      console.error(`${name}: the Go blob imports with another ${what}:\n go   ${g.slice(0, 400)}\n loro ${w.slice(0, 400)}`);
      failed++;
    }
  }
}
if (failed > 0) {
  console.error(`${failed} mismatches in ${names.length} pairs`);
  process.exit(1);
}
console.log(`ok: loro-crdt imports all ${names.length} re-encoded blobs to the same state, version and history`);
