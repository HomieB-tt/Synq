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
	srv := newEchoServer(t, func(conn *websocket.Conn) {
		n := atomic.AddInt32(&connections, 1)
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
	waitForStatus(t, c, StatusDisconnected, 2*time.Second)
	waitForStatus(t, c, StatusConnected, 2*time.Second)

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
