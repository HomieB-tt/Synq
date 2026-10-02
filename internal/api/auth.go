package api

import (
	"context"
	"net/http"
)

// TokenPair is what register/verify and login/verify both return.
type TokenPair struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
}

type challengeResponse struct {
	Challenge string `json:"challenge"`
}

// RegisterChallenge is step one of registering a new username. The
// returned challenge is single-use and short-lived - redeem it
// promptly with RegisterVerify.
func (c *Client) RegisterChallenge(ctx context.Context, username string) (string, error) {
	var resp challengeResponse
	err := c.do(ctx, http.MethodPost, "/auth/register/challenge", "",
		map[string]string{"username": username}, &resp)
	return resp.Challenge, err
}

// RegisterVerify is step two of registering a new username. signatureHex
// must be computed over challenge's UTF-8 bytes exactly as received
// from RegisterChallenge - see crypto.Identity.SignChallenge, which
// does this correctly.
func (c *Client) RegisterVerify(ctx context.Context, username, pubKeyHex, boxPubKeyHex, signatureHex string) (*TokenPair, error) {
	var resp TokenPair
	err := c.do(ctx, http.MethodPost, "/auth/register/verify", "", map[string]string{
		"username":    username,
		"pub_key":     pubKeyHex,
		"box_pub_key": boxPubKeyHex,
		"signature":   signatureHex,
	}, &resp)
	if err != nil {
		return nil, err
	}
	return &resp, nil
}

// LoginChallenge is step one of logging in to an existing username.
func (c *Client) LoginChallenge(ctx context.Context, username string) (string, error) {
	var resp challengeResponse
	err := c.do(ctx, http.MethodPost, "/auth/login/challenge", "",
		map[string]string{"username": username}, &resp)
	return resp.Challenge, err
}

// LoginVerify is step two of logging in. Unlike RegisterVerify, no
// pub_key is sent - the server checks the signature against the key
// already on file for this username.
func (c *Client) LoginVerify(ctx context.Context, username, signatureHex string) (*TokenPair, error) {
	var resp TokenPair
	err := c.do(ctx, http.MethodPost, "/auth/login/verify", "", map[string]string{
		"username":  username,
		"signature": signatureHex,
	}, &resp)
	if err != nil {
		return nil, err
	}
	return &resp, nil
}

// Refresh exchanges a refresh token for a new access token. The same
// refresh token remains valid for further renewals - this does not
// rotate it. Returns an *APIError with StatusCode 401 if the refresh
// token is invalid, expired, or has been revoked (see IsUnauthorized).
func (c *Client) Refresh(ctx context.Context, refreshToken string) (string, error) {
	var resp struct {
		AccessToken string `json:"access_token"`
	}
	err := c.do(ctx, http.MethodPost, "/auth/refresh", "",
		map[string]string{"refresh_token": refreshToken}, &resp)
	return resp.AccessToken, err
}

// Logout revokes one specific session (the one that issued
// refreshToken). Idempotent - safe to call with an already-invalid
// token, and always succeeds from the caller's point of view. Does
// NOT invalidate that session's current access token, which keeps
// working until its own TTL runs out - see synq-server's API.md.
func (c *Client) Logout(ctx context.Context, refreshToken string) error {
	return c.do(ctx, http.MethodPost, "/auth/logout", "",
		map[string]string{"refresh_token": refreshToken}, nil)
}

// LogoutAll revokes every session for the calling user at once.
// Requires the current access token.
func (c *Client) LogoutAll(ctx context.Context, accessToken string) error {
	return c.do(ctx, http.MethodPost, "/auth/logout/all", accessToken, nil, nil)
}

// VerifyGitHub confirms a GitHub verification badge. githubAccessToken
// must already be a real token obtained by completing GitHub's OAuth
// Device Flow (see internal/github) - this call only verifies it
// against GitHub's API and records the resulting handle; it does not
// run the Device Flow itself.
func (c *Client) VerifyGitHub(ctx context.Context, accessToken, githubAccessToken string) (string, error) {
	var resp struct {
		GitHubHandle string `json:"github_handle"`
	}
	err := c.do(ctx, http.MethodPost, "/auth/github/verify", accessToken,
		map[string]string{"github_access_token": githubAccessToken}, &resp)
	return resp.GitHubHandle, err
}
