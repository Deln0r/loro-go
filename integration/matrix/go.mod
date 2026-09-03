// The Matrix integration is a separate module on purpose: the loro-go library
// itself has no dependencies, and pulling a Matrix client into it would change
// that for every consumer. Users who want the transport opt into it here.
module github.com/Deln0r/loro-go/integration/matrix

go 1.26

require (
	github.com/Deln0r/loro-go v0.1.0
	maunium.net/go/mautrix v0.30.0
)

require (
	filippo.io/edwards25519 v1.2.0 // indirect
	github.com/mattn/go-colorable v0.1.14 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/rs/zerolog v1.35.1 // indirect
	github.com/tidwall/gjson v1.19.0 // indirect
	github.com/tidwall/match v1.1.1 // indirect
	github.com/tidwall/pretty v1.2.1 // indirect
	github.com/tidwall/sjson v1.2.5 // indirect
	go.mau.fi/util v0.10.0 // indirect
	golang.org/x/crypto v0.55.0 // indirect
	golang.org/x/exp v0.0.0-20260813180055-c1d0aacb2297 // indirect
	golang.org/x/net v0.58.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.41.0 // indirect
)

replace github.com/Deln0r/loro-go => ../..
