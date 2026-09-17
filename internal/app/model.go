// Package app contains the Bubble Tea root model, update, and view
// loops for the Synq client: the four global tabs (Feed, Nodes, Chat,
// Profile), pane focus, and the command palette.
package app

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/HomieB-tt/synq/internal/crypto"
	"github.com/HomieB-tt/synq/internal/db"
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

	return Model{
		identity:  id,
		store:     store,
		theme:     theme,
		activeTab: tabFeed,
		booting:   true,
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
		result, quit := m.runCommand(strings.TrimSpace(m.commandInput))
		m.commandMsg = result
		m.commandMode = false
		m.commandInput = ""
		if quit {
			m.quitting = true
			return m, tea.Quit
		}
		return m, nil

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

// runCommand handles a submitted command palette entry. Only `:theme`
// and `:quit` are implemented so far; everything else is a placeholder
// for future wiring (post composer, :verify, node requests, etc. - see
// synq-DESIGN.md and synq-server-DESIGN.md for what each will
// eventually need to do). The bool return tells the caller whether to
// actually issue tea.Quit - setting a "quitting" flag alone does not
// stop the Bubble Tea event loop.
func (m *Model) runCommand(cmd string) (result string, quit bool) {
	if cmd == "" {
		return "", false
	}

	fields := strings.Fields(cmd)
	switch fields[0] {
	case "theme":
		if len(fields) != 2 {
			return fmt.Sprintf("Usage: :theme <%s>", strings.Join(styles.Names(), "|")), false
		}
		name := strings.ToLower(fields[1])
		palette, ok := styles.All[name]
		if !ok {
			return fmt.Sprintf("Unknown theme %q. Available: %s.", fields[1], strings.Join(styles.Names(), ", ")), false
		}
		m.theme = styles.New(palette)
		if err := m.store.SavePreference(db.PrefTheme, name); err != nil {
			// The theme still applies for this session even if saving
			// it failed - just tell the user it won't survive a
			// restart, rather than silently losing their choice or
			// refusing to apply it.
			return fmt.Sprintf("Theme set to %s, but couldn't save it: %v", palette.Name, err), false
		}
		return fmt.Sprintf("Theme set to %s.", palette.Name), false

	case "quit", "q":
		return "", true

	default:
		return fmt.Sprintf("Unknown command: %s", fields[0]), false
	}
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
		b.WriteString(m.renderTabBar())
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

// renderBoot draws the startup splash: the Synq header, a subtitle,
// and the current boot message with its spinner, all centered in the
// terminal. Skippable by pressing any key (see the tea.KeyPressMsg
// case in Update).
func (m Model) renderBoot() string {
	header := lipgloss.NewStyle().
		Bold(true).
		Foreground(m.theme.Palette.Accent).
		Render("SYNQ")

	subtitle := m.theme.Muted.Render("terminal-native developer network")

	msg := bootMessages[m.bootMsgIndex]
	status := m.theme.StatusBar.Render(spinnerFrames[m.spinnerFrame] + " " + msg)

	block := lipgloss.JoinVertical(lipgloss.Center,
		header,
		"",
		subtitle,
		"",
		status,
	)

	width := m.width
	if width <= 0 {
		width = 80
	}
	height := m.height
	if height <= 0 {
		height = 24
	}

	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, block)
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

func (m Model) renderContent() string {
	width := m.width
	if width <= 0 {
		width = 80
	}
	height := m.height - 6 // tab bar + spacing + bottom bar
	if height < 3 {
		height = 3
	}

	var body string
	switch m.activeTab {
	case tabFeed:
		body = "Feed is empty for now.\n\nPost with the CLI: cat file | synq post"
	case tabNodes:
		body = "No nodes yet. Node requests will show up here."
	case tabChat:
		body = "No open chats. Chat history is session-only - see DESIGN.md section 4."
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
		return "No identity loaded."
	}
	return fmt.Sprintf(
		"Public key:\n%x\n\nGitHub: not linked yet\nUse :verify <username> to check a contact's key fingerprint.",
		m.identity.SigningPublic,
	)
}

func (m Model) renderBottomBar() string {
	if m.commandMode {
		return m.theme.CommandBar.Render(":" + m.commandInput)
	}
	if m.commandMsg != "" {
		return m.theme.Warning.Render(m.commandMsg)
	}
	return m.theme.StatusBar.Render(
		"1-4 switch tabs · Tab/Shift+Tab cycle · : command palette · q quit",
	)
}
