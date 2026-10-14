package app

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/HomieB-tt/synq/internal/chat"
	"github.com/HomieB-tt/synq/internal/ui/styles"
)

// chatViewModel is a Model with just enough state to draw the Chat tab:
// a theme, a thread store, a size. No pin store, so keyBlockReason is
// always empty - the trust-banner tests live with the pin tests.
func chatViewModel(t *testing.T, width, height int) Model {
	t.Helper()
	return Model{
		theme:     styles.Default(),
		chatStore: chat.NewStore(),
		width:     width,
		height:    height,
	}
}

const alice = chat.ContactKey("alice-key")

func fill(m Model, n int, body func(i int) string) Model {
	base := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	for i := 1; i <= n; i++ {
		m.chatStore.Append(alice, chat.Message{
			Body:     []byte(body(i)),
			At:       base.Add(time.Duration(i) * time.Hour), // an hour apart: every message gets its own header
			Outgoing: i%2 == 0,
		})
	}
	m.chatActive = alice
	return m
}

func plain(s string) string { return ansi.Strip(s) }

// Whatever the thread holds and however small the terminal, the Chat
// body is never wider or taller than the pane it is drawn in - the bug
// the old renderChat had, where a long thread pushed the composer and the
// status bar off the screen.
func TestChatThreadNeverOverflowsPane(t *testing.T) {
	long := strings.Repeat("a fairly long message that has to wrap ", 8)
	for _, size := range [][2]int{{80, 24}, {120, 40}, {40, 14}, {60, 10}, {30, 12}} {
		for _, n := range []int{0, 1, 3, 200} {
			m := fill(chatViewModel(t, size[0], size[1]), n, func(i int) string {
				if i%3 == 0 {
					return long
				}
				return fmt.Sprintf("message %d", i)
			})
			m.chatInput = strings.Repeat("typing a draft that is longer than the box ", 4)

			innerW, innerH := m.innerSize()
			out := plain(m.renderChat())
			lines := strings.Split(out, "\n")

			if len(lines) > innerH {
				t.Errorf("%dx%d, %d msgs: %d lines, pane holds %d", size[0], size[1], n, len(lines), innerH)
			}
			for _, l := range lines {
				if w := lipgloss.Width(l); w > innerW {
					t.Errorf("%dx%d, %d msgs: line %q is %d wide, pane holds %d", size[0], size[1], n, l, w, innerW)
					break
				}
			}
		}
	}
}

// Roomy terminals use the pane exactly: the composer sits on the bottom
// row instead of floating wherever the content happened to end.
func TestChatThreadFillsPaneExactly(t *testing.T) {
	for _, n := range []int{0, 2, 100} {
		m := fill(chatViewModel(t, 100, 30), n, func(i int) string { return fmt.Sprintf("m%d", i) })
		_, innerH := m.innerSize()
		if got := len(strings.Split(m.renderChat(), "\n")); got != innerH {
			t.Errorf("%d msgs: %d lines, want exactly %d", n, got, innerH)
		}
	}
}

func TestChatShowsNewestAtTheBottomAndScrollsBack(t *testing.T) {
	m := fill(chatViewModel(t, 100, 30), 50, func(i int) string { return fmt.Sprintf("msg-%02d", i) })

	live := plain(m.renderChat())
	if !strings.Contains(live, "msg-50") {
		t.Fatalf("live view should end at the newest message:\n%s", live)
	}
	if strings.Contains(live, "newer") {
		t.Errorf("live view shouldn't claim there's anything newer:\n%s", live)
	}

	m.scrollChat(10)
	back := plain(m.renderChat())
	if strings.Contains(back, "msg-50") || !strings.Contains(back, "msg-40") {
		t.Errorf("scrolled back 10: want msg-40 as the newest shown, got:\n%s", back)
	}
	if !strings.Contains(back, "↓ 10 newer") {
		t.Errorf("scrolled view should say how far from live it is:\n%s", back)
	}
}

func TestChatScrollIsClampedBothWays(t *testing.T) {
	m := fill(chatViewModel(t, 100, 30), 5, func(i int) string { return "x" })

	m.scrollChat(1000)
	if got := m.chatOffset(); got != 4 {
		t.Errorf("scrolling far past the start: offset = %d, want 4 (the oldest message)", got)
	}
	m.scrollChat(-1000)
	if got := m.chatOffset(); got != 0 {
		t.Errorf("scrolling far past the end: offset = %d, want 0 (live)", got)
	}

	empty := chatViewModel(t, 100, 30)
	empty.chatActive = alice
	empty.scrollChat(5) // an empty thread has nothing to scroll to
	if got := empty.chatOffset(); got != 0 {
		t.Errorf("empty thread: offset = %d, want 0", got)
	}
}

