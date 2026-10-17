// Package app contains the Bubble Tea root model, update, and view
// loops for the Synq client: the four global tabs (Feed, Nodes, Chat,
// Profile), pane focus, and the command palette.
package app

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"fmt"
	"image/color"
	"net/url"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/HomieB-tt/synq/internal/api"
	"github.com/HomieB-tt/synq/internal/chat"
	"github.com/HomieB-tt/synq/internal/crypto"
	"github.com/HomieB-tt/synq/internal/db"
	"github.com/HomieB-tt/synq/internal/github"
	"github.com/HomieB-tt/synq/internal/lineedit"
	"github.com/HomieB-tt/synq/internal/transfer"
	"github.com/HomieB-tt/synq/internal/ui/styles"
	"github.com/HomieB-tt/synq/internal/ws"
)

// tab identifies one of the four global views, switched with 1-4 as
// described in the original feature spec.
type tab int

const (
	tabFeed tab = iota
	tabNodes
	tabChat
	tabProfile
)

func (t tab) String() string {
	switch t {
	case tabFeed:
		return "Feed"
	case tabNodes:
		return "Nodes"
	case tabChat:
		return "Chat"
	case tabProfile:
		return "Profile"
	default:
		return "?"
	}
}

var allTabs = []tab{tabFeed, tabNodes, tabChat, tabProfile}

// --- GitHub Device Flow wiring ---
//
// See internal/github/device_flow.go for the actual HTTP client (pure
// stdlib, fully unit-tested against the real github.com/api.github.com
// endpoints - see its tests). Everything here is just the Bubble Tea
// glue: message types carrying results back into Update, and the
// tea.Cmd functions that make the (blocking, but backgrounded-by-
// Bubble-Tea) HTTP calls.
//
// This is a genuinely optional verification badge, not part of Synq's
// own account system - see synq-server-DESIGN.md section 1.

const githubHTTPTimeout = 15 * time.Second

type githubDeviceCodeMsg struct {
	dc  *github.DeviceCode
	err error
}

type githubPollTickMsg struct{}

type githubPollResultMsg struct {
	result *github.PollResult
	err    error
}

type githubUserMsg struct {
	user *github.User
	err  error
}

func requestGitHubDeviceCodeCmd(clientID string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), githubHTTPTimeout)
		defer cancel()
		dc, err := github.RequestDeviceCode(ctx, clientID)
		return githubDeviceCodeMsg{dc: dc, err: err}
	}
}

// githubPollTickCmd waits intervalSecs (falling back to a safe default
// if GitHub ever reported zero or a negative value) before triggering
// the next poll. This is a plain tea.Tick, not a busy-loop - it costs
// nothing while waiting.
func githubPollTickCmd(intervalSecs int) tea.Cmd {
	d := time.Duration(intervalSecs) * time.Second
	if d <= 0 {
		d = 5 * time.Second
	}
	return tea.Tick(d, func(t time.Time) tea.Msg {
		return githubPollTickMsg{}
	})
}

func pollGitHubOnceCmd(clientID, deviceCode string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), githubHTTPTimeout)
		defer cancel()
		result, err := github.PollOnce(ctx, clientID, deviceCode)
		return githubPollResultMsg{result: result, err: err}
	}
}

func fetchGitHubUserCmd(token string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), githubHTTPTimeout)
		defer cancel()
		user, err := github.FetchUser(ctx, token)
		return githubUserMsg{user: user, err: err}
	}
}

// --- synq-server WebSocket connection (see internal/ws) ---
//
// wsClientReadyMsg/wsStatusMsg/wsFrameMsg/wsConnectErrorMsg and the
// three Cmds below follow the same "async event as a tea.Msg" shape as
// the GitHub device-flow messages just above, with one addition:
// waitForWSStatusCmd and waitForWSFrameCmd re-arm themselves (returned
// again from within Update's matching cases) rather than firing once,
// since a connection's status and its inbound frame stream both go on
// for the rest of the program's life, unlike a one-shot HTTP poll.
// They're separate Cmds, not one loop over both channels: each blocks
// until its own channel has something, and Bubble Tea runs each Cmd in
// its own goroutine.

type wsClientReadyMsg struct {
	client *ws.Client
}

type wsStatusMsg ws.Status

// wsFrameMsg is one inbound frame, already decoded by ws.DecodeFrame.
// err is non-nil only for a frame that violated the wire contract
// (malformed JSON, a message payload that isn't base64) - a frame
// with a type this client doesn't know parses fine and arrives with
// err nil, so an unexpected event type never reads as a broken
// connection. See ws.DecodeFrame for why that distinction exists.
type wsFrameMsg struct {
	event ws.Event
	err   error
}

type wsConnectErrorMsg string

// wsSendErrorMsg reports a frame Client.Send refused to queue - in
// practice, the connection was closed between Update deciding to send
// and the command running. Surfacing it matters more than it would for
// a failed handshake frame: a failed *message* send already has its
// local echo in the thread, so staying quiet would read as delivered.
// body (nil for handshake frames) comes back so Update can put the
// message where it will still go out later.
type wsSendErrorMsg struct {
	contact chat.ContactKey
	body    []byte
	err     error
}

// deriveWSURL builds synq-server's WebSocket URL from its REST base
// URL (SYNQ_SERVER_URL / apiClient.BaseURL): same host, /ws path,
// scheme swapped from http/https to ws/wss, and accessToken attached
// as the ?token=... query parameter synq-server's upgrade handler
// expects. Returns an error if baseURL doesn't parse, or uses a scheme
// other than http/https - there's no sensible ws(s) equivalent to fall
// back to in that case.
func deriveWSURL(baseURL, accessToken string) (string, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("parse %q: %w", baseURL, err)
	}

	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	default:
		return "", fmt.Errorf("unsupported scheme %q (expected http or https)", u.Scheme)
	}

	u.Path = "/ws"
	q := url.Values{}
	q.Set("token", accessToken)
	u.RawQuery = q.Encode()

	return u.String(), nil
}

// startWSCmd creates the ws.Client for synq-server's WS endpoint,
// derived from baseURL and accessToken (see deriveWSURL), and starts
// its Run loop in a background goroutine for the remaining lifetime of
// the program - Run only returns once Close is called, which happens
// on quit (see the "q"/"ctrl+c" and ":quit" handling in
// updateNormalMode and updateCommandMode). The returned message hands
// the client back to Update so it can be stored and its Status() and
// Incoming() channels listened to via waitForWSStatusCmd and
// waitForWSFrameCmd.
func startWSCmd(baseURL, accessToken string) tea.Cmd {
	return func() tea.Msg {
		wsURL, err := deriveWSURL(baseURL, accessToken)
		if err != nil {
			return wsConnectErrorMsg(fmt.Sprintf("SYNQ_SERVER_URL is invalid: %v", err))
		}
		client, err := ws.NewClient(wsURL)
		if err != nil {
			return wsConnectErrorMsg(fmt.Sprintf("SYNQ_SERVER_URL is invalid: %v", err))
		}
		go client.Run(context.Background())
		return wsClientReadyMsg{client: client}
	}
}

// waitForWSStatusCmd blocks for exactly one value from client's
// Status() channel and returns it as a tea.Msg. It does not loop
// itself - Update's wsStatusMsg case returns a fresh call to this
// function to keep listening, the same re-arming pattern
// githubPollTickCmd uses for polling.
func waitForWSStatusCmd(client *ws.Client) tea.Cmd {
	return func() tea.Msg {
		status, ok := <-client.Status()
		if !ok {
			return nil
		}
		return wsStatusMsg(status)
	}
}

// waitForWSFrameCmd blocks for exactly one raw frame on client's
// Incoming() channel, decodes it (ws.DecodeFrame), and returns it as
// a tea.Msg - the inbound half of the connection, where waitForWSStatusCmd
// is the lifecycle half. Re-arms itself the same way, from Update's
// wsFrameMsg case.
//
// The Done() case is what keeps this from leaking: Close never closes
// Incoming (unlike a channel a producer closes when it's finished),
// so once :logout or quit has closed the client this receive would
// otherwise block forever on a channel nothing will ever write to
// again. Returning nil stops the re-arm chain, exactly like
// wsStatusMsg's StatusClosed handling does.
func waitForWSFrameCmd(client *ws.Client) tea.Cmd {
	return func() tea.Msg {
		select {
		case data, ok := <-client.Incoming():
			if !ok {
				return nil
			}
			event, err := ws.DecodeFrame(data)
			return wsFrameMsg{event: event, err: err}
		case <-client.Done():
			return nil
		}
	}
}

