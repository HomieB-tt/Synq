// Package chat implements the ephemeral, session-scoped message cache
// described in DESIGN.md section 4: while Synq is running, an open
// thread's history survives switching tabs or a network drop, but
// nothing here is ever written to disk, and quitting purges it all -
// there is deliberately no persistence, no soft-delete, and no
// "restore last session".
//
// This package holds already-decrypted plaintext only. Sealing and
// opening message content is internal/ws's job (SealMessage/
// OpenMessage, keyed by internal/ws.DeriveSessionKey's output) - by
// the time a message reaches Store.Append, it has already been
// through that step.
package chat

import (
	"encoding/hex"
	"sync"
	"time"
)

// Message is one chat message, already decrypted (if it arrived over
// the wire) or not yet encrypted (if it's about to be sent).
//
// Body is []byte rather than string specifically so Purge can zero the
// actual backing bytes in place - converting a string to []byte always
// copies in Go, which would leave the original string's memory
// (holding the real plaintext) untouched and defeat the point. See
// zero's doc comment for how much this guarantees in practice (not
// much, but it costs nothing).
type Message struct {
	Body     []byte
	At       time.Time
	Outgoing bool // true if this client sent it, false if a contact did

	// The fields below are used by room threads only (see internal/room);
	// a 1:1 message leaves them zero.

	// ID is the author's id for the message, which replies refer to.
	ID string
	// Author is the sender's username in a room. Empty in a 1:1 chat,
	// where the thread itself says who the other person is.
	Author string
	// Quote is a short excerpt of the message this one replies to, with
	// its author ("bob: lunch?"), or empty.
	Quote string
	// Seq is the room's sequence number for the message, 0 while it is
	// not known yet (a message you just sent, before the server's ack).
	Seq int64
	// System marks a line the app wrote itself ("alice joined"), not a
	// person.
	System bool
}

// ContactKey identifies a chat thread by the contact's static X25519
// public key - hex-encoded, matching how this client already displays
// a public key elsewhere (see Model.renderProfile).
type ContactKey string

// NewContactKey derives the ContactKey for a contact's raw static
// public key.
func NewContactKey(pub *[32]byte) ContactKey {
	return ContactKey(hex.EncodeToString(pub[:]))
}

// Store is an in-memory, per-contact chat history for the current
// process's lifetime only. The zero value is not ready to use - call
// NewStore. Safe for concurrent use.
type Store struct {
	mu      sync.RWMutex
	threads map[ContactKey][]Message
	order   []ContactKey // insertion order of each thread's first message, for a stable thread list
}

// NewStore creates an empty Store.
func NewStore() *Store {
	return &Store{threads: make(map[ContactKey][]Message)}
}

// Append adds msg to contact's thread, creating the thread (and adding
// it to Threads' ordering) if this is its first message.
func (s *Store) Append(contact ContactKey, msg Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.threads[contact]; !ok {
		s.order = append(s.order, contact)
	}
	s.threads[contact] = append(s.threads[contact], msg)
}

// AppendOrdered adds a room message, placing it by sequence number so
// everyone sees the room's one agreed order even if frames arrive out of
// order. A message with no Seq yet (your own, awaiting its ack) goes at
// the end, and incoming ones slot in before such unacknowledged messages
// of yours. Without a Seq it is the same as Append.
func (s *Store) AppendOrdered(contact ContactKey, msg Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.threads[contact]; !ok {
		s.order = append(s.order, contact)
	}
	s.threads[contact] = insertBySeq(s.threads[contact], msg)
}

// insertBySeq puts msg where its Seq belongs: after every message with a
// lower Seq and any system line, before any message with a higher one.
// Your own unacknowledged messages (no Seq yet, sent by you) don't count
// as "lower" - they are newer than anything that has a number.
func insertBySeq(msgs []Message, msg Message) []Message {
	idx := len(msgs)
	if msg.Seq > 0 {
		for idx > 0 {
			prev := msgs[idx-1]
			unacked := prev.Seq == 0 && prev.Outgoing && !prev.System
			if unacked || prev.Seq > msg.Seq {
				idx--
				continue
			}
			break
		}
	}
	msgs = append(msgs, Message{})
	copy(msgs[idx+1:], msgs[idx:])
	msgs[idx] = msg
	return msgs
}

