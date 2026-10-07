package app

import (
	"encoding/hex"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/HomieB-tt/synq/internal/chat"
	"github.com/HomieB-tt/synq/internal/ws"
)

// This file is the wire half of chat (DESIGN.md §3): the per-launch
// X3DH-lite handshake carried inside ordinary relayed frames, and the
// sealing/opening of message bodies under the session key it derives.
// Everything here assumes a contact already exists - the thread was
// opened by `:chat <username>`, which is also where this client
// learned the contact's static key (it's the thread's own ContactKey)
// and their server-side user id (chatContacts, from stage 2).

// chatSession is one contact's handshake state for this launch.
//
// Zero value means "no handshake started yet", which is a valid state
// to be in (a thread that exists but has never exchanged a frame), so
// this is a pointer per contact only so callers can mutate it in
// place without re-storing it.
type chatSession struct {
	// peerEphemeral is the contact's per-launch X25519 public key, as
	// last received in their handshake frame. nil until the first one
	// arrives.
	peerEphemeral *[32]byte

	// peerSeen records that at least one handshake frame has arrived
	// from this contact. It gates *replying* to one (see
	// handleHandshake): without that gate, two clients each answering
	// every handshake frame with their own would answer each other
	// forever.
	peerSeen bool

	// key is the established session key, set once both ephemeral
	// keys are on hand. nil means messages to this contact can't be
	// sealed yet, and pending holds what was composed meanwhile.
	key *[32]byte

	// pending holds bodies the user composed before key existed,
	// oldest first, to go out the moment the handshake completes.
	// Nothing here is encrypted yet - it's exactly the window DESIGN.md
	// §3's forward secrecy is *not* providing, kept as short as one
	// round trip and held in memory only.
	pending [][]byte
}

// sendMessage hands one body the user just typed to contact: sealed
// and sent if a session key exists, otherwise parked in the session's
// pending list to go out the moment one does. Either way, while no key
// exists it (re)sends our handshake frame - the contact may never have
// seen the last one, since synq-server relays to a peer who is
// currently offline by dropping the frame entirely, and there is no
// server-side queue to catch that (DESIGN.md §4: the server holds no
// message state at all).
func (m *Model) sendMessage(contact chat.ContactKey, body []byte) tea.Cmd {
	s := m.sessionFor(contact)
	if s.key == nil {
		s.pending = append(s.pending, body)
		return m.handshakeCmd(contact, s)
	}
	// Anything still pending is older than this body - a send that
	// failed after the local echo was written, say - so it goes first
	// to keep the thread in the order the user wrote it.
	return tea.Batch(m.flushPending(contact, s), m.sealAndSendCmd(contact, s, body))
}

// handshakeCmd queues a frame carrying our own ephemeral public key.
// Our key is per-launch, so resending it is idempotent - the contact
// simply learns (or relearns) the same value.
func (m *Model) handshakeCmd(contact chat.ContactKey, s *chatSession) tea.Cmd {
	if m.wsClient == nil || m.wsClient.Ephemeral == nil {
		// No connection and no ephemeral key to send: the pending
		// body stays pending, which is the honest outcome - nothing
		// has been lost, it just hasn't gone out yet.
		return nil
	}
	payload, err := ws.EncodeHandshake(m.wsClient.Ephemeral.Public)
	if err != nil {
		m.commandMsg = fmt.Sprintf("Couldn't build a handshake frame: %v", err)
		return nil
	}
	return m.sendFrameCmd(contact, payload, nil)
}

// sealAndSendCmd encrypts body under the contact's session key and
// queues the frame. A sealing failure is reported rather than retried:
// with a valid key it shouldn't happen, and the local echo has already
// been appended either way (see updateChatCompose), so the user sees
// what they wrote even if the wire disagreed.
func (m *Model) sealAndSendCmd(contact chat.ContactKey, s *chatSession, body []byte) tea.Cmd {
	sealed, err := ws.SealMessage(s.key, body)
	if err != nil {
		m.commandMsg = fmt.Sprintf("Couldn't encrypt that message: %v", err)
		return nil
	}
	return m.sendFrameCmd(contact, ws.EncodeSealed(sealed), body)
}

