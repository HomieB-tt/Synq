package app

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/HomieB-tt/synq/internal/chat"
)

// threadWith appends n incoming messages "m1".."mn" to contact, an hour
// apart starting at base (so recency is controllable).
func threadWith(m Model, contact chat.ContactKey, n int, base time.Time) {
	for i := 1; i <= n; i++ {
		m.chatStore.Append(contact, chat.Message{
			Body: []byte(fmt.Sprintf("m%d", i)),
			At:   base.Add(time.Duration(i) * time.Hour),
		})
	}
}

var t0 = time.Date(2026, 10, 14, 8, 0, 0, 0, time.UTC)

func tick(m Model) Model {
	next, _ := m.Update(tea.WindowSizeMsg{Width: m.width, Height: m.height})
	return next.(Model)
}

// ---- the divider ----

func TestOpeningAThreadWithUnreadDrawsADividerAboveTheFirstUnread(t *testing.T) {
	m := chatViewModel(t, 100, 30)
	threadWith(m, alice, 10, t0)
	m.unread = map[chat.ContactKey]int{alice: 3}

	m.activeTab, m.chatActive = tabChat, alice
	m = tick(m)

	if m.chatMarkFor != alice || m.chatMarkCount != 3 || m.chatMarkIdx != 7 {
		t.Fatalf("marker = for %q idx %d count %d, want alice idx 7 count 3", m.chatMarkFor, m.chatMarkIdx, m.chatMarkCount)
	}
	if m.unread[alice] != 0 {
		t.Error("a thread on screen should have no unread")
	}

	lines := strings.Split(plain(m.renderChat()), "\n")
	div, first := -1, -1
	for i, l := range lines {
		if strings.Contains(l, "3 new messages") {
			div = i
		}
		if strings.Contains(l, "m8") {
			first = i
		}
	}
	if div < 0 {
		t.Fatalf("no divider drawn:\n%s", strings.Join(lines, "\n"))
	}
	if first <= div {
		t.Errorf("divider (row %d) must sit above the first unread message m8 (row %d)", div, first)
	}
	for _, l := range lines[:div] {
		if strings.Contains(l, "m8") || strings.Contains(l, "m9") || strings.Contains(l, "m10") {
			t.Errorf("an unread message appears above the divider: %q", l)
		}
	}
}

func TestDividerSingularAndFitsItsWidth(t *testing.T) {
	m := chatViewModel(t, 100, 30)
	if got := plain(m.chatDivider(60, 1)); !strings.Contains(got, "1 new message ") || strings.Contains(got, "messages") {
		t.Errorf("singular label: %q", got)
	}
	for _, w := range []int{4, 10, 20, 60, 120} {
		for _, n := range []int{1, 7, 1234} {
			if got := lipgloss.Width(plain(m.chatDivider(w, n))); got > w {
				t.Errorf("w=%d n=%d: divider is %d wide", w, n, got)
			}
		}
	}
	if got := lipgloss.Width(plain(m.chatDivider(60, 3))); got != 60 {
		t.Errorf("a roomy divider should span the pane exactly, got %d", got)
	}
}

func TestNoUnreadMeansNoDividerAndNoJump(t *testing.T) {
	m := chatViewModel(t, 100, 30)
	threadWith(m, alice, 40, t0)
	m.activeTab, m.chatActive = tabChat, alice
	m = tick(m)

	if m.chatMarkCount != 0 || m.chatOffset() != 0 {
		t.Errorf("count=%d offset=%d, want 0 and live", m.chatMarkCount, m.chatOffset())
	}
	if strings.Contains(plain(m.renderChat()), "new message") {
		t.Error("a divider appeared with nothing unread")
	}
}

