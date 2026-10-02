package api

import (
	"context"
	"net/http"
)

// NodeStatus is the state of a relationship between two users.
type NodeStatus string

const (
	NodeStatusPending NodeStatus = "PENDING"
	NodeStatusActive  NodeStatus = "ACTIVE"
	NodeStatusBlocked NodeStatus = "BLOCKED"
)

// NodeRelationship always resolves both sides to real usernames (and
// GitHub badges) - unlike the Feed, there's no anonymization case
// here: this always requires auth and only ever returns rows
// involving the caller. The caller is responsible for determining
// "which side is me" by comparing RequesterID/TargetID against its
// own user ID - this type doesn't flag that itself.
type NodeRelationship struct {
	RequesterID             string     `json:"requester_id"`
	RequesterUsername       string     `json:"requester_username"`
	RequesterGitHubVerified bool       `json:"requester_github_verified"`
	RequesterGitHubHandle   string     `json:"requester_github_handle"`
	TargetID                string     `json:"target_id"`
	TargetUsername          string     `json:"target_username"`
	TargetGitHubVerified    bool       `json:"target_github_verified"`
	TargetGitHubHandle      string     `json:"target_github_handle"`
	Status                  NodeStatus `json:"status"`
	CreatedAt               string     `json:"created_at"`
}

// ListNodes fetches every relationship (any status) involving the
// caller, newest first - both incoming and outgoing.
func (c *Client) ListNodes(ctx context.Context, accessToken string) ([]NodeRelationship, error) {
	var resp []NodeRelationship
	err := c.do(ctx, http.MethodGet, "/nodes", accessToken, nil, &resp)
	return resp, err
}

// SendNodeRequest sends a connection request to targetUsername. If
// they already sent one to the caller, the server auto-accepts
// instead of creating a redundant second pending row - still returns
// nil error either way, but the resulting relationship may already be
// ACTIVE rather than PENDING; call ListNodes to see which.
func (c *Client) SendNodeRequest(ctx context.Context, accessToken, targetUsername string) error {
	return c.do(ctx, http.MethodPost, "/nodes/requests", accessToken,
		map[string]string{"target_username": targetUsername}, nil)
}

// AcceptNodeRequest accepts a pending request from requesterID (a user
// ID, not a username - see NodeRelationship.RequesterID). Only the
// target of that request can accept it.
func (c *Client) AcceptNodeRequest(ctx context.Context, accessToken, requesterID string) error {
	return c.do(ctx, http.MethodPost, "/nodes/"+requesterID+"/accept", accessToken, nil, nil)
}

// BlockNode blocks otherID (a user ID) at any time, regardless of the
// current relationship status, or with no prior relationship at all.
// No notification is ever sent to the blocked party - by design.
func (c *Client) BlockNode(ctx context.Context, accessToken, otherID string) error {
	return c.do(ctx, http.MethodPost, "/nodes/"+otherID+"/block", accessToken, nil, nil)
}