// Model is the Bubble Tea root model for the whole application.
type Model struct {
	identity *crypto.Identity
	store    *db.KeyStore
	theme    styles.Theme

	activeTab tab
	width     int
	height    int

	// booting is true while the startup splash (header + the live
	// launch checklist in boot.go) is showing, before the main tab UI
	// appears. bootEnabled is the persisted `:boot` preference that
	// decides whether booting is ever true in the first place, and
	// bootSteps/bootStart/bootSettledAt/bootTickCount track the
	// checklist's progress (see handleBootTick).
	booting       bool
	bootEnabled   bool
	bootSteps     []bootStep
	bootStart     time.Time
	bootSettledAt int // tick on which every step settled; -1 until then
	bootTickCount int
	spinnerFrame  int

	// commandMode is true while the `:` / Ctrl+P command palette input
	// is open and capturing keystrokes.
	commandMode  bool
	commandInput string
	commandBack  int    // cursor: runes from the end of commandInput (see lineedit)
	commandMsg   string // last command result/error, shown until the next command

	// themePicker* back the interactive `:theme` popup (see
	// theme_picker.go): running `:theme` with no argument opens it.
	// While open, it owns key input the same way commandMode does, and
	// moving the highlighted entry immediately applies that theme so
	// the rest of the running UI - tabs, borders, status bar - re-skins
	// itself live as a preview, before anything is confirmed or saved.
	themePickerOpen  bool
	themePickerNames []string
	themePickerIndex int
	themePickerPrev  styles.Theme // theme active right before the picker opened; restored on Esc

	// File transfer (DESIGN.md section 10; see files.go). fileSession is
	// the server-side session attachments hang off, empty until one is
	// created. files lists the offers received this launch; fileCache is
	// the private directory they are decrypted into, created on first use
	// and deleted on quit. saveDir is where `s` copies a kept file to.
	// fileOpen is the "open with default app" hook, nil meaning the real
	// one - a field so tests never launch a program.
	fileSession     string
	fileCache       *transfer.Cache
	files           []*fileEntry
	filePickerOpen  bool
	filePickerIndex int
	saveDir         string
	fileOpen        transfer.Opener

	// Nodes tab (see nodes.go). nodes is the last relationship list the
	// server returned; nodesLoaded distinguishes "none" from "not
	// fetched yet", and nodesErr is the last failed refresh, if any.
	nodes       []api.NodeRelationship
	nodesLoaded bool
	nodesErr    string

	// Feed tab (see feed.go). feedPosts is what's loaded so far, newest
	// first, with feedIndex the selection; feedOpen is true while one
	// post and its comments are showing. composeOpen/compose are the
	// modal composer for posts and comments, and deleteArmed holds the
	// id of a post whose delete has been asked for once.
	feedPosts          []api.Post
	feedLoaded         bool
	feedErr            string
	feedMore           bool
	feedIndex          int       // selection: an index into feedVisible(), not feedPosts
	feedFilter         string    // category being shown ("" = all)
	feedSeenAt         time.Time // newest post the user has been shown; later ones are "new"
	feedOpen           bool
	feedComments       []api.Comment
	feedCommentsLoaded bool
	feedCommentsErr    string
	deleteArmed        string
	composeOpen        bool
	compose            composeState

	// deferredFrames / resolvingSenders hold message frames from active
	// nodes that this launch has not looked up yet, until the background
	// lookup finishes (sender.go). unread counts incoming messages per
	// thread that arrived while it was not on screen.
	// transfers are the uploads and downloads running now, polled for
	// progress (progress.go).
	transfers        []*activeTransfer
	deferredFrames   map[string][]ws.Event
	resolvingSenders map[string]bool
	unread           map[chat.ContactKey]int

	// cmdListOpen is true while the command list (Profile tab `c`, or
	// :commands) is showing, and cmdListIndex is the selected command's
	// position in commandSpecs. promptOpen / prompt back the popup that a
	// bare :send, :savedir etc. opens, and welcomeOpen the getting-started
	// guide. All three capture every key while open - see commands.go,
	// prompt.go and welcome.go.
	cmdListOpen  bool
	cmdListIndex int
	promptOpen   bool
	prompt       promptState
	welcomeOpen  bool
	welcomePage  int

	// helpOpen is true while the ? / `:help` overlay is showing (see
	// help.go). Like the theme picker it captures every key and
	// closes on the next press.
	helpOpen bool

	// connected reflects whether Synq has a live WS connection to
	// synq-server, driven by wsClient's Status() channel (see
	// startWSCmd/waitForWSStatusCmd). The WS connection itself is only
	// ever attempted once accessToken is non-empty (see Init) - a
	// guest, or an identity that has never registered or isn't
	// currently logged in, never gets a WS connection at all, matching
	// synq-server requiring ?token=... on the upgrade request.
	connected bool
	wsClient  *ws.Client

	// apiClient is synq-server's REST client (internal/api), nil if
	// SYNQ_SERVER_URL isn't configured - the same "stays off, zero
	// behavior change" default every optional integration here
	// follows. Unlike wsClient, this is safe to use even for a guest
	// (api.Client.ListFeed accepts an empty access token) or an
	// identity that hasn't logged in yet (e.g. :register itself calls
	// through this with no token at all).
	apiClient *api.Client

	// username is the name this identity has registered with
	// synq-server (db.PrefUsername), or "" if it never has - distinct
	// from displayName below; see PrefUsername's doc comment for why
	// conflating the two would be a real bug, not just a style choice.
	//
	// accessToken is intentionally never persisted anywhere, by
	// Model - not to db.KeyStore's preferences table (plaintext,
	// wrong trust level for a bearer credential even if this value
	// weren't short-lived anyway) and not to the identity vault either
	// (unlike the refresh token - see crypto.SealIdentity's doc
	// comment): Model never holds the vault passphrase at all (see
	// Session's doc comment), so re-sealing the vault to persist
	// anything is categorically not something Model can do. It lives
	// in memory only, for the life of the process, exactly like the
	// design calls for.
	username    string
	accessToken string

	// refreshToken renews accessToken when it runs out mid-session.
	// synq-server validates the token only once, at the WebSocket
	// handshake, so an expired one only breaks on the *next* reconnect
	// attempt - which is exactly what reports a ws.StatusAuthExpired,
	// answered by maybeReauthCmd. Memory only, like accessToken, and
	// never written anywhere (see Session.RefreshToken for why Model
	// can't simply read it out of the vault itself).
	//
	// reauthInFlight/reauthRetryAfter keep that from becoming a burst
	// of parallel refresh-or-login calls against synq-server's
	// rate-limited auth endpoints while the connection is failing
	// repeatedly: one re-auth at a time, and after a failed one, none
	// again until reauthRetryAfter has passed.
	refreshToken     string
	reauthInFlight   bool
	reauthRetryAfter time.Time

	// GitHub verification (synq-DESIGN.md section 9). Optional, and
	// entirely separate from Synq's own identity/auth.
	githubClientID        string // from SYNQ_GITHUB_CLIENT_ID; empty means "not configured"
	githubHandle          string // set once linking succeeds; persisted via db.PrefGitHubHandle
	githubUserCode        string // shown to the user while waiting for browser approval
	githubVerificationURI string
	githubDeviceCode      string
	githubPollInterval    int

	// displayName is a purely local, client-side label set via `:name`
	// and persisted via db.PrefDisplayName - not the server-backed
	// username system DESIGN.md describes (see that constant's doc
	// comment). Empty means "never set".
	displayName string

	// Chat (DESIGN.md section 4). chatStore is created once in New and
	// purged on quit alongside wsClient.Close() - nothing here is ever
	// persisted to db.KeyStore. chatActive is empty when the Chat tab
	// should show the thread list rather than an open conversation.
	//
	// Messages go out sealed under a per-contact session key derived
	// by the handshake in messaging.go, addressed with chatContacts'
	// user id, through ws.EncodeSend; inbound frames come back the
	// same way and are opened before they're appended (see
	// handleWSFrame). Composing while no key exists yet parks the body
	// until the handshake completes - see chatSessions below.
	chatStore  *chat.Store
	chatActive chat.ContactKey
	chatInput  string
	chatBack   int // cursor: runes from the end of chatInput (see lineedit)

	// chatContacts remembers what `:chat <username>`'s server-side
	// lookup learned about a contact (see handleChatResolveResult and
	// rememberContact): the username it was resolved through, and the
	// user id synq-server addresses messages with. Both matter, and
	// neither is a substitute for the other - a thread is still keyed
	// by chat.ContactKey (what the handshake and encryption need), the
	// thread list still displays it by username, while every frame on
	// the wire speaks only in user ids: ws.EncodeSend addresses the
	// recipient's, and an inbound frame's Event.From is the sender's.
	// Session-scoped like chatStore - nothing here is persisted, and a
	// thread without a lookup behind it simply has no entry (see
	// chatContactLabel's fallback).
	chatContacts map[chat.ContactKey]contactInfo

	// chatContactIDs is chatContacts' user id → contact index, the
	// half an inbound frame needs: From arrives as a bare id with no
	// username or key attached, so this is the only way to learn which
	// thread (if any) it belongs to. See Model.contactForUserID.
	chatContactIDs map[string]chat.ContactKey

	// chatSessions holds each contact's handshake state for this
	// launch - the other half of the wire protocol chatContacts
	// addresses: their ephemeral key, the session key derived from it,
	// and whatever was composed before that key existed. Session-
	// scoped like everything else here, and reset with it: the
	// ephemeral key this derives from dies with the process (§3), so
	// persisting a session key would preserve exactly the state DESIGN
	// says must not outlive the launch. See messaging.go.
	chatSessions map[chat.ContactKey]*chatSession

	// pendingKeyChanges holds lookups whose box key contradicted the
	// pin this device already trusts for that username - the state
	// DESIGN.md section 2's hard warning describes, waiting for the
	// user to either :accept it (which pins it and unblocks sending)
	// or discard the session. Session-scoped like the rest of chat:
	// a mismatch found again on the next launch re-lands here from
	// that launch's :chat lookup, so nothing is lost by not
	// persisting it - what persists is the pin itself (see
	// db.pinned_keys).
	pendingKeyChanges map[string]*api.PublicKeys

	// chatListIndex is the highlighted row in the thread-list view
	// (when chatActive == ""). Owned by chatview.go's moveChatList
	// and openSelectedChat.
	chatListIndex int

	// chatScroll is how many messages back from the newest the open
	// thread is scrolled. chatScrollFor records which contact that
	// offset belongs to, so switching threads resets the view
	// without each code path having to do it manually. Both are
	// owned by chatview.go's scrollChat / noteIncoming.
	chatScroll    int
	chatScrollFor chat.ContactKey

	// The unread divider (see syncChatMarker in chatview.go): drawn above
	// message chatMarkIdx of thread chatMarkFor, labelled with
	// chatMarkCount. A snapshot taken when the thread comes on screen.
	chatMarkFor   chat.ContactKey
	chatMarkIdx   int
	chatMarkCount int

	quitting bool
}

// contactInfo is one contact's non-key metadata: a display username
// and the server-side user id both wire directions speak in. Kept
// together so the two can't drift apart the way two parallel maps
// could - they are only ever written together, by rememberContact.
type contactInfo struct {
	Username string
	UserID   string
}

