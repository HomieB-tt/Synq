package app

import (
	"unicode/utf8"

	"charm.land/lipgloss/v2"

	"github.com/HomieB-tt/synq/internal/lineedit"
)

// This file is the drawing half of the text boxes whose editing lives in
// lineedit: where the cursor is painted and which slice of a too-long
// line is shown. The key handling is in model.go (chat box, command bar)
// and feed.go (post/comment composer).

// runeCells is the width of one rune in terminal cells, for lineedit.Window.
func runeCells(r rune) int { return lipgloss.Width(string(r)) }

// cursorCell splits the text after the cursor into the character the
// cursor sits on and what follows it. At the end of the text there is no
// character, and the cursor is drawn as a block of its own.
func cursorCell(after string) (cur, rest string) {
	if after == "" {
		return "", ""
	}
	_, n := utf8.DecodeRuneInString(after)
	return after[:n], after[n:]
}

// cursorSpans draws the cursor and the text after it for the chat
// composer: a solid block at the end of the draft, and the character
// under the cursor in reverse video anywhere else (so inserting in the
// middle doesn't visually push the rest of the line along). The cursor is
// static, not blinking: blinking would mean a timer redrawing the screen
// for the sake of a glyph.
func (m Model) cursorSpans(after string) string {
	t := m.theme
	cur, rest := cursorCell(after)
	if cur == "" {
		return t.AccentText.Render("█")
	}
	return t.AccentText.Reverse(true).Render(cur) + t.Text.Render(rest)
}

// commandBarLine draws the ":" command palette input with its cursor.
// The padding is drawn separately so the cursor's reverse-video cell can
// sit between two spans of the bar's own style without an inner reset
// cutting the bar's colours short.
func (m Model) commandBarLine() string {
	base := m.theme.CommandBar.UnsetPadding()
	pad := base.Render(" ")
	before, after := lineedit.Split(m.commandInput, m.commandBack)
	cur, rest := cursorCell(sanitizeForTerminal(after))
	if cur == "" {
		cur = " "
	}
	return pad + base.Render(":"+sanitizeForTerminal(before)) + base.Reverse(true).Render(cur) + base.Render(rest) + pad
}
