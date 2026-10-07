package app

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
)

// helpEntry is one row of the help panel: the keys/commands on the
// left, what they do on the right.
type helpEntry struct {
	left, right string
}

// helpKeys is the always-relevant key map. Commands live in
// helpCommands rather than being folded in here because they're a
// different affordance (palette entries, not single keys) and the
// panel reads better grouped by how you reach them.
var helpKeys = []helpEntry{
	{"1-4", "switch tabs"},
	{"Tab / Shift+Tab", "cycle tabs"},
	{": or Ctrl+P", "command palette"},
	{"?", "this help"},
	{"q / Ctrl+C", "quit"},
}

// helpCommands lists every command runCommand understands. Keeping it
// next to the dispatch switch (both in this package) is deliberate:
// an unlisted command is invisible, and a listed-but-removed one shows
// up in review as a dead row.
var helpCommands = []helpEntry{
	{":chat <user>", "open an encrypted thread"},
	{":accept <user>", "trust a contact's changed key"},
	{":verify <user|hex>", "compare key fingerprints"},
	{":theme [name]", "pick or set a theme"},
	{":name <text>", "set a local display name"},
	{":register <user>", "register a username (permanent)"},
	{":login / :logout", "end or start a session"},
	{":github", "link GitHub (device flow)"},
	{":boot on|off", "startup checklist on next launch"},
	{":help", "this panel"},
	{":quit", "leave Synq"},
}

// renderHelp draws the ? / `:help` overlay: the key map, then every
// palette command with a one-line description, centered in the
// content pane the same way renderBoot centers the splash (see
// Model.renderContent for where it's placed).
func (m Model) renderHelp() string {
	title := lipgloss.NewStyle().
		Bold(true).
		Foreground(m.theme.Palette.Accent).
		Background(m.theme.Palette.Background).
		Render("SYNQ")
	hint := m.theme.Muted.Render("help · any key to close")

	var b strings.Builder
	fmt.Fprintf(&b, "%s  %s\n\n", title, hint)

	b.WriteString(m.theme.StatusBar.Render("Keys") + "\n")
	for _, entry := range helpKeys {
		b.WriteString(m.renderHelpEntry(entry) + "\n")
	}

	b.WriteString("\n" + m.theme.StatusBar.Render("Commands") + "\n")
	for _, entry := range helpCommands {
		b.WriteString(m.renderHelpEntry(entry) + "\n")
	}

	b.WriteString("\n" + m.theme.Muted.Render(
		"Chat: type and enter to send · esc back to the thread list"))
	return b.String()
}

// renderHelpEntry renders one help row - the left column padded so
// the descriptions line up, the description muted to read as
// secondary.
func (m Model) renderHelpEntry(entry helpEntry) string {
	// Background on the unpadded half too, for the same reason
	// renderBootStep styles its plain runs: styled spans next to
	// unstyled ones leave gaps in the terminal's own background.
	left := lipgloss.NewStyle().
		Foreground(m.theme.Palette.Foreground).
		Background(m.theme.Palette.Background).
		Render("  " + fmt.Sprintf("%-24s", entry.left))
	return left + m.theme.Muted.Render(entry.right)
}
