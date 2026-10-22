package app

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// This file is the one place that says what the palette commands are and
// what they do. Three things read it: the `?` help panel (helpCommands),
// the interactive command list (`c` on the Profile tab, or `:commands`),
// and the prompts that a bare `:send`, `:savedir` and friends open. The
// dispatch itself stays in runCommand; commands_test.go fails if the two
// drift apart, so a command can't be added without being documented.

// cmdSpec describes one palette command.
type cmdSpec struct {
	name   string // as typed after the ":"
	usage  string // its arguments, for display: "<path>"; empty if none
	group  string
	brief  string // one line, for the help panel
	detail string // a sentence or two, for the command list

	// prompt, when set, is the label of the prompt that opens when the
	// command is run with no argument (see prompt.go); hint is the help
	// shown under it.
	prompt string
	hint   string

	// fill marks a command that needs typed arguments but isn't worth a
	// prompt - it is destructive (:block) or takes a fixed word (:boot).
	// Choosing it from the command list pre-fills the palette instead.
	fill bool
}

// Group names, in the order the command list shows them.
const (
	grpConnect  = "Connect with people"
	grpChat     = "Chat and files"
	grpFeed     = "Feed"
	grpAccount  = "Your account"
	grpSettings = "Look and settings"
	grpApp      = "Synq"
)

var commandGroups = []string{grpConnect, grpChat, grpFeed, grpAccount, grpSettings, grpApp}

