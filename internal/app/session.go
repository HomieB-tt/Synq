package app

import "github.com/HomieB-tt/synq/internal/api"

// Session is what cmd/synq/main.go resolves, synchronously, before
// the TUI starts: whether synq-server is configured at all, and - for
// a returning, already-registered user - whatever username and access
// token resulted from main.go's automatic refresh-or-login attempt
// against the identity it just unlocked.
//
// This is a snapshot handed to New once, not something Model goes on
// to resolve for itself, because of what main.go can do that Model
// categorically cannot: main.go briefly holds the vault passphrase
// (see cmd/synq/main.go's establishSession/unlockIdentity), which is
// what makes re-sealing the vault with a freshly issued refresh token
// possible there. Once app.Run hands off to the Bubble Tea program,
// that passphrase is gone - zeroed by main.go's own defer - and Model
// never receives it at all. A `:login` run from inside the running TUI
// (see runCommand) can still obtain a working access token the same
// way, but can never persist a new refresh token to the vault; see
// that command's own doc comment for the consequence (self-healing,
// but on a slightly different path, next launch).
type Session struct {
	// API is nil if SYNQ_SERVER_URL isn't configured - every other
	// field is meaningless in that case and Model treats a nil API
	// exactly like today's "no server configured" behavior.
	API *api.Client

	// Username is empty if this identity has never registered with
	// synq-server (see db.PrefUsername), regardless of whether API is
	// configured.
	Username string

	// AccessToken is empty unless main.go's automatic login/refresh
	// succeeded. Held in memory only for the life of the process - see
	// Model.accessToken's doc comment for why this, specifically,
	// never gets written anywhere.
	AccessToken string
}
