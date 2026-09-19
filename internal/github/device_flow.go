// Package github implements the client side of GitHub's OAuth Device
// Flow, used only for the optional verification badge described in
// synq-server-DESIGN.md section 1 - it is unrelated to Synq's own
// account system and grants nothing beyond that badge.
//
// The Device Flow is the right fit here (rather than a browser-redirect
// OAuth flow) because Synq is a terminal application with no local HTTP
// server to catch a redirect - see synq-DESIGN.md section 9.
package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	deviceCodeURL  = "https://github.com/login/device/code"
	accessTokenURL = "https://github.com/login/oauth/access_token"
	userURL        = "https://api.github.com/user"

	// Scope requests read-only access to the user's public profile -
	// just enough to confirm a username, nothing more.
	scope = "read:user"
)

// DeviceCode is GitHub's response to starting the Device Flow: a code
// for Synq to keep polling with, and a code for the human to type into
// their browser.
type DeviceCode struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

// PollStatus is the outcome of a single poll attempt.
type PollStatus string

const (
	PollPending  PollStatus = "pending"
	PollSuccess  PollStatus = "success"
	PollSlowDown PollStatus = "slow_down"
	PollExpired  PollStatus = "expired"
	PollDenied   PollStatus = "denied"
)

// PollResult is the outcome of a single poll of the access token
// endpoint. Token is only set when Status is PollSuccess. Interval is
// only set (to a new, larger value) when Status is PollSlowDown - per
// GitHub's spec, callers must widen their polling interval by this
// amount when told to slow down, or risk being rate-limited entirely.
type PollResult struct {
	Status   PollStatus
	Token    string
	Interval int
}

// User is the subset of GitHub's user API response Synq actually uses.
type User struct {
	Login string `json:"login"`
}

// httpClient is a package-level var (not the default client directly)
// so tests can point it at anything without touching global state
// elsewhere in the program.
var httpClient = &http.Client{Timeout: 15 * time.Second}

// RequestDeviceCode starts the Device Flow for the given OAuth App
// client ID.
func RequestDeviceCode(ctx context.Context, clientID string) (*DeviceCode, error) {
	form := url.Values{
		"client_id": {clientID},
		"scope":     {scope},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, deviceCodeURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("github: build device code request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github: request device code: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("github: read device code response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github: device code request failed (%d): %s", resp.StatusCode, string(body))
	}

	var dc DeviceCode
	if err := json.Unmarshal(body, &dc); err != nil {
		return nil, fmt.Errorf("github: parse device code response: %w (body: %s)", err, string(body))
	}
	if dc.DeviceCode == "" || dc.UserCode == "" {
		// GitHub returns HTTP 200 with an {"error": "..."} body for
		// several failure cases (e.g. an invalid client ID), rather
		// than a non-200 status. Treat a response with no actual
		// device code as that kind of error.
		return nil, fmt.Errorf("github: device code request did not return a device code (body: %s)", string(body))
	}

	return &dc, nil
}

// pollErrorResponse is the shape of GitHub's {"error": "..."} body,
// returned with HTTP 200 for expected poll states (pending, slow_down,
// expired, denied) as well as genuine failures.
type pollErrorResponse struct {
	Error    string `json:"error"`
	Interval int    `json:"interval"`
}

type pollSuccessResponse struct {
	AccessToken string `json:"access_token"`
}

// PollOnce makes a single poll of the access token endpoint and
// reports the result. Callers are responsible for waiting the
// device code's Interval (or the widened interval from a prior
// PollSlowDown result) between calls - this function does not sleep
// or retry on its own, so it can be driven from a Bubble Tea tea.Cmd /
// tea.Tick loop without blocking anything.
func PollOnce(ctx context.Context, clientID, deviceCode string) (*PollResult, error) {
	form := url.Values{
		"client_id":   {clientID},
		"device_code": {deviceCode},
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, accessTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("github: build poll request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github: poll request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("github: read poll response: %w", err)
	}

	// Try the success shape first.
	var success pollSuccessResponse
	if err := json.Unmarshal(body, &success); err == nil && success.AccessToken != "" {
		return &PollResult{Status: PollSuccess, Token: success.AccessToken}, nil
	}

	var errResp pollErrorResponse
	if err := json.Unmarshal(body, &errResp); err != nil {
		return nil, fmt.Errorf("github: parse poll response: %w (body: %s)", err, string(body))
	}

	switch errResp.Error {
	case "authorization_pending":
		return &PollResult{Status: PollPending}, nil
	case "slow_down":
		return &PollResult{Status: PollSlowDown, Interval: errResp.Interval}, nil
	case "expired_token":
		return &PollResult{Status: PollExpired}, nil
	case "access_denied":
		return &PollResult{Status: PollDenied}, nil
	default:
		return nil, fmt.Errorf("github: unexpected poll response (body: %s)", string(body))
	}
}

// FetchUser retrieves the authenticated user's GitHub login using a
// token obtained from a successful PollOnce.
func FetchUser(ctx context.Context, token string) (*User, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, userURL, nil)
	if err != nil {
		return nil, fmt.Errorf("github: build user request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github: user request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("github: read user response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github: user request failed (%d): %s", resp.StatusCode, string(body))
	}

	var u User
	if err := json.Unmarshal(body, &u); err != nil {
		return nil, fmt.Errorf("github: parse user response: %w (body: %s)", err, string(body))
	}
	if u.Login == "" {
		return nil, fmt.Errorf("github: user response had no login (body: %s)", string(body))
	}

	return &u, nil
}
