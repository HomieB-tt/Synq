package app

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HomieB-tt/synq/internal/api"
	"github.com/HomieB-tt/synq/internal/chat"
	"github.com/HomieB-tt/synq/internal/crypto"
	"github.com/HomieB-tt/synq/internal/db"
)

// newTestKeyStore opens a throwaway key store on disk, so tests that
// exercise persistence-backed behavior (the TOFU pin flow) do it the
// way production does rather than against a nil store.
func newTestKeyStore(t *testing.T) *db.KeyStore {
	t.Helper()
	ks, err := db.OpenKeyStore(filepath.Join(t.TempDir(), "synq-test.db"))
	if err != nil {
		t.Fatalf("OpenKeyStore: %v", err)
	}
	t.Cleanup(func() { ks.Close() })
	return ks
}

// newPinModel returns a Model with an identity and a real key store,
// so the TOFU pin flow under test can persist and re-read pins the way
// it does in production.
func newPinModel(t *testing.T) Model {
	t.Helper()
	id, err := crypto.GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	return Model{identity: id, store: newTestKeyStore(t), chatStore: chat.NewStore()}
}

// randomKeyHex returns n random bytes as lowercase hex - good enough
// for keys the pin flow only ever compares as opaque hex strings.
func randomKeyHex(t *testing.T, n int) string {
	t.Helper()
	raw := make([]byte, n)
	if _, err := rand.Read(raw); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	return hex.EncodeToString(raw)
}

func lookupResult(username, boxHex, signHex string) chatResolveResultMsg {
	return chatResolveResultMsg{
		username: username,
		keys: &api.PublicKeys{
			UserID:    "user-1",
			Username:  username,
			PubKey:    signHex,
			BoxPubKey: boxHex,
		},
	}
}

// runResolve applies a lookup result the way Update would and returns
// the updated model.
func runResolve(t *testing.T, m Model, msg chatResolveResultMsg) Model {
	t.Helper()
	next, _ := m.handleChatResolveResult(msg)
	got, ok := next.(Model)
	if !ok {
		t.Fatalf("handleChatResolveResult returned %T, want Model", next)
	}
	return got
}

func mustPin(t *testing.T, ks *db.KeyStore, pk db.PinnedKey) {
	t.Helper()
	if err := ks.PinKey(pk); err != nil {
		t.Fatalf("PinKey: %v", err)
	}
}

// The first time a username is looked up, its keys become the pin -
// the "first use" half of DESIGN.md section 2.
func TestFirstLookupPinsTheKey(t *testing.T) {
	m := newPinModel(t)
	box, sign := randomKeyHex(t, 32), randomKeyHex(t, 32)

	m = runResolve(t, m, lookupResult("alice", box, sign))

	pin, err := m.store.LoadPinnedKey("alice")
	if err != nil {
		t.Fatalf("LoadPinnedKey: %v", err)
	}
	if pin.BoxPubKey != box || pin.SigningPubKey != sign {
		t.Errorf("pin = %+v, want the looked-up keys", pin)
	}
	if len(m.pendingKeyChanges) != 0 {
		t.Errorf("pendingKeyChanges = %v, want empty on first use", m.pendingKeyChanges)
	}
	if m.chatActive == "" || !strings.Contains(m.commandMsg, "Started a new thread") {
		t.Errorf("thread state: active=%q commandMsg=%q, want a freshly opened thread", m.chatActive, m.commandMsg)
	}
	if reason := m.keyBlockReason(m.chatActive); reason != "" {
		t.Errorf("keyBlockReason = %q, want unblocked after first use", reason)
	}
}

// A second lookup agreeing with the pin is the boring, healthy case:
// quiet, unblocked, nothing stashed.
func TestMatchingLookupOpensQuietly(t *testing.T) {
	m := newPinModel(t)
	box, sign := randomKeyHex(t, 32), randomKeyHex(t, 32)

	m = runResolve(t, m, lookupResult("alice", box, sign))
	m = runResolve(t, m, lookupResult("alice", box, sign))

	if len(m.pendingKeyChanges) != 0 {
		t.Errorf("pendingKeyChanges = %v, want nothing stashed for an unchanged key", m.pendingKeyChanges)
	}
	if strings.Contains(m.commandMsg, "CHANGED") || !strings.Contains(m.commandMsg, "alice") {
		t.Errorf("commandMsg = %q, want a plain open with no warning", m.commandMsg)
	}
	if reason := m.keyBlockReason(m.chatActive); reason != "" {
		t.Errorf("keyBlockReason = %q, want unblocked", reason)
	}
}

