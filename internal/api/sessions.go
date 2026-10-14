package api

import (
	"context"
	"net/http"
)

// Session is a server-side application session (API.md, "Sessions"):
// the lease that file transfers hang off. An attachment lives only as
// long as its sender's session does, so a client keeps one alive with
// Heartbeat for as long as it runs and ends it with TerminateSession on
// the way out.
type Session struct {
	ID        string `json:"session_id"`
	ExpiresAt string `json:"expires_at"`
}

// CreateSession starts a new session for the caller.
func (c *Client) CreateSession(ctx context.Context, accessToken string) (*Session, error) {
	var s Session
	if err := c.do(ctx, http.MethodPost, "/sessions", accessToken, nil, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// Heartbeat extends the session's lease. A 404 (see IsNotFound) means the
// session is gone - expired or terminated - and the caller should start a
// new one.
func (c *Client) Heartbeat(ctx context.Context, accessToken, sessionID string) error {
	return c.do(ctx, http.MethodPost, "/sessions/"+sessionID+"/heartbeat", accessToken, nil, nil)
}

// TerminateSession ends the session, which makes the server delete the
// blobs of every attachment this user sent through it.
func (c *Client) TerminateSession(ctx context.Context, accessToken, sessionID, reason string) error {
	return c.do(ctx, http.MethodPost, "/sessions/"+sessionID+"/terminate", accessToken,
		map[string]string{"idempotency_key": "terminate:" + sessionID, "reason": reason}, nil)
}
