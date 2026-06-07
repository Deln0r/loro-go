// Cross-validation: import a blob PRODUCED BY THE GO PORT into the canonical
// loro-crdt (JS) and print its toJSON. Proves Go -> JS interop (the bidirectional
// dividend). Run after the Go test that writes ../go_out/from_go.update.bin.
import { LoroDoc } from "loro-crdt/nodejs";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";

const here = dirname(fileURLToPath(import.meta.url));
const blob = readFileSync(join(here, "..", "go_out", "from_go.update.bin"));
const doc = new LoroDoc();
doc.import(new Uint8Array(blob));
console.log(JSON.stringify(doc.toJSON()));
