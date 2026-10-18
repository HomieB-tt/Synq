package app

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

// paneModel is a Model on the Chat tab (empty thread list) at a given
// terminal size.
func paneModel(t *testing.T, w, h int) Model {
	t.Helper()
	m := chatViewModel(t, w, h)
	m.identity = testIdentityForRender(t)
	m.activeTab = tabChat
	return m
}

func TestPaneIsCappedOnAWideTerminal(t *testing.T) {
	m := paneModel(t, 240, 40)
	if w, _ := m.contentSize(); w != maxPaneWidth {
		t.Errorf("pane width = %d on a 240-column terminal, want %d", w, maxPaneWidth)
	}
	if got, want := m.paneMargin(), (240-maxPaneWidth)/2; got != want {
		t.Errorf("margin = %d, want %d", got, want)
	}
}

func TestPaneUsesTheWholeTerminalWhenNarrow(t *testing.T) {
	for _, w := range []int{60, 80, maxPaneWidth} {
		m := paneModel(t, w, 30)
		if got, _ := m.contentSize(); got != w {
			t.Errorf("width %d: pane width = %d", w, got)
		}
		if m.paneMargin() != 0 {
			t.Errorf("width %d: margin = %d, want 0", w, m.paneMargin())
		}
	}
}

// The Chat tab used to size itself for a border it never drew, so it sat
// flush against column 0 while the other tabs were boxed.
func TestChatTabIsBoxedAndCentred(t *testing.T) {
	m := paneModel(t, 240, 40)
	lines := strings.Split(plain(m.renderContent()), "\n")

	_, h := m.contentSize()
	if len(lines) != h {
		t.Errorf("pane is %d rows, want %d (the room contentSize left)", len(lines), h)
	}
	margin := strings.Repeat(" ", m.paneMargin())
	for i, l := range lines {
		if !strings.HasPrefix(l, margin) {
			t.Fatalf("row %d is not indented by the margin: %q", i, l)
		}
		if got := lipgloss.Width(l); got != m.paneMargin()+maxPaneWidth {
			t.Fatalf("row %d is %d wide, want %d", i, got, m.paneMargin()+maxPaneWidth)
		}
	}
	if !strings.HasPrefix(strings.TrimPrefix(lines[0], margin), "╭") {
		t.Errorf("first row has no top border: %q", lines[0])
	}
	if !strings.HasPrefix(strings.TrimPrefix(lines[len(lines)-1], margin), "╰") {
		t.Errorf("last row has no bottom border: %q", lines[len(lines)-1])
	}
}

func TestBottomBarLinesUpWithThePane(t *testing.T) {
	m := paneModel(t, 240, 40)
	bar := plain(m.indentPane(m.renderBottomBar()))
	if !strings.HasPrefix(bar, strings.Repeat(" ", m.paneMargin())+"1-4 switch tabs") {
		t.Errorf("hint line is not under the pane: %q", bar)
	}

	m.commandMsg = strings.Repeat("a long command result that wraps over several rows ", 4)
	rows := len(m.bottomLines())
	if got := strings.Count(m.renderBottomBar(), "\n") + 1; got != rows {
		t.Errorf("bottom bar draws %d rows but the pane was sized for %d", got, rows)
	}
	for _, l := range m.bottomLines() {
		if lipgloss.Width(l) > maxPaneWidth {
			t.Errorf("bottom line is wider than the pane: %d", lipgloss.Width(l))
		}
	}
}
