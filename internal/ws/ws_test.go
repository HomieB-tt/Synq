package ws

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// newEchoServer starts a local WebSocket server that echoes every
// message it receives back to the sender. onConnect, if non-nil, is
// called (in a new goroutine per connection) right after each upgrade
// succeeds - tests use it to count connections or to force an early
// close, without needing a second handler function.
func newEchoServer(t *testing.T, onConnect func(conn *websocket.Conn)) *httptest.Server {
	t.Helper()
	upgrader := websocket.Upgrader{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		if onConnect != nil {
			onConnect(conn)
			return
		}
		defer conn.Close()
		for {
			mt, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if err := conn.WriteMessage(mt, data); err != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// wsURL converts an httptest.Server's http(s):// URL to the matching
// ws(s):// URL a websocket.Dialer expects.
func wsURL(t *testing.T, httpURL string) string {
	t.Helper()
	switch {
	case strings.HasPrefix(httpURL, "https://"):
		return "wss://" + strings.TrimPrefix(httpURL, "https://")
	case strings.HasPrefix(httpURL, "http://"):
		return "ws://" + strings.TrimPrefix(httpURL, "http://")
	default:
		t.Fatalf("unexpected test server URL scheme: %s", httpURL)
		return ""
	}
}

// waitForStatus reads from Status() until it sees want or the timeout
// elapses, failing the test either way. Other statuses seen along the
// way are ignored - tests using this only care that want eventually
// shows up, not the exact sequence around it.
func waitForStatus(t *testing.T, c *Client, want Status, timeout time.Duration) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case got := <-c.Status():
			if got == want {
				return
			}
		case <-deadline:
			t.Fatalf("timed out waiting for status %s", want)
		}
	}
}

func TestClientConnectsSendsAndReceives(t *testing.T) {
	srv := newEchoServer(t, nil)

	c, err := NewClient(wsURL(t, srv.URL))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	defer c.Close()

	waitForStatus(t, c, StatusConnected, 2*time.Second)

	want := "hello, synq"
	if err := c.Send([]byte(want)); err != nil {
		t.Fatalf("Send: %v", err)
	}

	select {
	case got := <-c.Incoming():
		if string(got) != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for echoed message")
	}

	c.Close()
	select {
	case <-c.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Run to exit after Close")
	}
}

func TestClientReconnectsAfterServerDrop(t *testing.T) {
	// Keep the test fast: real backoff bounds are meant for a real
	// network, not a test that wants to observe several reconnects in
	// well under a second.
	oldMin, oldMax := MinBackoff, MaxBackoff
	MinBackoff, MaxBackoff = time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() { MinBackoff, MaxBackoff = oldMin, oldMax })

	var connections int32
	accepted := make(chan struct{}, 2)
	srv := newEchoServer(t, func(conn *websocket.Conn) {
		n := atomic.AddInt32(&connections, 1)
		accepted <- struct{}{}
		if n == 1 {
			// Drop the first connection immediately, with no
			// handshake at the application level - this is what a
			// server restart or a network blip looks like from the
			// client's side.
			conn.Close()
			return
		}
		// Every later connection behaves like a normal echo server,
		// so the test can tell a *second* real connection apart from
		// the first, dropped one.
		defer conn.Close()
		for {
			mt, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if err := conn.WriteMessage(mt, data); err != nil {
				return
			}
		}
	})

	c, err := NewClient(wsURL(t, srv.URL))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	defer c.Close()

	// First connect, then the forced drop, then Run should reconnect
	// on its own without anything external telling it to.
	waitForStatus(t, c, StatusConnected, 2*time.Second)
	select {
	case <-accepted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the server to accept the first connection")
	}
	waitForStatus(t, c, StatusDisconnected, 2*time.Second)
	waitForStatus(t, c, StatusConnected, 2*time.Second)
	select {
	case <-accepted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the server to accept the reconnected connection")
	}

	if got := atomic.LoadInt32(&connections); got < 2 {
		t.Fatalf("expected at least 2 connection attempts to reach the server, got %d", got)
	}

	// Confirm the reconnected client is actually usable, not just
	// reporting StatusConnected.
	want := "still alive"
	if err := c.Send([]byte(want)); err != nil {
		t.Fatalf("Send: %v", err)
	}
	select {
	case got := <-c.Incoming():
		if string(got) != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for echoed message after reconnect")
	}
}

