package loro_test

import (
	"fmt"

	"github.com/Deln0r/loro-go/loro"
)

// ExampleDocument_ExportUpdatesFrom syncs two peers by version: each sends
// the other its encoded version vector, and gets back only the changes it
// lacks.
func ExampleDocument_ExportUpdatesFrom() {
	alice := loro.NewDocument(1)
	bob := loro.NewDocument(2)
	if err := alice.Text("t").Insert(0, "shared "); err != nil {
		panic(err)
	}
	first, err := alice.ExportUpdates()
	if err != nil {
		panic(err)
	}
	if err := bob.Import(first); err != nil {
		panic(err)
	}
	if err := alice.Text("t").Insert(7, "alice"); err != nil {
		panic(err)
	}

	// Bob sends his version; Alice answers with what he lacks.
	msg := bob.Version().Encode()
	theirs, err := loro.DecodeVersionVector(msg)
	if err != nil {
		panic(err)
	}
	missing, err := alice.ExportUpdatesFrom(theirs)
	if err != nil {
		panic(err)
	}
	if err := bob.Import(missing); err != nil {
		panic(err)
	}
	state, err := bob.JSON()
	if err != nil {
		panic(err)
	}
	v := bob.Version()
	end, _ := v.Get(1)
	fmt.Println(state["t"], end)
	// Output: shared alice 12
}