// sendFrameCmd queues one already-built payload for contact. The work
// happens in the returned command rather than inline: Client.Send
// blocks when its outgoing queue is full (only possible while
// disconnected for a long time), and blocking Update would freeze the
// whole TUI on exactly the condition - a dead connection - where the
// user most wants to keep typing.
//
// requeue is the message body this frame carries, if it carries one -
// passed back out through wsSendErrorMsg so a failed send can put the
// body where it will still be transmitted later instead of leaving it
// only as a local echo. Handshake frames pass nil: they carry no
// message, and the next send attempt resends one anyway.
func (m *Model) sendFrameCmd(contact chat.ContactKey, payload, requeue []byte) tea.Cmd {
	to, ok := m.recipientUserID(contact)
	if !ok {
		m.commandMsg = fmt.Sprintf("No user id on file for %s - run :chat %s again to look them up.", m.chatContactLabel(contact), m.chatContactLabel(contact))
		return nil
	}
	client := m.wsClient
	if client == nil {
		m.commandMsg = "Not connected to synq-server - that wasn't sent."
		return nil
	}
	frame, err := ws.EncodeSend(to, payload)
	if err != nil {
		m.commandMsg = fmt.Sprintf("Couldn't build a frame: %v", err)
		return nil
	}
	return func() tea.Msg {
		if err := client.Send(frame); err != nil {
			return wsSendErrorMsg{contact: contact, body: requeue, err: err}
		}
		return nil
	}
}

// handleIncomingPayload is the receive side: one relayed message frame
// from contact, split into handshake or sealed message. Returns a
// command when answering is needed (our handshake reply, flushed
// pending bodies).
func (m *Model) handleIncomingPayload(contact chat.ContactKey, payload []byte) tea.Cmd {
	kind, body, err := ws.DecodePayload(payload)
	if err != nil {
		m.commandMsg = fmt.Sprintf("Bad frame from %s: %v", m.chatContactLabel(contact), err)
		return nil
	}
	switch kind {
	case ws.PayloadKindHandshake:
		return m.handleHandshake(contact, body)
	case ws.PayloadKindSealed:
		return m.handleSealed(contact, body)
	default:
		// Unreachable: DecodePayload only returns the two kinds it
		// knows about, and rejects anything else.
		return nil
	}
}

// handleHandshake records the contact's ephemeral key, derives (or
// re-derives) the session key from it, and answers with our own key
// when the contact plausibly doesn't have it yet.
//
// Answering is gated on "first frame received, or a key different from
// the one recorded last time", never on "we have a key": those are the
// two cases where the contact is missing our ephemeral key - either
// they've never had it, or they relaunched and generated a new key of
// their own, invalidating the one this client derived from theirs.
// Frames repeating an ephemeral key we already know get no answer,
// which is what stops two clients from answering each other forever.
//
// Re-deriving on a changed key also matches what a relaunched contact
// does to us: their messages are sealed under a key built from their
// new ephemeral, so continuing to use the old one would silently fail
// every open from here on.
func (m *Model) handleHandshake(contact chat.ContactKey, body []byte) tea.Cmd {
	ephemeral, err := ws.ParseHandshake(body)
	if err != nil {
		m.commandMsg = fmt.Sprintf("Bad handshake from %s: %v", m.chatContactLabel(contact), err)
		return nil
	}

	s := m.sessionFor(contact)
	changed := !s.peerSeen || !sameKey(s.peerEphemeral, ephemeral)
	s.peerEphemeral = ephemeral
	s.peerSeen = true

	key, err := m.deriveSessionKey(contact, s)
	if err != nil {
		// Nothing to answer with and nothing to open yet: say why
		// (it's a real misconfiguration - this client's own identity
		// or ephemeral key is missing), and leave the state ready for
		// the next frame.
		m.commandMsg = fmt.Sprintf("Handshake with %s couldn't be completed: %v", m.chatContactLabel(contact), err)
		return nil
	}
	s.key = key

	var reply tea.Cmd
	if changed {
		reply = m.handshakeCmd(contact, s)
	}
	return tea.Batch(reply, m.flushPending(contact, s))
}