// An offset belongs to the thread it was set in; any other thread reads
// as live, so no code path that changes chatActive has to reset it.
func TestChatScrollOffsetIsPerThread(t *testing.T) {
	m := fill(chatViewModel(t, 100, 30), 20, func(i int) string { return "x" })
	m.scrollChat(7)
	m.chatActive = "someone-else"
	if got := m.chatOffset(); got != 0 {
		t.Errorf("another thread inherited offset %d", got)
	}
	m.chatActive = alice
	if got := m.chatOffset(); got != 7 {
		t.Errorf("returning to the thread lost its offset: %d, want 7", got)
	}
}

func TestIncomingMessageDoesNotMoveAScrolledBackView(t *testing.T) {
	m := fill(chatViewModel(t, 100, 30), 30, func(i int) string { return fmt.Sprintf("msg-%02d", i) })
	m.scrollChat(5)
	before := plain(m.renderChat())

	m.chatStore.Append(alice, chat.Message{Body: []byte("brand new"), At: time.Now()})
	m.noteIncoming(alice)
	after := plain(m.renderChat())

	if !strings.Contains(after, "msg-25") || strings.Contains(after, "brand new") {
		t.Errorf("view slid when a message arrived:\nbefore:\n%s\nafter:\n%s", before, after)
	}

	// While live, an arrival just appears.
	live := fill(chatViewModel(t, 100, 30), 3, func(i int) string { return "old" })
	live.chatStore.Append(alice, chat.Message{Body: []byte("brand new"), At: time.Now()})
	live.noteIncoming(alice)
	if live.chatOffset() != 0 || !strings.Contains(plain(live.renderChat()), "brand new") {
		t.Error("a live view should show the new message")
	}
}

// Consecutive messages from one sender share a header; a change of
// sender, or a long pause, starts a new one.
func TestChatGroupsConsecutiveMessages(t *testing.T) {
	m := chatViewModel(t, 100, 30)
	t0 := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	add := func(at time.Duration, out bool, body string) {
		m.chatStore.Append(alice, chat.Message{Body: []byte(body), At: t0.Add(at), Outgoing: out})
	}
	add(0, true, "one")
	add(time.Minute, true, "two")      // same sender, close: no new header
	add(2*time.Minute, false, "three") // other sender: header
	add(30*time.Minute, false, "four") // same sender, long pause: header
	m.chatActive = alice

	out := plain(m.renderChat())
	if got := strings.Count(out, "you  09:"); got != 1 {
		t.Errorf("want one 'you' header for the two quick messages, got %d:\n%s", got, out)
	}
	if got := strings.Count(out, "09:02") + strings.Count(out, "09:30"); got != 2 {
		t.Errorf("want headers at 09:02 and 09:30, got:\n%s", out)
	}
}

func TestChatWrapsLongMessagesInsteadOfOverflowing(t *testing.T) {
	m := fill(chatViewModel(t, 60, 30), 1, func(int) string {
		return "alpha bravo charlie delta echo foxtrot golf hotel india juliet kilo lima mike november"
	})
	out := plain(m.renderChat())
	for _, word := range []string{"alpha", "november"} {
		if !strings.Contains(out, word) {
			t.Errorf("wrapped message lost %q:\n%s", word, out)
		}
	}
}

func TestChatComposerShowsTheEndOfALongDraft(t *testing.T) {
	m := fill(chatViewModel(t, 60, 30), 0, func(int) string { return "" })
	m.chatInput = strings.Repeat("x", 100) + "THE-END"
	out := plain(m.renderChat())
	if !strings.Contains(out, "THE-END") {
		t.Errorf("composer should keep the cursor end of the draft in view:\n%s", out)
	}
	if !strings.Contains(out, "█") {
		t.Errorf("composer should draw a cursor:\n%s", out)
	}
}

