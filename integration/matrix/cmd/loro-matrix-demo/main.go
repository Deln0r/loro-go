// Command loro-matrix-demo drives two Loro peers over a real Matrix homeserver
// and checks that they converge.
//
// The point is that neither peer ever talks to the other. They edit while
// disconnected, publish their updates into a shared room, replay that room, and
// end up with the same document. The demo exits non-zero if they do not, so it
// is a check rather than a printout.
//
//	docker compose up -d
//	go run ./cmd/loro-matrix-demo
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/id"

	loromatrix "github.com/Deln0r/loro-go/integration/matrix"
	"github.com/Deln0r/loro-go/loro"
)

func main() {
	homeserver := flag.String("homeserver", "http://localhost:8008", "homeserver base URL")
	password := flag.String("password", "demo-password", "password for the demo accounts")
	flag.Parse()

	if err := run(*homeserver, *password); err != nil {
		fmt.Fprintf(os.Stderr, "\nFAILED: %v\n", err)
		os.Exit(1)
	}
}

func run(homeserver, password string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	fmt.Printf("homeserver: %s\n", homeserver)

	alice, err := login(ctx, homeserver, "alice", password)
	if err != nil {
		return fmt.Errorf("alice login: %w", err)
	}
	bob, err := login(ctx, homeserver, "bob", password)
	if err != nil {
		return fmt.Errorf("bob login: %w", err)
	}
	fmt.Printf("logged in: %s, %s\n", alice.UserID, bob.UserID)

	room, err := alice.CreateRoom(ctx, &mautrix.ReqCreateRoom{
		Name:   "loro-go convergence demo",
		Invite: []id.UserID{bob.UserID},
	})
	if err != nil {
		return fmt.Errorf("create room: %w", err)
	}
	if _, err := bob.JoinRoom(ctx, room.RoomID.String(), nil); err != nil {
		return fmt.Errorf("bob join: %w", err)
	}
	fmt.Printf("room: %s\n\n", room.RoomID)

	// Offline. Each peer edits its own replica; neither has seen the other's
	// bytes. Same containers, concurrent edits, different peer ids.
	fmt.Println("-- offline edits (peers cannot see each other) --")
	aliceDoc := loro.NewDoc(1)
	aliceDoc.TextInsert("notes", 0, "alice wrote this. ")
	aliceDoc.MapSet("meta", "author", "alice")
	fmt.Println("  alice: notes += \"alice wrote this. \", meta.author = alice")

	bobDoc := loro.NewDoc(2)
	bobDoc.TextInsert("notes", 0, "bob wrote this. ")
	bobDoc.MapSet("meta", "reviewed", int64(1))
	fmt.Println("  bob:   notes += \"bob wrote this. \",   meta.reviewed = 1")

	// Reconnect. Bob publishes first, so nothing depends on alice's edit
	// arriving before bob's.
	fmt.Println("\n-- reconnect and publish --")
	bobEvt, err := loromatrix.Publish(ctx, bob, room.RoomID, bobDoc.ExportUpdates())
	if err != nil {
		return fmt.Errorf("bob publish: %w", err)
	}
	fmt.Printf("  bob   -> %s (%s)\n", bobEvt, loromatrix.EventType)

	aliceEvt, err := loromatrix.Publish(ctx, alice, room.RoomID, aliceDoc.ExportUpdates())
	if err != nil {
		return fmt.Errorf("alice publish: %w", err)
	}
	fmt.Printf("  alice -> %s (%s)\n", aliceEvt, loromatrix.EventType)

	fmt.Println("\n-- replay the room and merge --")
	aliceState, err := replayState(ctx, alice, room.RoomID, "alice")
	if err != nil {
		return err
	}
	bobState, err := replayState(ctx, bob, room.RoomID, "bob")
	if err != nil {
		return err
	}

	aliceJSON, _ := json.Marshal(aliceState)
	bobJSON, _ := json.Marshal(bobState)
	fmt.Printf("\n  alice sees: %s\n  bob sees:   %s\n\n", aliceJSON, bobJSON)

	if string(aliceJSON) != string(bobJSON) {
		return fmt.Errorf("peers diverged")
	}
	// Converging on an empty document would satisfy the comparison above and
	// mean nothing, so check both edits actually survived.
	notes, _ := aliceState["notes"].(string)
	meta, _ := aliceState["meta"].(map[string]any)
	if len(notes) == 0 || meta == nil {
		return fmt.Errorf("converged, but the document is empty: %s", aliceJSON)
	}
	if meta["author"] == nil || meta["reviewed"] == nil {
		return fmt.Errorf("converged, but an edit was lost: %s", aliceJSON)
	}

	fmt.Println("OK: both peers converged, and both edits survived the merge.")
	return nil
}

func login(ctx context.Context, homeserver, user, password string) (*mautrix.Client, error) {
	cli, err := mautrix.NewClient(homeserver, "", "")
	if err != nil {
		return nil, err
	}
	resp, err := cli.Login(ctx, &mautrix.ReqLogin{
		Type:             mautrix.AuthTypePassword,
		Identifier:       mautrix.UserIdentifier{Type: mautrix.IdentifierTypeUser, User: user},
		Password:         password,
		StoreCredentials: true,
	})
	if err != nil {
		return nil, err
	}
	_ = resp
	return cli, nil
}

func replayState(ctx context.Context, cli *mautrix.Client, room id.RoomID, who string) (map[string]any, error) {
	u, errs, err := loromatrix.Replay(ctx, cli, room, 50)
	if err != nil {
		return nil, fmt.Errorf("%s replay: %w", who, err)
	}
	for _, e := range errs {
		fmt.Fprintf(os.Stderr, "  %s: skipped a bad update: %v\n", who, e)
	}
	fmt.Printf("  %s replayed %d change(s) from the room\n", who, len(u.Changes))
	return loromatrix.State(u)
}