// With far more unread than a screenful, opening lands on the divider
// and the first unread message, not on the newest end of the backlog.
func TestOpeningWithABacklogJumpsToTheFirstUnread(t *testing.T) {
	m := chatViewModel(t, 100, 30)
	threadWith(m, alice, 120, t0)
	m.unread = map[chat.ContactKey]int{alice: 100}

	m.activeTab, m.chatActive = tabChat, alice
	m = tick(m)

	out := plain(m.renderChat())
	if !strings.Contains(out, "100 new messages") {
		t.Fatalf("the divider should be in view after the jump:\n%s", out)
	}
	if !strings.Contains(out, "m21 ") && !strings.Contains(out, "m21\n") && !strings.Contains(out, "m21") {
		t.Errorf("the first unread message (m21) should be in view:\n%s", out)
	}
	if strings.Contains(out, "m120") {
		t.Errorf("should not have landed on the newest end:\n%s", out)
	}
	if m.chatOffset() == 0 {
		t.Error("expected a scrolled-back view")
	}

	// End returns to the live edge.
	m.scrollChat(-m.chatStore.Len(alice))
	if !strings.Contains(plain(m.renderChat()), "m120") {
		t.Error("End should reach the newest message")
	}
}

// The divider is a snapshot: later arrivals don't move it, and don't
// count as unread while you're looking.
func TestDividerStaysPutWhileTheThreadIsOpen(t *testing.T) {
	m := chatViewModel(t, 100, 30)
	threadWith(m, alice, 6, t0)
	m.unread = map[chat.ContactKey]int{alice: 2}
	m.activeTab, m.chatActive = tabChat, alice
	m = tick(m)
	idx := m.chatMarkIdx

	for i := 0; i < 5; i++ {
		m.chatStore.Append(alice, chat.Message{Body: []byte("later"), At: time.Now()})
		m.noteIncoming(alice)
		m = tick(m)
	}
	if m.chatMarkIdx != idx || m.chatMarkCount != 2 {
		t.Errorf("divider moved: idx %d count %d, want idx %d count 2", m.chatMarkIdx, m.chatMarkCount, idx)
	}
	if m.unread[alice] != 0 {
		t.Errorf("messages arriving in the open thread counted unread: %d", m.unread[alice])
	}
}

// Leaving drops the snapshot; coming back shows what's new since then.
func TestDividerResetsWhenTheThreadLeavesTheScreen(t *testing.T) {
	m := chatViewModel(t, 100, 30)
	threadWith(m, alice, 6, t0)
	m.unread = map[chat.ContactKey]int{alice: 2}
	m.activeTab, m.chatActive = tabChat, alice
	m = tick(m)
	if m.chatMarkCount != 2 {
		t.Fatal("setup: no divider")
	}

	// Away to another tab, where two more arrive.
	m.activeTab = tabNodes
	m = tick(m)
	if m.chatMarkFor != "" {
		t.Error("the snapshot should be dropped when the thread goes off screen")
	}
	for i := 0; i < 2; i++ {
		m.chatStore.Append(alice, chat.Message{Body: []byte("away"), At: time.Now()})
		m.noteIncoming(alice)
	}

	m.activeTab = tabChat
	m = tick(m)
	if m.chatMarkCount != 2 || m.chatMarkIdx != 6 {
		t.Errorf("fresh divider = idx %d count %d, want idx 6 count 2", m.chatMarkIdx, m.chatMarkCount)
	}

	// Back to the list and in again with nothing new: no divider.
	m.chatActive = ""
	m = tick(m)
	m.chatActive = alice
	m = tick(m)
	if m.chatMarkCount != 0 {
		t.Errorf("a revisit with nothing new still shows a divider (count %d)", m.chatMarkCount)
	}
}

