package loro_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Deln0r/loro-go/loro"
)

// Example edits a document in Go and exports it as a FastUpdates blob that
// loro-crdt can import, then reads the blob back without a document.
func Example() {
	d := loro.NewDocument(1)
	if err := d.Text("title").Insert(0, "hello"); err != nil {
		panic(err)
	}
	if err := d.Map("meta").Set("n", 7); err != nil {
		panic(err)
	}
	blob, err := d.ExportUpdates()
	if err != nil {
		panic(err)
	}

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
	export := func(peer uint64, text string) []byte {
		d := loro.NewDocument(peer)
		if err := d.Text("notes").Insert(0, text); err != nil {
			panic(err)
		}
		blob, err := d.ExportUpdates()
		if err != nil {
			panic(err)
		}
		return blob
	}
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
	a, b := export(1, "alice"), export(2, "bob")
	fmt.Println(merge(a, b))
	fmt.Println(merge(b, a))
	// Output:
	// alicebob
	// alicebob
}

// ExampleList_Insert builds a list. Positions count the items already in it,
// as in loro-crdt's LoroList.insert.
func ExampleList_Insert() {
	d := loro.NewDocument(7)
	todo := d.List("todo")
	if err := todo.Insert(0, "milk", "eggs"); err != nil {
		panic(err)
	}
	if err := todo.Insert(1, "bread"); err != nil {
		panic(err)
	}
	items, err := todo.JSON()
	if err != nil {
		panic(err)
	}
	fmt.Println(items)
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

	var all loro.Updates
	for _, p := range []struct {
		peer uint64
		text string
	}{{2, "bob"}, {1, "alice"}} {
		d := loro.NewDocument(p.peer)
		if err := d.Text("notes").Insert(0, p.text); err != nil {
			panic(err)
		}
		b, err := d.ExportUpdates()
		if err != nil {
			panic(err)
		}
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
