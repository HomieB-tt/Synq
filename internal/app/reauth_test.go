package app

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/HomieB-tt/synq/internal/api"
	"github.com/HomieB-tt/synq/internal/crypto"
	"github.com/HomieB-tt/synq/internal/db"
	"github.com/HomieB-tt/synq/internal/ws"
)

// hitLog records which auth endpoints a test's fake server saw, in
// order - enough to distinguish "refreshed silently" from "fell back
// to a full login" from "made calls it shouldn't have".
type hitLog struct {
	mu    sync.Mutex
	paths []string
}

func (h *hitLog) record(path string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.paths = append(h.paths, path)
}

func (h *hitLog) saw(path string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, p := range h.paths {
		if p == path {
			return true
		}
	}
	return false
}

func (h *hitLog) order() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.paths...)
}

// newAuthServer is a stand-in for synq-server's three auth endpoints
// the re-auth path can touch. refresh overrides the default 200
// response; everything else answers the way a working server does.
func newAuthServer(t *testing.T, refresh http.HandlerFunc) (*httptest.Server, *hitLog) {
	t.Helper()
	hits := &hitLog{}

	mux := http.NewServeMux()
	mux.HandleFunc("/auth/refresh", func(w http.ResponseWriter, r *http.Request) {
		hits.record("/auth/refresh")
		w.Header().Set("Content-Type", "application/json")
		if refresh != nil {
			refresh(w, r)
			return
		}
		fmt.Fprint(w, `{"access_token":"refreshed-access"}`)
	})
	mux.HandleFunc("/auth/login/challenge", func(w http.ResponseWriter, r *http.Request) {
		hits.record("/auth/login/challenge")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"challenge":"nonce-123"}`)
	})
	mux.HandleFunc("/auth/login/verify", func(w http.ResponseWriter, r *http.Request) {
		hits.record("/auth/login/verify")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"access_token":"relogin-access","refresh_token":"relogin-refresh"}`)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, hits
}

func writeAPIError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	fmt.Fprintf(w, `{"error":%q}`, message)
}

func runReauthCmd(t *testing.T, cmd tea.Cmd) reauthResultMsg {
	t.Helper()
	if cmd == nil {
		t.Fatal("refreshSessionCmd returned a nil command")
	}
	msg, ok := cmd().(reauthResultMsg)
	if !ok {
		t.Fatalf("re-auth command returned %T, want reauthResultMsg", cmd())
	}
	return msg
}

// The happy path has to be refresh: it's one round trip, isn't
// rate-limited server-side, and doesn't spend a challenge/verify pair
// the server limits to roughly one per 30s per IP.
func TestRefreshSessionCmdUsesTheRefreshEndpointAlone(t *testing.T) {
	srv, hits := newAuthServer(t, nil)

	msg := runReauthCmd(t, refreshSessionCmd(api.NewClient(srv.URL), "rt-1", nil, ""))

	if msg.err != nil {
		t.Fatalf("refreshSessionCmd: %v", msg.err)
	}
	if msg.tokens.AccessToken != "refreshed-access" {
		t.Errorf("AccessToken = %q, want %q", msg.tokens.AccessToken, "refreshed-access")
	}
	// Refresh doesn't rotate the token, so what we sent is what stays
	// valid - and what has to be kept for the next expiry too.
	if msg.tokens.RefreshToken != "rt-1" {
		t.Errorf("RefreshToken = %q, want the unrotated %q", msg.tokens.RefreshToken, "rt-1")
	}
	if got := hits.order(); len(got) != 1 || got[0] != "/auth/refresh" {
		t.Errorf("endpoints hit = %v, want [/auth/refresh] only", got)
	}
}

// A 401 means the refresh token itself is dead (revoked, expired) -
// that's the one case where a full login is both necessary and
// possible, since the identity's signing key still works.
func TestRefreshSessionCmdFallsBackToLoginWhenRefreshIsRejected(t *testing.T) {
	srv, hits := newAuthServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeAPIError(w, http.StatusUnauthorized, "refresh token revoked")
	})
	id, err := crypto.GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}

	msg := runReauthCmd(t, refreshSessionCmd(api.NewClient(srv.URL), "dead-refresh", id, "alice"))

	if msg.err != nil {
		t.Fatalf("refreshSessionCmd: %v", msg.err)
	}
	if msg.tokens.AccessToken != "relogin-access" {
		t.Errorf("AccessToken = %q, want the login's %q", msg.tokens.AccessToken, "relogin-access")
	}
	want := []string{"/auth/refresh", "/auth/login/challenge", "/auth/login/verify"}
	if got := hits.order(); !equalStrings(got, want) {
		t.Errorf("endpoints hit = %v, want %v", got, want)
	}
}