// The case section 2 exists for: an existing contact's key comes back
// different. The new keys must be stashed rather than trusted, the
// user told plainly, and sending refused before anything is sealed.
func TestChangedKeyWarnsAndBlocksSends(t *testing.T) {
	m := newPinModel(t)
	oldBox, newBox := randomKeyHex(t, 32), randomKeyHex(t, 32)
	sign := randomKeyHex(t, 32)

	m = runResolve(t, m, lookupResult("alice", oldBox, sign))
	m = runResolve(t, m, lookupResult("alice", newBox, sign))

	if !strings.Contains(m.commandMsg, "key CHANGED") {
		t.Errorf("commandMsg = %q, want the hard warning", m.commandMsg)
	}
	pending, ok := m.pendingKeyChanges["alice"]
	if !ok || pending.BoxPubKey != newBox {
		t.Fatalf("pendingKeyChanges = %v, want the contradicting keys stashed", m.pendingKeyChanges)
	}
	pin, err := m.store.LoadPinnedKey("alice")
	if err != nil {
		t.Fatalf("LoadPinnedKey: %v", err)
	}
	if pin.BoxPubKey != oldBox {
		t.Errorf("pin = %q, want it left at the pinned key until :accept", pin.BoxPubKey)
	}

	blockedContact := m.chatActive
	if reason := m.keyBlockReason(blockedContact); !strings.Contains(reason, "key changed") {
		t.Fatalf("keyBlockReason = %q, want a change warning", reason)
	}

	// Sending must refuse before the local echo: no message in the
	// thread, the draft kept, and the reason surfaced.
	m.chatInput = "are you still you?"
	next, cmd := m.updateChatCompose(enterKey())
	got := next.(Model)
	if cmd != nil {
		t.Errorf("updateChatCompose returned a cmd, want none for a blocked send")
	}
	if msgs := got.chatStore.Messages(blockedContact); len(msgs) != 0 {
		t.Errorf("thread has %d messages, want none - the echo must not happen", len(msgs))
	}
	if got.chatInput == "" {
		t.Error("blocked draft was discarded from the composer")
	}
	if !strings.Contains(got.commandMsg, "key changed") {
		t.Errorf("commandMsg = %q, want the blocking reason", got.commandMsg)
	}

	// The same guarantee at the choke point where encryption happens.
	if cmd := got.sendMessage(blockedContact, []byte("typed directly")); cmd != nil {
		t.Errorf("sendMessage returned a cmd, want nil for a blocked contact")
	}
	if s := got.chatSessions[blockedContact]; s != nil && len(s.pending) != 0 {
		t.Errorf("pending = %d bodies, want none parked for a blocked contact", len(s.pending))
	}
}

// :accept is the acknowledgement the design requires before a changed
// key is trusted: it pins the stashed keys, which is also what lets
// sends go through again.
func TestAcceptUnblocksSends(t *testing.T) {
	m := newPinModel(t)
	oldBox, newBox := randomKeyHex(t, 32), randomKeyHex(t, 32)
	sign := randomKeyHex(t, 32)

	m = runResolve(t, m, lookupResult("alice", oldBox, sign))
	m = runResolve(t, m, lookupResult("alice", newBox, sign))
	blocked := m.chatActive

	result, quit, cmd := m.runCommand("accept alice")
	if quit || cmd != nil {
		t.Fatalf("accept returned quit=%v cmd=%v, want a plain acknowledgement", quit, cmd)
	}
	if !strings.Contains(result, "unblocked") {
		t.Errorf("result = %q, want it to say sending is unblocked", result)
	}
	if len(m.pendingKeyChanges) != 0 {
		t.Errorf("pendingKeyChanges = %v, want the change consumed", m.pendingKeyChanges)
	}
	pin, err := m.store.LoadPinnedKey("alice")
	if err != nil {
		t.Fatalf("LoadPinnedKey: %v", err)
	}
	if pin.BoxPubKey != newBox {
		t.Errorf("pin = %q, want the accepted key %q", pin.BoxPubKey, newBox)
	}
	if reason := m.keyBlockReason(blocked); reason != "" {
		t.Errorf("keyBlockReason = %q, want unblocked after :accept", reason)
	}
	if reason := m.keyBlockReason(m.chatActive); reason != "" {
		t.Errorf("keyBlockReason for the reopened thread = %q, want unblocked", reason)
	}

	// A repeat accept has nothing left to do.
	if res, _, _ := m.runCommand("accept alice"); !strings.Contains(res, "No unaccepted key change") {
		t.Errorf("second accept result = %q, want nothing left to accept", res)
	}
}

// If the pin can't be checked at all, the thread must not open: a
// lookup that skips the pin check is the exact hole section 2 closes.
func TestPinCheckFailureKeepsThreadClosed(t *testing.T) {
	m := newPinModel(t)
	if err := m.store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	m = runResolve(t, m, lookupResult("alice", randomKeyHex(t, 32), randomKeyHex(t, 32)))

	if m.chatActive != "" {
		t.Errorf("chatActive = %q, want no thread opened on a failed pin check", m.chatActive)
	}
	if !strings.Contains(m.commandMsg, "Couldn't check") {
		t.Errorf("commandMsg = %q, want the pin-check failure", m.commandMsg)
	}
}

