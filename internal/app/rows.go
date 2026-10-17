package app

import (
	"fmt"
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/HomieB-tt/synq/internal/api"
)

// This file holds the small drawing helpers that lists share, so a row
// looks the same on every tab.

// rowStyles are the styles for one row of a list: the normal ones, or -
// for the selected row - the same on the raised Surface background, so a
// highlighted row is one unbroken bar. bg is the row's background, for
// building any further span that has to match.
func (m Model) rowStyles(selected bool) (base, muted, accent lipgloss.Style, bg color.Color) {
	t := m.theme
	if !selected {
		return t.Text, t.Muted, t.AccentText, t.Palette.Background
	}
	return t.Selected,
		lipgloss.NewStyle().Foreground(t.Palette.Muted).Background(t.Surface),
		t.Selected.Foreground(t.Palette.Accent),
		t.Surface
}

// badgeSpans draws the " ✓ @handle" that follows a GitHub-verified
// user's name (nothing for an unverified one). The Nodes list and the
// Profile use it, so a verified user looks the same everywhere.
func (m Model) badgeSpans(verified bool, handle string, base, muted lipgloss.Style, bg color.Color) string {
	if !verified {
		return ""
	}
	var b strings.Builder
	b.WriteString(base.Render(" "))
	b.WriteString(lipgloss.NewStyle().Foreground(m.theme.Success).Background(bg).Render("✓"))
	if handle != "" {
		b.WriteString(muted.Render(" @" + sanitizeForTerminal(handle)))
	}
	return b.String()
}

// badgeWidth is the width badgeSpans adds, so a row can be padded to
// the pane exactly.
func badgeWidth(verified bool, handle string) int {
	if !verified {
		return 0
	}
	n := 2 // " ✓"
	if handle != "" {
		n += 2 + lipgloss.Width(sanitizeForTerminal(handle)) // " @handle"
	}
	return n
}

// categoryColor is the chip colour for a Feed category. An unknown
// category gets the warning colour rather than passing as a known one.
func (m Model) categoryColor(category string) color.Color {
	t := m.theme
	switch category {
	case api.CategoryProject:
		return t.Palette.Accent
	case api.CategoryHiring:
		return t.Success
	case api.CategoryGeneral:
		return t.Palette.Muted
	}
	return t.Palette.Warning
}

// categoryChip is the category drawn as a coloured tag. The label goes
// through categoryTag, so text this client doesn't know is never echoed.
func (m Model) categoryChip(category string) string {
	return m.theme.Chip(categoryTag(category), m.categoryColor(category))
}

// fingerprintGroups formats a key as rows of four-digit hex groups,
// perRow groups to a row. Grouping is what makes a key comparable by eye
// (and by voice): "a3f9 c21b 77d0" can be read out and checked a block at
// a time, where 64 unbroken digits can't.
func fingerprintGroups(key []byte, perRow int) []string {
	h := fmt.Sprintf("%x", key)
	var groups []string
	for i := 0; i < len(h); i += 4 {
		groups = append(groups, h[i:min(i+4, len(h))])
	}
	perRow = max(perRow, 1)
	var rows []string
	for i := 0; i < len(groups); i += perRow {
		rows = append(rows, strings.Join(groups[i:min(i+perRow, len(groups))], " "))
	}
	return rows
}