// New builds the initial root model for a given, already-unlocked
// identity (see cmd/synq/main.go for the passphrase bootstrap that
// produces it), the same KeyStore that identity was loaded from (which
// also holds non-secret preferences like the selected theme), and the
// Session main.go already resolved - see that type's doc comment for
// why establishing it is main.go's job, not something Model goes on to
// do for itself.
//
// The saved theme (if any) is loaded here, synchronously, rather than
// via a tea.Cmd - this runs once, before the program starts, not
// during the event loop, so there's no risk of it blocking input
// handling.
func New(id *crypto.Identity, store *db.KeyStore, session Session) Model {
	theme := styles.Default()
	if name, err := store.LoadPreference(db.PrefTheme); err == nil {
		if palette, ok := styles.All[name]; ok {
			theme = styles.New(palette)
		}
	}

	// Ignoring the error here is deliberate: ErrPreferenceNotFound just
	// means "never linked", which is exactly what an empty string
	// already represents - there's nothing to distinguish or report.
	githubHandle, _ := store.LoadPreference(db.PrefGitHubHandle)
	displayName, _ := store.LoadPreference(db.PrefDisplayName)
	var feedSeenAt time.Time
	if v, err := store.LoadPreference(db.PrefFeedSeen); err == nil {
		feedSeenAt, _ = time.Parse(time.RFC3339Nano, v) // a bad value just means no baseline
	}

	// The startup checklist (see boot.go) is opt-out via `:boot off` -
	// default on, but never forced on someone who turned it off.
	bootEnabled := true
	if v, err := store.LoadPreference(db.PrefBoot); err == nil && v == "off" {
		bootEnabled = false
	}

	saveDir, _ := store.LoadPreference(db.PrefSaveDir)

	return Model{
		saveDir:        saveDir,
		identity:       id,
		store:          store,
		theme:          theme,
		activeTab:      tabFeed,
		booting:        bootEnabled,
		bootEnabled:    bootEnabled,
		bootStart:      time.Now(),
		bootSettledAt:  -1,
		bootSteps:      bootStepsFor(session),
		githubClientID: os.Getenv("SYNQ_GITHUB_CLIENT_ID"),
		githubHandle:   githubHandle,
		displayName:    displayName,
		feedSeenAt:     feedSeenAt,
		apiClient:      session.API,
		username:       session.Username,
		accessToken:    session.AccessToken,
		refreshToken:   session.RefreshToken,
		chatStore:      chat.NewStore(),
	}
}

// Run starts the Bubble Tea program. This is the hand-off point from
// the passphrase bootstrap and session resolution in cmd/synq/main.go
// into the interactive TUI.
func Run(id *crypto.Identity, store *db.KeyStore, session Session) error {
	m := New(id, store, session)
	// The getting-started guide appears once, over the main UI after the
	// startup splash, and is remembered when dismissed. It is set here and
	// not in New so that building a Model (tests, mostly) never starts
	// with an overlay capturing the first key.
	if store != nil {
		if _, err := store.LoadPreference(db.PrefWelcomed); err != nil {
			m.welcomeOpen = true
		}
	}
	_, err := tea.NewProgram(m).Run()
	return err
}

// Init requests the terminal's background color so the theme can pick
// a sensible starting point (DESIGN.md section 7), kicks off the
// startup splash animation, and - only once there's an access token
// already in hand (main.go's automatic login/refresh, or a `:register`/
// `:login` run earlier in this same session) - starts connecting to
// synq-server (see startWSCmd). A guest, or an identity that hasn't
// registered or isn't currently logged in, never gets a WS connection
// attempt at all: synq-server's upgrade handler requires ?token=...,
// so attempting one with no token would just fail anyway, and the
// REST side (api.Client.ListFeed) already covers anonymous Feed
// browsing without needing a socket at all.
func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{tea.RequestBackgroundColor}
	if m.booting {
		cmds = append(cmds, bootTick())
	}
	if m.apiClient != nil {
		// Reading the Feed needs no login.
		cmds = append(cmds, m.fetchFeedCmd(0))
	}
	if m.apiClient != nil && m.accessToken != "" {
		cmds = append(cmds, startWSCmd(m.apiClient.BaseURL, m.accessToken))
		cmds = append(cmds, uploadBoxKeySigCmd(m.apiClient, m.accessToken, m.identity, m.username))
		cmds = append(cmds, m.startFileSession())
		cmds = append(cmds, m.fetchNodesCmd())
	}
	return tea.Batch(cmds...)
}

// Update implements tea.Model.
//
// It is a thin wrapper around update that does the bookkeeping which
// depends on what ended up on screen rather than on which message caused
// it: the unread divider and counts for the open thread
// (syncChatMarker), and keeping the thread-list selection on the same
// contact when a new message re-ranks the list. Doing these here, once,
// means none of the several ways of opening a thread (or of receiving a
// message) has to remember to.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	prev := m.selectedThread()
	next, cmd := m.update(msg)
	nm, ok := next.(Model)
	if !ok {
		return next, cmd
	}
	// "Since you last looked" ends when you leave the Feed or quit.
	if (m.activeTab == tabFeed && nm.activeTab != tabFeed) || (nm.quitting && !m.quitting) {
		nm.markFeedSeen()
	}
	nm.syncChatMarker()
	// A key press that moved the list selection means it; anything else
	// (a message arriving, say) may have re-ranked the list under it, so
	// put the selection back on the contact it was on.
	if _, isKey := msg.(tea.KeyPressMsg); !isKey {
		nm = nm.withListAnchor(prev)
	}
	return nm, cmd
}

func (m Model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case bootTickMsg:
		return m.handleBootTick()

	case transferTickMsg:
		// Redraw while a transfer runs; stop once none is.
		if len(m.transfers) == 0 {
			return m, nil
		}
		return m, transferTickCmd()

	case githubDeviceCodeMsg:
		return m.handleGitHubDeviceCode(msg)

	case githubPollTickMsg:
		return m, pollGitHubOnceCmd(m.githubClientID, m.githubDeviceCode)

	case githubPollResultMsg:
		return m.handleGitHubPollResult(msg)

	case githubUserMsg:
		return m.handleGitHubUser(msg)

	case registerResultMsg:
		return m.handleRegisterResult(msg)

	case loginResultMsg:
		return m.handleLoginResult(msg)

	case logoutResultMsg:
		return m.handleLogoutResult(msg)

	case reauthResultMsg:
		return m.handleReauthResult(msg)

	case chatResolveResultMsg:
		return m.handleChatResolveResult(msg)

	case feedLoadedMsg:
		return m.handleFeedLoaded(msg)

	case commentsLoadedMsg:
		return m.handleCommentsLoaded(msg)

	case feedActionMsg:
		return m.handleFeedAction(msg)

	case editorDoneMsg:
		return m.handleEditorDone(msg)

	case nodesLoadedMsg:
		return m.handleNodesLoaded(msg)

	case nodeActionMsg:
		return m.handleNodeAction(msg)

	case fileSessionMsg:
		return m.handleFileSession(msg)

	case fileHeartbeatTickMsg:
		return m.handleFileHeartbeatTick(msg)

	case fileHeartbeatResultMsg:
		return m.handleFileHeartbeatResult(msg)

	case fileSendResultMsg:
		return m.handleFileSendResult(msg)

	case fileReceiveResultMsg:
		return m.handleFileReceiveResult(msg)

	case fileViewResultMsg:
		return m.handleFileViewResult(msg)

	case fileSaveResultMsg:
		return m.handleFileSaveResult(msg)

	case pingResultMsg:
		m.commandMsg = string(msg)
		return m, nil

	case wsConnectErrorMsg:
		// Nothing the user can do about a malformed SYNQ_SERVER_URL
		// from inside the running TUI - surface it the same one-line
		// way a misconfigured GitHub client ID would be, rather than
		// crashing or silently staying "disconnected" with no
		// explanation.
		m.finishBootStep(bootConnect, stepFailed, string(msg))
		m.commandMsg = string(msg)
		return m, nil

	case wsSendErrorMsg:
		if msg.body != nil {
			// The body never left this process: park it so the next
			// send or handshake flush transmits it, instead of
			// letting the local echo stand in for delivery.
			session := m.sessionFor(msg.contact)
			session.pending = append(session.pending, msg.body)
		}
		m.commandMsg = fmt.Sprintf("That didn't reach synq-server: %v", msg.err)
		return m, nil

	case wsClientReadyMsg:
		m.wsClient = msg.client
		// Status and inbound frames are two independent streams off
		// the same connection, so they get two independent re-arming
		// Cmds rather than one loop trying to read both - a frame
		// arriving must not delay a status event, or vice versa.
		return m, tea.Batch(waitForWSStatusCmd(msg.client), waitForWSFrameCmd(msg.client))

	case wsStatusMsg:
		if m.wsClient == nil {
			// handleLogoutResult tore the client down (Close, then
			// nil) while this event was already in flight. Nothing to
			// listen on any more, and re-arming would deref the nil
			// client - but the connection is definitionally gone, so
			// this is also the moment to stop claiming otherwise.
			m.connected = false
			return m, nil
		}
		status := ws.Status(msg)
		// The splash's connect step tracks this connection's real
		// outcome (see boot.go): a successful dial is what "connected"
		// means, and a 401 is a failure the boot line can name before
		// the re-auth machinery below even starts.
		switch status {
		case ws.StatusConnected:
			m.finishBootStep(bootConnect, stepDone, "")
		case ws.StatusAuthExpired:
			m.finishBootStep(bootConnect, stepFailed, "access token rejected by synq-server")
		}
		if status == ws.StatusAuthExpired {
			// The dial was rejected because the access token carried
			// in the URL expired (synq-server checks it once, at the
			// handshake - API.md). Only a new token helps, so ask for
			// one - but keep the status pump running alongside it: if
			// this re-auth fails, the *next* AuthExpired out of Run's
			// backoff loop is the retry.
			m.connected = false
			reauth := m.maybeReauthCmd()
			return m, tea.Batch(waitForWSStatusCmd(m.wsClient), reauth)
		}
		m.connected = status == ws.StatusConnected
		if status == ws.StatusClosed {
			// The client has fully shut down (see Client.Run's doc
			// comment: StatusClosed is always last) and its Status()
			// channel will never produce another value - re-arming
			// waitForWSStatusCmd here would just block forever on a
			// goroutine nothing will ever wake again.
			return m, nil
		}
		return m, waitForWSStatusCmd(m.wsClient)

	case wsFrameMsg:
		return m.handleWSFrame(msg)

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tea.BackgroundColorMsg:
		return m.handleBackgroundColor(msg), nil

	case tea.KeyPressMsg:
		if m.booting {
			// Any key skips straight to the main UI rather than
			// trapping the user in a fixed-duration animation.
			m.booting = false
			return m, nil
		}
		if m.welcomeOpen {
			return m.updateWelcome(msg)
		}
		if m.cmdListOpen {
			return m.updateCommandList(msg)
		}
		if m.promptOpen {
			return m.updatePrompt(msg)
		}
		if m.helpOpen {
			// Any key closes it - same as the boot skip: the panel is
			// reference material, not a mode with its own key map.
			m.helpOpen = false
			return m, nil
		}
		if m.composeOpen {
			return m.updateCompose(msg)
		}
		if m.themePickerOpen {
			return m.updateThemePicker(msg)
		}
		if m.filePickerOpen {
			return m.updateFilePicker(msg)
		}
		if m.commandMode {
			return m.updateCommandMode(msg)
		}
		if m.activeTab == tabChat && m.chatActive != "" {
			return m.updateChatCompose(msg)
		}
		return m.updateNormalMode(msg)
	}

	return m, nil
}

