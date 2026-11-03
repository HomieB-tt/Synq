package app

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/HomieB-tt/synq/internal/chat"
)

func loudModel() Model {
	return Model{notify: notifyState{title: true, bell: true}}
}

func TestWindowTitle(t *testing.T) {
	m := loudModel()
	if got := m.windowTitle(); got != "Synq" {
		t.Errorf("idle title = %q, want Synq", got)
	}
	m.notify.attention = 3
	if got := m.windowTitle(); got != "(3) Synq" {
		t.Errorf("badge title = %q, want (3) Synq", got)
	}
	m.notify.title = false
	if got := m.windowTitle(); got != "" {
		t.Errorf("title layer off should leave the title alone, got %q", got)
	}
}

func TestRaiseAlertRules(t *testing.T) {
	const k = chat.ContactKey("abc")

	m := loudModel()
	m.raiseAlert(alertMessage, k)
	if m.notify.attention != 1 || m.notify.pending != alertMessage || m.notify.pendingContact != k {
		t.Fatalf("an unfocused alert should be recorded, got %+v", m.notify)
	}

	// Looking at Synq: no alert.
	m = loudModel()
	m.notify.focusKnown, m.notify.focused = true, true
	m.raiseAlert(alertMessage, k)
	if m.notify.attention != 0 || m.notify.pending != alertNone {
		t.Errorf("a focused window should not be alerted, got %+v", m.notify)
	}

	// A terminal that reported blur is away.
	m.notify.focused = false
	m.raiseAlert(alertMessage, k)
	if m.notify.attention != 1 {
		t.Errorf("a blurred window should be alerted, got %+v", m.notify)
	}

	// Muted thread: no alert.
	m = loudModel()
	m.notify.muted = map[chat.ContactKey]bool{k: true}
	m.raiseAlert(alertMessage, k)
	if m.notify.attention != 0 {
		t.Errorf("a muted thread should not alert, got %+v", m.notify)
	}
	// ...but node events have no thread to mute.
	m.raiseAlert(alertNodeRequest, "")
	if m.notify.attention != 1 {
		t.Errorf("a node request should alert, got %+v", m.notify)
	}

	// Every layer off: nothing recorded at all.
	m = Model{}
	m.raiseAlert(alertMessage, k)
	if m.notify.attention != 0 || m.notify.pending != alertNone {
		t.Errorf("with all layers off nothing should be recorded, got %+v", m.notify)
	}
}

func TestNoteUnreadAlertsEvenInTheOpenThread(t *testing.T) {
	const k = chat.ContactKey("abc")
	m := loudModel()
	m.activeTab, m.chatActive = tabChat, k
	m.noteUnread(k, true)
	if m.unread[k] != 0 {
		t.Errorf("the open thread should not gain unread, got %d", m.unread[k])
	}
	if m.notify.attention != 1 {
		t.Errorf("but an unfocused window should still be alerted, got %+v", m.notify)
	}

	m = loudModel()
	m.noteUnread(roomThread("r1"), false)
	if m.notify.pending != alertRoom {
		t.Errorf("room traffic should raise a room alert, got %v", m.notify.pending)
	}
}

func TestFlushAlertBellAndThrottle(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	notifyNow = func() time.Time { return now }
	defer func() { notifyNow = time.Now }()

	m := loudModel()
	m.raiseAlert(alertMessage, "abc")
	m, cmd := m.flushAlert(nil)
	if cmd == nil {
		t.Fatal("the first alert should ring the bell")
	}
	if m.notify.pending != alertNone {
		t.Error("flushAlert should clear the pending alert")
	}

	// A burst inside the gap: counted for the title, silent otherwise.
	now = now.Add(time.Second)
	m.raiseAlert(alertMessage, "abc")
	m, cmd = m.flushAlert(nil)
	if cmd != nil {
		t.Error("a second alert inside the gap should be silent")
	}
	if m.notify.attention != 2 {
		t.Errorf("but it should still count toward the title, got %d", m.notify.attention)
	}

	// After the gap it rings again.
	now = now.Add(notifyGap)
	m.raiseAlert(alertMessage, "abc")
	if _, cmd = m.flushAlert(nil); cmd == nil {
		t.Error("an alert after the gap should ring again")
	}

	// Title only: no commands at all.
	m = Model{notify: notifyState{title: true}}
	m.raiseAlert(alertMessage, "abc")
	if _, cmd = m.flushAlert(nil); cmd != nil {
		t.Error("with bell and desktop off there is nothing to run")
	}
}

func TestTrackFocus(t *testing.T) {
	m := loudModel()
	m.notify.attention = 4

	m.trackFocus(tea.BlurMsg{})
	if !m.notify.focusKnown || m.notify.focused {
		t.Errorf("blur should mark the window away, got %+v", m.notify)
	}
	if m.notify.attention != 4 {
		t.Error("blur should not clear the badge")
	}

	m.trackFocus(tea.FocusMsg{})
	if !m.notify.focused || m.notify.attention != 0 {
		t.Errorf("focus should mark the window present and clear the badge, got %+v", m.notify)
	}

	m.notify.attention = 2
	m.trackFocus(tea.KeyPressMsg{Code: 'a'})
	if m.notify.attention != 0 {
		t.Error("a key press means you are here; the badge should clear")
	}
}

