package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/gorilla/websocket"

	"github.com/HomieB-tt/synq/internal/chat"
	"github.com/HomieB-tt/synq/internal/crypto"
	"github.com/HomieB-tt/synq/internal/ws"
)

// hub is a minimal stand-in for synq-server's relay: it accepts a
// connection per user id and forwards {"to","payload"} frames to
// whichever peer is connected under that id, rewriting them into the
// server → client shape (type/from/payload). Frames to a peer with no
// connection are dropped, exactly like publishing to a Redis channel
// nobody is subscribed to - which matters here, because that's what
// makes a lost handshake something the protocol has to survive.
type hub struct {
	srv    *httptest.Server
	mu     sync.Mutex
	conns  map[string]*websocket.Conn
	writes sync.Mutex
}

func newHub(t *testing.T) *hub {
	t.Helper()
	h := &hub{conns: make(map[string]*websocket.Conn)}

	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		user := r.URL.Query().Get("user")
		if user == "" {
			http.Error(w, "missing user", http.StatusBadRequest)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		h.mu.Lock()
		h.conns[user] = conn
		h.mu.Unlock()

		defer func() {
			h.mu.Lock()
			if h.conns[user] == conn {
				delete(h.conns, user)
			}
			h.mu.Unlock()
			conn.Close()
		}()

		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var frame struct {
				To      string          `json:"to"`
				Payload json.RawMessage `json:"payload"`
			}
			if json.Unmarshal(data, &frame) != nil {
				continue
			}

			h.mu.Lock()
			dst := h.conns[frame.To]
			h.mu.Unlock()
			if dst == nil {
				continue // offline peer: dropped, not queued
			}

			outbound := fmt.Sprintf(`{"type":"message","from":%q,"payload":%s}`, user, frame.Payload)
			h.writes.Lock()
			err = dst.WriteMessage(websocket.TextMessage, []byte(outbound))
			h.writes.Unlock()
			if err != nil {
				return
			}
		}
	})

	h.srv = httptest.NewServer(mux)
	t.Cleanup(h.srv.Close)
	return h
}

func (h *hub) wsURL(user string) string {
	return "ws" + strings.TrimPrefix(h.srv.URL, "http") + "/ws?user=" + user
}

func (h *hub) waitConnected(t *testing.T, user string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		h.mu.Lock()
		conn, ok := h.conns[user]
		h.mu.Unlock()
		if ok && conn != nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s to reach the relay", user)
}

// newWireModel builds a Model wired to the relay under userID, with
// its connection loop running for the rest of the test.
func newWireModel(t *testing.T, h *hub, userID string, id *crypto.Identity) Model {
	t.Helper()
	client, err := ws.NewClient(h.wsURL(userID))
	if err != nil {
		t.Fatalf("ws.NewClient: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	t.Cleanup(func() {
		client.Close()
		select {
		case <-client.Done():
		case <-time.After(2 * time.Second):
			t.Error("timed out waiting for the connection loop to exit")
		}
	})
	go client.Run(ctx)

	h.waitConnected(t, userID)
	return Model{
		identity:  id,
		wsClient:  client,
		chatStore: chat.NewStore(),
		activeTab: tabChat,
	}
}

// runAll executes a command tree (batches included) the way the
// Bubble Tea program would, failing the test if a send is refused.
func runAll(t *testing.T, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		return
	}
	msg := cmd()
	switch m := msg.(type) {
	case tea.BatchMsg:
		for _, sub := range m {
			runAll(t, sub)
		}
	case wsSendErrorMsg:
		t.Fatalf("sending a frame failed: %v", m.err)
	}
}

// deliver reads one frame addressed to m and processes it the way
// handleWSFrame's message branch does: route by sender, hand the
// payload to messaging.go, run any reply.
//
// Update itself is deliberately not used here. Its first batch element
// is the frame pump, which blocks until the next frame arrives - and
// executing it in this test would swallow that frame and throw it
// away. Routing through Update is covered separately in model_test.go.
func deliver(t *testing.T, m Model) Model {
	t.Helper()
	raw := recvFrame(t, m.wsClient)
	event, err := ws.DecodeFrame(raw)
	if err != nil {
		t.Fatalf("DecodeFrame(%s): %v", raw, err)
	}
	if event.Type != ws.EventTypeMessage {
		t.Fatalf("relay delivered a %q event, want a message", event.Type)
	}
	contact, ok := m.contactForUserID(event.From)
	if !ok {
		t.Fatalf("frame from %q: no contact on file", event.From)
	}
	runAll(t, m.handleIncomingPayload(contact, event.Payload))
	return m
}

func recvFrame(t *testing.T, c *ws.Client) []byte {
	t.Helper()
	select {
	case raw := <-c.Incoming():
		return raw
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for a frame from the relay (buffered locally: %d)", len(c.Incoming()))
		return nil
	}
}

func enterKey() tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: tea.KeyEnter}
}

