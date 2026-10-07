package app

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/HomieB-tt/synq/internal/api"
	"github.com/HomieB-tt/synq/internal/chat"
	"github.com/HomieB-tt/synq/internal/crypto"
)

// newResolveModel returns a Model ready for handleChatResolveResult,
// with the real key store that handler needs to run its TOFU pin check
// (a lookup with no store fails closed rather than opening the thread).
func newResolveModel(t *testing.T) Model {
	t.Helper()
	return Model{store: newTestKeyStore(t), chatStore: chat.NewStore()}
}

// mustBoxKey returns a fresh X25519 static public key - any valid one
// works, since these tests never talk to a real contact.
func mustBoxKey(t *testing.T) *[32]byte {
	t.Helper()
	id, err := crypto.GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	return id.BoxPublic
}

// boxKeyHex is mustBoxKey in the form GetPublicKeys returns it.
func boxKeyHex(t *testing.T) string {
	t.Helper()
	return hex.EncodeToString(mustBoxKey(t)[:])
}

// The lookup is where a contact's server-side user id enters this
// client at all - nothing else knows it - so every wire direction
// depends on it being recorded here: EncodeSend addresses with it, and
// inbound frames can only be routed back to a thread through it.
func TestChatResolveStoresTheUserIDForBothDirections(t *testing.T) {
	m := newResolveModel(t)
	box := boxKeyHex(t)

	next, cmd := m.handleChatResolveResult(chatResolveResultMsg{
		username: "alice",
		keys:     &api.PublicKeys{UserID: "user-uuid-1", Username: "alice", BoxPubKey: box},
	})
	got := next.(Model)

	if cmd != nil {
		t.Errorf("handleChatResolveResult returned a command, want none: %v", cmd())
	}
	if got.activeTab != tabChat || got.chatActive == "" {
		t.Fatalf("thread not opened (tab=%v contact=%q)", got.activeTab, got.chatActive)
	}

	contact := got.chatActive
	if info, ok := got.chatContacts[contact]; !ok {
		t.Fatal("contact recorded no metadata")
	} else {
		if info.Username != "alice" {
			t.Errorf("Username = %q, want %q", info.Username, "alice")
		}
		if info.UserID != "user-uuid-1" {
			t.Errorf("UserID = %q, want %q", info.UserID, "user-uuid-1")
		}
	}

	// Send side: what EncodeSend has to be addressed to.
	if id, ok := got.recipientUserID(contact); !ok || id != "user-uuid-1" {
		t.Errorf("recipientUserID = (%q, %v), want (user-uuid-1, true)", id, ok)
	}
	// Receive side: what an inbound frame's From resolves to.
	if routed, ok := got.contactForUserID("user-uuid-1"); !ok || routed != contact {
		t.Errorf("contactForUserID = (%q, %v), want the thread %q", routed, ok, contact)
	}

	// Display is unchanged by any of this - the thread list still shows
	// the username, not an id or a raw key.
	if label := got.chatContactLabel(contact); label != "alice" {
		t.Errorf("chatContactLabel = %q, want %q", label, "alice")
	}
}

// An id nobody ever resolved can't name a thread, and a thread that
// somehow has no id can't be sent to - both have to say "no" rather
// than invent a mapping (an empty From would otherwise resolve to an
// empty contact key).
func TestContactLookupsAreNegativeWhenUnmapped(t *testing.T) {
	m := newResolveModel(t)

	if contact, ok := m.contactForUserID("someone-else"); ok {
		t.Errorf("contactForUserID resolved an unknown id to %q", contact)
	}
	if contact, ok := m.contactForUserID(""); ok {
		t.Errorf("contactForUserID resolved an empty id to %q", contact)
	}
	if id, ok := m.recipientUserID("deadbeef"); ok {
		t.Errorf("recipientUserID = (%q, true) for a contact with no lookup", id)
	}
}

// Re-resolving the same contact under a new user id (the account
// re-registered its key) must move the reverse index rather than leave
// the old id pointing at the thread - otherwise an inbound frame from
// either id resolves, to contradictory results.
func TestRememberContactMovesTheReverseIndexOnReResolve(t *testing.T) {
	m := newResolveModel(t)
	contact := chat.NewContactKey(mustBoxKey(t))

	m.rememberContact(contact, "alice", "id-v1")
	m.rememberContact(contact, "alice", "id-v2")

	if _, ok := m.contactForUserID("id-v1"); ok {
		t.Error("stale user id still resolves to the contact")
	}
	if routed, ok := m.contactForUserID("id-v2"); !ok || routed != contact {
		t.Errorf("contactForUserID(id-v2) = (%q, %v), want the contact", routed, ok)
	}
	if id, ok := m.recipientUserID(contact); !ok || id != "id-v2" {
		t.Errorf("recipientUserID = (%q, %v), want the new id", id, ok)
	}
}

// The same re-registration from the other direction: a *different*
// contact key arriving with an id this store already had. The newest
// lookup wins the reverse index, since that's the key the account is
// reachable at now.
func TestRememberContactLetsANewerContactClaimAnUserID(t *testing.T) {
	m := newResolveModel(t)
	old := chat.NewContactKey(mustBoxKey(t))
	renewed := chat.NewContactKey(mustBoxKey(t))

	m.rememberContact(old, "alice", "shared-id")
	m.rememberContact(renewed, "alice", "shared-id")

	if routed, ok := m.contactForUserID("shared-id"); !ok || routed != renewed {
		t.Errorf("contactForUserID = (%q, %v), want the newer contact %q", routed, ok, renewed)
	}
}

// A lookup response without a user id isn't a thread worth opening -
// there'd be no way to address a single frame to it.
func TestChatResolveWithoutUserIDOpensNothing(t *testing.T) {
	m := newResolveModel(t)

	next, _ := m.handleChatResolveResult(chatResolveResultMsg{
		username: "alice",
		keys:     &api.PublicKeys{Username: "alice", BoxPubKey: boxKeyHex(t)},
	})
	got := next.(Model)

	if got.chatActive != "" || got.activeTab == tabChat {
		t.Error("opened a thread for a contact with no user id")
	}
	if !strings.Contains(got.commandMsg, "user id") {
		t.Errorf("commandMsg = %q, want it to name the missing user id", got.commandMsg)
	}
	if len(got.chatContacts) != 0 || len(got.chatContactIDs) != 0 {
		t.Error("recorded metadata for a contact that can't be messaged")
	}
}