// :verify <username> reads its side of the fingerprint from the pin -
// the stable, out-of-band-comparable value, not whatever the server
// would say today.
func TestVerifyUsernameUsesThePin(t *testing.T) {
	m := newPinModel(t)
	box, sign := randomKeyHex(t, 32), randomKeyHex(t, 32)
	m = runResolve(t, m, lookupResult("alice", box, sign))

	result, _, _ := m.runCommand("verify alice")
	theirKey, err := hex.DecodeString(sign)
	if err != nil {
		t.Fatalf("DecodeString: %v", err)
	}
	want := crypto.Fingerprint(m.identity.SigningPublic, ed25519.PublicKey(theirKey))
	if !strings.Contains(result, want) {
		t.Errorf("result = %q, want it to contain the pinned-key fingerprint %q", result, want)
	}
	if strings.Contains(result, "NEW") {
		t.Errorf("result = %q, want no pending-change note when nothing is pending", result)
	}
}

// When a change is pending, the fingerprint must come from the key
// about to be trusted - comparing the old pin would just re-confirm
// what the user already checked.
func TestVerifyUsernamePrefersThePendingKey(t *testing.T) {
	m := newPinModel(t)
	oldSign, newSign := randomKeyHex(t, 32), randomKeyHex(t, 32)
	m = runResolve(t, m, lookupResult("alice", randomKeyHex(t, 32), oldSign))
	m = runResolve(t, m, lookupResult("alice", randomKeyHex(t, 32), newSign))

	result, _, _ := m.runCommand("verify alice")
	theirKey, err := hex.DecodeString(newSign)
	if err != nil {
		t.Fatalf("DecodeString: %v", err)
	}
	want := crypto.Fingerprint(m.identity.SigningPublic, ed25519.PublicKey(theirKey))
	if !strings.Contains(result, want) {
		t.Errorf("result = %q, want the NEW key's fingerprint %q", result, want)
	}
	if !strings.Contains(result, "NEW") {
		t.Errorf("result = %q, want it to say which key was compared", result)
	}
}

// The raw-hex form still works - it's how you compare a key someone
// handed you out of band, with no lookup involved.
func TestVerifyHexKeyStillWorks(t *testing.T) {
	m := newPinModel(t)
	raw := make([]byte, ed25519.PublicKeySize)
	if _, err := rand.Read(raw); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}

	result, _, _ := m.runCommand("verify " + hex.EncodeToString(raw))
	want := crypto.Fingerprint(m.identity.SigningPublic, ed25519.PublicKey(raw))
	if !strings.Contains(result, want) {
		t.Errorf("result = %q, want the fingerprint %q", result, want)
	}
}

func TestVerifyUsernameWithoutAPinSaysToChatFirst(t *testing.T) {
	m := newPinModel(t)

	result, _, _ := m.runCommand("verify alice")
	if !strings.Contains(result, "No pinned key") || !strings.Contains(result, ":chat alice") {
		t.Errorf("result = %q, want it to point at :chat first", result)
	}
}

func TestAcceptWithoutAPendingChangeSaysSo(t *testing.T) {
	m := newPinModel(t)

	result, _, _ := m.runCommand("accept alice")
	if !strings.Contains(result, "No unaccepted key change") {
		t.Errorf("result = %q, want an empty-handed refusal", result)
	}
}

// A store-less model (the bare Models several other tests build) must
// neither panic nor block: with nothing to check against, keyBlockReason
// has no opinion.
func TestKeyBlockReasonToleratesAModelWithoutAStore(t *testing.T) {
	m := Model{chatStore: chat.NewStore()}
	if reason := m.keyBlockReason("deadbeef"); reason != "" {
		t.Errorf("keyBlockReason = %q, want empty with no store", reason)
	}
	if _, err := m.applyPin("alice", &api.PublicKeys{BoxPubKey: "x"}); err == nil {
		t.Error("applyPin without a store succeeded, want an error")
	}
}

// The block has to be visible where the user is typing, not just in
// the one-shot command message: a banner over the thread and a composer
// hint that names the fix.
func TestBlockedThreadWarnsInRender(t *testing.T) {
	m := newPinModel(t)
	oldBox, newBox := randomKeyHex(t, 32), randomKeyHex(t, 32)
	sign := randomKeyHex(t, 32)

	m = runResolve(t, m, lookupResult("alice", oldBox, sign))
	m = runResolve(t, m, lookupResult("alice", newBox, sign))

	if hint := m.composeHint(); !strings.Contains(hint, ":accept alice") {
		t.Errorf("composeHint = %q, want it to name :accept alice", hint)
	}
	if out := m.renderChat(); !strings.Contains(out, "key changed") {
		t.Errorf("renderChat = %q, want the pin-change banner", out)
	}
}
