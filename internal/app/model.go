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
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/HomieB-tt/synq/internal/crypto"
	"github.com/HomieB-tt/synq/internal/db"
	"github.com/HomieB-tt/synq/internal/github"
	"github.com/HomieB-tt/synq/internal/ui/styles"
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

	// connected reflects whether Synq has a live connection to
	// synq-server. It is currently a stub that is never set true -
	// there is no server to connect to yet (see synq-server-DESIGN.md).
	// It exists now so the UI has a place to show real status the
	// moment a real WS client exists, without a later layout change.
	connected bool

	// GitHub verification (synq-DESIGN.md section 9). Optional, and
	// entirely separate from Synq's own identity/auth.
	githubClientID        string // from SYNQ_GITHUB_CLIENT_ID; empty means "not configured"
	githubHandle          string // set once linking succeeds; persisted via db.PrefGitHubHandle
	githubUserCode        string // shown to the user while waiting for browser approval
	githubVerificationURI string
	githubDeviceCode      string
	githubPollInterval    int

	quitting bool
}

// New builds the initial root model for a given, already-unlocked
// identity (see cmd/synq/main.go for the passphrase bootstrap that
// produces it) and the same KeyStore that identity was loaded from,
// which also holds non-secret preferences like the selected theme.
//
// The saved theme (if any) is loaded here, synchronously, rather than
// via a tea.Cmd - this runs once, before the program starts, not
// during the event loop, so there's no risk of it blocking input
// handling.
func New(id *crypto.Identity, store *db.KeyStore) Model {
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

	return Model{
		identity:       id,
		store:          store,
		theme:          theme,
		activeTab:      tabFeed,
		booting:        true,
		githubClientID: os.Getenv("SYNQ_GITHUB_CLIENT_ID"),
		githubHandle:   githubHandle,
	}
}

// Run starts the Bubble Tea program. This is the hand-off point from
// the passphrase bootstrap in cmd/synq/main.go into the interactive
// TUI.
func Run(id *crypto.Identity, store *db.KeyStore) error {
	_, err := tea.NewProgram(New(id, store)).Run()
	return err
}

// Init requests the terminal's background color so the theme can pick
// a sensible starting point (DESIGN.md section 7), and kicks off the
// startup splash animation.
func (m Model) Init() tea.Cmd {
	return tea.Batch(tea.RequestBackgroundColor, bootTick())
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
// (right), spanning the full terminal width. connected is currently
// always false (see the Model.connected doc comment) - the dot and
// label are real UI, just wired to a stub for now.
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
			body = "No open chats. Chat history is session-only - see DESIGN.md section 4."
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

func (m Model) renderProfile() string {
	if m.identity == nil {
		return "You're browsing as a guest.\n\n" +
			"Create an identity to post, chat, build your network, and see\n" +
			"real usernames on the Feed instead of \"node\".\n\n" +
			"Restart Synq and choose \"Create your identity\" from the menu.\n\n" +
			"Theme switching works right now, even as a guest:\n" +
			"  :theme                  open the interactive theme picker (live preview)\n" +
			"  :theme <name>           set a theme directly"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Public key:\n%x\n\n", m.identity.SigningPublic)
	fmt.Fprintf(&b, "Connection: %s\n", connectionLabel(m.connected))
	fmt.Fprintf(&b, "GitHub: %s\n", m.githubStatusLabel())
	fmt.Fprintf(&b, "Theme: %s\n\n", m.theme.Palette.Name)
	b.WriteString("Commands:\n")
	b.WriteString("  :verify <hex-pubkey>   compare a contact's key fingerprint\n")
	b.WriteString("  :github                link your GitHub account\n")
	b.WriteString("  :theme                  open the interactive theme picker (live preview)\n")
	b.WriteString("  :theme <name>           set a theme directly\n")
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
