package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/HomieB-tt/synq/internal/api"
	"github.com/HomieB-tt/synq/internal/chat"
	"github.com/HomieB-tt/synq/internal/crypto"
	"github.com/HomieB-tt/synq/internal/db"
)

// apiHTTPTimeout bounds every individual synq-server REST call Model
// makes - registration, login, logout, a :chat username lookup, and a
// mid-session re-auth. Matches api.NewClient's own http.Client timeout;
// this is just the per-tea.Cmd equivalent of it for calls that chain two
// requests (challenge then verify).
const apiHTTPTimeout = 15 * time.Second

// reauthCooldownPeriod is how long Model waits after a *failed*
// automatic re-auth before it will try another one. This is deliberately
// not a general backoff: it only covers the background path
// (maybeReauthCmd), where a user isn't waiting on anything and the
// obvious failure mode - a revoked refresh token on a connection
// already hammering reconnects - would otherwise turn into an
// unattended stream of 401s against synq-server's rate-limited auth
// endpoints until the account gets throttled. A deliberate `:login`
// bypasses it entirely, and a successful re-auth clears it.
const reauthCooldownPeriod = time.Minute

// --- :register ---

type registerResultMsg struct {
	username string
	tokens   *api.TokenPair
	err      error
}

// registerUsernameCmd runs the register/challenge → sign → register/
// verify exchange (API.md, "Authentication") against synq-server,
// exactly like cmd/synq/main.go's offerRegistration does at first
// launch - this is the `:register <username>` command's path for
// anyone who skipped that, or wants to register later.
func registerUsernameCmd(apiClient *api.Client, id *crypto.Identity, username string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), apiHTTPTimeout)
		defer cancel()

		challenge, err := apiClient.RegisterChallenge(ctx, username)
		if err != nil {
			return registerResultMsg{username: username, err: fmt.Errorf("request challenge: %w", err)}
		}
		sig := id.SignChallenge(challenge)
		tokens, err := apiClient.RegisterVerify(ctx, username, id.SigningPublicHex(), id.BoxPublicHex(), sig)
		if err != nil {
			return registerResultMsg{username: username, err: fmt.Errorf("verify registration: %w", err)}
		}
		return registerResultMsg{username: username, tokens: tokens}
	}
}

// handleRegisterResult stores the new username (a plain preference -
// see db.PrefUsername's doc comment on why that's fine for a username
// specifically) and holds the access token in memory.
//
// What it deliberately does not do: *persist* the refresh token
// RegisterVerify also returned. It is kept for the rest of this run
// (see Model.refreshToken, so the session survives its own expiry),
// but writing it out means re-sealing the identity vault, which needs
// the passphrase - and Model never holds it (see Session's doc
// comment). The practical consequence: after a :register run from
// inside the TUI, this session works normally, but the *next* launch's
// automatic login (cmd/synq/main.go's establishSession) finds no
// refresh token in the vault yet, so it does one full login instead of
// a cheap refresh - which succeeds (this identity really is registered
// now) and persists the vault properly at that point, since main.go
// does hold the passphrase. Self-healing, just not optimal on that one
// specific next reconnect.
func (m Model) handleRegisterResult(msg registerResultMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.commandMsg = fmt.Sprintf("Registration failed: %v", msg.err)
		return m, nil
	}

	m.username = msg.username
	m.accessToken = msg.tokens.AccessToken
	m.refreshToken = msg.tokens.RefreshToken

	if err := m.store.SavePreference(db.PrefUsername, msg.username); err != nil {
		m.commandMsg = fmt.Sprintf("Registered as %s, but failed to save that locally: %v", msg.username, err)
		return m, nil
	}

	m.commandMsg = fmt.Sprintf("Registered as %s.", msg.username)
	// Now that there's an access token, start the WS connection the
	// same way Init does at startup - this is the only other place
	// that can transition "no token" into "should be connected".
	return m, startWSCmd(m.apiClient.BaseURL, m.accessToken)
}

// --- :login (manual) ---

type loginResultMsg struct {
	tokens *api.TokenPair
	err    error
}

