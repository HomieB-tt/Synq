package app

import (
	"context"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/HomieB-tt/synq/internal/api"
	"github.com/HomieB-tt/synq/internal/chat"
	"github.com/HomieB-tt/synq/internal/crypto"
	"github.com/HomieB-tt/synq/internal/db"
)

// apiHTTPTimeout bounds every individual synq-server REST call Model
// makes - registration, login, logout, and a :chat username lookup.
// Matches api.NewClient's own http.Client timeout; this is just the
// per-tea.Cmd equivalent of it for calls that chain two requests
// (challenge then verify).
const apiHTTPTimeout = 15 * time.Second

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
// What it deliberately does not do: persist the refresh token
// RegisterVerify also returned. Doing that means re-sealing the
// identity vault, which needs the passphrase - and Model never holds
// it (see Session's doc comment). The practical consequence: after a
// :register run from inside the TUI, this session works normally, but
// the *next* launch's automatic login (cmd/synq/main.go's
// establishSession) finds no refresh token in the vault yet, so it
// does one full login instead of a cheap refresh - which succeeds
// (this identity really is registered now) and persists the vault
// properly at that point, since main.go does hold the passphrase.
// Self-healing, just not optimal on that one specific next reconnect.
func (m Model) handleRegisterResult(msg registerResultMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.commandMsg = fmt.Sprintf("Registration failed: %v", msg.err)
		return m, nil
	}

	m.username = msg.username
	m.accessToken = msg.tokens.AccessToken

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

// handleLoginResult holds the new access token in memory only - like
// handleRegisterResult, it cannot persist the refresh token LoginVerify
// also returned, for the same reason (no passphrase in Model). A
// manual :login's session is real but vault-ephemeral: it works for
// the rest of this run, and self-heals into a properly persisted one
// on the next launch via main.go's own login fallback.
func (m Model) handleLoginResult(msg loginResultMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.commandMsg = fmt.Sprintf("Login failed: %v", msg.err)
		return m, nil
	}

	m.accessToken = msg.tokens.AccessToken
	m.commandMsg = fmt.Sprintf("Logged in as %s.", m.username)
	return m, startWSCmd(m.apiClient.BaseURL, m.accessToken)
}

// --- :logout ---

type logoutResultMsg struct {
	err error
}

// logoutCmd revokes every session for this account at once
// (LogoutAll), not just the current one - Model never holds a refresh
// token to revoke individually (see package doc comments throughout
// this file on why), so "all" is the only revocation this client is
// able to ask for, which is also arguably the more intuitive meaning
// for a user-facing :logout anyway.
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
// remembers the username → contact mapping (see Model.chatUsernames)
// so the thread list can display it instead of a raw key.
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

	contact := chat.NewContactKey(contactPub)
	if m.chatUsernames == nil {
		m.chatUsernames = make(map[chat.ContactKey]string)
	}
	m.chatUsernames[contact] = msg.username

	isNew := !m.chatStore.HasThread(contact)
	m.activeTab = tabChat
	m.chatActive = contact

	if isNew {
		m.commandMsg = fmt.Sprintf("Started a new thread with %s.", msg.username)
	} else {
		m.commandMsg = fmt.Sprintf("Opened your thread with %s.", msg.username)
	}
	return m, nil
}
