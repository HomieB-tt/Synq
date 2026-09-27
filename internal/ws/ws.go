// Package ws implements the WebSocket client connection, reconnect
// handling, and the per-launch ephemeral handshake described in
// synq-DESIGN.md section 3.
//
// What this package deliberately does not do yet: define a wire
// message format. synq-server (see synq-server-DESIGN.md) doesn't
// exist as a runnable project, and nothing in this repo's design docs
// pins down an envelope shape, endpoint path, or auth-at-connect
// scheme - inventing one here and presenting it as settled would be
// guessing at another project's contract, not implementing this one's.
// Client is deliberately schema-agnostic: Send/Receive move raw
// []byte frames, so the transport (dial, reconnect, keepalive) can be
// built and tested for real today, and whatever envelope format
// synq-server ends up wanting can be layered on top later without
// touching this file.
package ws

import (
	"context"
	"errors"
	"math/rand"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Status is a Client connection-lifecycle event, delivered on
// Client.Status() so internal/app can drive Model.connected without
// polling.
type Status int

const (
	// StatusConnecting is sent before each dial attempt, including
	// reconnect attempts after a drop.
	StatusConnecting Status = iota
	// StatusConnected is sent once a dial succeeds.
	StatusConnected
	// StatusDisconnected is sent when a previously-established
	// connection is lost and a reconnect attempt is about to begin.
	StatusDisconnected
	// StatusClosed is sent exactly once, after Close is called and the
	// run loop has exited for good. No further Status or Message
	// values follow.
	StatusClosed
)

func (s Status) String() string {
	switch s {
	case StatusConnecting:
		return "connecting"
	case StatusConnected:
		return "connected"
	case StatusDisconnected:
		return "disconnected"
	case StatusClosed:
		return "closed"
	default:
		return "unknown"
	}
}

// Default backoff bounds for reconnect attempts. Exported as variables
// rather than buried as literals so a test (or, later, a config
// option) can shrink them instead of a real test needing to wait
// through a real multi-second backoff.
var (
	MinBackoff = 500 * time.Millisecond
	MaxBackoff = 30 * time.Second

	pingInterval = 25 * time.Second
	pingTimeout  = 10 * time.Second
)

// Client is a reconnecting WebSocket client. It owns exactly one
// EphemeralKeypair for its entire process lifetime (see that type's
// doc comment) - not one per connection or reconnect attempt.
type Client struct {
	url string

	Ephemeral *EphemeralKeypair

	incoming chan []byte
	outgoing chan []byte
	status   chan Status

	closeOnce sync.Once
	closeCh   chan struct{}
	doneCh    chan struct{}
}

// NewClient creates a Client for the given synq-server WebSocket URL
// (e.g. "wss://example.invalid/ws") and generates its per-launch
// ephemeral keypair. It does not dial yet - call Run to start the
// connect/reconnect loop.
func NewClient(url string) (*Client, error) {
	eph, err := NewEphemeralKeypair()
	if err != nil {
		return nil, err
	}
	return &Client{
		url:       url,
		Ephemeral: eph,
		incoming:  make(chan []byte, 32),
		outgoing:  make(chan []byte, 32),
		status:    make(chan Status, 4),
		closeCh:   make(chan struct{}),
		doneCh:    make(chan struct{}),
	}, nil
}

// Status returns the channel Client delivers connection lifecycle
// events on. Reads from it must keep up (or run in their own
// goroutine, e.g. as a tea.Cmd loop) - Run drops a status value rather
// than blocking forever if this channel is full, since a connection
// event that arrives late is still more useful than a run loop wedged
// on a reader that stopped reading.
func (c *Client) Status() <-chan Status { return c.status }

// Incoming returns the channel raw application messages read off the
// connection are delivered on, one []byte per WriteMessage the other
// side sent. See the package doc comment for why this is raw bytes,
// not a parsed envelope type.
func (c *Client) Incoming() <-chan []byte { return c.incoming }

// Send queues a raw message for the connection to write, once one is
// established. Send does not block on the network; it only blocks if
// the internal outgoing queue (32 messages) is full, and returns an
// error instead of blocking forever if Close has already been called.
//
// The up-front, non-blocking check below matters, not just the second
// select's <-c.closeCh case: once closeCh is closed, that case and the
// c.outgoing<- case can both be immediately ready at once (outgoing
// has spare buffer capacity), and select picks between two ready cases
// at random - without checking closeCh first, a Send made after Close
// would only report the closed error about half the time.
func (c *Client) Send(msg []byte) error {
	select {
	case <-c.closeCh:
		return errors.New("ws: client closed")
	default:
	}

	select {
	case c.outgoing <- msg:
		return nil
	case <-c.closeCh:
		return errors.New("ws: client closed")
	}
}

// Close stops the run loop and closes the underlying connection, if
// any. Safe to call more than once and from any goroutine. Close does
// not wait for the run loop to fully exit - see Done for that.
func (c *Client) Close() {
	c.closeOnce.Do(func() { close(c.closeCh) })
}

// Done returns a channel that's closed once Run has fully exited
// (after StatusClosed has already been sent on Status()).
func (c *Client) Done() <-chan struct{} { return c.doneCh }

// Run dials, and on any read/write error or clean server close,
// reconnects with jittered exponential backoff (MinBackoff..MaxBackoff)
// until Close is called or ctx is canceled. It's meant to be started
// once in its own goroutine (or as a long-running tea.Cmd) and left
// running for the lifetime of the program.
func (c *Client) Run(ctx context.Context) {
	defer close(c.doneCh)
	defer func() { c.sendStatus(StatusClosed) }()

	attempt := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.closeCh:
			return
		default:
		}

		c.sendStatus(StatusConnecting)
		// DialContext closes the HTTP response body itself on both
		// success and failure, so it's fine to discard it here.
		conn, _, err := websocket.DefaultDialer.DialContext(ctx, c.url, nil)
		if err != nil {
			if !c.backoff(ctx, attempt) {
				return
			}
			attempt++
			continue
		}

		attempt = 0
		c.sendStatus(StatusConnected)
		c.runConnection(ctx, conn)
		c.sendStatus(StatusDisconnected)

		select {
		case <-ctx.Done():
			return
		case <-c.closeCh:
			return
		default:
		}
	}
}

