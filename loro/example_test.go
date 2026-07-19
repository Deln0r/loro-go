package loro_test

import (
	"fmt"

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
