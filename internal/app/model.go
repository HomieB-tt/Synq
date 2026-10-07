// Package app contains the Bubble Tea root model, update, and view
// loops for the Synq client: the four global tabs (Feed, Nodes, Chat,
// Profile), pane focus, and the command palette.
package app

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
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

// bootMessages are shown in sequence on the startup splash screen, one
// at a time, before the main TUI appears.
//
// "Establishing connection..." is currently cosmetic - synq-server's
// auth/WS handshake (synq-server-DESIGN.md section 1-2) doesn't exist
// yet, so there is no real connection to establish. This is written so
// swapping the fixed-duration boot sequence for a real connection
// check later only touches bootTickMsg handling in Update, not the
// rendering code.
var bootMessages = []string{
	"Loading identity...",
	"Establishing connection...",
	"Syncing feed...",
}

// spinnerFrames is a small hand-rolled animation (deliberately not
// using bubbles/spinner - see TECH_STACK.md on why this project avoids
// pulling in bubbles components whose exact v2 API hasn't been
// confirmed against live documentation).
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

const (
	bootTickInterval = 80 * time.Millisecond
	bootTicksPerMsg  = 6 // ~480ms shown per boot message
)

// bootTickMsg drives both the spinner animation and, every
// bootTicksPerMsg ticks, advancing to the next boot message (or ending
// the boot sequence after the last one).
type bootTickMsg time.Time

func bootTick() tea.Cmd {
	return tea.Tick(bootTickInterval, func(t time.Time) tea.Msg {
		return bootTickMsg(t)
	})
}

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
// wsClientReadyMsg/wsStatusMsg/wsConnectErrorMsg and the two Cmds
// below follow the same "async event as a tea.Msg" shape as the
// GitHub device-flow messages just above, with one addition:
// waitForWSStatusCmd re-arms itself (returned again from within
// Update's wsStatusMsg case) rather than firing once, since a
// WebSocket connection's status can change at any time for the rest
// of the program's life, unlike a one-shot HTTP poll.

type wsClientReadyMsg struct {
	client *ws.Client
}

type wsStatusMsg ws.Status

type wsConnectErrorMsg string

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
// the client back to Update so it can be stored and its Status()
// channel listened to via waitForWSStatusCmd.
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

// Model is the Bubble Tea root model for the whole application.
type Model struct {
	identity *crypto.Identity
	store    *db.KeyStore
	theme    styles.Theme

	activeTab tab
	width     int
	height    int

	// booting is true while the startup splash (header + loading
	// animation) is showing, before the main tab UI appears.
	booting       bool
	bootMsgIndex  int
	bootTickCount int
	spinnerFrame  int

	// commandMode is true while the `:` / Ctrl+P command palette input
	// is open and capturing keystrokes.
	commandMode  bool
	commandInput string
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
	// There is no wire protocol wired up yet for actually sending or
	// receiving a chat message (see internal/ws's package doc comment)
	// - opening a thread with `:chat <hex-pubkey>` and composing into
	// it is real, tested, local UI and storage, but sending currently
	// only appends to chatStore as a local echo. See the TODO in
	// updateChatCompose's "enter" case for exactly where that changes
	// once synq-server's message envelope is known.
	chatStore  *chat.Store
	chatActive chat.ContactKey
	chatInput  string

	// chatUsernames remembers the username a contact was resolved
	// through via `:chat <username>` (see handleChatResolveResult), so
	// the thread list can display it instead of chat.ContactKey's raw
	// hex public key. Purely a display convenience - chat.Store itself
	// is keyed by ContactKey regardless, since that's what the
	// handshake and encryption actually need; a username is never a
	// substitute for the key itself. Not persisted - like chatStore,
	// this starts empty every launch.
	chatUsernames map[chat.ContactKey]string

	quitting bool
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

	return Model{
		identity:       id,
		store:          store,
		theme:          theme,
		activeTab:      tabFeed,
		booting:        true,
		githubClientID: os.Getenv("SYNQ_GITHUB_CLIENT_ID"),
		githubHandle:   githubHandle,
		displayName:    displayName,
		apiClient:      session.API,
		username:       session.Username,
		accessToken:    session.AccessToken,
		chatStore:      chat.NewStore(),
	}
}

