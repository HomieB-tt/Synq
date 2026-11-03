package app

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/HomieB-tt/synq/internal/chat"
	"github.com/HomieB-tt/synq/internal/db"
	"github.com/HomieB-tt/synq/internal/sanitize"
)

// This file is the "you have something waiting" signal for when Synq is
// not the thing in front of you: another terminal tab, another app. It
// has three independent layers, set with :notify.
//
//   - title: the window or tab title reads "(3) Synq" while activity is
//     waiting. Works in nearly every terminal.
//   - bell: one BEL on new activity. Many terminals turn it into a tab
//     highlight, a taskbar flash or a sound.
//   - desktop: a real desktop notification, through the terminal's own
//     escape sequence where it has one, else notify-send / osascript.
//
// Nothing here ever carries message text. A notification says "New
// message" and, only if :notify sender on, who from - previews leak onto
// lock screens and into notification history, which a privacy-first app
// should not do by default.

// alertKind says what happened, which decides the wording.
type alertKind int

const (
	alertNone alertKind = iota
	alertMessage
	alertRoom
	alertNodeRequest
	alertNodeAccepted
)

// notifyGap is the shortest time between two sounds or desktop
// notifications, so a burst of ten messages is one alert, not ten.
const notifyGap = 5 * time.Second

// notifyNow is the clock, replaceable in tests.
var notifyNow = time.Now

// notifyState is everything :notify owns. The zero value is quiet (all
// layers off), which is what tests that build a bare Model get;
// loadNotify applies the real defaults.
type notifyState struct {
	title, bell, desktop, sender bool

	// muted threads raise no alert (their unread counts still grow).
	muted map[chat.ContactKey]bool

	// focusKnown turns true once the terminal has reported focus at least
	// once; until then Synq assumes it is not being looked at, so a
	// terminal without focus reporting still gets alerts.
	focusKnown bool
	focused    bool

	// attention counts alerts raised while you were away. It drives the
	// title badge and resets when the window regains focus or you press a
	// key.
	attention int

	// pending is set while a message is being handled and turned into
	// commands by flushAlert at the end of Update.
	pending        alertKind
	pendingContact chat.ContactKey

	lastAt time.Time
}

// loadNotify reads the saved switches, applying the defaults for any that
// were never set.
func loadNotify(store *db.KeyStore) notifyState {
	n := notifyState{title: true, bell: true}
	if store == nil {
		return n
	}
	read := func(key string, def bool) bool {
		v, err := store.LoadPreference(key)
		if err != nil {
			return def
		}
		return v == "on"
	}
	n.title = read(db.PrefNotifyTitle, true)
	n.bell = read(db.PrefNotifyBell, true)
	n.desktop = read(db.PrefNotifyDesktop, false)
	n.sender = read(db.PrefNotifySender, false)
	if v, err := store.LoadPreference(db.PrefNotifyMuted); err == nil {
		var keys []string
		if json.Unmarshal([]byte(v), &keys) == nil {
			n.muted = make(map[chat.ContactKey]bool, len(keys))
			for _, k := range keys {
				n.muted[chat.ContactKey(k)] = true
			}
		}
	}
	return n
}

// raiseAlert records that something arrived that deserves attention. It is
// cheap and has no side effects beyond the model; flushAlert, at the end of
// Update, turns it into a bell or notification.
func (m *Model) raiseAlert(kind alertKind, contact chat.ContactKey) {
	n := &m.notify
	if !n.title && !n.bell && !n.desktop {
		return
	}
	if contact != "" && n.muted[contact] {
		return
	}
	if n.focusKnown && n.focused {
		return // you are looking at Synq; the in-app indicators do the job
	}
	n.attention++
	if n.pending == alertNone {
		n.pending, n.pendingContact = kind, contact
	}
}