// A refresh that failed for a reason other than 401 (server down,
// proxy error, 500) must not be papered over with a login: the login
// would hit the same server, fail the same way, and burn a
// rate-limited challenge attempt on the way.
func TestRefreshSessionCmdDoesNotLogInAfterATransientRefreshFailure(t *testing.T) {
	srv, hits := newAuthServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeAPIError(w, http.StatusInternalServerError, "boom")
	})
	id, err := crypto.GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}

	msg := runReauthCmd(t, refreshSessionCmd(api.NewClient(srv.URL), "rt-1", id, "alice"))

	if msg.err == nil {
		t.Fatal("refreshSessionCmd succeeded, want the 500 surfaced as an error")
	}
	if !strings.Contains(msg.err.Error(), "refresh session") {
		t.Errorf("err = %v, want it to name the refresh step", msg.err)
	}
	if hits.saw("/auth/login/challenge") {
		t.Error("a login was attempted after a non-401 refresh failure")
	}
}

// No refresh token at all (older vault, a :register/:login from inside
// the TUI without persistence) still deserves an automatic reconnect
// rather than a demand for a manual :login.
func TestRefreshSessionCmdLogsInDirectlyWithoutARefreshToken(t *testing.T) {
	srv, hits := newAuthServer(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("refresh was called with no refresh token to use")
		writeAPIError(w, http.StatusUnauthorized, "missing refresh_token")
	})
	id, err := crypto.GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}

	msg := runReauthCmd(t, refreshSessionCmd(api.NewClient(srv.URL), "", id, "alice"))

	if msg.err != nil {
		t.Fatalf("refreshSessionCmd: %v", msg.err)
	}
	if msg.tokens.AccessToken != "relogin-access" {
		t.Errorf("AccessToken = %q, want %q", msg.tokens.AccessToken, "relogin-access")
	}
	if hits.saw("/auth/refresh") {
		t.Error("refresh endpoint hit despite there being no refresh token")
	}
}

// Nothing to refresh with and nothing to log in with (a guest, or a
// nil identity) has to report that rather than panic or loop.
func TestRefreshSessionCmdRefusesToInventASession(t *testing.T) {
	msg := runReauthCmd(t, refreshSessionCmd(api.NewClient("http://127.0.0.1:0"), "", nil, ""))

	if msg.err == nil {
		t.Fatal("refreshSessionCmd succeeded without a refresh token or an identity")
	}
}

func TestMaybeReauthCmdGuards(t *testing.T) {
	id, err := crypto.GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	srv, _ := newAuthServer(t, nil)

	for _, tc := range []struct {
		name string
		m    Model
		want bool // whether a re-auth should start
	}{
		{
			name: "one at a time",
			m:    Model{apiClient: api.NewClient(srv.URL), refreshToken: "rt", username: "alice", identity: id, reauthInFlight: true},
			want: false,
		},
		{
			name: "cooldown after a failure",
			m:    Model{apiClient: api.NewClient(srv.URL), refreshToken: "rt", username: "alice", identity: id, reauthRetryAfter: time.Now().Add(time.Minute)},
			want: false,
		},
		{
			name: "no API client to call",
			m:    Model{refreshToken: "rt"},
			want: false,
		},
		{
			name: "ready",
			m:    Model{apiClient: api.NewClient(srv.URL), refreshToken: "rt", username: "alice", identity: id},
			want: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Update calls this on its own copy of the model, so the
			// flag's new value is what Update returns - which is the
			// only way a later StatusAuthExpired knows whether a
			// re-auth is already under way.
			next := tc.m
			cmd := next.maybeReauthCmd()

			if got := cmd != nil; got != tc.want {
				t.Fatalf("maybeReauthCmd() nil = %v, want %v", !got, !tc.want)
			}
			// A start sets the flag; a decline leaves whatever it
			// found (an already in-flight re-auth isn't cancelled by
			// another signal arriving meanwhile).
			wantInFlight := tc.want || tc.m.reauthInFlight
			if got := next.reauthInFlight; got != wantInFlight {
				t.Errorf("reauthInFlight = %v, want %v", got, wantInFlight)
			}
			if tc.want {
				// The command itself is exercised by the
				// refreshSessionCmd tests; here it just has to be a
				// runnable command, not a nil-shaped dead end.
				if _, ok := cmd().(reauthResultMsg); !ok {
					t.Errorf("re-auth command returned %T, want reauthResultMsg", cmd())
				}
			}
		})
	}
}

