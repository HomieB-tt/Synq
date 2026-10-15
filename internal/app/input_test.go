package app

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// Cursor editing in the three text boxes: the chat composer, the command
// palette and the Feed composer. The editing rules themselves are tested
// in package lineedit; these tests cover the wiring - that each box
// routes keys to it, keeps its cursor, and draws it.

func key(code rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: code} }

func typeText(m Model, s string) Model {
	for _, r := range s {
		next, _ := m.updateChatCompose(keyPressWith(string(r)))
		m = next.(Model)
	}
	return m
}

func pressChat(m Model, keys ...tea.KeyPressMsg) Model {
	for _, k := range keys {
		next, _ := m.updateChatCompose(k)
		m = next.(Model)
	}
	return m
}

// The reported bug: with the arrows dead, one typo meant retyping the
// whole message.
func TestChatBoxFixesATypoInTheMiddle(t *testing.T) {
	m := fill(chatViewModel(t, 100, 30), 0, nil)
	m = typeText(m, "helo world")
	m = pressChat(m, key(tea.KeyHome), key(tea.KeyRight), key(tea.KeyRight), key(tea.KeyRight))
	m = typeText(m, "l")
	if m.chatInput != "hello world" {
		t.Fatalf("draft = %q, want %q", m.chatInput, "hello world")
	}
	// The cursor stays where the fix was made: type on and it continues there.
	m = typeText(m, "X")
	if m.chatInput != "hellXo world" {
		t.Fatalf("draft = %q", m.chatInput)
	}
}

func TestChatBoxBackspaceAndDeleteAtCursor(t *testing.T) {
	m := fill(chatViewModel(t, 100, 30), 0, nil)
	m = typeText(m, "abcd")
	m = pressChat(m, key(tea.KeyLeft), key(tea.KeyLeft), key(tea.KeyBackspace))
	if m.chatInput != "acd" {
		t.Fatalf("after backspace: %q", m.chatInput)
	}
	m = pressChat(m, key(tea.KeyDelete))
	if m.chatInput != "ad" {
		t.Fatalf("after delete: %q", m.chatInput)
	}
}

func TestChatBoxNeverSplitsMultiByteCharacters(t *testing.T) {
	m := fill(chatViewModel(t, 100, 30), 0, nil)
	m = typeText(m, "aé😀b")
	m = pressChat(m, key(tea.KeyLeft), key(tea.KeyLeft), key(tea.KeyBackspace))
	if m.chatInput != "a😀b" {
		t.Fatalf("draft = %q", m.chatInput)
	}
}

// The arrow keys carry no text, so they must move the cursor and not be
// typed into the message.
func TestArrowKeysAreNotTyped(t *testing.T) {
	m := fill(chatViewModel(t, 100, 30), 0, nil)
	m = typeText(m, "ab")
	m = pressChat(m, key(tea.KeyLeft), key(tea.KeyRight), key(tea.KeyUp), key(tea.KeyDown))
	if m.chatInput != "ab" {
		t.Fatalf("draft = %q", m.chatInput)
	}
}

// Home and End moved to the cursor; the jump to the oldest/newest message
// went with them to Ctrl+Home / Ctrl+End.
func TestHomeEndMoveCursorAndCtrlHomeEndScroll(t *testing.T) {
	m := fill(chatViewModel(t, 100, 30), 20, func(int) string { return "x" })
	m = typeText(m, "draft")
	m = pressChat(m, key(tea.KeyHome))
	if m.chatOffset() != 0 {
		t.Fatalf("Home scrolled the thread (offset %d)", m.chatOffset())
	}
	m = typeText(m, ">")
	if m.chatInput != ">draft" {
		t.Fatalf("Home didn't move the cursor to the start: %q", m.chatInput)
	}

	m = pressChat(m, tea.KeyPressMsg{Code: tea.KeyHome, Mod: tea.ModCtrl})
	if m.chatOffset() == 0 {
		t.Fatal("Ctrl+Home should scroll to the oldest message")
	}
	m = pressChat(m, tea.KeyPressMsg{Code: tea.KeyEnd, Mod: tea.ModCtrl})
	if m.chatOffset() != 0 {
		t.Fatalf("Ctrl+End should return to the newest message, offset %d", m.chatOffset())
	}
	if m.chatInput != ">draft" {
		t.Fatalf("scrolling changed the draft: %q", m.chatInput)
	}
}

