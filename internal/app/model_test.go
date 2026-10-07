package app

import (
	"errors"
	"strings"
	"testing"

	"github.com/HomieB-tt/synq/internal/chat"
	"github.com/HomieB-tt/synq/internal/crypto"
	"github.com/HomieB-tt/synq/internal/ws"
)

// newFrameModel returns a Model wired with a real (never-dialed) WS
// client, so handleWSFrame has something to re-arm against.
func newFrameModel(t *testing.T) Model {
	t.Helper()
	client, err := ws.NewClient("ws://example.invalid/ws")
	if err != nil {
		t.Fatalf("ws.NewClient: %v", err)
	}
	return Model{wsClient: client}
}

func frameToModel(t *testing.T, m Model, msg wsFrameMsg) (Model, string) {
	t.Helper()
	next, cmd := m.handleWSFrame(msg)
	if cmd == nil {
		t.Fatal("handleWSFrame did not re-arm the frame pump - inbound frames would stop after this one")
	}
	got, ok := next.(Model)
	if !ok {
		t.Fatalf("handleWSFrame returned %T, want Model", next)
	}
	return got, got.commandMsg
}

func TestHandleWSFrameSurfacesRateLimit(t *testing.T) {
	got, out := frameToModel(t, newFrameModel(t), wsFrameMsg{
		event: ws.Event{Type: ws.EventTypeError, Error: "rate_limited"},
	})
	if !strings.Contains(out, "rate-limited") {
		t.Errorf("commandMsg = %q, want a rate-limit notice", out)
	}
	if got.wsClient == nil {
		t.Error("handler dropped the WS client")
	}
}

func TestHandleWSFrameSurfacesMalformedFrame(t *testing.T) {
	_, out := frameToModel(t, newFrameModel(t), wsFrameMsg{
		err: errors.New("ws: message payload is not valid base64"),
	})
	if !strings.Contains(out, "Bad frame") {
		t.Errorf("commandMsg = %q, want it to name a bad frame", out)
	}
}

func TestHandleWSFrameSurfacesNodeEvents(t *testing.T) {
	for _, tc := range []struct {
		event ws.Event
		want  string
	}{
		{ws.Event{Type: ws.EventTypeNodeRequest, From: "usr_a"}, "Node request from usr_a"},
		{ws.Event{Type: ws.EventTypeNodeAccepted, From: "usr_b"}, "accepted by usr_b"},
	} {
		_, out := frameToModel(t, newFrameModel(t), wsFrameMsg{event: tc.event})
		if !strings.Contains(out, tc.want) {
			t.Errorf("event %+v: commandMsg = %q, want it to contain %q", tc.event, out, tc.want)
		}
	}
}

// A frame type this client doesn't know about must neither error nor
// surface anything to the user - but it must still not stop the pump.
func TestHandleWSFrameIgnoresUnknownTypeWithoutStopping(t *testing.T) {
	_, out := frameToModel(t, newFrameModel(t), wsFrameMsg{
		event: ws.Event{Type: "key_exchange_request", From: "usr_a"},
	})
	if out != "" {
		t.Errorf("commandMsg = %q, want untouched for an unknown frame type", out)
	}
}

// Chat payloads are still opaque (no session key exists yet), so a
// message frame is deliberately dropped rather than surfaced - but the
// pump has to keep running for the frame after it.
func TestHandleWSFrameDropsUndecryptableChatSilentlyButKeepsPumping(t *testing.T) {
	_, out := frameToModel(t, newFrameModel(t), wsFrameMsg{
		event: ws.Event{Type: ws.EventTypeMessage, From: "usr_a", Payload: []byte("still sealed")},
	})
	if out != "" {
		t.Errorf("commandMsg = %q, want untouched - an undecryptable payload has nothing to show", out)
	}
}

// After :logout closes and clears the client, an in-flight frame must
// not try to re-arm against the nil client (that would deref it) and
// must not surface anything - the connection is already gone.
func TestHandleWSFrameStopsOnceClientIsGone(t *testing.T) {
	next, cmd := Model{}.handleWSFrame(wsFrameMsg{event: ws.Event{Type: ws.EventTypeNodeRequest, From: "usr_a"}})
	if cmd != nil {
		t.Error("re-armed the pump on a model with no client - would block or panic")
	}
	if out := next.(Model).commandMsg; out != "" {
		t.Errorf("commandMsg = %q, want untouched after logout", out)
	}
}

// The pump's actual entry point has to take a message frame from a
// contact on file, put it in that contact's thread, and re-arm - the
// reverse half of contactForUserID. The returned command is not run
// here: its first element is the frame pump, which would block on a
// frame this model's client never receives. Opening the payload is
// messaging.go's job and is covered there.
func TestHandleWSFrameRoutesMessageIntoTheContactThread(t *testing.T) {
	alice, err := crypto.GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	contact := chat.NewContactKey(alice.BoxPublic)

	m := newFrameModel(t)
	m.chatStore = chat.NewStore()
	m.rememberContact(contact, "alice", "user-a")
	var key [32]byte
	m.sessionFor(contact).key = &key

	sealed, err := ws.SealMessage(&key, []byte("wired"))
	if err != nil {
		t.Fatalf("SealMessage: %v", err)
	}
	got, out := frameToModel(t, m, wsFrameMsg{
		event: ws.Event{Type: ws.EventTypeMessage, From: "user-a", Payload: ws.EncodeSealed(sealed)},
	})

	msgs := got.chatStore.Messages(contact)
	if len(msgs) != 1 || string(msgs[0].Body) != "wired" {
		t.Fatalf("thread = %d messages, want the routed one", len(msgs))
	}
	if out != "" {
		t.Errorf("commandMsg = %q, want untouched for a frame that opened cleanly", out)
	}
}