func TestHandleReauthResultInstallsTheNewSession(t *testing.T) {
	client, err := ws.NewClient("ws://synq.example/ws?token=old-access")
	if err != nil {
		t.Fatalf("ws.NewClient: %v", err)
	}

	m := Model{
		wsClient:       client,
		apiClient:      api.NewClient("https://synq.example"),
		accessToken:    "old-access",
		refreshToken:   "old-refresh",
		reauthInFlight: true,
	}

	next, cmd := m.handleReauthResult(reauthResultMsg{tokens: &api.TokenPair{
		AccessToken:  "new-access",
		RefreshToken: "new-refresh",
	}})
	got, ok := next.(Model)
	if !ok {
		t.Fatalf("handleReauthResult returned %T, want Model", next)
	}

	if got.accessToken != "new-access" || got.refreshToken != "new-refresh" {
		t.Errorf("session = (%q, %q), want the renewed pair", got.accessToken, got.refreshToken)
	}
	if got.reauthInFlight {
		t.Error("reauthInFlight still set after the re-auth finished")
	}
	if got.commandMsg != "" {
		t.Errorf("commandMsg = %q, want silence: reconnecting after an expiry is the normal experience", got.commandMsg)
	}
	if cmd != nil {
		t.Error("handleReauthResult returned a command; the reconnect is Run's backoff loop's job")
	}
	// The whole point of the exercise: Run is mid-backoff and will
	// dial whatever URL is set now, so swapping it is what makes the
	// next attempt succeed.
	want := "wss://synq.example/ws?token=new-access"
	if url := got.wsClient.URL(); url != want {
		t.Errorf("wsClient.URL() = %q, want %q", url, want)
	}
}

func TestHandleReauthResultFailureStartsCooldownAndSaysWhy(t *testing.T) {
	m := Model{reauthInFlight: true}

	next, _ := m.handleReauthResult(reauthResultMsg{err: errors.New("server said no")})
	got := next.(Model)

	if got.reauthInFlight {
		t.Error("reauthInFlight still set after a failed re-auth")
	}
	remaining := time.Until(got.reauthRetryAfter)
	if remaining <= 0 || remaining > reauthCooldownPeriod {
		t.Errorf("reauthRetryAfter is %v away, want within (0, %s]", remaining, reauthCooldownPeriod)
	}
	if !strings.Contains(got.commandMsg, "server said no") {
		t.Errorf("commandMsg = %q, want it to name the failure", got.commandMsg)
	}
	if !strings.Contains(got.commandMsg, ":login") {
		t.Errorf("commandMsg = %q, want it to point at :login as the way out", got.commandMsg)
	}
}

// A connection failing repeatedly must not turn into a burst of
// parallel auth calls: the status pump keeps running (each
// StatusAuthExpired is the retry signal), while the re-auth itself
// happens at most once.
func TestAuthExpiredStartsReauthAndKeepsTheStatusPumpArmed(t *testing.T) {
	srv, _ := newAuthServer(t, nil)
	client, err := ws.NewClient("ws://synq.example/ws?token=expired")
	if err != nil {
		t.Fatalf("ws.NewClient: %v", err)
	}

	m := Model{
		wsClient:     client,
		apiClient:    api.NewClient(srv.URL),
		refreshToken: "rt-1",
		username:     "alice",
		connected:    true,
	}

	next, cmd := m.Update(wsStatusMsg(ws.StatusAuthExpired))
	got := next.(Model)

	if got.connected {
		t.Error("still reported as connected after the server rejected the token")
	}
	if !got.reauthInFlight {
		t.Fatal("AuthExpired did not start a re-auth")
	}
	if cmd == nil {
		t.Fatal("AuthExpired returned no command - the status pump would stop")
	}
	batch, ok := cmd().(tea.BatchMsg)
	if !ok || len(batch) != 2 {
		t.Fatal("command is not a two-command batch (status pump + re-auth)")
	}

	// Second command is the re-auth (the first is the status pump,
	// which would block on a channel no one is feeding here). Running
	// it proves it talks to the server rather than being a placeholder.
	msg, ok := batch[1]().(reauthResultMsg)
	if !ok || msg.err != nil {
		t.Fatalf("re-auth command returned %#v, want a successful reauthResultMsg", msg)
	}
	if msg.tokens.AccessToken != "refreshed-access" {
		t.Errorf("AccessToken = %q, want %q", msg.tokens.AccessToken, "refreshed-access")
	}
}

