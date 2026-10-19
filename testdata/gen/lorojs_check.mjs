// Second oracle: loro.js, the TypeScript implementation of Loro, against the
// answers loro-crdt recorded for every fixture and every random corpus here.
// Run from this directory with `node lorojs_check.mjs`. It exits non-zero when
// loro.js differs anywhere it is not known to, or agrees somewhere it is known
// not to, so a loro.js release that fixes a listed bug is noticed and the
// entry, and the README line about it, can go.
import { LoroDoc } from "loro.js";
import { readFileSync, readdirSync, existsSync } from "node:fs";
import { join } from "node:path";

const fixtures = join("..", "fixtures");
const corpora = [
  "ordering_corpus.json", "seq_single_corpus.json", "seq_concurrent_corpus.json",
  "movable_single_corpus.json", "movable_concurrent_corpus.json", "tree_corpus.json",
];
// Known loro.js divergences, each with the upstream report.
const known = {
  // Crossing concurrent tree moves left the tree empty, and a move loro skips
  // came back later: loro-dev/loro#1182, fixed by loro-dev/loro#1205 after
  // loro.js 0.3.0.
  tree_crossing_moves: "loro-dev/loro#1182",
  tree_rejected_stays: "loro-dev/loro#1182",
};

const version = JSON.parse(readFileSync("node_modules/loro.js/package.json", "utf8")).version;
const canon = (v) => JSON.stringify(v, (_k, x) =>
  x && typeof x === "object" && !Array.isArray(x) ? Object.fromEntries(Object.keys(x).sort().map((k) => [k, x[k]])) : x);
const differs = (blob, want) => {
  const doc = new LoroDoc();
  try {
    doc.import(blob);
    return canon(doc.toJSON()) !== canon(want);
  } catch {
    return true;
  }
};

let failures = 0;
let checked = 0;
for (const file of readdirSync(fixtures).filter((n) => n.endsWith(".update.bin")).sort()) {
  const name = file.slice(0, -".update.bin".length);
  if (!existsSync(join(fixtures, name + ".json"))) continue;
  let want;
  try {
    want = JSON.parse(readFileSync(join(fixtures, name + ".json"), "utf8"));
  } catch {
    continue;
  }
  checked++;
  const bad = differs(readFileSync(join(fixtures, file)), want);
  if (bad && !known[name]) {
    console.log(`fixture ${name}: loro.js ${version} differs from loro-crdt`);
    failures++;
  } else if (!bad && known[name]) {
    console.log(`fixture ${name}: loro.js ${version} now agrees; drop the ${known[name]} entry and its README line`);
    failures++;
  }
}
for (const name of corpora) {
  for (const entry of JSON.parse(readFileSync(join(fixtures, name), "utf8"))) {
    checked++;
    if (differs(Buffer.from(entry.update, "base64"), entry.expected)) {
      console.log(`${name} seed ${entry.seed}: loro.js ${version} differs from loro-crdt`);
      failures++;
    }
  }
}
console.log(`loro.js ${version}: ${checked} documents checked, ${failures} unexpected results`);
process.exit(failures === 0 ? 0 : 1);