// Run starts the Bubble Tea program. This is the hand-off point from
// the passphrase bootstrap and session resolution in cmd/synq/main.go
// into the interactive TUI.
func Run(id *crypto.Identity, store *db.KeyStore, session Session) error {
	_, err := tea.NewProgram(New(id, store, session)).Run()
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
	cmds := []tea.Cmd{tea.RequestBackgroundColor, bootTick()}
	if m.apiClient != nil && m.accessToken != "" {
		cmds = append(cmds, startWSCmd(m.apiClient.BaseURL, m.accessToken))
	}
	return tea.Batch(cmds...)
}

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case bootTickMsg:
		return m.handleBootTick()

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

	case chatResolveResultMsg:
		return m.handleChatResolveResult(msg)

	case wsConnectErrorMsg:
		// Nothing the user can do about a malformed SYNQ_SERVER_URL
		// from inside the running TUI - surface it the same one-line
		// way a misconfigured GitHub client ID would be, rather than
		// crashing or silently staying "disconnected" with no
		// explanation.
		m.commandMsg = string(msg)
		return m, nil

	case wsClientReadyMsg:
		m.wsClient = msg.client
		return m, waitForWSStatusCmd(msg.client)

	case wsStatusMsg:
		status := ws.Status(msg)
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
		if m.themePickerOpen {
			return m.updateThemePicker(msg)
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

// handleBootTick advances the spinner every tick, and every
// bootTicksPerMsg ticks either moves to the next boot message or, after
// the last one, ends the splash screen and stops ticking.
func (m Model) handleBootTick() (tea.Model, tea.Cmd) {
	if !m.booting {
		// A stray tick arriving after boot ended (e.g. the user skipped
		// it by pressing a key) - do nothing, and critically, don't
		// requeue another tick, or it would tick forever in the
		// background.
		return m, nil
	}

	m.spinnerFrame = (m.spinnerFrame + 1) % len(spinnerFrames)
	m.bootTickCount++

	if m.bootTickCount >= bootTicksPerMsg {
		m.bootTickCount = 0
		m.bootMsgIndex++
		if m.bootMsgIndex >= len(bootMessages) {
			m.booting = false
			return m, nil
		}
	}

	return m, bootTick()
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
	switch msg.String() {
	case "q", "ctrl+c":
		m.quitting = true
		if m.wsClient != nil {
			m.wsClient.Close()
		}
		m.chatStore.Purge()
		return m, tea.Quit

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
		m.commandInput = ""
		m.commandMsg = ""
	}

	return m, nil
}

// updateCommandMode handles keys while the `:` command palette input
// is open.
func (m Model) updateCommandMode(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.commandMode = false
		m.commandInput = ""
		return m, nil

	case "enter":
		result, quit, cmd := m.runCommand(strings.TrimSpace(m.commandInput))
		m.commandMsg = result
		m.commandMode = false
		m.commandInput = ""
		if quit {
			m.quitting = true
			if m.wsClient != nil {
				m.wsClient.Close()
			}
			m.chatStore.Purge()
			return m, tea.Quit
		}
		return m, cmd

	case "backspace":
		if len(m.commandInput) > 0 {
			m.commandInput = m.commandInput[:len(m.commandInput)-1]
		}
		return m, nil
	}

	// Any other printable key: append its text to the command buffer.
	// msg.Text is the actual typed character(s) for a KeyPressMsg; this
	// deliberately ignores non-text keys (arrows, function keys, etc.)
	// rather than trying to enumerate every key that should NOT be
	// appended.
	if msg.Text != "" {
		m.commandInput += msg.Text
	}

	return m, nil
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
		m.chatInput = ""
		return m, nil

	case "enter":
		body := strings.TrimSpace(m.chatInput)
		m.chatInput = ""
		if body == "" {
			return m, nil
		}
		m.chatStore.Append(m.chatActive, chat.Message{
			Body:     []byte(body),
			At:       time.Now(),
			Outgoing: true,
		})
		// TODO(wire protocol): this only appends locally for now.
		// Actually transmitting it - sealing with SealMessage under
		// the session key from DeriveSessionKey, then sending it
		// through wsClient in whatever envelope synq-server's WS
		// protocol turns out to expect - is the one piece still
		// waiting on that protocol being specified. See internal/ws's
		// package doc comment.
		return m, nil

	case "backspace":
		if len(m.chatInput) > 0 {
			// Trim one rune, not one byte, so multi-byte UTF-8 input
			// (emoji, accented characters) doesn't get corrupted into
			// an invalid partial sequence - see commandInput's own
			// backspace case just above for the simpler byte-slice
			// version this deliberately doesn't copy.
			r := []rune(m.chatInput)
			m.chatInput = string(r[:len(r)-1])
		}
		return m, nil
	}

	if msg.Text != "" {
		m.chatInput += msg.Text
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
			if m.displayName == "" {
				return "No display name set. Usage: :name <your name> (or :name clear).", false, nil
			}
			return fmt.Sprintf("Display name: %s. Usage: :name <your name> (or :name clear).", m.displayName), false, nil
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

	case "verify":
		if m.identity == nil {
			return "Create an identity first. Restart Synq and choose \"Create your identity.\"", false, nil
		}
		if len(fields) != 2 {
			return "Usage: :verify <hex-encoded public key>", false, nil
		}
		theirKey, err := decodeHexPublicKey(fields[1])
		if err != nil {
			return fmt.Sprintf("Invalid public key: %v", err), false, nil
		}
		fp := crypto.Fingerprint(m.identity.SigningPublic, theirKey)
		return fmt.Sprintf("Fingerprint: %s", fp), false, nil

	case "chat":
		if m.identity == nil {
			return "Create an identity first. Restart Synq and choose \"Create your identity.\"", false, nil
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

// renderBoot draws the startup splash: the Synq header, a subtitle,
// and the current boot message with its spinner, all centered in the
// terminal. Skippable by pressing any key (see the tea.KeyPressMsg
// case in Update).
func (m Model) renderBoot() string {
	header := lipgloss.NewStyle().
		Bold(true).
		Foreground(m.theme.Palette.Accent).
		Background(m.theme.Palette.Background).
		Render("SYNQ")

	subtitle := m.theme.Muted.Render("terminal-native developer network")

	msg := bootMessages[m.bootMsgIndex]
	status := m.theme.StatusBar.Render(spinnerFrames[m.spinnerFrame] + " " + msg)

	block := strings.Join([]string{header, "", subtitle, "", status}, "\n")

	width := m.width
	if width <= 0 {
		width = 80
	}
	height := m.height
	if height <= 0 {
		height = 24
	}

	return placeWithBackground(block, width, height, m.theme.Palette.Background)
}

func (m Model) renderTabBar() string {
	var parts []string
	for i, t := range allTabs {
		label := fmt.Sprintf("%d %s", i+1, t)
		if t == m.activeTab {
			parts = append(parts, m.theme.TabActive.Render(label))
		} else {
			parts = append(parts, m.theme.TabInactive.Render(label))
		}
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, parts...)
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

	var body string
	switch m.activeTab {
	case tabFeed:
		body = "Feed is empty for now.\n\nPost with the CLI: cat file | synq post"
		if m.identity == nil {
			body += "\n\nYou're browsing as a guest - post authors show as \"node\" until you create an identity."
		}
	case tabNodes:
		if m.identity == nil {
			body = "Create an identity to build your network. Restart Synq and choose \"Create your identity.\""
		} else {
			body = "No nodes yet. Node requests will show up here."
		}
	case tabChat:
		if m.identity == nil {
			body = "Create an identity to send encrypted messages. Restart Synq and choose \"Create your identity.\""
		} else {
			body = m.renderChat()
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

// renderChat draws the Chat tab: the thread list when no thread is
// open (m.chatActive == ""), or one open conversation plus its compose
// input otherwise. See updateChatCompose for the key handling this
// pairs with, and the Model.chatStore doc comment for what "session-
// only" and "not yet connected" mean here.
func (m Model) renderChat() string {
	if m.chatActive == "" {
		threads := m.chatStore.Threads()
		if len(threads) == 0 {
			return "No open chats. Chat history is session-only - see DESIGN.md section 4.\n\n" +
				"Start one with :chat <username>."
		}

		var b strings.Builder
		b.WriteString("Chats:\n\n")
		for _, contact := range threads {
			msgs := m.chatStore.Messages(contact)
			last := msgs[len(msgs)-1]
			fmt.Fprintf(&b, "  %s  (%d) %s\n", m.chatContactLabel(contact), len(msgs), truncateRunes(string(last.Body), 40))
		}
		b.WriteString("\nOpen one with :chat <username>.")
		return b.String()
	}

	msgs := m.chatStore.Messages(m.chatActive)

	var b strings.Builder
	fmt.Fprintf(&b, "Chat with %s\n", m.chatContactLabel(m.chatActive))
	b.WriteString(m.theme.Muted.Render("Not yet connected to synq-server chat - messages stay local only for now.") + "\n\n")

	if len(msgs) == 0 {
		b.WriteString("(no messages yet)\n")
	}
	for _, msg := range msgs {
		who := "them"
		if msg.Outgoing {
			who = "you"
		}
		fmt.Fprintf(&b, "[%s] %s: %s\n", msg.At.Format("15:04:05"), who, msg.Body)
	}

	fmt.Fprintf(&b, "\n> %s\n", m.chatInput)
	b.WriteString(m.theme.Muted.Render("enter send (local only) · esc back to chat list"))
	return b.String()
}

// chatContactLabel displays contact by whatever username it was last
// resolved through (see handleChatResolveResult), falling back to its
// shortened raw key if none is on file - e.g. for a thread that
// predates this lookup ever happening (shouldn't occur today, since
// :chat requires a successful lookup to open a thread at all, but
// chat.ContactKey itself doesn't depend on a username ever having been
// known, so this stays correct even if that changes later).
func (m Model) chatContactLabel(contact chat.ContactKey) string {
	if username, ok := m.chatUsernames[contact]; ok {
		return username
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
			"  :theme <name>           set a theme directly"
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
	b.WriteString("Commands:\n")
	b.WriteString("  :name <your name>      set your local display name\n")
	b.WriteString("  :name clear            clear your local display name\n")
	b.WriteString("  :verify <hex-pubkey>   compare a contact's key fingerprint\n")
	b.WriteString("  :register <username>   register a username with synq-server (permanent)\n")
	b.WriteString("  :login                 reconnect after :logout, without restarting\n")
	b.WriteString("  :logout                revoke all sessions and disconnect\n")
	b.WriteString("  :chat <username>       open or start a chat thread (local only for now)\n")
	b.WriteString("  :github                link your GitHub account\n")
	b.WriteString("  :theme                 open the interactive theme picker (live preview)\n")
	b.WriteString("  :theme <name>          set a theme directly\n")
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
		return m.theme.CommandBar.Render(":" + m.commandInput)
	}
	if m.commandMsg != "" {
		return m.theme.Warning.Render(m.commandMsg)
	}
	if m.identity == nil {
		return m.theme.StatusBar.Render(
			"Guest mode - restart Synq to create an identity · 1-4 switch tabs · : command palette · q quit",
		)
	}
	return m.theme.StatusBar.Render(
		"1-4 switch tabs · Tab/Shift+Tab cycle · : command palette · q quit",
	)
}
