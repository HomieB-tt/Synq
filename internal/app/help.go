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
	{"← → Home End", "move the cursor in any text box"},
	{"Alt+← → / Ctrl+W", "jump by word / delete a word"},
	{"Ctrl+U / Ctrl+K", "delete to start / to end of line"},
	{"PgUp PgDn (chat)", "scroll; Ctrl+Home/End: oldest/newest"},
	{"Feed: ↑/↓ enter p", "select, open, write a post"},
	{"[ ] (Feed)", "previous / next category"},
	{"c (Profile)", "browse all commands, with explanations"},
	{"r (Feed/Nodes)", "refresh"},
	{"q / Ctrl+C", "quit"},
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

	b.WriteString(m.theme.StatusBar.Render("Keys"))
	b.WriteByte('\n')
	for _, entry := range helpKeys {
		b.WriteString(m.renderHelpEntry(entry))
		b.WriteByte('\n')
	}

	b.WriteByte('\n')
	b.WriteString(m.theme.StatusBar.Render("Commands"))
	b.WriteByte('\n')
	for _, entry := range helpCommands {
		b.WriteString(m.renderHelpEntry(entry))
		b.WriteByte('\n')
	}

	b.WriteByte('\n')
	b.WriteString(m.theme.Muted.Render(
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