func TestWordAndLineKillsInChatBox(t *testing.T) {
	m := fill(chatViewModel(t, 100, 30), 0, nil)
	m = typeText(m, "one two three")
	m = pressChat(m, tea.KeyPressMsg{Code: 'w', Mod: tea.ModCtrl})
	if m.chatInput != "one two " {
		t.Fatalf("ctrl+w: %q", m.chatInput)
	}
	m = pressChat(m, tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
	if m.chatInput != "" {
		t.Fatalf("ctrl+u: %q", m.chatInput)
	}
}

// Sending and clearing leave the cursor at the end of the next draft, not
// wherever the old one was.
func TestClearedDraftStartsWithCursorAtEnd(t *testing.T) {
	m := fill(chatViewModel(t, 100, 30), 0, nil)
	m = typeText(m, "hello")
	m = pressChat(m, key(tea.KeyHome))
	m.chatInput, m.chatBack = "", 0 // what sending does
	m = typeText(m, "new")
	if m.chatInput != "new" {
		t.Fatalf("draft = %q", m.chatInput)
	}
	// Even if a stale cursor survived (code that only clears the text):
	m.chatBack = 99
	m.chatInput = ""
	m = typeText(m, "ok")
	if m.chatInput != "ok" {
		t.Fatalf("stale cursor leaked into the next draft: %q", m.chatInput)
	}
}

func TestComposerDrawsCursorWhereItIs(t *testing.T) {
	m := fill(chatViewModel(t, 100, 30), 0, nil)
	m.chatInput = "abc"
	if out := plain(m.renderChat()); !strings.Contains(out, "█") {
		t.Errorf("cursor at the end should be a block:\n%s", out)
	}
	m.chatBack = 1
	out := plain(m.renderChat())
	if strings.Contains(out, "█") {
		t.Errorf("a mid-text cursor sits on a character, it doesn't add a block:\n%s", out)
	}
	if !strings.Contains(out, "abc") {
		t.Errorf("draft text lost:\n%s", out)
	}
}

// A draft longer than the box must show the cursor's neighbourhood: after
// moving back to the start, the start of the draft is what's on screen.
func TestLongDraftWindowFollowsCursor(t *testing.T) {
	m := fill(chatViewModel(t, 60, 20), 0, nil)
	m.chatInput = "START-" + strings.Repeat("m", 120) + "-FINISH"

	if out := plain(m.renderChat()); !strings.Contains(out, "FINISH") || strings.Contains(out, "START") {
		t.Errorf("cursor at the end should show the tail:\n%s", out)
	}
	m = pressChat(m, key(tea.KeyHome))
	if out := plain(m.renderChat()); !strings.Contains(out, "START") || strings.Contains(out, "FINISH") {
		t.Errorf("after Home the box should show the start:\n%s", out)
	}
}

// --- command palette ---

func pressCommand(m Model, keys ...tea.KeyPressMsg) Model {
	for _, k := range keys {
		next, _ := m.updateCommandMode(k)
		m = next.(Model)
	}
	return m
}

func TestCommandBarEditsAtCursor(t *testing.T) {
	m := fill(chatViewModel(t, 100, 30), 0, nil)
	m.commandMode = true
	m = pressCommand(m, keyPressWith("t"), keyPressWith("h"), keyPressWith("m"), keyPressWith("e"))
	m = pressCommand(m, key(tea.KeyLeft), key(tea.KeyLeft), keyPressWith("e"))
	if m.commandInput != "theme" {
		// "thme" with the cursor between h and m, plus "e", is "theme"
		t.Fatalf("command = %q, want theme", m.commandInput)
	}
	m = pressCommand(m, key(tea.KeyHome), key(tea.KeyDelete))
	if m.commandInput != "heme" {
		t.Fatalf("command = %q", m.commandInput)
	}
	if got := plain(m.commandBarLine()); !strings.Contains(got, ":heme") {
		t.Errorf("command bar = %q", got)
	}
}

// The old byte-slicing backspace turned "é" into an invalid byte.
func TestCommandBarBackspaceIsRuneSafe(t *testing.T) {
	m := fill(chatViewModel(t, 100, 30), 0, nil)
	m.commandMode = true
	m = pressCommand(m, keyPressWith("é"), key(tea.KeyBackspace))
	if m.commandInput != "" {
		t.Fatalf("command = %q (len %d)", m.commandInput, len(m.commandInput))
	}
}

func TestCommandModeEscResetsCursor(t *testing.T) {
	m := fill(chatViewModel(t, 100, 30), 0, nil)
	m.commandMode = true
	m = pressCommand(m, keyPressWith("a"), keyPressWith("b"), key(tea.KeyLeft), key(tea.KeyEsc))
	if m.commandMode || m.commandInput != "" || m.commandBack != 0 {
		t.Fatalf("esc left state behind: %v %q %d", m.commandMode, m.commandInput, m.commandBack)
	}
}

// --- Feed composer ---

func TestFeedComposerEditsBodyAtCursorAndMovesBetweenLines(t *testing.T) {
	m := feedModel()
	m.startPost("")
	m = typeInto(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}) // title -> content
	m = typeInto(t, m, keyPressWith("a"), keyPressWith("b"), tea.KeyPressMsg{Code: tea.KeyEnter},
		keyPressWith("c"), keyPressWith("d"))
	if m.compose.body != "ab\ncd" {
		t.Fatalf("body = %q", m.compose.body)
	}
	// Up to the first line (same column), then insert there.
	m = typeInto(t, m, key(tea.KeyUp), keyPressWith("X"))
	if m.compose.body != "abX\ncd" {
		t.Fatalf("body = %q", m.compose.body)
	}
	// Enter splits a line at the cursor instead of appending a new one.
	m = typeInto(t, m, key(tea.KeyLeft), tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.compose.body != "ab\nX\ncd" {
		t.Fatalf("body = %q", m.compose.body)
	}
}

func TestFeedComposerTitleIsOneLineWithItsOwnCursor(t *testing.T) {
	m := feedModel()
	m.startPost("")
	m = typeInto(t, m, keyPressWith("H"), keyPressWith("i"), key(tea.KeyLeft), keyPressWith("e"))
	if m.compose.title != "Hei" {
		t.Fatalf("title = %q", m.compose.title)
	}
	// Up/Down mean nothing in a one-line field and must not type.
	m = typeInto(t, m, key(tea.KeyUp), key(tea.KeyDown))
	if m.compose.title != "Hei" {
		t.Fatalf("title = %q", m.compose.title)
	}
	// Switching to the body leaves the title's cursor alone.
	m = typeInto(t, m, tea.KeyPressMsg{Code: tea.KeyTab}, keyPressWith("z"))
	if m.compose.body != "z" || m.compose.title != "Hei" {
		t.Fatalf("title/body = %q / %q", m.compose.title, m.compose.body)
	}
}

func TestFeedComposerRendersCursorInView(t *testing.T) {
	m := feedModel()
	m.width, m.height = 100, 40
	m.startPost("")
	m = typeInto(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	var lines []string
	for i := 0; i < 60; i++ {
		lines = append(lines, "line")
	}
	m.compose.body = "TOP\n" + strings.Join(lines, "\n") + "\nBOTTOM"

	if out := plain(m.renderCompose()); !strings.Contains(out, "BOTTOM") || strings.Contains(out, "TOP") {
		t.Errorf("cursor at the end should show the end of the body")
	}
	m.compose.bodyBack = len([]rune(m.compose.body)) // cursor at the very start
	out := plain(m.renderCompose())
	if !strings.Contains(out, "TOP") || strings.Contains(out, "BOTTOM") {
		t.Errorf("cursor at the start should scroll the body up to it")
	}
	if !strings.Contains(out, "▏") {
		t.Errorf("the caret is missing from the composer")
	}
}
