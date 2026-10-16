package app

import (
	"context"
	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/HomieB-tt/synq/internal/api"
	"github.com/HomieB-tt/synq/internal/chat"
	"github.com/HomieB-tt/synq/internal/ws"
)

// Receiving from someone you have not looked up.
//
// Every message frame names its sender by user id, and everything needed
// to read it - their keys, the thread - comes from a `:chat <username>`
// lookup. Until this file existed, a frame from an id that had not been
// looked up this launch was dropped without a word, so a message only
// arrived if the recipient had happened to run `:chat <sender>` first, and
// the sender was never told. Chat history and lookups are session-only,
// so that was true after every restart.
//
// The server only relays between active nodes, and this client already
// holds its own list of them (m.nodes). So a frame from an active node is
// held back, the lookup `:chat` would have done runs in the background,
// and the held frames are replayed once it succeeds. A frame from anyone
// else is still dropped silently: it is the same spam vector it always
// was, and nothing here widens it.

const (
	// maxDeferredPerSender bounds what is held for one sender while the
	// lookup is in flight. A real exchange is a handshake and a message or
	// two; a sender who sends more than this before the lookup returns
	// loses the surplus and, as they resend handshakes anyway, recovers.
	maxDeferredPerSender = 16
	// maxDeferredSenders bounds how many lookups can be in flight at once.
	maxDeferredSenders = 16
)

// activeNodeUsername is the username of the active node with this user
// id, from the relationships already loaded. A pending, blocked or
// unknown id is not an active node.
func (m *Model) activeNodeUsername(id string) (string, bool) {
	if id == "" {
		return "", false
	}
	for _, n := range m.nodes {
		if n.Status != api.NodeStatusActive {
			continue
		}
		var name string
		switch id {
		case n.RequesterID:
			name = n.RequesterUsername
		case n.TargetID:
			name = n.TargetUsername
		default:
			continue
		}
		if name != "" && name != m.username {
			return name, true
		}
	}
	return "", false
}

// deferFromActiveNode holds a frame from a sender this launch has not
// looked up, and starts the lookup if one is not already running for
// them. It returns nil - the frame is dropped - when the sender is not an
// active node, when there is no session to look anyone up with, or when
// the bounds above are reached.
func (m *Model) deferFromActiveNode(ev ws.Event) tea.Cmd {
	username, ok := m.activeNodeUsername(ev.From)
	if !ok || m.apiClient == nil || m.accessToken == "" {
		return nil
	}
	if m.deferredFrames == nil {
		m.deferredFrames = make(map[string][]ws.Event)
		m.resolvingSenders = make(map[string]bool)
	}
	queue := m.deferredFrames[ev.From]
	if len(queue) == 0 && len(m.deferredFrames) >= maxDeferredSenders {
		return nil
	}
	if len(queue) >= maxDeferredPerSender {
		return nil
	}
	m.deferredFrames[ev.From] = append(queue, ev)

	if m.resolvingSenders[ev.From] {
		return nil
	}
	m.resolvingSenders[ev.From] = true
	return resolveSenderCmd(m.apiClient, m.accessToken, username, ev.From)
}

// resolveSenderCmd is resolveChatContactCmd for a lookup nobody typed.
func resolveSenderCmd(apiClient *api.Client, accessToken, username, senderID string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), apiHTTPTimeout)
		defer cancel()
		keys, err := apiClient.GetPublicKeys(ctx, accessToken, username)
		return chatResolveResultMsg{username: username, keys: keys, err: err, background: true, senderID: senderID}
	}
}

// handleSenderResolved finishes a background lookup: the same checks as
// `:chat` (see acceptResolved), then the held frames are replayed in the
// order they arrived. Unlike `:chat` it never opens the thread or changes
// tab - the user is in the middle of something else.
func (m Model) handleSenderResolved(msg chatResolveResultMsg) (tea.Model, tea.Cmd) {
	frames := m.deferredFrames[msg.senderID]
	delete(m.deferredFrames, msg.senderID)
	delete(m.resolvingSenders, msg.senderID)

	if msg.err != nil || msg.keys == nil || len(frames) == 0 {
		// Nothing to tell the user: they never asked for this lookup,
		// and the sender's next handshake will trigger another try.
		return m, nil
	}
	// The lookup was by the username the node list gave us; it has to
	// land on the id the frames came from, or the server is serving one
	// user's keys under another's name.
	if msg.keys.UserID != msg.senderID {
		return m, nil
	}

	contact, outcome, fail := m.acceptResolved(msg)
	if fail != "" {
		m.commandMsg = fail
		return m, nil
	}
	if outcome == pinChanged {
		// Same hard warning as `:chat`, but nothing is opened or
		// replayed: the user has not chosen to trust the new key.
		m.commandMsg = fmt.Sprintf("%s sent you a message, but their key CHANGED since you pinned it. "+
			"Run :chat %s to review it - nothing was opened.", msg.username, msg.username)
		return m, nil
	}
	if m.wsClient == nil {
		return m, nil
	}

	var cmds []tea.Cmd
	for _, ev := range frames {
		if c := m.handleIncomingPayload(contact, ev.Payload); c != nil {
			cmds = append(cmds, c)
		}
	}
	return m, tea.Batch(cmds...)
}

// --- unread ---

// viewingThread is true while contact's thread is the one on screen.
func (m Model) viewingThread(contact chat.ContactKey) bool {
	return contact != "" && m.activeTab == tabChat && m.chatActive == contact
}

// noteUnread records one incoming message in contact's thread. If the
// thread is the one being looked at nothing is recorded; otherwise the
// thread's unread count goes up (shown on the Chat tab and in the thread
// list), and, when notify is set, the bottom bar says who wrote - a
// message arriving while you are on another tab used to give no sign.
func (m *Model) noteUnread(contact chat.ContactKey, notify bool) {
	if m.viewingThread(contact) {
		return
	}
	if m.unread == nil {
		m.unread = make(map[chat.ContactKey]int)
	}
	m.unread[contact]++
	if notify {
		m.commandMsg = fmt.Sprintf("New message from %s - press 3 for Chat", m.chatContactLabel(contact))
	}
}

// markViewedRead clears the unread count of the thread now on screen. It
// runs after every update, so every way of arriving at a thread - the
// list, :chat, a pinned-key prompt - clears it without each having to
// remember to.
func (m *Model) markViewedRead() {
	if m.viewingThread(m.chatActive) {
		delete(m.unread, m.chatActive)
	}
}

func (m Model) unreadIn(contact chat.ContactKey) int { return m.unread[contact] }

func (m Model) unreadTotal() int {
	n := 0
	for _, c := range m.unread {
		n += c
	}
	return n
}