// runConnection owns conn for exactly one connected session: one
// reader goroutine, one keepalive-ping ticker, and the write loop,
// all torn down together the moment any of read, write, or the
// keepalive ping fails, or Close/ctx cancellation fires.
func (c *Client) runConnection(ctx context.Context, conn *websocket.Conn) {
	defer conn.Close()

	sessionCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	readErr := make(chan error, 1)
	go func() {
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				readErr <- err
				return
			}
			select {
			case c.incoming <- data:
			case <-sessionCtx.Done():
				return
			}
		}
	}()

	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-sessionCtx.Done():
			return
		case <-c.closeCh:
			_ = conn.WriteControl(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
				time.Now().Add(time.Second))
			return
		case err := <-readErr:
			_ = err // the loop in Run treats any exit from here the same: reconnect
			return
		case msg := <-c.outgoing:
			if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		case <-ticker.C:
			if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(pingTimeout)); err != nil {
				return
			}
		}
	}
}

// backoff waits an exponentially increasing, jittered delay before the
// next connection attempt, bounded by MinBackoff/MaxBackoff. It
// returns false (meaning the caller should stop, not wait) if ctx is
// canceled or Close is called during the wait.
func (c *Client) backoff(ctx context.Context, attempt int) bool {
	delay := MinBackoff * time.Duration(1<<uint(minInt(attempt, 10)))
	if delay > MaxBackoff || delay <= 0 {
		delay = MaxBackoff
	}
	// Full jitter: a uniformly random wait between 0 and delay, so a
	// server restart doesn't get every client reconnecting in
	// lockstep.
	delay = time.Duration(rand.Int63n(int64(delay) + 1))

	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	case <-c.closeCh:
		return false
	}
}

func (c *Client) sendStatus(s Status) {
	select {
	case c.status <- s:
	default:
		// See Status()'s doc comment: a full status channel means the
		// reader has fallen behind, and blocking here would wedge the
		// whole run loop over a channel nothing is actively reading -
		// worse than the reader simply missing one transition.
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