func TestChatListSelectionAndOpen(t *testing.T) {
	m := chatViewModel(t, 100, 30)
	for _, c := range []chat.ContactKey{"k1", "k2", "k3"} {
		m.chatStore.Append(c, chat.Message{Body: []byte("hello from " + string(c)), At: time.Now()})
	}

	m.moveChatList(1)
	m.moveChatList(1)
	m.moveChatList(1) // past the end: clamped
	if m.chatListIndex != 2 {
		t.Fatalf("index = %d, want 2", m.chatListIndex)
	}
	m.moveChatList(-10)
	if m.chatListIndex != 0 {
		t.Fatalf("index = %d, want 0", m.chatListIndex)
	}

	m.moveChatList(1)
	m.openSelectedChat()
	if m.chatActive != "k2" {
		t.Errorf("opened %q, want k2", m.chatActive)
	}
}

func TestChatListRowsFitTheirWidth(t *testing.T) {
	for _, w := range []int{20, 40, 80, 120} {
		m := chatViewModel(t, w+6, 20) // +6: the pane's border and padding
		m.chatStore.Append(alice, chat.Message{
			Body: []byte("a preview that is much longer than any of these rows can show"),
			At:   time.Now(), Outgoing: true,
		})
		innerW, _ := m.innerSize()
		for _, sel := range []bool{false, true} {
			row := plain(m.chatListRow(alice, sel, innerW))
			if got := lipgloss.Width(row); got != innerW {
				t.Errorf("width %d selected=%v: row is %d wide, want exactly %d: %q", innerW, sel, got, innerW, row)
			}
		}
	}
}

// The pane plus its neighbours must add up to the terminal exactly, or
// the header scrolls off the top / a gap opens at the bottom.
func TestContentSizeAccountsForTheBottomBar(t *testing.T) {
	m := chatViewModel(t, 80, 24)
	_, oneRow := m.contentSize()
	if oneRow != 24-2-1 {
		t.Errorf("one-row bottom bar: pane height = %d, want %d", oneRow, 24-3)
	}

	m.commandMsg = strings.Repeat("a long command result that wraps over several rows ", 3)
	rows := len(m.bottomLines())
	if rows < 2 || rows > 4 {
		t.Fatalf("expected a wrapped 2-4 row bottom bar, got %d", rows)
	}
	if _, h := m.contentSize(); h != 24-2-rows {
		t.Errorf("%d-row bottom bar: pane height = %d, want %d", rows, h, 24-2-rows)
	}
}

// The key-change warning is the one banner a user must be able to read
// and act on, so it has to stay legible on a narrow terminal: wrapped to
// the pane, never cut to nothing, never taller than the cap.
func TestBlockedBannerWrapsAndNamesTheProblem(t *testing.T) {
	m := fill(chatViewModel(t, 100, 30), 3, func(int) string { return "x" })
	reason := "alice's key changed since you pinned it - sending to them is blocked. " +
		":verify alice compares fingerprints, :accept alice trusts the new key."

	for _, w := range []int{30, 50, 80} {
		lines := m.chatStatusLines(w, reason)
		if len(lines) < 1 || len(lines) > chatMaxStatusLines {
			t.Errorf("width %d: %d banner lines, want 1..%d", w, len(lines), chatMaxStatusLines)
		}
		joined := plain(strings.Join(lines, "\n"))
		if !strings.Contains(joined, "key changed") {
			t.Errorf("width %d: banner lost the problem statement:\n%s", w, joined)
		}
		for _, l := range lines {
			if got := lipgloss.Width(plain(l)); got > w {
				t.Errorf("width %d: banner line is %d wide: %q", w, got, plain(l))
			}
		}
	}
}

// With nothing wrong, the banner says what state the thread is in.
func TestStatusBannerDescribesTheThread(t *testing.T) {
	m := fill(chatViewModel(t, 100, 30), 1, func(int) string { return "x" })
	if got := plain(m.chatStatusLines(80, "")[0]); !strings.Contains(got, "offline") {
		t.Errorf("no connection: %q", got)
	}
}

// The composer is where typing happens; even on a pane too short for
// anything else it must survive, along with the newest message.
func TestShortPaneKeepsComposerAndNewestMessage(t *testing.T) {
	m := fill(chatViewModel(t, 80, 10), 5, func(i int) string { return fmt.Sprintf("msg-%d", i) })
	m.chatInput = "draft"
	out := plain(m.renderChat())
	if !strings.Contains(out, "draft") || !strings.Contains(out, "msg-5") {
		t.Errorf("short pane dropped the composer or newest message:\n%s", out)
	}
}
