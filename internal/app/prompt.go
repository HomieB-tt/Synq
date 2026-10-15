package app

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/HomieB-tt/synq/internal/lineedit"
)

// A prompt is the popup a bare `:send`, `:savedir`, `:chat`... opens, the
// way a bare `:theme` opens the theme picker: it asks for the one thing
// the command needs, instead of printing a usage line to be retyped. What
// is typed is handed back to runCommand as `<name> <value>`, so the
// command's own validation and messages are unchanged.
//
// Which commands get a prompt, and what they ask, is in commandSpecs.

// promptState is the open prompt. input and back are edited with lineedit
// like every other text box.
type promptState struct {
	name  string // the command it belongs to
	input string
	back  int
}

// openPrompt opens the prompt for command name. It returns "" on success,
// or the reason it can't - the same message the command would give once
// the value was typed, shown now so nobody types a path only to be told
// to open a chat first.
func (m *Model) openPrompt(name string) string {
	if _, ok := specByName(name); !ok {
		return ""
	}
	if msg := m.promptBlocker(name); msg != "" {
		return msg
	}
	p := promptState{name: name}
	switch name {
	case "savedir":
		p.input = m.saveDir // start from what's set, so Enter alone keeps it
	case "name":
		p.input = m.displayName
	}
	m.prompt = p
	m.promptOpen = true
	return ""
}

// promptBlocker is the cheap precondition check for a prompt, "" if none.
func (m *Model) promptBlocker(name string) string {
	switch name {
	case "send":
		if m.activeTab != tabChat || m.chatActive == "" {
			return "Open a chat first (:chat <username>), then run :send."
		}
	case "chat":
		switch {
		case m.identity == nil:
			return "Create an identity first. Restart Synq and choose \"Create your identity.\""
		case m.apiClient == nil:
			return "Chat needs synq-server configured. Set SYNQ_SERVER_URL and restart Synq (see README.md)."
		case m.accessToken == "":
			return "Log in first - :register <username> if you haven't registered, or :login if you have."
		}
	case "node":
		return m.nodePrecheck()
	case "register":
		switch {
		case m.identity == nil:
			return "Create an identity first. Restart Synq and choose \"Create your identity.\""
		case m.apiClient == nil:
			return "Registration needs synq-server configured. Set SYNQ_SERVER_URL and restart Synq (see README.md)."
		case m.username != "":
			return "Already registered as " + m.username + "."
		}
	}
	return ""
}

func (m Model) updatePrompt(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	p := &m.prompt
	switch msg.String() {
	case "esc", "ctrl+c":
		m.promptOpen = false
		m.prompt = promptState{}
		m.commandMsg = "Cancelled."
		return m, nil

	case "enter":
		name, value := p.name, strings.TrimSpace(p.input)
		m.promptOpen = false
		m.prompt = promptState{}
		if value == "" {
			m.commandMsg = "Cancelled."
			return m, nil
		}
		return m.execCommand(name + " " + value)
	}

	if text, back, ok := lineedit.Apply(p.input, p.back, msg.String(), false); ok {
		p.input, p.back = text, back
		return m, nil
	}
	if msg.Text != "" {
		p.input, p.back = lineedit.Insert(p.input, p.back, sanitizeForTerminal(msg.Text))
	}
	return m, nil
}

func (m Model) renderPrompt() string {
	t := m.theme
	spec, _ := specByName(m.prompt.name)

	w, _ := m.overlayArea()
	innerW := min(max(w-12, 30), 64)
	pad := func(s string) string {
		if gap := innerW - lipgloss.Width(s); gap > 0 {
			s += strings.Repeat(" ", gap)
		}
		return s
	}

	// The field: what's typed around the cursor, windowed to fit.
	room := max(innerW-2-1, 1) // "› " and a cell for the cursor
	before, after := lineedit.Split(m.prompt.input, m.prompt.back)
	b, a := lineedit.Window(sanitizeForTerminal(before), sanitizeForTerminal(after), room, runeCells)
	field := t.AccentText.Render("› ") + t.Text.Render(b) + m.cursorSpans(a)
	if m.prompt.input == "" {
		field = t.AccentText.Render("› ") + t.AccentText.Render("█")
	}
	if gap := innerW - lipgloss.Width(field); gap > 0 {
		field += t.Text.Render(strings.Repeat(" ", gap))
	}

	lines := []string{t.Warning.Render(pad(spec.prompt)), "", field, ""}
	for _, l := range wrapToWidth(spec.hint, innerW) {
		lines = append(lines, t.Muted.Render(pad(l)))
	}
	lines = append(lines, "", t.Muted.Render(pad("enter confirm · esc cancel")))

	return t.Border.
		BorderForeground(t.Palette.Accent).
		Padding(1, 2).
		Render(strings.Join(lines, "\n"))
}
