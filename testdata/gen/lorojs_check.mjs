// Second oracle: loro.js, the TypeScript implementation of Loro, against the
// answers loro-crdt recorded for every fixture and every random corpus here:
// each document's toJSON, and the rich-text toDelta where one was recorded.
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
  "rich_corpus.json", "nested_corpus.json",
];
// Known loro.js divergences, each with the upstream report.
// Crossing concurrent tree moves, which loro.js 0.3.0 got wrong
// (loro-dev/loro#1182), agree since 0.4.0 and are no longer listed.
const known = {
  // toDelta breaks runs where loro-crdt does not: see runBreaks below.
  rt_struct: "run breaks",
  rt_unmark_alone: "run breaks",
  // A mark whose end anchor was deleted still styles its text in loro.js and
  // styles nothing in loro-crdt. Only a hand-written history deletes an anchor.
  rt_end_deleted: "deleted end anchor",
};

// loro.js and loro-crdt can give the same text with the same styles but break
// it into runs at different places: loro.js joins text under an unmark with
// unstyled text beside it, which loro-crdt keeps apart, and keeps apart runs
// whose style values are equal objects or arrays, which loro-crdt joins. A
// corpus delta that differs only in that is counted rather than failed; once
// none do, the note in the README can go.
const join_ = (delta) => {
  const out = [];
  for (const run of delta) {
    const last = out[out.length - 1];
    if (last && canon(last.attributes ?? {}) === canon(run.attributes ?? {})) last.insert += run.insert;
    else out.push({ ...run });
  }
  return out;
};
let runBreaks = 0;

const version = JSON.parse(readFileSync("node_modules/loro.js/package.json", "utf8")).version;
const canon = (v) => JSON.stringify(v, (_k, x) =>
  x && typeof x === "object" && !Array.isArray(x) ? Object.fromEntries(Object.keys(x).sort().map((k) => [k, x[k]])) : x);
// compare imports blob and checks toJSON against want and, when a delta was
// recorded, the toDelta of the Text called text: "same", "breaks" when the
// delta differs only in where its runs break, or "differs".
const compare = (blob, want, delta, text) => {
  const doc = new LoroDoc();
  try {
    doc.import(blob);
    if (canon(doc.toJSON()) !== canon(want)) return "differs";
    if (delta === undefined) return "same";
    const got = doc.getText(text).toDelta();
    if (canon(got) === canon(delta)) return "same";
    return canon(join_(got)) === canon(join_(delta)) ? "breaks" : "differs";
  } catch {
    return "differs";
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
  const deltaFile = join(fixtures, name + ".delta.json");
  const delta = existsSync(deltaFile) ? JSON.parse(readFileSync(deltaFile, "utf8")) : undefined;
  checked++;
  // A known entry must still diverge the way it is known to: "run breaks"
  // only in where runs break, the others in any way.
  const got = compare(readFileSync(join(fixtures, file)), want, delta, "rt");
  const expect = !known[name] ? "same" : known[name] === "run breaks" ? "breaks" : "differs";
  if (got === "same" && expect !== "same") {
    console.log(`fixture ${name}: loro.js ${version} now agrees; drop the ${known[name]} entry and its README line`);
    failures++;
  } else if (got !== expect && !(expect === "differs" && got === "breaks")) {
    console.log(`fixture ${name}: loro.js ${version} differs from loro-crdt (${got}, expected ${expect})`);
    failures++;
  }
}
for (const name of corpora) {
  for (const entry of JSON.parse(readFileSync(join(fixtures, name), "utf8"))) {
    checked++;
    const got = compare(Buffer.from(entry.update, "base64"), entry.expected, entry.delta ?? undefined, "t");
    if (got === "breaks") runBreaks++;
    if (got === "differs") {
      console.log(`${name} seed ${entry.seed}: loro.js ${version} differs from loro-crdt`);
      failures++;
    }
  }
}
if (runBreaks === 0) {
  console.log(`loro.js ${version} breaks every corpus delta into runs as loro-crdt does; drop the run-breaks note`);
  failures++;
}
console.log(`loro.js ${version}: ${checked} documents checked, ${runBreaks} corpus deltas differ only in run breaks, ${failures} unexpected results`);
process.exit(failures === 0 ? 0 : 1);