// loginCmd runs a full login/challenge → sign → login/verify exchange
// for username, which must already be registered. This is only ever
// triggered manually (the `:login` command) - the automatic version of
// this exact exchange lives in cmd/synq/main.go's establishSession,
// used as the normal-startup fallback when refreshing the stored
// refresh token doesn't work. See runCommand's "login" case for why
// this manual path exists at all alongside that automatic one.
func loginCmd(apiClient *api.Client, id *crypto.Identity, username string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), apiHTTPTimeout)
		defer cancel()

		challenge, err := apiClient.LoginChallenge(ctx, username)
		if err != nil {
			return loginResultMsg{err: fmt.Errorf("request login challenge: %w", err)}
		}
		sig := id.SignChallenge(challenge)
		tokens, err := apiClient.LoginVerify(ctx, username, sig)
		if err != nil {
			return loginResultMsg{err: fmt.Errorf("verify login: %w", err)}
		}
		return loginResultMsg{tokens: tokens}
	}
}

// handleLoginResult holds the new tokens in memory only - like
// handleRegisterResult, it cannot *persist* the refresh token
// LoginVerify also returned, for the same reason (no passphrase in
// Model), but it does keep it for the rest of this run so an automatic
// re-auth can use it. A manual :login's session is real but
// vault-ephemeral: it works for the rest of this run, and self-heals
// into a properly persisted one on the next launch via main.go's own
// login fallback.
//
// A successful login also cancels any failed-automatic-re-auth
// bookkeeping: whatever made that attempt fail, this session supersedes
// it, and there's no reason to leave the connection waiting out a
// cooldown it no longer applies to.
func (m Model) handleLoginResult(msg loginResultMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.commandMsg = fmt.Sprintf("Login failed: %v", msg.err)
		return m, nil
	}

	m.accessToken = msg.tokens.AccessToken
	m.refreshToken = msg.tokens.RefreshToken
	m.reauthRetryAfter = time.Time{}
	m.reauthInFlight = false
	m.commandMsg = fmt.Sprintf("Logged in as %s.", m.username)
	return m, startWSCmd(m.apiClient.BaseURL, m.accessToken)
}

// --- :logout ---

type logoutResultMsg struct {
	err error
}

// logoutCmd revokes every session for this account at once
// (LogoutAll), not just the current one. Model now does hold a refresh
// token, so the single-session /auth/logout would be reachable too -
// this is a semantic choice rather than a limitation: "sign out"
// meaning "sign me out everywhere" is the intuitive reading of a
// user-facing :logout, and LogoutAll works off the access token alone,
// which keeps it functional even if the refresh token is already dead
// (in which case revoking it would have been a no-op anyway).
func logoutCmd(apiClient *api.Client, accessToken string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), apiHTTPTimeout)
		defer cancel()
		err := apiClient.LogoutAll(ctx, accessToken)
		return logoutResultMsg{err: err}
	}
}

// handleLogoutResult clears the in-memory access token and tears down
// the WS connection unconditionally, even if the server-side
// revocation call itself failed (e.g. offline) - a failed revocation
// shouldn't leave this client still acting as logged in locally, and
// :login can always re-establish a session later regardless of whether
// LogoutAll actually reached the server this time.
func (m Model) handleLogoutResult(msg logoutResultMsg) (tea.Model, tea.Cmd) {
	m.accessToken = ""
	// The refresh token and any pending re-auth state go with it: the
	// server just revoked every session for this account (LogoutAll),
	// so a leftover refresh token is dead, and a leftover cooldown
	// would only delay the next :login's clean slate. With no WS client
	// left, nothing can fire a re-auth anyway.
	m.refreshToken = ""
	m.reauthInFlight = false
	m.reauthRetryAfter = time.Time{}
	if m.wsClient != nil {
		m.wsClient.Close()
		m.wsClient = nil
	}

	if msg.err != nil {
		m.commandMsg = fmt.Sprintf("Logged out locally, but the server call failed: %v", msg.err)
		return m, nil
	}
	m.commandMsg = "Logged out."
	return m, nil
}

// --- automatic re-auth (mid-session token expiry) ---

type reauthResultMsg struct {
	tokens *api.TokenPair
	err    error
}