// handleWSFrame processes one decoded inbound WebSocket frame (see
// wsFrameMsg and ws.DecodeFrame).
//
// Re-arming below is unconditional for as long as the client exists,
// including for frames this handler does nothing with. Dropping one
// frame and stopping the pump are very different failures: the first
// loses a single message, the second silently mutes the connection
// from then on, with nothing left in the UI to show why.
func (m Model) handleWSFrame(msg wsFrameMsg) (tea.Model, tea.Cmd) {
	if m.wsClient == nil {
		// handleLogoutResult tore the client down while this frame was
		// already in flight - same race as wsStatusMsg's nil guard, and
		// re-arming here would deref the nil client.
		return m, nil
	}

	switch {
	case msg.err != nil:
		m.commandMsg = fmt.Sprintf("Bad frame from synq-server: %v", msg.err)

	case msg.event.IsRateLimited():
		// The server's one self-delivered error (API.md): a send
		// exceeded this user's rate bucket. Reported as a "try again
		// shortly" notice rather than a failed send, because there is
		// no send-failure path to report against yet - see the TODO in
		// updateChatCompose.
		m.commandMsg = "synq-server rate-limited that - wait a moment and try again."

	case msg.event.Type == ws.EventTypeMessage:
		contact, ok := m.contactForUserID(msg.event.From)
		if !ok {
			// No `:chat` lookup on file for this sender, so there is
			// neither a static key to derive with nor a thread to put
			// anything in. If they are an active node - the only people
			// the server relays from - hold the frame and look them up
			// in the background (sender.go); anyone else is dropped
			// silently, because announcing each unknown sender would
			// hand them a spam vector for the command bar.
			return m, tea.Batch(waitForWSFrameCmd(m.wsClient), m.deferFromActiveNode(msg.event))
		}
		// Handshake or sealed message - messaging.go decides which,
		// derives/opens accordingly, and answers when answering is
		// needed. The reply is built before m is copied into the
		// return value (handleIncomingPayload mutates it - the
		// command message, session state, the thread itself), and the
		// frame pump re-arms alongside that work rather than after it,
		// so a slow reply can't delay the next frame.
		handle := m.handleIncomingPayload(contact, msg.event.Payload)
		return m, tea.Batch(waitForWSFrameCmd(m.wsClient), handle)

	case msg.event.Type == ws.EventTypeNodeRequest:
		// The frame carries only the requester's id; the refresh below
		// brings their username into the Nodes tab.
		m.commandMsg = "New node request - see the Nodes tab (2) and :accept <username>."
		return m, tea.Batch(waitForWSFrameCmd(m.wsClient), m.fetchNodesCmd())

	case msg.event.Type == ws.EventTypeNodeAccepted:
		m.commandMsg = "A node request you sent was accepted - see the Nodes tab (2)."
		return m, tea.Batch(waitForWSFrameCmd(m.wsClient), m.fetchNodesCmd())

	default:
		// A frame type this client has never heard of. DecodeFrame
		// deliberately parses those instead of erroring, so the server
		// can add event types without breaking every connected client
		// - and there's nothing about an unknown event to tell the
		// user yet.
	}

	return m, waitForWSFrameCmd(m.wsClient)
}

// handleGitHubDeviceCode processes the result of starting the Device
// Flow: either an error (shown and the flow left un-started), or the
// code the user needs to enter in their browser, plus kicking off the
// first poll after GitHub's requested interval.
func (m Model) handleGitHubDeviceCode(msg githubDeviceCodeMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.commandMsg = fmt.Sprintf("GitHub linking failed: %v", msg.err)
		return m, nil
	}

	m.githubUserCode = msg.dc.UserCode
	m.githubVerificationURI = msg.dc.VerificationURI
	m.githubDeviceCode = msg.dc.DeviceCode
	m.githubPollInterval = msg.dc.Interval
	m.commandMsg = fmt.Sprintf("Open %s and enter code: %s", msg.dc.VerificationURI, msg.dc.UserCode)

	return m, githubPollTickCmd(m.githubPollInterval)
}

// handleGitHubPollResult processes a single poll attempt against
// GitHub's access token endpoint. See internal/github's PollStatus
// values for what each of these means per GitHub's own Device Flow
// spec.
func (m Model) handleGitHubPollResult(msg githubPollResultMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.commandMsg = fmt.Sprintf("GitHub linking failed: %v", msg.err)
		m.resetGitHubFlow()
		return m, nil
	}

	switch msg.result.Status {
	case github.PollPending:
		return m, githubPollTickCmd(m.githubPollInterval)

	case github.PollSlowDown:
		// GitHub requires widening the interval when told to slow
		// down - ignoring this risks the whole flow being rejected,
		// not just this one request.
		m.githubPollInterval = msg.result.Interval
		return m, githubPollTickCmd(m.githubPollInterval)

	case github.PollExpired:
		m.commandMsg = "GitHub linking timed out before you approved it. Run :github to try again."
		m.resetGitHubFlow()
		return m, nil

	case github.PollDenied:
		m.commandMsg = "GitHub authorization was denied."
		m.resetGitHubFlow()
		return m, nil

	case github.PollSuccess:
		m.commandMsg = "Authorized - fetching your GitHub username..."
		return m, fetchGitHubUserCmd(msg.result.Token)

	default:
		m.commandMsg = fmt.Sprintf("GitHub linking failed: unexpected status %q", msg.result.Status)
		m.resetGitHubFlow()
		return m, nil
	}
}

// handleGitHubUser processes the final step: turning a successful
// token into an actual username, and persisting it so it survives a
// restart (mirroring how the selected theme is persisted).
func (m Model) handleGitHubUser(msg githubUserMsg) (tea.Model, tea.Cmd) {
	m.resetGitHubFlow()

	if msg.err != nil {
		m.commandMsg = fmt.Sprintf("GitHub linking failed: %v", msg.err)
		return m, nil
	}

	m.githubHandle = msg.user.Login
	if err := m.store.SavePreference(db.PrefGitHubHandle, msg.user.Login); err != nil {
		m.commandMsg = fmt.Sprintf("Linked as @%s, but couldn't save it - you'll need to relink after restarting: %v", msg.user.Login, err)
		return m, nil
	}

	m.commandMsg = fmt.Sprintf("GitHub linked: @%s", msg.user.Login)
	return m, nil
}

// resetGitHubFlow clears in-progress Device Flow state, whether the
// flow succeeded, failed, expired, or was denied. It deliberately does
// not touch githubHandle - only the transient, in-progress fields.
func (m *Model) resetGitHubFlow() {
	m.githubUserCode = ""
	m.githubVerificationURI = ""
	m.githubDeviceCode = ""
	m.githubPollInterval = 0
}

// handleBackgroundColor uses the terminal's reported background only
// as a light/dark hint, not to pick between unrelated named palettes
// (Dracula/Nord/Monokai are all dark themes - see styles.go). On an
// unusually light terminal, this nudges toward higher-contrast
// defaults within the current palette rather than silently swapping
// the whole theme out from under the user.
func (m Model) handleBackgroundColor(msg tea.BackgroundColorMsg) Model {
	if !msg.IsDark() {
		m.commandMsg = "Note: your terminal background looks light. " +
			"Synq's built-in themes are designed for dark backgrounds - " +
			"try :theme nord or :theme monokai if contrast looks off."
	}
	return m
}

// updateNormalMode handles keys when the command palette is closed.
func (m Model) updateNormalMode(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.activeTab == tabFeed {
		next, cmd, handled := m.updateFeedKeys(msg)
		if handled {
			return next, cmd
		}
		m = next.(Model)
	}

	// The thread list is the one normal-mode screen with a selection to
	// move. These keys are only claimed there - j/k/enter mean nothing
	// anywhere else, and an open thread never reaches this function
	// (see Update: it routes to updateChatCompose first).
	if m.activeTab == tabChat && m.identity != nil && m.chatActive == "" {
		switch msg.String() {
		case "up", "k":
			m.wrapChatList(-1)
			return m, nil
		case "down", "j":
			m.wrapChatList(1)
			return m, nil
		case "enter":
			m.openSelectedChat()
			return m, nil
		}
	}

	switch msg.String() {
	case "q", "ctrl+c":
		m.quitting = true
		if m.wsClient != nil {
			m.wsClient.Close()
		}
		m.chatStore.Purge()
		return m, m.quitCmd()

	case "1":
		m.activeTab = tabFeed
	case "2":
		m.activeTab = tabNodes
	case "3":
		m.activeTab = tabChat
	case "4":
		m.activeTab = tabProfile

	case "tab":
		m.activeTab = nextTab(m.activeTab, 1)
	case "shift+tab":
		m.activeTab = nextTab(m.activeTab, -1)

	case ":", "ctrl+p":
		m.commandMode = true
		m.commandInput, m.commandBack = "", 0
		m.commandMsg = ""

	case "?":
		m.helpOpen = true

	case "c":
		// The command list lives under Profile; anywhere else "c" is
		// not a key (on the Feed it's handled before this point).
		if m.activeTab == tabProfile {
			m.openCommandList()
		}

	case "r":
		if m.activeTab == tabNodes {
			return m, m.fetchNodesCmd()
		}
	}

	return m, nil
}