// SetSeq records the sequence number the server gave the message with the
// given id (the room_ack for a message you sent) and moves it into place.
// It reports whether such a message was found.
func (s *Store) SetSeq(contact ContactKey, id string, seq int64) bool {
	if id == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	msgs := s.threads[contact]
	for i := range msgs {
		if msgs[i].ID != id {
			continue
		}
		m := msgs[i]
		m.Seq = seq
		rest := append(msgs[:i:i], msgs[i+1:]...)
		s.threads[contact] = insertBySeq(rest, m)
		return true
	}
	return false
}

// FindByID returns a copy of the message with the given id.
func (s *Store) FindByID(contact ContactKey, id string) (Message, bool) {
	if id == "" {
		return Message{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, m := range s.threads[contact] {
		if m.ID == id {
			m.Body = append([]byte(nil), m.Body...)
			return m, true
		}
	}
	return Message{}, false
}

// Delete wipes one thread (zeroing its bytes) and removes it from the
// thread list - what ending or leaving a room does.
func (s *Store) Delete(contact ContactKey) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.threads[contact] {
		zero(s.threads[contact][i].Body)
	}
	delete(s.threads, contact)
	for i, c := range s.order {
		if c == contact {
			s.order = append(s.order[:i:i], s.order[i+1:]...)
			break
		}
	}
}

// Messages returns a copy of contact's message history, oldest first,
// or nil if no thread exists yet for contact. This is a deep copy, not
// just of the outer slice: each Message's Body is also copied to its
// own backing array, not just the same slice header - otherwise a
// caller mutating a returned Body (or Purge zeroing the real one)
// would reach back into Store's own state through the shared
// underlying array. See TestStoreMessagesReturnsACopy.
func (s *Store) Messages(contact ContactKey) []Message {
	s.mu.RLock()
	defer s.mu.RUnlock()
	msgs := s.threads[contact]
	if msgs == nil {
		return nil
	}
	out := make([]Message, len(msgs))
	for i, m := range msgs {
		out[i] = m
		out[i].Body = append([]byte(nil), m.Body...)
	}
	return out
}

// Threads returns every contact with at least one message, ordered by
// when each thread first received a message (oldest thread first).
func (s *Store) Threads() []ContactKey {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ContactKey, len(s.order))
	copy(out, s.order)
	return out
}

// Len returns the number of messages in contact's thread, or 0 if
// no thread exists yet.
func (s *Store) Len(contact ContactKey) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.threads[contact])
}

// Slice returns a copy of the messages in contact's thread in the
// half-open range [start, end), clamped to the thread's actual length.
// Like Messages, each Body is deep-copied so callers cannot reach back
// into Store's state through the returned slice.
func (s *Store) Slice(contact ContactKey, start, end int) []Message {
	s.mu.RLock()
	defer s.mu.RUnlock()
	msgs := s.threads[contact]
	if start < 0 {
		start = 0
	}
	if end > len(msgs) {
		end = len(msgs)
	}
	if start >= end {
		return nil
	}
	src := msgs[start:end]
	out := make([]Message, len(src))
	for i, m := range src {
		out[i] = m
		out[i].Body = append([]byte(nil), m.Body...)
	}
	return out
}

// HasThread reports whether contact has an existing thread, so a
// caller (e.g. the `:chat <pubkey>` command) can tell "opening an
// existing conversation" apart from "starting a new one" without
// needing Messages' length just to check for zero.
func (s *Store) HasThread(contact ContactKey) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.threads[contact]
	return ok
}

// Purge wipes every thread and best-effort zeroes each message's
// bytes. Call this on quit (DESIGN.md section 4: "Quitting Synq
// purges everything"). Relying on process exit alone to free this
// memory would already satisfy that guarantee, since nothing here is
// ever persisted - but purging explicitly, and zeroing while doing it,
// narrows the window these bytes are sitting in memory before the
// process actually exits, the same reasoning internal/crypto's own
// zero helper documents.
func (s *Store) Purge() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for contact, msgs := range s.threads {
		for i := range msgs {
			zero(msgs[i].Body)
		}
		delete(s.threads, contact)
	}
	s.order = nil
}

// zero overwrites b with zeros. Best-effort, like internal/crypto's
// identical helper (duplicated rather than exported and imported
// across packages for one four-line function): Go's garbage collector
// and compiler optimizations mean this isn't a hard guarantee against
// a copy existing elsewhere, but it costs nothing and narrows the
// window regardless.
func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
