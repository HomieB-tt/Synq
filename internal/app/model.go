// Package app contains the Bubble Tea root model, update, and view
// loops for the Synq client: the four global tabs (Feed, Nodes, Chat,
// Profile), pane focus, and the command palette.
package app

import (
	"fmt"
	"strings"

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

// Model is the Bubble Tea root model for the whole application.
type Model struct {
	identity *crypto.Identity
	store    *db.KeyStore
	theme    styles.Theme

	activeTab tab
	width     int
	height    int

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
// a sensible starting point. Lip Gloss v2 removed automatic background
// detection (DESIGN.md section 7), so this is deliberate and explicit,
// not automatic - see handleBackgroundColor.
func (m Model) Init() tea.Cmd {
	return tea.RequestBackgroundColor
}

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tea.BackgroundColorMsg:
		return m.handleBackgroundColor(msg), nil

	case tea.KeyPressMsg:
		if m.commandMode {
			return m.updateCommandMode(msg)
		}
		return m.updateNormalMode(msg)
	}

	return m, nil
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
			return "Usage: :theme <dracula|nord|monokai>", false
		}
		name := strings.ToLower(fields[1])
		palette, ok := styles.All[name]
		if !ok {
			return fmt.Sprintf("Unknown theme %q. Try dracula, nord, or monokai.", fields[1]), false
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

	var b strings.Builder
	b.WriteString(m.renderTabBar())
	b.WriteString("\n\n")
	b.WriteString(m.renderContent())
	b.WriteString("\n")
	b.WriteString(m.renderBottomBar())

	v := tea.NewView(b.String())
	v.AltScreen = true
	return v
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