// updateCommandMode handles keys while the `:` command palette input
// is open.
func (m Model) updateCommandMode(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.commandMode = false
		m.commandInput, m.commandBack = "", 0
		return m, nil

	case "enter":
		line := m.commandInput
		m.commandMode = false
		m.commandInput, m.commandBack = "", 0
		return m.execCommand(line)
	}

	// Cursor movement and deletion (arrows, Home/End, Backspace, Delete,
	// Ctrl+W/U/K, word jumps). Backspace used to slice a byte here, which
	// corrupted a multi-byte character; lineedit works in runes.
	if text, back, ok := lineedit.Apply(m.commandInput, m.commandBack, msg.String(), false); ok {
		m.commandInput, m.commandBack = text, back
		return m, nil
	}

	// Any other printable key: insert its text at the cursor. msg.Text is
	// the actual typed character(s) for a KeyPressMsg; this deliberately
	// ignores non-text keys (function keys, etc.) rather than trying to
	// enumerate every key that should NOT be inserted.
	if msg.Text != "" {
		m.commandInput, m.commandBack = lineedit.Insert(m.commandInput, m.commandBack, sanitizeForTerminal(msg.Text))
	}

	return m, nil
}

// bare is runCommand's answer to a command that needs an argument but was
// run without one: open its prompt (see prompt.go), or say why it can't.
func (m *Model) bare(name string) (string, bool, tea.Cmd) {
	return m.openPrompt(name), false, nil
}

// execCommand runs one palette command line and turns the result into the
// model update: the message for the bottom bar, an async command, or quit.
// It is the one path for a command typed in the palette, picked from the
// command list, or finished in a prompt.
func (m Model) execCommand(line string) (tea.Model, tea.Cmd) {
	result, quit, cmd := m.runCommand(strings.TrimSpace(line))
	m.commandMsg = result
	if quit {
		m.quitting = true
		if m.wsClient != nil {
			m.wsClient.Close()
		}
		m.chatStore.Purge()
		return m, m.quitCmd()
	}
	return m, cmd
}

// updateChatCompose handles keys while a chat thread is open (Chat tab
// active, m.chatActive non-empty) - the same "own all key input while
// this mode is active" pattern updateCommandMode and updateThemePicker
// use, so normal-mode keys like "1" or "tab" type into the message
// instead of switching tabs out from under it.
func (m Model) updateChatCompose(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		// Back to the thread list, not out of the Chat tab entirely -
		// symmetric with how Esc closes the theme picker back to
		// whatever was showing before, not all the way out of Profile.
		m.chatActive = ""
		m.chatInput, m.chatBack = "", 0
		return m, nil

	case "ctrl+p":
		// The palette is otherwise unreachable here - ":" types into
		// the message - and :send only makes sense inside a thread.
		// The thread stays open underneath; the draft is kept.
		m.commandMode = true
		m.commandInput, m.commandBack = "", 0
		m.commandMsg = ""
		return m, nil

	case "pgup":
		m.scrollChat(chatPageStep)
		return m, nil
	case "pgdown":
		m.scrollChat(-chatPageStep)
		return m, nil
	// Home and End belong to the text cursor now (as in any text box);
	// jumping to the oldest/newest message moved to Ctrl+Home/Ctrl+End.
	case "ctrl+home":
		m.scrollChat(m.chatStore.Len(m.chatActive))
		return m, nil
	case "ctrl+end":
		m.scrollChat(-m.chatStore.Len(m.chatActive))
		return m, nil

	case "enter":
		body := strings.TrimSpace(m.chatInput)
		if body == "" {
			m.chatInput, m.chatBack = "", 0
			return m, nil
		}
		if reason := m.keyBlockReason(m.chatActive); reason != "" {
			// Refused before the local echo: a draft that can't be
			// encrypted must not look like it was sent, and it stays
			// in the composer so the warning doesn't eat what the
			// user typed. See DESIGN.md section 2's hard warning.
			m.commandMsg = reason
			return m, nil
		}
		m.chatInput, m.chatBack = "", 0
		m.chatStore.Append(m.chatActive, chat.Message{
			Body:     []byte(body),
			At:       time.Now(),
			Outgoing: true,
		})
		// Sending is a request to see what you sent: jump back to the
		// live end however far up you were reading.
		m.chatScroll, m.chatScrollFor = 0, m.chatActive
		// The echo above is local and instant; what comes back is
		// either the sealed frame (session key already established) or
		// a handshake frame plus a parked body to seal once the
		// contact's ephemeral key arrives - see messaging.go. Built
		// before m is copied into the return value, since sendMessage
		// may create the contact's session state (a map assignment on
		// m itself, not just on shared state behind a pointer).
		send := m.sendMessage(m.chatActive, []byte(body))
		return m, send
	}

	// Editing happens at the cursor: arrows, Home/End, Backspace, Delete,
	// Ctrl+W/U/K and word jumps are all lineedit's, in runes so a
	// multi-byte character is never split.
	if text, back, ok := lineedit.Apply(m.chatInput, m.chatBack, msg.String(), false); ok {
		m.chatInput, m.chatBack = text, back
		return m, nil
	}

	if msg.Text != "" {
		m.chatInput, m.chatBack = lineedit.Insert(m.chatInput, m.chatBack, sanitizeForTerminal(msg.Text))
	}

	return m, nil
}