// flushAlert runs after every Update. If an alert was raised it adds the
// bell and desktop commands (at most one set per notifyGap) to cmd.
func (m Model) flushAlert(cmd tea.Cmd) (Model, tea.Cmd) {
	if m.notify.pending == alertNone {
		return m, cmd
	}
	kind, contact := m.notify.pending, m.notify.pendingContact
	m.notify.pending, m.notify.pendingContact = alertNone, ""

	now := notifyNow()
	if !m.notify.lastAt.IsZero() && now.Sub(m.notify.lastAt) < notifyGap {
		return m, cmd
	}
	m.notify.lastAt = now

	cmds := []tea.Cmd{}
	if cmd != nil {
		cmds = append(cmds, cmd)
	}
	if m.notify.bell {
		cmds = append(cmds, tea.Raw("\a"))
	}
	if m.notify.desktop {
		cmds = append(cmds, desktopNotifyCmd(m.alertText(kind, contact)))
	}
	if len(cmds) == 0 {
		return m, nil
	}
	return m, tea.Batch(cmds...)
}

// trackFocus keeps the focus and attention state in step with the
// terminal; Update calls it first for every message.
func (m *Model) trackFocus(msg tea.Msg) {
	switch msg.(type) {
	case tea.FocusMsg:
		m.notify.focusKnown, m.notify.focused, m.notify.attention = true, true, 0
	case tea.BlurMsg:
		m.notify.focusKnown, m.notify.focused = true, false
	case tea.KeyPressMsg:
		m.notify.attention = 0
	}
}

// windowTitle is the title View sets: "Synq", or "(3) Synq" while alerts
// are waiting. Empty when the title layer is off, which leaves whatever
// title the terminal had.
func (m Model) windowTitle() string {
	if !m.notify.title {
		return ""
	}
	if m.notify.attention > 0 {
		return fmt.Sprintf("(%d) Synq", m.notify.attention)
	}
	return "Synq"
}

// alertText is the notification wording. The sender is named only when
// :notify sender on.
func (m Model) alertText(kind alertKind, contact chat.ContactKey) string {
	label := ""
	if m.notify.sender && contact != "" {
		label = truncateRunes(sanitize.OneLine(m.chatContactLabel(contact)), 40)
	}
	switch kind {
	case alertMessage:
		if label != "" {
			return "New message from " + label
		}
		return "New message"
	case alertRoom:
		if label != "" {
			return "New activity in " + label
		}
		return "New activity in a synapse"
	case alertNodeRequest:
		return "New node request"
	case alertNodeAccepted:
		return "Node request accepted"
	}
	return "New activity"
}

// --- desktop notifications ---

// desktopSequence is the escape sequence that makes the terminal itself
// raise a notification, for the terminals that have one; "" for the rest.
func desktopSequence(body string, getenv func(string) string) string {
	body = sanitize.OneLine(body)
	const st = "\x1b\\"
	switch {
	case getenv("KITTY_WINDOW_ID") != "":
		return "\x1b]99;i=1:d=0;Synq" + st + "\x1b]99;i=1:p=body;" + body + st
	case getenv("TERM_PROGRAM") == "iTerm.app":
		return "\x1b]9;Synq: " + body + st
	case getenv("TERM_PROGRAM") == "WezTerm", getenv("TERM_PROGRAM") == "ghostty",
		strings.HasPrefix(getenv("TERM"), "foot"):
		return "\x1b]777;notify;Synq;" + body + st
	}
	return ""
}

// desktopNotifyCmd raises a desktop notification: through the terminal when
// it supports that, otherwise through the platform's notifier (notify-send
// on Linux, osascript on macOS). Where there is neither it does nothing.
// body never contains message text; see the file comment.
func desktopNotifyCmd(body string) tea.Cmd {
	if seq := desktopSequence(body, os.Getenv); seq != "" {
		return tea.Raw(seq)
	}
	return func() tea.Msg {
		body := sanitize.OneLine(body)
		var c *exec.Cmd
		switch runtime.GOOS {
		case "linux", "freebsd", "openbsd", "netbsd":
			path, err := exec.LookPath("notify-send")
			if err != nil {
				return nil
			}
			c = exec.Command(path, "-a", "Synq", "-i", "synq", "--", "Synq", body)
		case "darwin":
			path, err := exec.LookPath("osascript")
			if err != nil {
				return nil
			}
			esc := strings.NewReplacer("\\", "\\\\", "\"", "\\\"").Replace(body)
			c = exec.Command(path, "-e", `display notification "`+esc+`" with title "Synq"`)
		default:
			return nil
		}
		if c.Start() == nil {
			go func() { _ = c.Wait() }()
		}
		return nil
	}
}