func TestAlertTextNeverHasMessageTextAndSanitizesNames(t *testing.T) {
	const k = chat.ContactKey("abc")
	m := loudModel()
	m.chatContacts = map[chat.ContactKey]contactInfo{k: {Username: "bad\x1b]0;x\x07guy"}}

	if got := m.alertText(alertMessage, k); got != "New message" {
		t.Errorf("sender hidden by default, got %q", got)
	}
	if got := m.alertText(alertRoom, roomThread("r1")); got != "New activity in a synapse" {
		t.Errorf("room wording = %q", got)
	}
	if got := m.alertText(alertNodeRequest, ""); got != "New node request" {
		t.Errorf("node request wording = %q", got)
	}
	if got := m.alertText(alertNodeAccepted, ""); got != "Node request accepted" {
		t.Errorf("node accepted wording = %q", got)
	}

	m.notify.sender = true
	got := m.alertText(alertMessage, k)
	if !strings.HasPrefix(got, "New message from ") {
		t.Errorf("with sender on the name should appear, got %q", got)
	}
	if strings.ContainsAny(got, "\x1b\x07") {
		t.Errorf("a hostile name must not carry escape bytes, got %q", got)
	}
}

func TestDesktopSequence(t *testing.T) {
	env := func(kv map[string]string) func(string) string {
		return func(k string) string { return kv[k] }
	}
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"kitty", map[string]string{"KITTY_WINDOW_ID": "1"}, "\x1b]99;i=1:d=0;Synq\x1b\\\x1b]99;i=1:p=body;New message\x1b\\"},
		{"iterm", map[string]string{"TERM_PROGRAM": "iTerm.app"}, "\x1b]9;Synq: New message\x1b\\"},
		{"wezterm", map[string]string{"TERM_PROGRAM": "WezTerm"}, "\x1b]777;notify;Synq;New message\x1b\\"},
		{"ghostty", map[string]string{"TERM_PROGRAM": "ghostty"}, "\x1b]777;notify;Synq;New message\x1b\\"},
		{"foot", map[string]string{"TERM": "foot"}, "\x1b]777;notify;Synq;New message\x1b\\"},
		{"unknown", map[string]string{"TERM": "xterm-256color"}, ""},
	}
	for _, c := range cases {
		if got := desktopSequence("New message", env(c.env)); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}

	got := desktopSequence("hi\x1b]0;pwned\x07there", env(map[string]string{"TERM_PROGRAM": "WezTerm"}))
	if strings.Count(got, "\x1b") != 2 || strings.Contains(got, "\x07") {
		t.Errorf("the body must not be able to add escape sequences, got %q", got)
	}
}

func TestNotifyCommandSwitches(t *testing.T) {
	m := loudModel()

	if got := m.notifyCommand(nil); !strings.Contains(got, "title on") || !strings.Contains(got, "desktop off") {
		t.Errorf("status should show the defaults, got %q", got)
	}

	m.notifyCommand([]string{"bell", "off"})
	if m.notify.bell {
		t.Error(":notify bell off should turn the bell off")
	}
	m.notifyCommand([]string{"desktop", "on"})
	if !m.notify.desktop {
		t.Error(":notify desktop on should turn desktop alerts on")
	}
	got := m.notifyCommand([]string{"sender", "on"})
	if !m.notify.sender || !strings.Contains(got, "never include message text") {
		t.Errorf(":notify sender on should say what it does and does not reveal, got %q", got)
	}

	m.notify.attention = 5
	m.notifyCommand([]string{"title", "off"})
	if m.notify.title || m.notify.attention != 0 {
		t.Error(":notify title off should switch it off and drop the badge")
	}

	for _, bad := range [][]string{{"bell"}, {"bell", "maybe"}, {"loud", "on"}, {"bell", "on", "now"}} {
		if got := m.notifyCommand(bad); got != notifyUsage {
			t.Errorf("%v should print the usage, got %q", bad, got)
		}
	}
}

func TestNotifyMute(t *testing.T) {
	const k = chat.ContactKey("abc")
	m := loudModel()
	m.chatContacts = map[chat.ContactKey]contactInfo{k: {Username: "alice"}}

	if got := m.notifyCommand([]string{"mute"}); !strings.Contains(got, "Open a chat first") {
		t.Errorf("mute with nothing open should say so, got %q", got)
	}
	if got := m.notifyCommand([]string{"mute", "nobody"}); !strings.Contains(got, "No chat with") {
		t.Errorf("an unknown name should be refused, got %q", got)
	}

	m.notifyCommand([]string{"mute", "ALICE"})
	if !m.notify.muted[k] {
		t.Fatal("muting by name should be case-insensitive")
	}
	m.raiseAlert(alertMessage, k)
	if m.notify.attention != 0 {
		t.Error("a muted contact should not raise alerts")
	}
	if got := m.notifyStatus(); !strings.Contains(got, "Muted: alice") {
		t.Errorf("status should list muted chats, got %q", got)
	}

	m.notifyCommand([]string{"unmute", "alice"})
	if m.notify.muted[k] {
		t.Error("unmute should lift the mute")
	}

	// With no name, the open thread is the target.
	m.activeTab, m.chatActive = tabChat, k
	m.notifyCommand([]string{"mute"})
	if !m.notify.muted[k] {
		t.Error("mute with no name should mute the open thread")
	}
}