// newRejectingServer starts a local HTTP server that answers every
// request with the given status and a synq-server-shaped error body -
// what the upgrade handler does when the ?token= on a /ws dial is
// expired or invalid (API.md: a plain 401 response, before any
// upgrade happens).
func newRejectingServer(t *testing.T, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"error":"missing or invalid access token"}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// stopClient closes c and waits for Run to fully exit. Tests that
// shrink MinBackoff/MaxBackoff need this before their cleanup restores
// the real bounds: Run reads those globals on every backoff, so
// returning while it's still looping races the restore.
func stopClient(t *testing.T, c *Client) {
	t.Helper()
	c.Close()
	select {
	case <-c.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Run to exit after Close")
	}
}

// An expired access token is the one dial failure that can't heal by
// retrying: the URL still carries the dead token. The client has to
// say so (StatusAuthExpired), and SetURL has to be enough to fix it -
// otherwise a long-running session reconnect-loops forever against a
// token that expired 15 minutes in.
func TestClientSignalsAuthExpiredAndDialsTheURLSetAfterwards(t *testing.T) {
	oldMin, oldMax := MinBackoff, MaxBackoff
	MinBackoff, MaxBackoff = time.Millisecond, 5*time.Millisecond
	t.Cleanup(func() { MinBackoff, MaxBackoff = oldMin, oldMax })

	expired := newRejectingServer(t, http.StatusUnauthorized)
	good := newEchoServer(t, nil)

	c, err := NewClient(wsURL(t, expired.URL))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	defer stopClient(t, c)

	waitForStatus(t, c, StatusAuthExpired, 2*time.Second)

	// Exactly what the app does in response: swap in a URL carrying a
	// fresh token, and let the backoff loop pick it up.
	c.SetURL(wsURL(t, good.URL))

	waitForStatus(t, c, StatusConnected, 2*time.Second)

	// Connected isn't enough - confirm the new URL is the one actually
	// being dialed, by round-tripping through the (echoing) server it
	// points at.
	want := "after refresh"
	if err := c.Send([]byte(want)); err != nil {
		t.Fatalf("Send: %v", err)
	}
	select {
	case got := <-c.Incoming():
		if string(got) != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for an echo from the server SetURL pointed at")
	}
}

// 401 means "your credentials are wrong"; anything else the handshake
// fails with (a proxy 404, a server restarting mid-dial) means
// "try again" and must not trigger a token refresh, which would
// eventually trip synq-server's auth rate limit for no reason.
func TestClientDoesNotSignalAuthExpiredOnOtherHandshakeFailures(t *testing.T) {
	oldMin, oldMax := MinBackoff, MaxBackoff
	MinBackoff, MaxBackoff = time.Millisecond, 5*time.Millisecond
	t.Cleanup(func() { MinBackoff, MaxBackoff = oldMin, oldMax })

	notFound := newRejectingServer(t, http.StatusNotFound)

	c, err := NewClient(wsURL(t, notFound.URL))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	defer stopClient(t, c)

	// Long enough for plenty of failed attempts at these backoff
	// bounds - the absence of a single AuthExpired is the assertion.
	deadline := time.After(300 * time.Millisecond)
	for {
		select {
		case got := <-c.Status():
			if got == StatusAuthExpired {
				t.Fatalf("status %s from a %d handshake failure", got, http.StatusNotFound)
			}
		case <-deadline:
			return
		}
	}
}

func TestClientClosePreventsFurtherSend(t *testing.T) {
	srv := newEchoServer(t, nil)

	c, err := NewClient(wsURL(t, srv.URL))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)

	waitForStatus(t, c, StatusConnected, 2*time.Second)
	c.Close()

	select {
	case <-c.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Run to exit after Close")
	}

	if err := c.Send([]byte("too late")); err == nil {
		t.Fatal("expected Send after Close to return an error")
	}
}
