// Package loro reads, edits and writes Loro CRDT documents in pure Go, in the
// Fast wire format of loro-crdt 1.17.1, byte for byte.
//
// Document is a live Loro peer. It imports FastUpdates and FastSnapshot blobs
// exported by loro-crdt or by another Document, edits Text, Map, List,
// MovableList, Tree and Counter containers through handles, commits, and
// exports its history, whole or from another peer's version, as FastUpdates
// blobs that loro-crdt imports:
//
//	d := loro.NewDocument(1)
//	if err := d.Import(blob); err != nil { // blob = doc.export({mode: "update"})
//		return err
//	}
//	if err := d.Text("title").Insert(0, "hello "); err != nil {
//		return err
//	}
//	d.CommitWith(loro.CommitOptions{Message: "edit from Go"})
//	out, err := d.ExportUpdates() // loro-crdt's doc.import(out) takes it
//
// Without a document, DecodeUpdates and DecodeSnapshot read a blob into its
// changes and ops, MergeState computes the state those changes give as
// loro's toJSON shows it, TextDelta gives a Text's styled runs as loro's
// toDelta does, and EncodeUpdates writes changes back as a FastUpdates blob.
//
// Text positions count Unicode code points, where loro-crdt's JavaScript API
// counts UTF-16 code units, so a position taken from JavaScript has to be
// converted first. List and MovableList positions count items, as in loro.
//
// # Values
//
// A map value, a list or MovableList item and a mark's value are nil, bool,
// int64, float64, string, []byte, []any or map[string]any, the last two
// holding values of the same kinds, nested up to 128 levels. Other integer
// types are stored as int64, an unsigned one past math.MaxInt64 being an
// error, and float32 as float64. A float64 stays a float
// even when it is whole: loro-crdt's JavaScript binding turns a whole number
// within ±2^53 into an integer, which a caller converting JavaScript values
// has to do itself. A container is never a value; Map.SetContainer and its
// siblings store one.
//
// The wire codec, the header, the columnar strategies, change blocks, the
// snapshot's SSTable and LZ4, is in the packages under encoding/.
package loro