var commandSpecs = []cmdSpec{
	// --- connect ---
	{name: "node", usage: "<username>", group: grpConnect,
		brief:  "send a node request (exact username)",
		detail: "Ask someone to connect. There is no user search, so you need their exact username. They see the request on their Nodes tab and can accept or decline.",
		prompt: "Send a node request to", hint: "Their exact username. You can chat once they accept."},
	{name: "nodes", group: grpConnect,
		brief:  "refresh the Nodes tab",
		detail: "Open the Nodes tab and reload your requests and connections."},
	{name: "accept", usage: "<username>", group: grpConnect,
		brief:  "accept a node request or a changed key",
		detail: "Accept a node request from someone. Also used when a contact's key has changed: Synq refuses to send to them until you accept the new key.",
		prompt: "Accept", hint: "The username of the person whose request or new key you are accepting."},
	{name: "block", usage: "<username> confirm", group: grpConnect, fill: true,
		brief:  "block a user (silent, no undo)",
		detail: "Block someone. They are not told, and there is no undo. Type the username and the word confirm."},
	{name: "verify", usage: "<user|hex>", group: grpConnect,
		brief:  "compare key fingerprints",
		detail: "Show the fingerprint of the keys you and a contact share. Compare it with them over another channel, such as a call, to be sure nobody is impersonating either of you.",
		prompt: "Verify", hint: "A username you have chatted with, or a hex public key they gave you."},

	// --- chat and files ---
	{name: "chat", usage: "<username>", group: grpChat,
		brief:  "open an encrypted thread",
		detail: "Open an end-to-end encrypted thread with someone. You need to be connected nodes first (see :node). Inside a thread, type and press Enter to send; Esc goes back.",
		prompt: "Chat with", hint: "A username you are connected to."},
	{name: "send", usage: "<path>", group: grpChat,
		brief:  "send a file in the open chat",
		detail: "Send a file (up to 100 MB) to the person in the open chat. It is encrypted on your machine, and the server never sees its contents, name or key. It stays available only while you remain connected. Open a chat first; from inside a thread press Ctrl+P to reach this.",
		prompt: "File to send", hint: "The full path of the file, for example /home/you/report.pdf. A leading ~ is fine."},
	{name: "files", group: grpChat,
		brief:  "view, save or ignore received files",
		detail: "List files people have sent you. Choose one to view it in your default app, save it to your save folder, or ignore it. Viewing is refused for programs and files with no extension."},
	{name: "savedir", usage: "[path]", group: grpChat,
		brief:  "folder that saved files go to",
		detail: "Choose the folder that saved files are written to. Nothing is saved until you choose to, and saving never overwrites an existing file.",
		prompt: "Folder for saved files", hint: "A folder that already exists. It is kept between launches."},

	{name: roomCommandName, usage: "<new|invite|accept|decline|leave|kick|transfer|end|members|list> ...", group: grpChat, fill: true,
		brief:  "group chat: make or manage a blink",
		detail: "A blink is a small, invite-only group chat of up to 16 people that vanishes when it ends. :blink new <name> makes one and you own it; :blink invite <username> invites someone you are connected to; they join with :blink accept <name>. Everyone's messages are end-to-end encrypted, Ctrl+R replies to a message, and :send shares a file (members view or save it from :files; anything unsaved is wiped when the blink ends). The owner can remove someone with :blink kick <username> (the key is changed so they can read nothing new) and hand the blink to a member with :blink transfer <username>, who takes it with :blink accept (or refuses with :blink decline). A blink lasts only while its owner is here: :blink end confirm, quitting Synq, or the owner going offline ends it for everyone and wipes it - so hand it over first if you need to leave."},

	// --- feed ---
	{name: "post", usage: "[category]", group: grpFeed,
		brief:  "write a Feed post (project|hiring|general)",
		detail: "Write a Feed post. The category is project, hiring or general. Same as pressing p on the Feed tab. Ctrl+S sends it, Esc discards it."},
	{name: "report", usage: "[reason]", group: grpFeed,
		brief:  "report the open Feed post",
		detail: "Report the Feed post you have open, with an optional reason."},

	// --- account ---
	{name: "register", usage: "<username>", group: grpAccount,
		brief:  "register a username (permanent)",
		detail: "Claim your username on synq-server. It is permanent - there is no rename yet - and it is how people find and connect with you. Needs SYNQ_SERVER_URL to be set.",
		prompt: "Choose a username", hint: "This is permanent and cannot be renamed later."},
	{name: "login", group: grpAccount,
		brief:  "log in again after :logout",
		detail: "Reconnect to synq-server after :logout, without restarting Synq."},
	{name: "logout", group: grpAccount,
		brief:  "revoke all sessions and disconnect",
		detail: "Sign out everywhere and disconnect. Run :login to come back."},
	{name: "name", usage: "<text>", group: grpAccount,
		brief:  "set a local display name",
		detail: "Set a display name that only you see on this machine. It is not your username and the server never knows it. :name clear removes it.",
		prompt: "Display name", hint: "Only shown on this machine. Leave empty to cancel; use :name clear to remove it."},
	{name: "github", group: grpAccount,
		brief:  "link GitHub (device flow)",
		detail: "Link your GitHub account. You are given a code to enter on GitHub's website, in your browser."},

	// --- look and settings ---
	{name: "theme", usage: "[name]", group: grpSettings,
		brief:  "pick or set a theme",
		detail: "Pick a colour theme with a live preview. Works without an account too."},
	{name: "server", usage: "[address|reset]", group: grpSettings,
		brief:  "show or change the synq-server",
		detail: "Show which synq-server Synq talks to, or save a different address (https://host). Takes effect after you restart Synq. 'reset' goes back to the built-in default."},
	{name: "ping", group: grpSettings,
		brief:  "measure the round trip to the server",
		detail: "Time a few requests to synq-server. The first includes connection setup; the rest show the steady latency. Useful for telling a slow server from a slow link."},
	{name: "boot", usage: "<on|off>", group: grpSettings, fill: true,
		brief:  "startup checklist on next launch",
		detail: "Turn the startup checklist on or off. Takes effect from the next launch."},

	// --- Synq ---
	{name: "commands", group: grpApp,
		brief:  "browse all commands",
		detail: "Open this list. On the Profile tab, press c."},
	{name: "welcome", group: grpApp,
		brief:  "show the getting-started guide",
		detail: "Show the getting-started guide again."},
	{name: "help", group: grpApp,
		brief:  "keys and commands on one screen",
		detail: "Show every key and command on one screen. Press ? for the same thing."},
	{name: "quit", group: grpApp,
		brief:  "leave Synq",
		detail: "Leave Synq. Chat history is session-only and is wiped. q does the same."},
}

func specByName(name string) (cmdSpec, bool) {
	for _, s := range commandSpecs {
		if s.name == name {
			return s, true
		}
	}
	return cmdSpec{}, false
}

// label is how a command is written in lists: ":send <path>".
func (s cmdSpec) label() string {
	if s.usage == "" {
		return ":" + s.name
	}
	return ":" + s.name + " " + s.usage
}

// helpCommands is the help panel's command section, derived from
// commandSpecs so the two can't disagree.
var helpCommands = func() []helpEntry {
	rows := make([]helpEntry, 0, len(commandSpecs))
	for _, s := range commandSpecs {
		rows = append(rows, helpEntry{left: s.label(), right: s.brief})
	}
	return rows
}()

// --- the command list (Profile tab, `c`, or :commands) ---

// cmdListRows is the list as displayed: a header row before each group
// and one row per command. sel is the display row of command number i.
type cmdListRow struct {
	header string   // group heading, when set
	spec   *cmdSpec // otherwise the command
	index  int      // position in commandSpecs, for rows with a spec
}

func buildCmdListRows() []cmdListRow {
	var rows []cmdListRow
	for _, g := range commandGroups {
		rows = append(rows, cmdListRow{header: g})
		for i := range commandSpecs {
			if commandSpecs[i].group == g {
				rows = append(rows, cmdListRow{spec: &commandSpecs[i], index: i})
			}
		}
	}
	return rows
}

