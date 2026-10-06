// Frozen fixtures: bytes written by an OLD loro-crdt that a current one no
// longer writes. Run under loro-crdt 1.16.3 only; the output is committed, and
// gen.mjs does not touch it. Regenerating under 1.16.4 or later would write the
// corrected bytes and the fixture would stop testing anything.
//
// delete_astral_ids_1163: loro-dev/loro#1149. In the WASM build up to 1.16.3,
// a text delete spanning text from separate inserts records a wrong start_id
// when an astral character ends one of them: deleting "a😀x" from "a😀xb"
// records ids 0..2, which name "a😀b". loro applies a delete by its position
// on import, so its text is "b!"; reading the delete by its ids gives "x!".
import { LoroDoc } from "loro-crdt";
import { writeFileSync, readFileSync } from "node:fs";
import { join } from "node:path";

const version = JSON.parse(readFileSync("node_modules/loro-crdt/package.json", "utf8")).version;
if (version !== "1.16.3") {
  throw new Error(`frozen fixtures need loro-crdt 1.16.3, found ${version}`);
}
const outDir = join("..", "fixtures", "frozen");
const doc = new LoroDoc();
doc.setPeerId(7n);
const t = doc.getText("t");
t.insert(0, "a😀b");
doc.commit();
t.insert(3, "x");
doc.commit();
t.delete(0, 4);
doc.commit();
t.insert(1, "!");
doc.commit();
const update = doc.export({ mode: "update" });
const replay = new LoroDoc();
replay.import(update);
writeFileSync(join(outDir, "delete_astral_ids_1163.update.bin"), Buffer.from(update));
writeFileSync(join(outDir, "delete_astral_ids_1163.json"), JSON.stringify(replay.toJSON(), null, 2) + "\n");
const deletes = doc.exportJsonUpdates().changes.flatMap((c) => c.ops).map((o) => o.content).filter((c) => c.type === "delete");
console.log(`delete_astral_ids_1163: replay=${JSON.stringify(replay.toJSON())} deletes=${JSON.stringify(deletes)}`);
