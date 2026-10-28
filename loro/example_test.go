package loro_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Deln0r/loro-go/loro"
)

// Example builds a document in Go, exports it as a FastUpdates blob that
// loro-crdt can import, then decodes and reconstructs the state again.
func Example() {
	d := loro.NewDoc(1)
	d.TextInsert("title", 0, "hello")
	d.MapSet("meta", "n", int64(7))
	blob := d.ExportUpdates()

	u, err := loro.DecodeUpdates(blob)
	if err != nil {
		panic(err)
	}
	state, err := loro.MergeState(u)
	if err != nil {
		panic(err)
	}
	fmt.Println(state["title"], state["meta"])
	// Output: hello map[n:7]
}

// ExampleMergeState merges the updates of two peers that edited without
// seeing each other. Merging is a matter of collecting their changes into one
// Updates; the order they are collected in does not change the result, and
// loro-crdt shows the same text after importing the two blobs.
func ExampleMergeState() {
	alice := loro.NewDoc(1)
	alice.TextInsert("notes", 0, "alice")
	bob := loro.NewDoc(2)
	bob.TextInsert("notes", 0, "bob")

	merge := func(blobs ...[]byte) any {
		var all loro.Updates
		for _, blob := range blobs {
			u, err := loro.DecodeUpdates(blob)
			if err != nil {
				panic(err)
			}
			all.Changes = append(all.Changes, u.Changes...)
		}
		state, err := loro.MergeState(&all)
		if err != nil {
			panic(err)
		}
		return state["notes"]
	}
	a, b := alice.ExportUpdates(), bob.ExportUpdates()
	fmt.Println(merge(a, b))
	fmt.Println(merge(b, a))
	// Output:
	// alicebob
	// alicebob
}

// ExampleDoc_ListInsert builds a list. Positions count the items already in
// it, as in loro-crdt's LoroList.insert.
func ExampleDoc_ListInsert() {
	d := loro.NewDoc(7)
	d.ListInsert("todo", 0, []any{"milk", "eggs"})
	d.ListInsert("todo", 1, []any{"bread"})

	u, err := loro.DecodeUpdates(d.ExportUpdates())
	if err != nil {
		panic(err)
	}
	state, err := loro.MergeState(u)
	if err != nil {
		panic(err)
	}
	fmt.Println(state["todo"])
	// Output: [milk bread eggs]
}

// ExampleDecodeSnapshot reads a FastSnapshot exported by loro-crdt: "hello"
// with two characters deleted.
func ExampleDecodeSnapshot() {
	blob, err := os.ReadFile(filepath.Join("..", "testdata", "fixtures", "text_del.snapshot.bin"))
	if err != nil {
		panic(err)
	}
	u, err := loro.DecodeSnapshot(blob)
	if err != nil {
		panic(err)
	}
	state, err := loro.MergeState(u)
	if err != nil {
		panic(err)
	}
	fmt.Println(state["t"])
	// Output: hlo
}

// ExampleTextDelta reads the styled runs of a rich text exported by
// loro-crdt: "abcde" with bold over "abcd" and italic over "cde".
func ExampleTextDelta() {
	blob, err := os.ReadFile(filepath.Join("..", "testdata", "fixtures", "rt_overlap.update.bin"))
	if err != nil {
		panic(err)
	}
	u, err := loro.DecodeUpdates(blob)
	if err != nil {
		panic(err)
	}
	delta, err := loro.TextDelta(u, "rt")
	if err != nil {
		panic(err)
	}
	for _, run := range delta {
		fmt.Printf("%q %v\n", run.Insert, run.Attributes)
	}
	// Output:
	// "ab" map[bold:true]
	// "cd" map[bold:true italic:true]
	// "e" map[italic:true]
}

// ExampleEncodeUpdates decodes an export by loro-crdt and writes it back: the
// bytes are loro's own. Changes collected from several exports have no block
// grouping recorded, and EncodeUpdates groups them as loro's export would, in
// one blob that loro-crdt imports.
func ExampleEncodeUpdates() {
	blob, err := os.ReadFile(filepath.Join("..", "testdata", "fixtures", "cross_del.update.bin"))
	if err != nil {
		panic(err)
	}
	u, err := loro.DecodeUpdates(blob)
	if err != nil {
		panic(err)
	}
	out, err := loro.EncodeUpdates(u)
	if err != nil {
		panic(err)
	}
	fmt.Println(bytes.Equal(out, blob))

	alice := loro.NewDoc(1)
	alice.TextInsert("notes", 0, "alice")
	bob := loro.NewDoc(2)
	bob.TextInsert("notes", 0, "bob")
	var all loro.Updates
	for _, b := range [][]byte{bob.ExportUpdates(), alice.ExportUpdates()} {
		u, err := loro.DecodeUpdates(b)
		if err != nil {
			panic(err)
		}
		all.Changes = append(all.Changes, u.Changes...)
	}
	merged, err := loro.EncodeUpdates(&all)
	if err != nil {
		panic(err)
	}
	back, err := loro.DecodeUpdates(merged)
	if err != nil {
		panic(err)
	}
	state, err := loro.MergeState(back)
	if err != nil {
		panic(err)
	}
	fmt.Println(back.Blocks, state["notes"])
	// Output:
	// true
	// [1 1] alicebob
}