// openCommandList opens the command list on the first command.
func (m *Model) openCommandList() {
	m.cmdListOpen = true
	m.cmdListIndex = 0
}

// moveCommandList moves the selection by delta commands, clamped to the
// list. Group headers are skipped because they aren't commands.
func (m *Model) moveCommandList(delta int) {
	m.cmdListIndex = min(max(m.cmdListIndex+delta, 0), len(commandSpecs)-1)
}

func (m Model) updateCommandList(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c", "q", "c":
		m.cmdListOpen = false
		return m, nil
	case "up", "k":
		m.moveCommandList(-1)
	case "down", "j":
		m.moveCommandList(1)
	case "pgup":
		m.moveCommandList(-8)
	case "pgdown":
		m.moveCommandList(8)
	case "home":
		m.cmdListIndex = 0
	case "end":
		m.cmdListIndex = len(commandSpecs) - 1
	case "enter":
		spec := commandSpecs[min(max(m.cmdListIndex, 0), len(commandSpecs)-1)]
		m.cmdListOpen = false
		if spec.fill {
			// Needs typed arguments: leave the palette open with the
			// command already in it.
			m.commandMode = true
			m.commandInput, m.commandBack = spec.name+" ", 0
			m.commandMsg = ""
			return m, nil
		}
		// A bare command does the right thing by itself: the ones with
		// arguments open a prompt for them (see runCommand).
		return m.execCommand(spec.name)
	}
	return m, nil
}

// renderCommandList draws the list as a popup like the theme picker: the
// commands grouped, one selected, and the selected command explained in
// full underneath.
func (m Model) renderCommandList() string {
	t := m.theme
	rows := buildCmdListRows()

	_, oh := m.overlayArea()
	// Everything but the list: border and padding (4), title and blank
	// (2), blank before the description (1), the description (4), blank
	// and footer (2) - 13 rows.
	visible := max(oh-13, 3)

	selRow := 0
	for i, r := range rows {
		if r.spec != nil && r.index == m.cmdListIndex {
			selRow = i
		}
	}
	start := 0
	if len(rows) > visible {
		start = min(max(selRow-visible/2, 0), len(rows)-visible)
	}
	end := min(start+visible, len(rows))

	const labelW = 26
	innerW := 76
	if w, _ := m.overlayArea(); w-10 < innerW {
		innerW = max(w-10, 30)
	}

	pad := func(s string) string {
		if gap := innerW - lipgloss.Width(s); gap > 0 {
			s += strings.Repeat(" ", gap)
		}
		return s
	}

	var lines []string
	lines = append(lines, t.Warning.Render(pad("Commands")), "")
	for _, r := range rows[start:end] {
		switch {
		case r.spec == nil:
			lines = append(lines, t.StatusBar.Render(pad(r.header)))
		case r.index == m.cmdListIndex:
			text := fmt.Sprintf("› %-*s %s", labelW, r.spec.label(), r.spec.brief)
			lines = append(lines, t.TabActive.Render(pad(truncateToWidth(text, innerW))))
		default:
			text := fmt.Sprintf("  %-*s %s", labelW, r.spec.label(), r.spec.brief)
			lines = append(lines, t.Muted.Render(pad(truncateToWidth(text, innerW))))
		}
	}

	sel := commandSpecs[min(max(m.cmdListIndex, 0), len(commandSpecs)-1)]
	lines = append(lines, pad(""))
	detail := wrapToWidth(sel.detail, innerW)
	if len(detail) > 4 {
		detail = detail[:4]
	}
	// Always four lines, so the popup doesn't change height (and jump
	// about the screen) as the selection moves between short and long
	// descriptions.
	for len(detail) < 4 {
		detail = append(detail, "")
	}
	for _, l := range detail {
		lines = append(lines, t.Text.Render(pad(l)))
	}
	enter := "enter run"
	switch {
	case sel.prompt != "":
		enter = "enter fills in a prompt"
	case sel.fill:
		enter = "enter types it for you"
	}
	lines = append(lines, pad(""), t.Muted.Render(pad("↑/↓ select · "+enter+" · esc close")))

	return t.Border.
		BorderForeground(t.Palette.Accent).
		Padding(1, 2).
		Render(strings.Join(lines, "\n"))
}

// overlayArea is the size popups are centred in: the content pane.
func (m Model) overlayArea() (w, h int) {
	w, h = m.width, m.height-6 // tab bar + spacing + bottom bar, as renderContent
	if w <= 0 {
		w = 80
	}
	if m.height <= 0 {
		h = 18
	}
	return w, max(h, 3)
}