func TestChatWithDividerStillFitsThePane(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {50, 14}, {60, 10}} {
		m := chatViewModel(t, size[0], size[1])
		threadWith(m, alice, 50, t0)
		m.unread = map[chat.ContactKey]int{alice: 20}
		m.activeTab, m.chatActive = tabChat, alice
		m = tick(m)
		w, h := m.innerSize()
		lines := strings.Split(plain(m.renderChat()), "\n")
		if len(lines) > h {
			t.Errorf("%dx%d: %d rows, pane holds %d", size[0], size[1], len(lines), h)
		}
		for _, l := range lines {
			if got := lipgloss.Width(l); got > w {
				t.Errorf("%dx%d: row %d wide, pane %d: %q", size[0], size[1], got, w, l)
			}
		}
	}
}

// ---- thread ranking ----

func TestThreadsRankUnreadFirstThenByRecency(t *testing.T) {
	m := chatViewModel(t, 100, 30)
	threadWith(m, "old", 2, t0)                   // created first, oldest messages
	threadWith(m, "mid", 2, t0.Add(10*time.Hour)) // newer
	threadWith(m, "new", 2, t0.Add(20*time.Hour)) // newest
	threadWith(m, "unr", 1, t0)                   // oldest of all, but unread

	m.unread = map[chat.ContactKey]int{"unr": 1}
	got := m.orderedThreads()
	want := []chat.ContactKey{"unr", "new", "mid", "old"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("order = %v, want %v", got, want)
	}

	// Several unread rank among themselves by recency.
	m.unread = map[chat.ContactKey]int{"old": 1, "mid": 1}
	got = m.orderedThreads()
	want = []chat.ContactKey{"mid", "old", "new", "unr"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("order with two unread = %v, want %v", got, want)
	}
}

func TestEqualThreadsKeepTheirStoredOrder(t *testing.T) {
	m := chatViewModel(t, 100, 30)
	for _, k := range []chat.ContactKey{"c", "a", "b"} {
		m.chatStore.Append(k, chat.Message{Body: []byte("x"), At: t0}) // identical times
	}
	if got := m.orderedThreads(); fmt.Sprint(got) != "[c a b]" {
		t.Errorf("ties must not shuffle: %v", got)
	}
}

// A message arriving re-ranks the list; the highlight has to stay on
// the contact it was on, or Enter opens the wrong thread.
func TestSelectionFollowsTheContactWhenTheListReRanks(t *testing.T) {
	m := chatViewModel(t, 100, 30)
	threadWith(m, "a", 1, t0.Add(3*time.Hour))
	threadWith(m, "b", 1, t0.Add(2*time.Hour))
	threadWith(m, "c", 1, t0.Add(1*time.Hour))
	// ranked: a, b, c. Select b.
	m.chatListIndex = 1
	prev := m.selectedThread()
	if prev != "b" {
		t.Fatalf("setup: selected %q", prev)
	}

	// c gets a new message: it jumps to the top.
	m.chatStore.Append("c", chat.Message{Body: []byte("hi"), At: t0.Add(9 * time.Hour)})
	m.noteIncoming("c")
	if m.selectedThread() == "b" {
		t.Fatal("setup: the re-rank should have moved the selection")
	}

	m = m.withListAnchor(prev)
	if m.selectedThread() != "b" {
		t.Errorf("selection moved off b onto %q", m.selectedThread())
	}
	m.openSelectedChat()
	if m.chatActive != "b" {
		t.Errorf("Enter opened %q, want b", m.chatActive)
	}
}

func TestUpdateKeepsSelectionAnchoredButLetsKeysMoveIt(t *testing.T) {
	m := chatViewModel(t, 100, 30)
	m.identity = testIdentityForRender(t)
	m.activeTab = tabChat
	threadWith(m, "a", 1, t0.Add(3*time.Hour))
	threadWith(m, "b", 1, t0.Add(2*time.Hour))

	// A non-key message with nothing re-ranked leaves the index alone.
	m.chatListIndex = 1
	if got := tick(m).chatListIndex; got != 1 {
		t.Errorf("a quiet update moved the selection to %d", got)
	}

	// A key press moves it - the anchor must not fight the user.
	next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if got := next.(Model).chatListIndex; got != 0 {
		t.Errorf("up moved the selection to %d, want 0", got)
	}
}