// runCommand handles a submitted command palette entry. The bool
// return tells the caller whether to actually issue tea.Quit - setting
// a "quitting" flag alone does not stop the Bubble Tea event loop. The
// tea.Cmd return lets commands that need to do async work (currently
// just :github) kick that off; every other command returns nil here.
func (m *Model) runCommand(cmd string) (result string, quit bool, extraCmd tea.Cmd) {
	if cmd == "" {
		return "", false, nil
	}

	fields := strings.Fields(cmd)
	switch fields[0] {
	case "theme":
		if len(fields) == 1 {
			// No argument: open the interactive picker instead of just
			// printing a usage string - see theme_picker.go. The picker
			// itself is the response, so there's nothing to show in
			// commandMsg.
			m.openThemePicker()
			return "", false, nil
		}
		if len(fields) != 2 {
			return fmt.Sprintf("Usage: :theme [<%s>]", strings.Join(styles.Names(), "|")), false, nil
		}
		name := strings.ToLower(fields[1])
		palette, ok := styles.All[name]
		if !ok {
			return fmt.Sprintf("Unknown theme %q. Available: %s.", fields[1], strings.Join(styles.Names(), ", ")), false, nil
		}
		m.theme = styles.New(palette)
		if err := m.store.SavePreference(db.PrefTheme, name); err != nil {
			// The theme still applies for this session even if saving
			// it failed - just tell the user it won't survive a
			// restart, rather than silently losing their choice or
			// refusing to apply it.
			return fmt.Sprintf("Theme set to %s, but couldn't save it: %v", palette.Name, err), false, nil
		}
		return fmt.Sprintf("Theme set to %s.", palette.Name), false, nil

	case "name":
		if m.identity == nil {
			return "Create an identity first. Restart Synq and choose \"Create your identity.\"", false, nil
		}
		if len(fields) == 1 {
			// The prompt starts from the current name, so it also shows it.
			return m.bare("name")
		}
		if len(fields) == 2 && fields[1] == "clear" {
			m.displayName = ""
			if err := m.store.SavePreference(db.PrefDisplayName, ""); err != nil {
				return fmt.Sprintf("Cleared for this session, but couldn't save it: %v", err), false, nil
			}
			return "Display name cleared.", false, nil
		}
		name := strings.Join(fields[1:], " ")
		m.displayName = name
		if err := m.store.SavePreference(db.PrefDisplayName, name); err != nil {
			// Same "still applies this session" reasoning as :theme
			// above - don't lose or refuse the choice just because
			// persisting it failed.
			return fmt.Sprintf("Display name set to %q, but couldn't save it: %v", name, err), false, nil
		}
		return fmt.Sprintf("Display name set to %q.", name), false, nil

	case "accept":
		// The acknowledgement half of DESIGN.md section 2's hard
		// warning: adopt the stashed (contradicting) keys as the new
		// pin, which is also what unblocks sending to that contact.
		if len(fields) == 1 {
			return m.bare("accept")
		}
		if len(fields) != 2 {
			return "Usage: :accept <username>", false, nil
		}
		username := fields[1]
		pending, ok := m.pendingKeyChanges[username]
		if !ok {
			// Not a key change: it's a node request from them, if
			// anything (nodes.go).
			result, acceptCmd := m.acceptNode(username)
			return result, false, acceptCmd
		}
		if m.store == nil {
			return "No key store on this model - can't pin a new key.", false, nil
		}
		if err := m.store.PinKey(db.PinnedKey{
			Username:      username,
			BoxPubKey:     pending.BoxPubKey,
			SigningPubKey: pending.PubKey,
		}); err != nil {
			return fmt.Sprintf("Couldn't pin %s's new key: %v", username, err), false, nil
		}
		contactPub, err := decodeHexBoxPublicKey(pending.BoxPubKey)
		if err != nil {
			// Already validated when the change was stashed (see
			// handleChatResolveResult) - belt and braces, since
			// unpinning here without opening the thread would leave
			// the send path thinking the old contact is fine.
			return fmt.Sprintf("%s's stashed key is unusable: %v", username, err), false, nil
		}
		delete(m.pendingKeyChanges, username)
		contact := chat.NewContactKey(contactPub)
		m.rememberContact(contact, username, pending.UserID)
		m.activeTab = tabChat
		m.chatActive = contact
		return fmt.Sprintf("Pinned %s's new key - sending to them is unblocked. "+
			"Messages already in their earlier thread stay there, under the old key.", username), false, nil

	case "verify":
		if m.identity == nil {
			return "Create an identity first. Restart Synq and choose \"Create your identity\".", false, nil
		}
		if len(fields) == 1 {
			return m.bare("verify")
		}
		if len(fields) != 2 {
			return "Usage: :verify <username|hex-encoded public key>", false, nil
		}

		// Hex form first: an out-of-band key someone handed you over
		// another channel, compared directly. Checked before the
		// username form because a username that happens to be valid
		// 64-char hex is the only collision between the two, and
		// treating it as a key is the documented (if odd) behavior.
		if theirKey, err := decodeHexPublicKey(fields[1]); err == nil {
			fp := crypto.Fingerprint(m.identity.SigningPublic, theirKey)
			return fmt.Sprintf("Fingerprint: %s", fp), false, nil
		}

		username := fields[1]
		var theirSignHex, against string
		if pending, ok := m.pendingKeyChanges[username]; ok {
			// Verify the key you're about to trust, not the one you
			// already did - comparing the pin while its change is
			// pending would just re-confirm the old fingerprint.
			theirSignHex = pending.PubKey
			against = " against their NEW, not-yet-accepted key"
		} else {
			if m.store == nil {
				return "No key store on this model.", false, nil
			}
			pin, err := m.store.LoadPinnedKey(username)
			if errors.Is(err, db.ErrPinNotFound) {
				return fmt.Sprintf("No pinned key for %s yet - :chat %s first, then verify.", username, username), false, nil
			}
			if err != nil {
				return fmt.Sprintf("Couldn't read %s's pinned key: %v", username, err), false, nil
			}
			theirSignHex = pin.SigningPubKey
		}
		if theirSignHex == "" {
			return fmt.Sprintf("synq-server has no signing key on file for %s - nothing to compare.", username), false, nil
		}
		theirKey, err := decodeHexPublicKey(theirSignHex)
		if err != nil {
			return fmt.Sprintf("%s has an invalid signing key on file: %v", username, err), false, nil
		}
		fp := crypto.Fingerprint(m.identity.SigningPublic, theirKey)
		return fmt.Sprintf("Fingerprint%s for %s and you: %s", against, username, fp), false, nil

	case "chat":
		if m.identity == nil {
			return "Create an identity first. Restart Synq and choose \"Create your identity.\"", false, nil
		}
		if len(fields) == 1 {
			return m.bare("chat")
		}
		if len(fields) != 2 {
			return "Usage: :chat <username>", false, nil
		}
		if m.apiClient == nil {
			return "Chat needs synq-server configured. Set SYNQ_SERVER_URL and restart Synq (see README.md).", false, nil
		}
		if m.accessToken == "" {
			return "Log in first - :register <username> if you haven't registered, or :login if you have.", false, nil
		}
		return fmt.Sprintf("Looking up %s...", fields[1]), false, resolveChatContactCmd(m.apiClient, m.accessToken, fields[1])

	case "node":
		if len(fields) == 1 {
			return m.bare("node")
		}
		result, nodeCmd := m.requestNode(strings.Join(fields[1:], ""))
		return result, false, nodeCmd

	case "nodes":
		m.activeTab = tabNodes
		if msg := m.nodePrecheck(); msg != "" {
			return msg, false, nil
		}
		return "Refreshing your nodes...", false, m.fetchNodesCmd()

	case "block":
		result, blockCmd := m.blockNode(fields[1:])
		return result, false, blockCmd

	case "post":
		cat := ""
		if len(fields) > 1 {
			cat = fields[1]
		}
		return m.startPost(cat), false, nil

	case "report":
		result, reportCmd := m.reportOpenPost(strings.TrimSpace(strings.TrimPrefix(cmd, "report")))
		return result, false, reportCmd

	case "send":
		if len(fields) == 1 {
			return m.bare("send")
		}
		result, sendCmd := m.sendFileCmd(strings.TrimSpace(strings.TrimPrefix(cmd, "send")))
		return result, false, sendCmd

	case "files":
		return m.openFilePicker(), false, nil

	case "savedir":
		if len(fields) == 1 {
			return m.bare("savedir")
		}
		return m.savedirCommand(strings.TrimSpace(strings.TrimPrefix(cmd, "savedir"))), false, nil

	case "register":
		if m.identity == nil {
			return "Create an identity first. Restart Synq and choose \"Create your identity.\"", false, nil
		}
		if m.apiClient == nil {
			return "Registration needs synq-server configured. Set SYNQ_SERVER_URL and restart Synq (see README.md).", false, nil
		}
		if m.username != "" {
			return fmt.Sprintf("Already registered as %s.", m.username), false, nil
		}
		if len(fields) == 1 {
			return m.bare("register")
		}
		if len(fields) != 2 {
			return "Usage: :register <username> (this is permanent - synq-server has no rename yet)", false, nil
		}
		return fmt.Sprintf("Registering as %s...", fields[1]), false, registerUsernameCmd(m.apiClient, m.identity, fields[1])

	case "login":
		if m.identity == nil {
			return "Create an identity first. Restart Synq and choose \"Create your identity.\"", false, nil
		}
		if m.apiClient == nil {
			return "Login needs synq-server configured. Set SYNQ_SERVER_URL and restart Synq (see README.md).", false, nil
		}
		if m.username == "" {
			return "Not registered yet - use :register <username> first.", false, nil
		}
		if m.accessToken != "" {
			return fmt.Sprintf("Already logged in as %s.", m.username), false, nil
		}
		return "Logging in...", false, loginCmd(m.apiClient, m.identity, m.username)

	case "logout":
		if m.accessToken == "" {
			return "Not logged in.", false, nil
		}
		return "Logging out...", false, logoutCmd(m.apiClient, m.accessToken)

	case "github":
		if m.identity == nil {
			return "Create an identity first. Restart Synq and choose \"Create your identity.\"", false, nil
		}
		if m.githubHandle != "" {
			return fmt.Sprintf("Already linked as @%s.", m.githubHandle), false, nil
		}
		if m.githubUserCode != "" {
			return fmt.Sprintf("Already waiting for authorization. Open %s and enter code: %s",
				m.githubVerificationURI, m.githubUserCode), false, nil
		}
		if m.githubClientID == "" {
			return "GitHub linking isn't configured. Set SYNQ_GITHUB_CLIENT_ID and restart Synq (see README.md).", false, nil
		}
		return "Starting GitHub authorization...", false, requestGitHubDeviceCodeCmd(m.githubClientID)

	case "commands":
		m.openCommandList()
		return "", false, nil

	case "welcome":
		m.welcomeOpen, m.welcomePage = true, 0
		return "", false, nil

	case "help":
		// The panel is the response - same pattern as `:theme` with
		// no argument opening the picker instead of printing a list.
		m.helpOpen = true
		return "", false, nil

	case "server":
		return m.serverCommand(fields[1:]), false, nil

	case "ping":
		if m.apiClient == nil {
			return "No synq-server configured. Set one with :server <address>.", false, nil
		}
		return "Pinging the server...", false, pingCmd(m.apiClient)

	case "boot":
		if len(fields) == 1 {
			state := "on"
			if !m.bootEnabled {
				state = "off"
			}
			return fmt.Sprintf("Startup checklist: %s. Usage: :boot <on|off> (applies from the next launch).", state), false, nil
		}
		if len(fields) != 2 || (fields[1] != "on" && fields[1] != "off") {
			return "Usage: :boot <on|off>", false, nil
		}
		m.bootEnabled = fields[1] == "on"
		value := "off"
		if m.bootEnabled {
			value = "on"
		}
		if err := m.store.SavePreference(db.PrefBoot, value); err != nil {
			return fmt.Sprintf("Startup checklist set to %s, but couldn't save it: %v", value, err), false, nil
		}
		return fmt.Sprintf("Startup checklist %s - effective from the next launch.", value), false, nil

	case "quit", "q":
		return "", true, nil

	default:
		return fmt.Sprintf("Unknown command: %s", fields[0]), false, nil
	}
}

// decodeHexPublicKey parses a hex-encoded Ed25519 public key, as
// pasted by the user for :verify. There is no key lookup yet (no
// server, no Nodes tab data - see synq-server-DESIGN.md) so this takes
// the raw key directly rather than a username.
func decodeHexPublicKey(s string) (ed25519.PublicKey, error) {
	raw, err := hex.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("not valid hex: %w", err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("expected %d bytes, got %d", ed25519.PublicKeySize, len(raw))
	}
	return ed25519.PublicKey(raw), nil
}

// decodeHexBoxPublicKey parses a hex-encoded X25519 public key - the
// "static" key DESIGN.md section 3's handshake, and chat.ContactKey,
// both key off of. Kept separate from decodeHexPublicKey just above,
// which parses an Ed25519 *signing* key for :verify's fingerprint
// comparison: both happen to be 32 bytes, but conflating the two key
// types because the encoding is the same size would be a real,
// easy-to-miss correctness bug - an identity's signing key and its
// key-exchange key are never interchangeable.
func decodeHexBoxPublicKey(s string) (*[32]byte, error) {
	raw, err := hex.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("not valid hex: %w", err)
	}
	if len(raw) != 32 {
		return nil, fmt.Errorf("expected 32 bytes, got %d", len(raw))
	}
	var key [32]byte
	copy(key[:], raw)
	return &key, nil
}

// nextTab cycles through allTabs by delta (1 forward, -1 backward),
// wrapping around at either end.
func nextTab(current tab, delta int) tab {
	idx := int(current)
	n := len(allTabs)
	idx = (idx + delta + n) % n
	return allTabs[idx]
}

// View implements tea.Model.
func (m Model) View() tea.View {
	if m.quitting {
		return tea.NewView("")
	}

	var content string
	if m.booting {
		content = m.renderBoot()
	} else {
		var b strings.Builder
		b.WriteString(m.renderHeader())
		b.WriteString("\n\n")
		b.WriteString(m.renderContent())
		b.WriteString("\n")
		b.WriteString(m.renderBottomBar())
		content = b.String()
	}

	width := m.width
	if width <= 0 {
		width = 80
	}
	height := m.height
	if height <= 0 {
		height = 24
	}

	// Fill every cell of the viewport via ordinary SGR styling, not
	// tea.View's BackgroundColor/ForegroundColor fields. Those fields
	// set the terminal's actual color profile via an OSC escape
	// sequence - a persistent, global change, not one scoped to this
	// program's alt-screen session - and there's no confirmed way to
	// reliably undo that on quit. Styling the content itself has none
	// of that risk: it's confined to the alt-screen buffer and is
	// discarded automatically when Synq exits, the same way vim or
	// htop never change your prompt's colors after you quit them.
	screen := m.theme.Screen.Width(width).Height(height).Render(content)

	v := tea.NewView(screen)
	v.AltScreen = true
	return v
}