// refreshSessionCmd obtains a fresh access token for a session whose
// WebSocket dial was just rejected with 401 (ws.StatusAuthExpired).
// Two paths, tried in order:
//
//  1. POST /auth/refresh with the in-memory refresh token. That
//     endpoint is deliberately not rate-limited server-side, it's a
//     single round trip, and the response doesn't rotate the refresh
//     token (see api.Refresh), so the one already held stays good.
//  2. Only when the server explicitly rejected *that* token (401 -
//     revoked, expired, unknown), a full challenge/verify login with
//     this identity's signing key: the same exchange loginCmd runs,
//     used here because a session with nothing left to refresh still
//     deserves to reconnect rather than demand a manual :login.
//
// Every other refresh error - offline, DNS, server restarting - is
// returned as-is instead of falling through to login: a login would
// fail the same way, and spending a challenge/verify round trip
// (IP-rate-limited to roughly one attempt per 30s) on a server we
// already can't reach only moves this client closer to that limit for
// nothing. The result lands in handleReauthResult either way, which is
// where the cooldown on retrying begins.
func refreshSessionCmd(apiClient *api.Client, refreshToken string, id *crypto.Identity, username string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), apiHTTPTimeout)
		defer cancel()

		if refreshToken != "" {
			accessToken, err := apiClient.Refresh(ctx, refreshToken)
			if err == nil {
				return reauthResultMsg{tokens: &api.TokenPair{
					AccessToken:  accessToken,
					RefreshToken: refreshToken,
				}}
			}
			if !api.IsUnauthorized(err) {
				return reauthResultMsg{err: fmt.Errorf("refresh session: %w", err)}
			}
		}

		if id == nil || username == "" {
			return reauthResultMsg{err: errors.New("no usable refresh token, and no registered identity to log back in with")}
		}

		challenge, err := apiClient.LoginChallenge(ctx, username)
		if err != nil {
			return reauthResultMsg{err: fmt.Errorf("request login challenge: %w", err)}
		}
		sig := id.SignChallenge(challenge)
		tokens, err := apiClient.LoginVerify(ctx, username, sig)
		if err != nil {
			return reauthResultMsg{err: fmt.Errorf("verify login: %w", err)}
		}
		return reauthResultMsg{tokens: tokens}
	}
}

// maybeReauthCmd starts a re-auth unless one is already in flight or a
// failed one is still within its cooldown, recording the in-flight
// mark when it does start one. Returning nil is a decline, not a dead
// end: the caller (Update's ws.StatusAuthExpired branch) keeps the
// status pump armed either way, so the next AuthExpired out of Run's
// backoff loop is a fresh chance once a guard clears.
func (m *Model) maybeReauthCmd() tea.Cmd {
	if m.reauthInFlight || time.Now().Before(m.reauthRetryAfter) || m.apiClient == nil {
		return nil
	}
	m.reauthInFlight = true
	return refreshSessionCmd(m.apiClient, m.refreshToken, m.identity, m.username)
}

// handleReauthResult installs a freshly obtained session, or starts
// the cooldown if there isn't one.
//
// On success this says nothing: reconnecting quietly after a token
// expired is the same experience as the silent auto-login at startup,
// and the reconnect itself will show up as a normal status change. The
// one remaining job is making the *next* dial use the new token - Run
// is already sitting in its backoff loop, so swapping the URL is
// enough to pick it up on the next attempt.
func (m Model) handleReauthResult(msg reauthResultMsg) (tea.Model, tea.Cmd) {
	if !m.reauthInFlight {
		// The re-auth this belonged to was overtaken: a :logout, or a
		// manual :login that produced fresher tokens, cleared the flag
		// while the request was still out. Dropping it here means a
		// session the user just signed out of doesn't come back to
		// life, and newer tokens don't get replaced by older ones.
		return m, nil
	}
	m.reauthInFlight = false

	if msg.err != nil {
		// Hold off until the cooldown passes. If this failed because
		// the server said no, retrying immediately just converts one
		// dead session into a stream of 401s against a rate-limited
		// endpoint; and if it failed because the server is down, the
		// reconnect loop will report AuthExpired again anyway, which
		// is when this gets another chance. :login is unaffected.
		m.reauthRetryAfter = time.Now().Add(reauthCooldownPeriod)
		m.commandMsg = fmt.Sprintf("Session expired and couldn't be renewed: %v - run :login to reconnect.", msg.err)
		return m, nil
	}

	m.accessToken = msg.tokens.AccessToken
	m.refreshToken = msg.tokens.RefreshToken

	if m.wsClient != nil && m.apiClient != nil {
		wsURL, err := deriveWSURL(m.apiClient.BaseURL, m.accessToken)
		if err != nil {
			m.commandMsg = fmt.Sprintf("Session renewed, but the WebSocket URL is invalid: %v", err)
			return m, nil
		}
		m.wsClient.SetURL(wsURL)
	}
	return m, nil
}

