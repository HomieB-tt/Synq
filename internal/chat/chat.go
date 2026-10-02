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
