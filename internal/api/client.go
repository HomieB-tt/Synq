// Package api is a thin REST client for synq-server. See that
// project's API.md for the full, authoritative contract - every
// method here corresponds directly to one endpoint documented there,
// and is named to match.
//
// Like internal/github, this package does no retrying, caching, or
// state management of its own - callers (internal/app's Bubble Tea
// Update loop) own retry policy, token storage, and when to call what.
// Every method takes whatever access token it needs as an explicit
// parameter rather than this package holding one itself, for the same
// reason internal/github's functions take clientID/deviceCode/token as
// parameters: it keeps this package safe to call concurrently from
// multiple in-flight tea.Cmds without any shared mutable state to
// guard.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// maxResponseBytes bounds how much of any API response body is read
// into memory. The largest legitimate JSON response (a full page of
// feed posts) is around a megabyte; a server sending more than this is
// misbehaving, and an unbounded read would let it exhaust the client's
// memory. File transfer, when added, must stream rather than use this
// path.
const maxResponseBytes = 8 << 20

// Client talks to one synq-server deployment, identified by BaseURL
// (e.g. "https://synq-server-production.up.railway.app", or
// "http://localhost:8080" for local development - see
// synq-server/DEPLOYMENT.md).
type Client struct {
	BaseURL string
	http    *http.Client
}

// NewClient builds a Client for the given base URL. A trailing slash,
// if present, is trimmed, so callers don't need to worry about
// whether SYNQ_SERVER_URL was configured with or without one.
func NewClient(baseURL string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: 15 * time.Second},
	}
}

// APIError is returned for any non-2xx response synq-server sends
// back. Every such response is {"error": "<message>"} per API.md's
// conventions - Message is that string, or the raw response body if
// it didn't parse as JSON for some reason (shouldn't happen against a
// well-behaved server, but better than swallowing the body silently).
type APIError struct {
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("api: server returned %d: %s", e.StatusCode, e.Message)
}

// IsNotFound reports whether err is an *APIError with a 404 status -
// a convenience for the common "does this exist" check, e.g. an
// unknown username. Safe to call with a nil err, or one that isn't an
// *APIError at all (a network-level failure, say) - both just return
// false, matching errors.As's own nil-safety.
func IsNotFound(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound
}

// IsUnauthorized reports whether err is an *APIError with a 401
// status - typically an expired or revoked token, the caller's signal
// to attempt a refresh (see synq-server's API.md on access-token
// lifetimes) rather than surface the error directly.
func IsUnauthorized(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusUnauthorized
}

// IsRateLimited reports whether err is an *APIError with a 429 status.
func IsRateLimited(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusTooManyRequests
}

// errorResponse mirrors synq-server's uniform error body shape.
type errorResponse struct {
	Error string `json:"error"`
}

// do is the shared request/response plumbing every endpoint method
// below is a thin wrapper around: encode reqBody as JSON (if any) and
// attach it, set the Authorization header (if accessToken is
// non-empty), send the request, and either decode the 2xx response
// into respBody or return an *APIError describing the failure.
//
// reqBody and respBody may each be nil - a nil reqBody means a
// bodyless request (most GETs, and a few POSTs like /auth/logout/all);
// a nil respBody means "I don't need anything back", for the many
// endpoints that just return 204 with no body.
func (c *Client) do(ctx context.Context, method, path, accessToken string, reqBody, respBody any) error {
	var bodyReader io.Reader
	if reqBody != nil {
		encoded, err := json.Marshal(reqBody)
		if err != nil {
			return fmt.Errorf("api: encode request body: %w", err)
		}
		bodyReader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, bodyReader)
	if err != nil {
		return fmt.Errorf("api: build request: %w", err)
	}
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if accessToken != "" {
		req.Header.Set("Authorization", "Bearer "+accessToken)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("api: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("api: read response body for %s %s: %w", method, path, err)
	}
	if len(body) > maxResponseBytes {
		return fmt.Errorf("api: response for %s %s is larger than %d bytes", method, path, maxResponseBytes)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := string(body)
		var parsed errorResponse
		if json.Unmarshal(body, &parsed) == nil && parsed.Error != "" {
			msg = parsed.Error
		}
		return &APIError{StatusCode: resp.StatusCode, Message: msg}
	}

	if respBody != nil && len(body) > 0 {
		if err := json.Unmarshal(body, respBody); err != nil {
			return fmt.Errorf("api: parse response for %s %s: %w (body: %s)", method, path, err, string(body))
		}
	}
	return nil
}