func TestListArrowsWrapAndOpenTheRankedThread(t *testing.T) {
	m := chatViewModel(t, 100, 30)
	threadWith(m, "a", 1, t0.Add(1*time.Hour))
	threadWith(m, "b", 1, t0.Add(2*time.Hour)) // ranks first
	// ranked: b, a
	m.wrapChatList(-1) // up from the top wraps to the last
	if m.chatListIndex != 1 {
		t.Errorf("wrap up: index %d, want 1", m.chatListIndex)
	}
	m.wrapChatList(1) // down from the last wraps to the first
	if m.chatListIndex != 0 {
		t.Errorf("wrap down: index %d, want 0", m.chatListIndex)
	}
	m.openSelectedChat()
	if m.chatActive != "b" {
		t.Errorf("the top-ranked thread should open, got %q", m.chatActive)
	}
	empty := chatViewModel(t, 100, 30)
	empty.wrapChatList(1)
	if empty.chatListIndex != 0 {
		t.Errorf("an empty list should hold index 0, got %d", empty.chatListIndex)
	}
}

// The jump has to land on the divider whatever shape the messages are:
// tightly spaced messages from one sender are a row each, messages after
// long pauses are three (spacer, header, body), and long ones wrap.
func TestJumpLandsOnTheDividerForAnyMessageShape(t *testing.T) {
	shapes := map[string]func(i int) (time.Duration, string){
		"one row each": func(i int) (time.Duration, string) { return time.Duration(i) * time.Second, fmt.Sprintf("msg%03d", i) },
		"three rows":   func(i int) (time.Duration, string) { return time.Duration(i) * time.Hour, fmt.Sprintf("msg%03d", i) },
		"wrapping body": func(i int) (time.Duration, string) {
			return time.Duration(i) * time.Hour, fmt.Sprintf("msg%03d ", i) + strings.Repeat("word ", 30)
		},
	}
	for name, shape := range shapes {
		for _, size := range [][2]int{{100, 40}, {80, 24}, {60, 16}} {
			// A single message taller than the whole conversation area
			// (a six-row wrapped message in a four-row area) can't be
			// shown with the divider above it; that combination is a
			// terminal too small for the content, not a jump bug.
			if name == "wrapping body" && size[1] < 24 {
				continue
			}
			m := chatViewModel(t, size[0], size[1])
			for i := 1; i <= 150; i++ {
				d, body := shape(i)
				m.chatStore.Append(alice, chat.Message{Body: []byte(body), At: t0.Add(d)})
			}
			m.unread = map[chat.ContactKey]int{alice: 90}
			m.activeTab, m.chatActive = tabChat, alice
			m = tick(m)

			out := plain(m.renderChat())
			if !strings.Contains(out, "90 new messages") {
				t.Errorf("%s %dx%d: divider not in view:\n%s", name, size[0], size[1], out)
			}
			if !strings.Contains(out, "msg061") { // first unread = 150-90+1
				t.Errorf("%s %dx%d: first unread message not in view:\n%s", name, size[0], size[1], out)
			}
		}
	}
}

// Update runs this bookkeeping after every message, so it must never be
// able to crash a model - including one built without a chat store, as
// several tests (and any future embedding) do.
func TestUpdateSurvivesAModelWithoutAChatStore(t *testing.T) {
	var m Model
	for _, tab := range []tab{tabFeed, tabNodes, tabChat, tabProfile} {
		m.activeTab = tab
		m.chatActive = "someone"
		for _, msg := range []tea.Msg{
			tea.WindowSizeMsg{Width: 80, Height: 24},
			tea.KeyPressMsg{Code: tea.KeyDown},
			tea.KeyPressMsg{Code: 'j'},
		} {
			next, _ := m.Update(msg)
			m = next.(Model)
		}
	}
}