// --- :chat <username> resolution ---

type chatResolveResultMsg struct {
	username string
	keys     *api.PublicKeys
	err      error
}

// resolveChatContactCmd looks up username's public keys so :chat can
// open a thread by username instead of requiring a raw hex public key.
func resolveChatContactCmd(apiClient *api.Client, accessToken, username string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), apiHTTPTimeout)
		defer cancel()
		keys, err := apiClient.GetPublicKeys(ctx, accessToken, username)
		return chatResolveResultMsg{username: username, keys: keys, err: err}
	}
}

// handleChatResolveResult opens (or starts) the resolved contact's
// thread, the same way the old direct hex-pubkey :chat path did, and
// records what the lookup learned - username for display, user id for
// addressing (see Model.rememberContact) - so the thread list can show
// a name instead of a raw key and later sends and inbound frames have
// an id to speak in. The lookup's keys also go through DESIGN.md
// section 2's TOFU pin check first (see Model.applyPin): the thread
// opens either way, but a key that contradicts the pin opens it with
// sends blocked, not silently trusted.
func (m Model) handleChatResolveResult(msg chatResolveResultMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		if api.IsNotFound(msg.err) {
			m.commandMsg = fmt.Sprintf("No such user: %s", msg.username)
		} else {
			m.commandMsg = fmt.Sprintf("Couldn't look up %s: %v", msg.username, msg.err)
		}
		return m, nil
	}

	if msg.keys.BoxPubKey == "" {
		// See PublicKeys.BoxPubKey's doc comment: empty specifically
		// means this user registered before synq-server tracked an
		// X25519 key, not a lookup failure - nothing to retry here.
		m.commandMsg = fmt.Sprintf("%s hasn't set up encrypted chat support yet.", msg.username)
		return m, nil
	}

	contactPub, err := decodeHexBoxPublicKey(msg.keys.BoxPubKey)
	if err != nil {
		m.commandMsg = fmt.Sprintf("%s has an invalid public key on file: %v", msg.username, err)
		return m, nil
	}

	if msg.keys.UserID == "" {
		// Both wire directions speak in this id - EncodeSend's
		// recipient and an inbound frame's sender - so a lookup that
		// didn't return one leaves a thread nobody could ever send to
		// or receive from. A server old enough to omit it (API.md's
		// user_id) can't be talked to by this client at all; better to
		// say so now than open a thread that silently goes nowhere.
		m.commandMsg = fmt.Sprintf("synq-server returned no user id for %s - this client can't message them.", msg.username)
		return m, nil
	}

	contact := chat.NewContactKey(contactPub)

	// Trust-on-first-use (DESIGN.md section 2), before anything is
	// opened: a first sighting pins the keys, an agreement stays
	// quiet, and a contradiction is stashed (not adopted) so the
	// thread below can open with sends blocked rather than encrypting
	// to a key this device never vouched for. A pin that can't be
	// read or written fails the lookup entirely - see pinFailed.
	outcome, err := m.applyPin(msg.username, msg.keys)
	if outcome == pinFailed {
		m.commandMsg = fmt.Sprintf("Couldn't check %s's pinned key: %v - not opening the thread until that works.",
			msg.username, err)
		return m, nil
	}

	m.rememberContact(contact, msg.username, msg.keys.UserID)

	isNew := !m.chatStore.HasThread(contact)
	m.activeTab = tabChat
	m.chatActive = contact

	if outcome == pinChanged {
		m.commandMsg = fmt.Sprintf(
			"%s's key CHANGED since you pinned it - a substituted key would look exactly like this. "+
				"Sending to them is blocked: compare fingerprints with :verify %s, or :accept %s to trust the new key.",
			msg.username, msg.username, msg.username)
		return m, nil
	}

	if isNew {
		m.commandMsg = fmt.Sprintf("Started a new thread with %s.", msg.username)
	} else {
		m.commandMsg = fmt.Sprintf("Opened your thread with %s.", msg.username)
	}
	return m, nil
}
