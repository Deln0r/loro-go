package loro_test

import (
	"fmt"

	"github.com/Deln0r/loro-go/loro"
)

// ExampleDocument edits a document in Go, sends its history to a second
// peer, edits there too, and syncs back. Each export is a FastUpdates blob
// that loro-crdt imports as well.
func ExampleDocument() {
	alice := loro.NewDocument(1)
	if err := alice.Text("notes").Insert(0, "hello"); err != nil {
		panic(err)
	}
	if err := alice.Map("meta").Set("author", "alice"); err != nil {
		panic(err)
	}
	alice.CommitWith(loro.CommitOptions{Message: "first draft"})
	blob, err := alice.ExportUpdates()
	if err != nil {
		panic(err)
	}

	bob := loro.NewDocument(2)
	if err := bob.Import(blob); err != nil {
		panic(err)
	}
	notes := bob.Text("notes")
	if err := notes.Insert(notes.Len(), " world"); err != nil {
		panic(err)
	}
	if err := bob.Counter("edits").Increment(1); err != nil {
		panic(err)
	}
	back, err := bob.ExportUpdates()
	if err != nil {
		panic(err)
	}
	if err := alice.Import(back); err != nil {
		panic(err)
	}
	state, err := alice.JSON()
	if err != nil {
		panic(err)
	}
	fmt.Println(state["notes"], state["meta"], state["edits"])
	// Output: hello world map[author:alice] 1
}
