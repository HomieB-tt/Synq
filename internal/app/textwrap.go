package app

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// The helpers in this file are the layout primitives the Chat view is
// built from. They are deliberately free of Model and theme state - plain
// strings in, plain strings out, measured in terminal cells (so wide
// characters count as two, combining marks as zero) - which keeps them
// trivially testable and means no styling decision can leak into the
// arithmetic. Callers style the results; none of these functions emit
// ANSI sequences, and none expect to be given any.

// wrapToWidth word-wraps s into lines no wider than width cells. Existing
// newlines are kept as paragraph breaks (an empty paragraph yields an
// empty line, so blank lines survive), runs of spaces collapse to one,
// and a single word wider than width is hard-broken rather than left to
// overflow the pane. width below 1 is treated as 1 so a pathologically
// narrow terminal degrades to one character per line instead of
// panicking or looping.
func wrapToWidth(s string, width int) []string {
	if width < 1 {
		width = 1
	}
	var out []string
	for _, para := range strings.Split(s, "\n") {
		out = append(out, wrapParagraph(para, width)...)
	}
	return out
}

func wrapParagraph(p string, width int) []string {
	words := strings.Fields(p)
	if len(words) == 0 {
		return []string{""}
	}

	var (
		lines []string
		cur   string
		curW  int
	)
	flush := func() {
		lines = append(lines, cur)
		cur, curW = "", 0
	}

	for _, w := range words {
		// A word that can never fit on any line is chopped to width
		// first; each full-width chunk becomes a line of its own.
		for lipgloss.Width(w) > width {
			if curW > 0 {
				flush()
			}
			head, tail := splitAtWidth(w, width)
			lines = append(lines, head)
			w = tail
		}
		ww := lipgloss.Width(w)
		switch {
		case curW == 0:
			cur, curW = w, ww
		case curW+1+ww <= width:
			cur += " " + w
			curW += 1 + ww
		default:
			flush()
			cur, curW = w, ww
		}
	}
	if curW > 0 || len(lines) == 0 {
		flush()
	}
	return lines
}

// splitAtWidth cuts s after the longest prefix that fits in width cells.
// It always consumes at least one rune, even when that rune alone is
// wider than width (a double-width character in a one-column pane) -
// otherwise a caller looping "split until it fits" would never advance.
func splitAtWidth(s string, width int) (head, tail string) {
	w := 0
	for i, r := range s {
		rw := lipgloss.Width(string(r))
		if w+rw > width && i > 0 {
			return s[:i], s[i:]
		}
		w += rw
	}
	return s, ""
}

// tailByWidth returns the longest suffix of s that fits in width cells.
// The composer uses it so the end of what's being typed - where the
// cursor is - always stays visible however long the draft gets.
func tailByWidth(s string, width int) string {
	if width <= 0 {
		return ""
	}
	rs := []rune(s)
	w := 0
	i := len(rs)
	for i > 0 {
		rw := lipgloss.Width(string(rs[i-1]))
		if w+rw > width {
			break
		}
		w += rw
		i--
	}
	return string(rs[i:])
}

// truncateToWidth shortens s to at most width cells, ending in "…" when
// anything was cut. Unlike truncateRunes (which counts runes and is right
// for a fixed preview length) this measures cells, so a row of wide
// characters can't push a list row past the pane edge.
func truncateToWidth(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= width {
		return s
	}
	var b strings.Builder
	w := 0
	for _, r := range s {
		rw := lipgloss.Width(string(r))
		if w+rw > width-1 {
			break
		}
		b.WriteRune(r)
		w += rw
	}
	return b.String() + "…"
}

// clipLines keeps at most n lines of s. It is the safety net under every
// tab's body: lipgloss pads a block up to a Height but never cuts one
// down, so a body taller than its pane would otherwise grow the pane and
// push the status bar off the bottom of the terminal.
func clipLines(s string, n int) string {
	if n < 1 {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[:n], "\n")
}