// handleSealed opens a message body under the contact's session key
// and appends it to the thread as received.
func (m *Model) handleSealed(contact chat.ContactKey, body []byte) tea.Cmd {
	s := m.chatSessions[contact]
	if s == nil || s.key == nil {
		// The contact is sealing to a key this client never finished
		// deriving - their handshake frame was lost, or arrived before
		// this thread existed. There is nothing to retry on our side;
		// the next handshake frame from them fixes it.
		m.commandMsg = fmt.Sprintf("Dropped a message from %s: the handshake hasn't completed yet.", m.chatContactLabel(contact))
		return nil
	}

	plaintext, err := ws.OpenMessage(s.key, body)
	if err != nil {
		// Authentication failing means the bytes were sealed under a
		// different key than the one in hand - in practice, the
		// contact relaunched since we last derived (new ephemeral) or
		// something tampered with the frame. Discarding our state
		// makes the next handshake re-establish from scratch instead
		// of wedging this thread on a key that no longer works.
		s.key = nil
		s.peerEphemeral = nil
		s.peerSeen = false
		m.commandMsg = fmt.Sprintf("Couldn't open a message from %s - resetting that handshake to try again.", m.chatContactLabel(contact))
		return nil
	}

	m.chatStore.Append(contact, chat.Message{
		Body:     plaintext,
		At:       time.Now(),
		Outgoing: false,
	})
	return nil
}

// flushPending seals and queues every body composed before the session
// key existed, oldest first. Called the moment a key is derived, which
// is also the moment the contact can finally open them.
func (m *Model) flushPending(contact chat.ContactKey, s *chatSession) tea.Cmd {
	if len(s.pending) == 0 {
		return nil
	}
	pending := s.pending
	s.pending = nil

	cmds := make([]tea.Cmd, 0, len(pending))
	for _, body := range pending {
		if cmd := m.sealAndSendCmd(contact, s, body); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	return tea.Batch(cmds...)
}

// deriveSessionKey computes this contact's session key from the four
// pieces X3DH-lite needs (DESIGN.md §3): our static and ephemeral
// keys, their static key - which is the thread's own ContactKey, since
// that's the hex of the box public key `:chat` looked up - and their
// ephemeral key as received.
func (m *Model) deriveSessionKey(contact chat.ContactKey, s *chatSession) (*[32]byte, error) {
	if m.identity == nil {
		return nil, fmt.Errorf("no identity loaded")
	}
	if m.wsClient == nil || m.wsClient.Ephemeral == nil {
		return nil, fmt.Errorf("no ephemeral key for this launch")
	}
	if s.peerEphemeral == nil {
		return nil, fmt.Errorf("no ephemeral key from them yet")
	}
	remoteStatic, err := staticKeyOf(contact)
	if err != nil {
		return nil, err
	}
	return ws.DeriveSessionKey(m.identity, m.wsClient.Ephemeral, remoteStatic, s.peerEphemeral)
}

// staticKeyOf recovers a contact's static X25519 public key from the
// thread key it was opened with - chat.NewContactKey is hex of exactly
// that key, which is what makes the ContactKey sufficient to derive
// with, with no second lookup and no key stored twice.
func staticKeyOf(contact chat.ContactKey) (*[32]byte, error) {
	raw, err := hex.DecodeString(string(contact))
	if err != nil {
		return nil, fmt.Errorf("contact key isn't hex: %w", err)
	}
	if len(raw) != 32 {
		return nil, fmt.Errorf("contact key is %d bytes, want 32", len(raw))
	}
	var key [32]byte
	copy(key[:], raw)
	return &key, nil
}

// sessionFor returns contact's handshake state, creating it the first
// time anything is said in either direction.
func (m *Model) sessionFor(contact chat.ContactKey) *chatSession {
	if m.chatSessions == nil {
		m.chatSessions = make(map[chat.ContactKey]*chatSession)
	}
	s := m.chatSessions[contact]
	if s == nil {
		s = &chatSession{}
		m.chatSessions[contact] = s
	}
	return s
}

// hasSessionKey reports whether contact's handshake has completed -
// what decides, in order, whether the next composed message seals
// immediately or waits (sendMessage), and which way the composer's
// hint is worded (composeHint).
func (m Model) hasSessionKey(contact chat.ContactKey) bool {
	s := m.chatSessions[contact]
	return s != nil && s.key != nil
}

func sameKey(a, b *[32]byte) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
