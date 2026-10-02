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