// The whole point of stages 2 and 3 together: two clients, each having
// resolved the other through :chat, exchange a first message - which
// can't be sealed yet - and end up with both session keys derived, the
// body delivered, and no frame ever carrying plaintext.
func TestTwoClientsHandshakeAndDeliverAMessage(t *testing.T) {
	h := newHub(t)

	alice, err := crypto.GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	bob, err := crypto.GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}

	a := newWireModel(t, h, "user-a", alice)
	b := newWireModel(t, h, "user-b", bob)

	// What each side's `:chat <username>` lookup recorded: the other
	// thread's key (its static X25519 key) and the user id to address
	// frames with.
	bobThread := chat.NewContactKey(bob.BoxPublic)
	aliceThread := chat.NewContactKey(alice.BoxPublic)
	a.rememberContact(bobThread, "bob", "user-b")
	b.rememberContact(aliceThread, "alice", "user-a")
	a.chatActive = bobThread

	// Alice types first: no session key exists, so the body parks and
	// her handshake frame goes out.
	a.chatInput = "hello bob"
	next, cmd := a.updateChatCompose(enterKey())
	a = next.(Model)
	runAll(t, cmd)

	if msgs := a.chatStore.Messages(bobThread); len(msgs) != 1 || string(msgs[0].Body) != "hello bob" {
		t.Fatalf("local echo = %d messages, want the one just typed", len(msgs))
	}
	if a.hasSessionKey(bobThread) {
		t.Fatal("a session key exists before any handshake frame arrived")
	}
	if s := a.chatSessions[bobThread]; s == nil || len(s.pending) != 1 {
		t.Fatalf("pending = %v, want the composed body parked", s)
	}

	// Bob's side: he receives Alice's ephemeral key, derives, and
	// answers with his own - which also happens to be the first frame
	// Alice has ever had from him.
	b = deliver(t, b)
	if !b.hasSessionKey(aliceThread) {
		t.Fatal("bob didn't derive a session key from alice's handshake")
	}

	// Alice receives Bob's key, derives, and flushes the parked body -
	// now sealed under a key Bob can open.
	a = deliver(t, a)
	if !a.hasSessionKey(bobThread) {
		t.Fatal("alice didn't derive a session key from bob's handshake")
	}
	if s := a.chatSessions[bobThread]; len(s.pending) != 0 {
		t.Errorf("pending = %d bodies after the handshake, want them flushed", len(s.pending))
	}

	// The delivery itself: opened on Bob's side, marked as received,
	// and - the part worth proving - never sent as readable bytes.
	// Alice's reply to Bob's handshake and her flush of the parked
	// body are independent commands, so Bob may receive her repeated
	// handshake frame (harmless, it repeats a key he already has) and
	// the sealed message in either order - drain until it lands.
	for i := 0; i < 4 && len(b.chatStore.Messages(aliceThread)) == 0; i++ {
		b = deliver(t, b)
	}
	got := b.chatStore.Messages(aliceThread)
	if len(got) != 1 {
		t.Fatalf("bob has %d messages, want 1", len(got))
	}
	if string(got[0].Body) != "hello bob" {
		t.Errorf("bob received %q, want %q", got[0].Body, "hello bob")
	}
	if got[0].Outgoing {
		t.Error("message marked as outgoing on the receiving side")
	}
}

// A second handshake frame repeating a key we already know must not be
// answered - otherwise two clients each answering every handshake
// would answer each other forever.
func TestHandshakeFrameIsAnsweredOnlyOnce(t *testing.T) {
	alice, err := crypto.GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	bob, err := crypto.GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}

	// A client that is never started - it exists for its per-launch
	// ephemeral key, which is half of what DeriveSessionKey needs.
	// Nothing is dialed, so the reply frame is built but never
	// delivered, which is all this test needs.
	aliceClient, err := ws.NewClient("ws://example.invalid/ws")
	if err != nil {
		t.Fatalf("ws.NewClient: %v", err)
	}
	bobClient, err := ws.NewClient("ws://example.invalid/ws")
	if err != nil {
		t.Fatalf("ws.NewClient: %v", err)
	}
	a := Model{identity: alice, wsClient: aliceClient, chatStore: chat.NewStore()}
	b := Model{identity: bob, wsClient: bobClient, chatStore: chat.NewStore()}
	bobThread := chat.NewContactKey(bob.BoxPublic)
	aliceThread := chat.NewContactKey(alice.BoxPublic)
	a.rememberContact(bobThread, "bob", "user-b")
	b.rememberContact(aliceThread, "alice", "user-a")

	// Alice's handshake frame, as Bob would see it on the wire. The
	// key material here is arbitrary - this test is about the reply
	// gating, not the derivation.
	var eph [32]byte
	copy(eph[:], alice.BoxPublic[:])
	payload, err := ws.EncodeHandshake(&eph)
	if err != nil {
		t.Fatalf("EncodeHandshake: %v", err)
	}

	if cmd := b.handleIncomingPayload(aliceThread, payload); cmd == nil {
		t.Fatal("first handshake produced no reply command")
	}
	if !b.hasSessionKey(aliceThread) {
		t.Fatal("first handshake didn't establish a session key")
	}

	// The same frame again - what a client would send if its reply
	// had been lost and it retried.
	if cmd := b.handleIncomingPayload(aliceThread, payload); cmd != nil {
		t.Fatal("repeating a known handshake was answered, which would loop forever")
	}
}