// --- the :notify command ---

const notifyUsage = "Usage: :notify [title|bell|desktop|sender on|off | mute [name] | unmute [name]]"

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// notifyStatus describes the current settings.
func (m Model) notifyStatus() string {
	n := m.notify
	s := fmt.Sprintf("Alerts: title %s, bell %s, desktop %s, sender name %s.",
		onOff(n.title), onOff(n.bell), onOff(n.desktop), onOff(n.sender))
	if names := m.mutedNames(); len(names) > 0 {
		s += " Muted: " + strings.Join(names, ", ") + "."
	}
	return s
}

func (m Model) mutedNames() []string {
	var names []string
	for k := range m.notify.muted {
		names = append(names, m.chatContactLabel(k))
	}
	sort.Strings(names)
	return names
}

// notifyCommand implements :notify.
func (m *Model) notifyCommand(args []string) string {
	if len(args) == 0 {
		return m.notifyStatus() + " :notify <title|bell|desktop|sender> on|off, :notify mute [name]."
	}
	switch strings.ToLower(args[0]) {
	case "mute", "unmute":
		return m.notifyMute(strings.ToLower(args[0]) == "mute", args[1:])
	case "title", "bell", "desktop", "sender":
		if len(args) != 2 {
			return notifyUsage
		}
		var on bool
		switch strings.ToLower(args[1]) {
		case "on":
			on = true
		case "off":
		default:
			return notifyUsage
		}
		key := ""
		switch strings.ToLower(args[0]) {
		case "title":
			m.notify.title, key = on, db.PrefNotifyTitle
			if !on {
				m.notify.attention = 0
			}
		case "bell":
			m.notify.bell, key = on, db.PrefNotifyBell
		case "desktop":
			m.notify.desktop, key = on, db.PrefNotifyDesktop
		case "sender":
			m.notify.sender, key = on, db.PrefNotifySender
		}
		msg := fmt.Sprintf("Alert %s is %s.", strings.ToLower(args[0]), onOff(on))
		if on && strings.EqualFold(args[0], "sender") {
			msg += " Notifications will name who wrote; they never include message text."
		}
		if m.store != nil {
			if err := m.store.SavePreference(key, onOff(on)); err != nil {
				msg += fmt.Sprintf(" It will not survive a restart: %v", err)
			}
		}
		return msg
	}
	return notifyUsage
}

// notifyMute mutes or unmutes one thread: the one named, else the one open
// on the Chat tab.
func (m *Model) notifyMute(mute bool, args []string) string {
	var key chat.ContactKey
	switch {
	case len(args) > 0:
		name := strings.Join(args, " ")
		for k, info := range m.chatContacts {
			if strings.EqualFold(info.Username, name) {
				key = k
				break
			}
		}
		if key == "" {
			e, errText := m.findRoom(name)
			if e == nil {
				return fmt.Sprintf("No chat with %q to %s. %s", name, onOffVerb(mute), errText)
			}
			key = roomThread(e.r.ID)
		}
	case m.activeTab == tabChat && m.chatActive != "":
		key = m.chatActive
	default:
		return "Open a chat first, or give a name: :notify " + onOffVerb(mute) + " <name>"
	}
	if mute {
		if m.notify.muted == nil {
			m.notify.muted = make(map[chat.ContactKey]bool)
		}
		m.notify.muted[key] = true
	} else {
		delete(m.notify.muted, key)
	}
	msg := fmt.Sprintf("%s %sd. Unread counts still update.", m.chatContactLabel(key), onOffVerb(mute))
	if m.store != nil {
		keys := make([]string, 0, len(m.notify.muted))
		for k := range m.notify.muted {
			keys = append(keys, string(k))
		}
		sort.Strings(keys)
		data, _ := json.Marshal(keys)
		if err := m.store.SavePreference(db.PrefNotifyMuted, string(data)); err != nil {
			msg += fmt.Sprintf(" It will not survive a restart: %v", err)
		}
	}
	return msg
}

func onOffVerb(mute bool) string {
	if mute {
		return "mute"
	}
	return "unmute"
}