// During the cooldown the pump still has to be re-armed - that's the
// only thing listening for a later chance to retry - but no second
// re-auth is started.
func TestAuthExpiredSkipsReauthDuringCooldownButKeepsPumping(t *testing.T) {
	client, err := ws.NewClient("ws://synq.example/ws?token=expired")
	if err != nil {
		t.Fatalf("ws.NewClient: %v", err)
	}

	m := Model{
		wsClient:         client,
		apiClient:        api.NewClient("https://synq.example"),
		refreshToken:     "rt-1",
		username:         "alice",
		reauthRetryAfter: time.Now().Add(time.Minute),
	}

	next, cmd := m.Update(wsStatusMsg(ws.StatusAuthExpired))
	got := next.(Model)

	if got.reauthInFlight {
		t.Error("a re-auth started during its cooldown")
	}
	if cmd == nil {
		t.Fatal("status pump not re-armed during the cooldown")
	}
	// The pump alone is what comes back here; distinguishing it from
	// the pump-plus-re-auth batch would mean running it, which blocks
	// on a status channel nothing is feeding. The observable claim -
	// no second re-auth started - is the reauthInFlight check above,
	// plus the guards covered by TestMaybeReauthCmdGuards.
}

// The tokens a manual :login/:register just obtained are exactly what
// the next expiry needs - dropping them would mean only startup-time
// sessions survive their own expiry.
func TestRegisterAndLoginKeepTheRefreshTokenInMemory(t *testing.T) {
	// handleRegisterResult persists the username, so it needs a real
	// store to persist it into.
	store, err := db.OpenKeyStore(filepath.Join(t.TempDir(), "ks.db"))
	if err != nil {
		t.Fatalf("OpenKeyStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	m := Model{store: store, apiClient: api.NewClient("https://synq.example")}
	registered, _ := m.handleRegisterResult(registerResultMsg{
		username: "alice",
		tokens:   &api.TokenPair{AccessToken: "reg-access", RefreshToken: "reg-refresh"},
	})
	reg := registered.(Model)
	if reg.refreshToken != "reg-refresh" {
		t.Errorf("after register, refreshToken = %q, want %q", reg.refreshToken, "reg-refresh")
	}
	if !strings.Contains(reg.commandMsg, "Registered as alice") {
		t.Errorf("commandMsg = %q, want the registration confirmation", reg.commandMsg)
	}

	m = Model{}
	// handleLoginResult restarts the WS pump, which needs a base URL.
	m.apiClient = api.NewClient("https://synq.example")
	loggedIn, _ := m.handleLoginResult(loginResultMsg{
		tokens: &api.TokenPair{AccessToken: "login-access", RefreshToken: "login-refresh"},
	})
	log := loggedIn.(Model)
	if log.refreshToken != "login-refresh" {
		t.Errorf("after login, refreshToken = %q, want %q", log.refreshToken, "login-refresh")
	}
	// A fresh session also clears a stale failure cooldown.
	if !log.reauthRetryAfter.IsZero() {
		t.Error("login left a re-auth cooldown in place for a brand-new session")
	}
}

// A result that arrives after the re-auth it belonged to was dropped
// (logout, or a manual login that already produced fresh tokens) must
// not resurrect the old session - or talk about a failure nobody is
// waiting on.
func TestHandleReauthResultIgnoresAResultNobodyIsWaitingFor(t *testing.T) {
	client, err := ws.NewClient("ws://synq.example/ws?token=still-old")
	if err != nil {
		t.Fatalf("ws.NewClient: %v", err)
	}
	m := Model{
		wsClient:     client,
		apiClient:    api.NewClient("https://synq.example"),
		accessToken:  "still-old",
		refreshToken: "still-old-refresh",
	}

	next, _ := m.handleReauthResult(reauthResultMsg{tokens: &api.TokenPair{
		AccessToken:  "too-late",
		RefreshToken: "too-late-refresh",
	}})
	got := next.(Model)

	if got.accessToken != "still-old" || got.refreshToken != "still-old-refresh" {
		t.Errorf("session = (%q, %q), want it left alone", got.accessToken, got.refreshToken)
	}
	if url := got.wsClient.URL(); url != "ws://synq.example/ws?token=still-old" {
		t.Errorf("wsClient.URL() = %q, want the URL left alone", url)
	}

	next, _ = got.handleReauthResult(reauthResultMsg{err: errors.New("late failure")})
	failed := next.(Model)
	if !failed.reauthRetryAfter.IsZero() {
		t.Error("a result nobody was waiting for started a cooldown")
	}
	if failed.commandMsg != "" {
		t.Errorf("commandMsg = %q, want it left alone", failed.commandMsg)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