// placeWithBackground centers block - which may have ragged (unequal-
// width) lines - within a width x height viewport. This replaces
// lipgloss.Place for every full-screen placement in this file: Place
// positions a block correctly but fills the margin it adds around
// that block - left/right to center it horizontally, top/bottom
// vertically, and between any of the block's own lines that are
// shorter than the widest one - with plain, unstyled space. That
// space carries no background, so it shows the terminal's raw default
// color instead of the theme's, visible as a stray patch or line next
// to any centered content (the same class of bug renderHeader's doc
// comment already describes for horizontal gaps between independently
// -rendered spans). Every cell placeWithBackground adds instead goes
// through fill, an explicitly backgrounded style, so nothing is ever
// left unstyled.
func placeWithBackground(block string, width, height int, bg color.Color) string {
	fill := lipgloss.NewStyle().Background(bg)

	lines := strings.Split(block, "\n")
	blockWidth := 0
	for _, l := range lines {
		if w := lipgloss.Width(l); w > blockWidth {
			blockWidth = w
		}
	}
	if blockWidth > width {
		blockWidth = width
	}

	sideGap := width - blockWidth
	if sideGap < 0 {
		sideGap = 0
	}
	leftMargin := fill.Render(strings.Repeat(" ", sideGap/2))
	rightMargin := fill.Render(strings.Repeat(" ", sideGap-sideGap/2))

	for i, l := range lines {
		if gap := blockWidth - lipgloss.Width(l); gap > 0 {
			l += fill.Render(strings.Repeat(" ", gap))
		}
		lines[i] = leftMargin + l + rightMargin
	}

	topGap := (height - len(lines)) / 2
	if topGap < 0 {
		topGap = 0
	}
	bottomGap := height - len(lines) - topGap
	if bottomGap < 0 {
		bottomGap = 0
	}
	blankRow := fill.Render(strings.Repeat(" ", width))

	out := make([]string, 0, topGap+len(lines)+bottomGap)
	for i := 0; i < topGap; i++ {
		out = append(out, blankRow)
	}
	out = append(out, lines...)
	for i := 0; i < bottomGap; i++ {
		out = append(out, blankRow)
	}
	return strings.Join(out, "\n")
}

func (m Model) renderTabBar() string {
	var parts []string
	for i, t := range allTabs {
		label := fmt.Sprintf("%d %s", i+1, t)
		badge := m.tabBadge(t)
		if badge > 0 {
			label += " " + m.tabBadgeText(t, badge)
		}
		switch {
		case t == m.activeTab:
			parts = append(parts, m.theme.TabActive.Render(label))
		case badge > 0:
			// Something needs you: the label stands out from the other
			// inactive tabs, in the theme's accent colour.
			parts = append(parts, m.theme.TabInactive.Foreground(m.theme.Palette.Accent).Bold(true).Render(label))
		default:
			parts = append(parts, m.theme.TabInactive.Render(label))
		}
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, parts...)
}

// tabBadge is the number shown beside a tab's name: chat messages you
// haven't seen, or node requests waiting on you. Zero shows nothing.
// Both count things that need you, not things that exist.
func (m Model) tabBadge(t tab) int {
	switch t {
	case tabChat:
		return m.unreadTotal()
	case tabNodes:
		if m.identity != nil && m.nodesLoaded {
			return len(groupNodes(m.nodes, m.username).Incoming)
		}
	}
	return 0
}

// tabBadgeText is the badge as written in the tab bar.
func (m Model) tabBadgeText(t tab, n int) string {
	if t == tabNodes {
		return fmt.Sprintf("(%d pending)", n)
	}
	return fmt.Sprintf("(%d new)", n)
}

// renderHeader combines the tab bar (left) with the connection status
// (right), spanning the full terminal width. connected reflects a real
// ws.Client connection when SYNQ_SERVER_URL is set (see Model.connected
// and startWSCmd) - and stays permanently false, same as before that
// existed, when it isn't.
//
// Every separator between independently-rendered spans uses an
// explicitly backgrounded style, not a bare " " - a plain space
// between two ANSI-reset spans has no background color of its own,
// and the outer full-screen wrap in View() only pads at the end of a
// line/screen, not gaps in the middle of one. This is the same class
// of bug as the terminal-background issue fixed earlier, just at
// smaller scale, so it's worth guarding against explicitly here too.
func (m Model) renderHeader() string {
	fill := lipgloss.NewStyle().Background(m.theme.Palette.Background)

	tabs := m.renderTabBar()
	status := m.renderConnectionStatus()
	if m.identity == nil {
		status = m.theme.Warning.Render("GUEST") + fill.Render(" ") + status
	}

	width := m.width
	if width <= 0 {
		width = 80
	}

	gap := width - lipgloss.Width(tabs) - lipgloss.Width(status)
	if gap < 1 {
		gap = 1
	}

	return tabs + fill.Render(strings.Repeat(" ", gap)) + status
}

// renderConnectionStatus draws the online/offline indicator. Green for
// online is a fixed, theme-independent color rather than something
// pulled from the palette - unlike everything else in styles.go, "is
// this thing connected" is a universal semantic that should look the
// same regardless of which theme is active, the same way a git diff's
// red/green doesn't change with your editor's color scheme.
func (m Model) renderConnectionStatus() string {
	if m.connected {
		return lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#4ade80")).
			Background(m.theme.Palette.Background).
			Render("● online")
	}
	return m.theme.Muted.Render("○ offline")
}

func (m Model) renderContent() string {
	width := m.width
	if width <= 0 {
		width = 80
	}
	height := m.height - 6 // tab bar + spacing + bottom bar
	if height < 3 {
		height = 3
	}

	if m.themePickerOpen {
		return placeWithBackground(m.renderThemePicker(), width, height, m.theme.Palette.Background)
	}
	if m.filePickerOpen {
		return placeWithBackground(m.renderFilePicker(), width, height, m.theme.Palette.Background)
	}
	if m.welcomeOpen {
		return placeWithBackground(m.renderWelcome(), width, height, m.theme.Palette.Background)
	}
	if m.cmdListOpen {
		return placeWithBackground(m.renderCommandList(), width, height, m.theme.Palette.Background)
	}
	if m.promptOpen {
		return placeWithBackground(m.renderPrompt(), width, height, m.theme.Palette.Background)
	}
	if m.helpOpen {
		return placeWithBackground(m.renderHelp(), width, height, m.theme.Palette.Background)
	}

	var body string
	if m.composeOpen {
		return m.theme.Border.
			Width(width-2).
			Height(height).
			Padding(1, 2).
			Render(m.renderCompose())
	}
	switch m.activeTab {
	case tabFeed:
		body = m.renderFeed()
	case tabNodes:
		body = m.renderNodes()
	case tabChat:
		if m.identity == nil {
			body = "Create an identity to send encrypted messages. Restart Synq and choose \"Create your identity.\""
		} else {
			// renderChat manages its own border and sizing via
			// panelStyle/contentSize/innerSize - return it directly
			// rather than double-wrapping it in the Border below.
			return m.renderChat()
		}
	case tabProfile:
		body = m.renderProfile()
	}

	return m.theme.Border.
		Width(width-2).
		Height(height).
		Padding(1, 2).
		Render(body)
}

// hintText returns the general key-hint shown in the status bar at the
// bottom of the chat pane. It covers thread-list navigation when no
// thread is open, and a lightweight reminder when one is (the
// compose-specific hint is drawn inside the pane by renderChatThread).
func (m Model) hintText() string {
	if m.chatActive == "" {
		return "↑/↓ select · enter open · :chat <username> start a new thread · ctrl+p commands"
	}
	return "ctrl+p commands · esc back to list"
}

// composeHint is the hint for the current open thread, computed from
// the active contact's block reason. It exists so tests and any code
// that already has a Model in hand can call it without pre-computing
// the reason; renderChatThread uses composeHintFor instead so it can
// share one keyBlockReason call with chatStatusLines.
func (m Model) composeHint() string {
	return m.composeHintFor(m.keyBlockReason(m.chatActive))
}

// composeHintFor is the hint line shown beneath the composer in an open
// thread, worded to match what pressing enter will actually do right
// now. It takes a pre-computed blockReason so renderChatThread can call
// keyBlockReason once and pass it to both chatStatusLines and here.
func (m Model) composeHintFor(blockReason string) string {
	if blockReason != "" {
		return fmt.Sprintf("enter won't send - :accept %s to trust their new key · ctrl+p commands · esc back to list",
			m.chatContactLabel(m.chatActive))
	}
	switch {
	case m.wsClient == nil:
		return "enter send (stays local while disconnected) · ctrl+p commands · esc back to list"
	case !m.hasSessionKey(m.chatActive):
		return "enter send (queued until the handshake completes) · ctrl+p commands · esc back to list"
	default:
		return "enter send · ctrl+p commands · esc back to list"
	}
}

// rememberContact records everything a successful :chat lookup learned
// about a contact, keeping the reverse index in step. Safe to call
// again for a contact already on file - the second lookup simply
// overwrites, and if this contact previously recorded a different user
// id (its key was re-registered under the same account, say) the stale
// reverse entry is removed first so no id can resolve to two contacts
// at once. The reverse index is also allowed to move to a newer
// contact: if someone else's ContactKey now claims a user id this
// store had under another contact, the newest lookup wins, which is
// the right behavior for exactly that re-registration case.
func (m *Model) rememberContact(contact chat.ContactKey, username, userID string) {
	if m.chatContacts == nil {
		m.chatContacts = make(map[chat.ContactKey]contactInfo)
		m.chatContactIDs = make(map[string]chat.ContactKey)
	}

	prev, had := m.chatContacts[contact]
	if had && prev.UserID != "" && prev.UserID != userID && m.chatContactIDs[prev.UserID] == contact {
		delete(m.chatContactIDs, prev.UserID)
	}

	m.chatContacts[contact] = contactInfo{Username: username, UserID: userID}
	if userID != "" {
		m.chatContactIDs[userID] = contact
	}
}

