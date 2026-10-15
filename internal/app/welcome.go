package app

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/HomieB-tt/synq/internal/db"
)

// The welcome screen is the getting-started guide: shown once on first
// launch, and again on demand with :welcome. It is deliberately short -
// the point is that nobody has to guess that everything beyond the four
// tabs is a command, or where the list of them lives.

// welcomePages is the guide's text, one entry per page. Each string is a
// paragraph that gets wrapped; a leading "#" marks a heading. Two short
// pages rather than one long one, so each fits a 24-row terminal.
var welcomePages = [][]string{
	{
		"#Welcome to Synq",
		"Private, end-to-end encrypted messaging in your terminal. Nothing is kept once you quit.",
		"",
		"#Find your way around",
		"Tabs: 1 Feed · 2 Nodes · 3 Chat · 4 Profile (or press Tab).",
		"Almost everything else is a command. Press : to open the command bar, type a command and press Enter. Inside a chat, press Ctrl+P instead.",
		"Not sure what a command does? On the Profile tab press c for the full list, with an explanation of each. Press ? for every key.",
	},
	{
		"#Get set up",
		":register <username> claims your username (permanent).",
		":node <username> asks someone to connect. There is no user search, so you need their exact username; they accept on their Nodes tab.",
		":chat <username> opens an encrypted thread once you are connected.",
		"",
		"#Share a file",
		"In a chat, press Ctrl+P and run :send. Received files are under :files. Commands like :send ask for what they need, so you never have to remember the syntax.",
	},
}

func (m Model) renderWelcome() string {
	t := m.theme
	w, _ := m.overlayArea()
	innerW := min(max(w-12, 30), 68)
	pad := func(s string) string {
		if gap := innerW - lipgloss.Width(s); gap > 0 {
			s += strings.Repeat(" ", gap)
		}
		return s
	}

	page := min(max(m.welcomePage, 0), len(welcomePages)-1)
	var lines []string
	for _, l := range welcomePages[page] {
		switch {
		case l == "":
			lines = append(lines, pad(""))
		case strings.HasPrefix(l, "#"):
			lines = append(lines, t.Warning.Render(pad(strings.TrimPrefix(l, "#"))))
		default:
			for _, wl := range wrapToWidth(l, innerW) {
				lines = append(lines, t.Text.Render(pad(wl)))
			}
		}
	}
	footer := fmt.Sprintf("any key for more (%d/%d) · esc skip", page+1, len(welcomePages))
	if page == len(welcomePages)-1 {
		footer = "any key to start · :welcome shows this again"
	}
	lines = append(lines, pad(""), t.Muted.Render(pad(footer)))

	return t.Border.
		BorderForeground(t.Palette.Accent).
		Padding(1, 2).
		Render(strings.Join(lines, "\n"))
}

// closeWelcome dismisses the guide and remembers that it has been seen,
// so it isn't shown on every launch. A failed save only means it shows
// once more; it is not worth an error.
func (m *Model) closeWelcome() {
	m.welcomeOpen = false
	m.welcomePage = 0
	if m.store != nil {
		_ = m.store.SavePreference(db.PrefWelcomed, "1")
	}
}

// updateWelcome pages through the guide: any key moves on, the last page's
// key closes it, and Esc closes it from anywhere. The key is consumed -
// it never also acts on the screen underneath.
func (m Model) updateWelcome(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if msg.String() != "esc" && m.welcomePage < len(welcomePages)-1 {
		m.welcomePage++
		return m, nil
	}
	m.closeWelcome()
	return m, nil
}