// A sealed frame arriving before the handshake completed has nothing
// to be opened with - and unlike an unknown sender (dropped silently),
// this one comes from a contact we can name in the warning.
func TestSealedFrameWithoutASessionIsReportedNotOpened(t *testing.T) {
	alice, err := crypto.GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	bob, err := crypto.GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}

	b := Model{identity: bob, chatStore: chat.NewStore()}
	aliceThread := chat.NewContactKey(alice.BoxPublic)
	b.rememberContact(aliceThread, "alice", "user-a")

	// Sealed under a key that was never exchanged with Bob.
	var unknownKey [32]byte
	sealed, err := ws.SealMessage(&unknownKey, []byte("too early"))
	if err != nil {
		t.Fatalf("SealMessage: %v", err)
	}

	if cmd := b.handleIncomingPayload(aliceThread, ws.EncodeSealed(sealed)); cmd != nil {
		t.Fatal("returned a command for a frame it couldn't open")
	}
	if msgs := b.chatStore.Messages(aliceThread); len(msgs) != 0 {
		t.Errorf("stored %d messages it couldn't open", len(msgs))
	}
	if !strings.Contains(b.commandMsg, "handshake") {
		t.Errorf("commandMsg = %q, want it to say the handshake never completed", b.commandMsg)
	}
}

// A body that fails authentication means the key in hand is the wrong
// one (the contact relaunched, or the frame was tampered with).
// Discarding the session is what lets the next handshake re-establish
// instead of leaving this thread wedged on a key that can't open
// anything.
func TestFailedOpenResetsTheSessionSoTheNextHandshakeCanFix(t *testing.T) {
	alice, err := crypto.GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	bob, err := crypto.GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}

	aliceClient, err := ws.NewClient("ws://example.invalid/ws")
	if err != nil {
		t.Fatalf("ws.NewClient: %v", err)
	}
	b := Model{identity: bob, wsClient: aliceClient, chatStore: chat.NewStore()}
	aliceThread := chat.NewContactKey(alice.BoxPublic)
	b.rememberContact(aliceThread, "alice", "user-a")

	// Complete a handshake so a key exists. The ephemeral key it
	// carries is this client's own stand-in for Alice's - arbitrary
	// bytes, since the next step is what's under test.
	aliceEph, err := ws.NewEphemeralKeypair()
	if err != nil {
		t.Fatalf("NewEphemeralKeypair: %v", err)
	}
	payload, err := ws.EncodeHandshake(aliceEph.Public)
	if err != nil {
		t.Fatalf("EncodeHandshake: %v", err)
	}
	b.handleIncomingPayload(aliceThread, payload)
	if !b.hasSessionKey(aliceThread) {
		t.Fatal("handshake didn't establish a key")
	}

	// ...then open a message sealed under a *different* key.
	var otherKey [32]byte
	sealed, err := ws.SealMessage(&otherKey, []byte("from a stale session"))
	if err != nil {
		t.Fatalf("SealMessage: %v", err)
	}
	b.handleIncomingPayload(aliceThread, ws.EncodeSealed(sealed))

	if b.hasSessionKey(aliceThread) {
		t.Error("session key kept after an authentication failure")
	}
	if s := b.chatSessions[aliceThread]; s.peerSeen || s.peerEphemeral != nil {
		t.Error("peer handshake state kept after an authentication failure")
	}
	if msgs := b.chatStore.Messages(aliceThread); len(msgs) != 0 {
		t.Errorf("stored %d messages that failed to open", len(msgs))
	}
	if !strings.Contains(b.commandMsg, "resetting") {
		t.Errorf("commandMsg = %q, want it to say the handshake was reset", b.commandMsg)
	}
}

// The composer's hint promises what pressing enter will do, so it has
// to track the three states that promise can take.
func TestComposeHintTracksConnectionAndHandshake(t *testing.T) {
	alice, err := crypto.GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	contact := chat.NewContactKey(alice.BoxPublic)

	m := Model{chatStore: chat.NewStore(), chatActive: contact}
	if got := m.composeHint(); !strings.Contains(got, "stays local") {
		t.Errorf("disconnected hint = %q, want it to say messages stay local", got)
	}

	m.wsClient, err = ws.NewClient("ws://example.invalid/ws")
	if err != nil {
		t.Fatalf("ws.NewClient: %v", err)
	}
	if got := m.composeHint(); !strings.Contains(got, "queued") {
		t.Errorf("handshake-pending hint = %q, want it to say queued", got)
	}

	m.sessionFor(contact).key = &[32]byte{}
	if got := m.composeHint(); strings.Contains(got, "queued") || strings.Contains(got, "stays local") {
		t.Errorf("established hint = %q, want it to promise a plain send", got)
	}
}
