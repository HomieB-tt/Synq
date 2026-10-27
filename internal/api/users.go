package api

import (
	"context"
	"net/http"
	"net/url"
)

// PublicKeys is a user's discoverable identity: PubKey (Ed25519) is
// what a safety-number-style fingerprint check is computed from
// (see crypto.Fingerprint); BoxPubKey (X25519) is what E2EE is
// actually computed against. BoxPubKey is empty if that user
// registered before synq-server tracked it - a client seeing an empty
// BoxPubKey knows it cannot start an encrypted session with them yet.
type PublicKeys struct {
	UserID    string `json:"user_id"`
	Username  string `json:"username"`
	PubKey    string `json:"pub_key"`
	BoxPubKey string `json:"box_pub_key"`

	// BoxKeySig is the owner's Ed25519 signature binding BoxPubKey to
	// Username (see crypto.VerifyBoxKeyBinding). Empty for accounts that
	// predate it and haven't uploaded one. Callers must verify it before
	// trusting BoxPubKey - the server is not trusted to have done so.
	BoxKeySig string `json:"box_key_sig"`
}

// UploadBoxKeySig attaches a box-key binding signature to the caller's
// own account. For accounts registered before the server stored them;
// idempotent, since Ed25519 signatures are deterministic. Requires auth.
func (c *Client) UploadBoxKeySig(ctx context.Context, accessToken, boxKeySigHex string) error {
	return c.do(ctx, http.MethodPut, "/users/me/box-key-sig", accessToken,
		map[string]string{"box_key_sig": boxKeySigHex}, nil)
}

// DeleteAccount deletes the caller's account on the server for good:
// nodes, posts, sessions, files and rooms go with it, and the username is
// free to claim again. confirm must be the account's exact username.
func (c *Client) DeleteAccount(ctx context.Context, accessToken, confirm string) error {
	return c.do(ctx, http.MethodDelete, "/users/me", accessToken,
		map[string]string{"confirm": confirm}, nil)
}

// GetPublicKeys looks up username's public keys. Requires auth - a
// public key is meant to be discoverable by definition, but looking
// one up is only useful to someone who has their own identity to pair
// it with, so there's no unauthenticated path here. Returns an
// *APIError with StatusCode 404 (see IsNotFound) if no such user
// exists.
func (c *Client) GetPublicKeys(ctx context.Context, accessToken, username string) (*PublicKeys, error) {
	var resp PublicKeys
	path := "/users/" + url.PathEscape(username) + "/keys"
	err := c.do(ctx, http.MethodGet, path, accessToken, nil, &resp)
	if err != nil {
		return nil, err
	}
	return &resp, nil
}