// pinOutcome is what applying DESIGN.md section 2's trust-on-first-use
// rule to one lookup turned out to be.
type pinOutcome int

const (
	// pinFirstUse: this device had never seen the username, so the
	// lookup's keys became the pin.
	pinFirstUse pinOutcome = iota
	// pinUnchanged: the lookup agreed with the existing pin.
	pinUnchanged
	// pinChanged: the lookup contradicted the pin. The new keys are
	// stashed in pendingKeyChanges for :accept, and the caller is
	// expected to say so loudly.
	pinChanged
	// pinFailed: the pin couldn't be read or written. Nothing was
	// stashed or updated - the caller must not open a thread on it,
	// since a thread that skips the pin check is exactly the hole
	// section 2 exists to close.
	pinFailed
)

// applyPin runs the TOFU decision for username's freshly-looked-up
// keys: pin on first sight, stay quiet when they match, stash (rather
// than adopt) them when they contradict an existing pin.
//
// A pin mismatch does NOT overwrite the stored keys - replacing them
// is the user's call, via :accept. The pin staying put is what lets
// keyBlockReason keep answering "this contact changed" for as long as
// the thread exists, and what makes :verify's fingerprint a stable
// thing to compare out-of-band rather than one that silently follows
// the server.
func (m *Model) applyPin(username string, keys *api.PublicKeys) (pinOutcome, error) {
	if m.store == nil {
		return pinFailed, errors.New("no key store on this model")
	}
	pin, err := m.store.LoadPinnedKey(username)
	switch {
	case errors.Is(err, db.ErrPinNotFound):
		if err := m.store.PinKey(db.PinnedKey{
			Username:      username,
			BoxPubKey:     keys.BoxPubKey,
			SigningPubKey: keys.PubKey,
		}); err != nil {
			return pinFailed, err
		}
		return pinFirstUse, nil
	case err != nil:
		return pinFailed, err
	case strings.EqualFold(pin.BoxPubKey, keys.BoxPubKey):
		return pinUnchanged, nil
	default:
		if m.pendingKeyChanges == nil {
			m.pendingKeyChanges = make(map[string]*api.PublicKeys)
		}
		m.pendingKeyChanges[username] = keys
		return pinChanged, nil
	}
}

// keyBlockReason reports why encrypting a new message to contact would
// be wrong right now (DESIGN.md section 2's hard warning), or "" when
// there's no reason to refuse.
//
// The comparison is against the persisted pin, not anything held in
// the session: the pin is the thing a substituted key has to contradict,
// and re-deriving the answer from it keeps every send-path caller
// honest even if some earlier lookup skipped the check. A contact with
// no username on file (a thread that predates :chat's lookup) has
// nothing to pin against and is not blocked - see chatContactLabel for
// why such a thread can exist.
func (m Model) keyBlockReason(contact chat.ContactKey) string {
	if m.store == nil || contact == "" {
		return ""
	}
	info, ok := m.chatContacts[contact]
	if !ok || info.Username == "" {
		return ""
	}
	pin, err := m.store.LoadPinnedKey(info.Username)
	if errors.Is(err, db.ErrPinNotFound) {
		return ""
	}
	if err != nil {
		return fmt.Sprintf("Can't check %s's pinned key (%v) - sending to them is blocked until that works.",
			info.Username, err)
	}
	if !strings.EqualFold(pin.BoxPubKey, string(contact)) {
		return fmt.Sprintf("%s's key changed since you pinned it - sending to them is blocked. "+
			":verify %s compares fingerprints, :accept %s trusts the new key.",
			info.Username, info.Username, info.Username)
	}
	return ""
}

// contactForUserID maps an inbound frame's sender id (Event.From, a
// bare server-side user id) back to the contact thread it belongs to.
// Reports false for an id we never resolved through :chat - which is
// a real possibility, not an error: the server relays anything to
// anybody's channel, and nothing yet ties an arbitrary inbound id to
// a thread. Callers treat that as "no thread to put this in", not as
// a malformed frame.
func (m Model) contactForUserID(userID string) (chat.ContactKey, bool) {
	if userID == "" {
		return "", false
	}
	contact, ok := m.chatContactIDs[userID]
	return contact, ok
}

// recipientUserID is the send side of contactForUserID: the id
// ws.EncodeSend must address a frame to. Reports false when this
// contact was never resolved through :chat, since a thread can't exist
// without that lookup having run first (it's what opens the thread),
// and addressing a frame with anything else - a username, a hex key -
// is not something synq-server's wire format accepts (API.md).
func (m Model) recipientUserID(contact chat.ContactKey) (string, bool) {
	info, ok := m.chatContacts[contact]
	if !ok || info.UserID == "" {
		return "", false
	}
	return info.UserID, true
}

// chatContactLabel displays contact by whatever username it was last
// resolved through (see handleChatResolveResult), falling back to its
// shortened raw key if none is on file - e.g. for a thread that
// predates this lookup ever happening (shouldn't occur today, since
// :chat requires a successful lookup to open a thread at all, but
// chat.ContactKey itself doesn't depend on a username ever having been
// known, so this stays correct even if that changes later).
func (m Model) chatContactLabel(contact chat.ContactKey) string {
	if info, ok := m.chatContacts[contact]; ok && info.Username != "" {
		return info.Username
	}
	return shortenContactKey(contact)
}

// shortenContactKey abbreviates a full hex-encoded public key for
// display (e.g. in the thread list) - plain byte-slicing is safe here
// specifically because a ContactKey is always hex, and hex is always
// single-byte ASCII, unlike message bodies elsewhere in this file that
// need rune-aware handling.
func shortenContactKey(c chat.ContactKey) string {
	s := string(c)
	if len(s) <= 16 {
		return s
	}
	return s[:8] + "…" + s[len(s)-4:]
}

// truncateRunes shortens s to at most n runes, appending an ellipsis if
// it was cut. Rune-, not byte-, based so truncating a message preview
// containing multi-byte UTF-8 (emoji, accented characters) can't slice
// through the middle of one and produce invalid text.
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func (m Model) renderProfile() string {
	if m.identity == nil {
		return "You're browsing as a guest.\n\n" +
			"Create an identity to set a display name, post, chat, build your\n" +
			"network, and see real usernames on the Feed instead of \"node\".\n\n" +
			"Restart Synq and choose \"Create your identity\" from the menu.\n\n" +
			"Theme switching works right now, even as a guest:\n" +
			"  :theme                  open the interactive theme picker (live preview)\n" +
			"  :theme <name>           set a theme directly\n\n" +
			"Press c to browse every command and what it does."
	}

	var b strings.Builder
	if m.displayName != "" {
		fmt.Fprintf(&b, "%s\n", m.displayName)
	} else {
		b.WriteString("(no display name set - see :name below)\n")
	}
	fmt.Fprintf(&b, "Public key:\n%x\n\n", m.identity.SigningPublic)
	fmt.Fprintf(&b, "synq-server: %s\n", m.serverStatusLabel())
	fmt.Fprintf(&b, "Connection: %s\n", connectionLabel(m.connected))
	fmt.Fprintf(&b, "GitHub: %s\n", m.githubStatusLabel())
	fmt.Fprintf(&b, "Theme: %s\n\n", m.theme.Palette.Name)
	b.WriteString("Commands - press c to browse them all, with what each one does.\n")
	b.WriteString("You can also press : (or Ctrl+P in a chat) and type one.\n\n")
	b.WriteString("Common ones:\n")
	b.WriteString("  :register <username>   claim your username (permanent)\n")
	b.WriteString("  :node <username>       ask someone to connect\n")
	b.WriteString("  :chat <username>       open an encrypted thread\n")
	b.WriteString("  :send                  send a file in the open chat\n")
	b.WriteString("  :theme                 pick a theme (live preview)\n")
	return b.String()
}

func connectionLabel(connected bool) string {
	if connected {
		return "online"
	}
	return "offline"
}

// githubStatusLabel covers all three states of the Device Flow: never
// started, waiting on the user to approve in a browser, or linked.
// serverStatusLabel summarizes synq-server account status - distinct
// from Connection below, which is specifically about the live WS
// connection. A registered-but-not-logged-in state is a real,
// reachable state this can show (e.g. right after :logout, or if
// cmd/synq/main.go's automatic login at startup failed because the
// server was unreachable) - not just "configured" vs "not".
func (m Model) serverStatusLabel() string {
	if m.apiClient == nil {
		return "not configured - set SYNQ_SERVER_URL to use it"
	}
	if m.username == "" {
		return "not registered - run :register <username>"
	}
	if m.accessToken == "" {
		return fmt.Sprintf("registered as %s, but not logged in - run :login", m.username)
	}
	return fmt.Sprintf("[✓ %s]", m.username)
}

func (m Model) githubStatusLabel() string {
	if m.githubHandle != "" {
		return fmt.Sprintf("[✓ @%s]", m.githubHandle)
	}
	if m.githubUserCode != "" {
		return fmt.Sprintf("waiting - open %s and enter code %s", m.githubVerificationURI, m.githubUserCode)
	}
	return "not linked - run :github to verify"
}

func (m Model) renderBottomBar() string {
	if m.commandMode {
		return m.commandBarLine()
	}
	if s := m.transferStatus(max(m.width, 20)); s != "" {
		return m.theme.AccentText.Render(s)
	}
	if m.commandMsg != "" {
		return m.theme.Warning.Render(m.commandMsg)
	}
	if m.identity == nil {
		return m.theme.StatusBar.Render(
			"Guest mode - restart Synq to create an identity · 1-4 switch tabs · : commands · ? help · q quit",
		)
	}
	return m.theme.StatusBar.Render(
		"1-4 switch tabs · Tab/Shift+Tab cycle · : commands · ? help · q quit",
	)
}
