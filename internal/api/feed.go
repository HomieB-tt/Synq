package api

import (
	"context"
	"fmt"
	"net/http"
)

// Post is one Feed entry. Author is "node" - and GitHubVerified/
// GitHubHandle are zero-valued - whenever the request that fetched it
// carried no valid access token. This is a blanket rule based on
// whether anyone is authenticated, not a per-post property - see
// synq-server's API.md on the Feed's anonymization behavior.
type Post struct {
	ID             string `json:"id"`
	Category       string `json:"category"`
	Title          string `json:"title"`
	Content        string `json:"content"`
	Author         string `json:"author"`
	GitHubVerified bool   `json:"github_verified"`
	GitHubHandle   string `json:"github_handle"`
	CreatedAt      string `json:"created_at"`
}

// Comment is one comment on a Post. Same anonymization rule as Post.
type Comment struct {
	ID             string `json:"id"`
	Content        string `json:"content"`
	Author         string `json:"author"`
	GitHubVerified bool   `json:"github_verified"`
	GitHubHandle   string `json:"github_handle"`
	CreatedAt      string `json:"created_at"`
}

// Valid values for Post.Category / the category field of
// CreatePost/UpdatePost requests.
const (
	CategoryProject = "PROJECT"
	CategoryHiring  = "HIRING"
	CategoryGeneral = "GENERAL"
)

// ListFeed fetches posts, newest first. accessToken may be empty - a
// guest can read the Feed, just with every author anonymized to
// "node". limit/offset of 0 use the server's defaults (25, 0); limit
// is clamped server-side to 100, never rejected outright.
func (c *Client) ListFeed(ctx context.Context, accessToken string, limit, offset int) ([]Post, error) {
	path := "/feed"
	if limit > 0 || offset > 0 {
		path += fmt.Sprintf("?limit=%d&offset=%d", limit, offset)
	}
	var resp []Post
	err := c.do(ctx, http.MethodGet, path, accessToken, nil, &resp)
	return resp, err
}

// CreatePost publishes a new Feed post and returns its ID.
func (c *Client) CreatePost(ctx context.Context, accessToken, category, title, content string) (string, error) {
	var resp struct {
		ID string `json:"id"`
	}
	err := c.do(ctx, http.MethodPost, "/feed", accessToken, map[string]string{
		"category": category, "title": title, "content": content,
	}, &resp)
	return resp.ID, err
}

// UpdatePost replaces an existing post's content - author only.
// Returns an *APIError with StatusCode 404 (see IsNotFound) if the
// post doesn't exist or isn't yours; the two cases are
// indistinguishable by design.
func (c *Client) UpdatePost(ctx context.Context, accessToken, postID, category, title, content string) error {
	return c.do(ctx, http.MethodPatch, "/feed/"+postID, accessToken, map[string]string{
		"category": category, "title": title, "content": content,
	}, nil)
}

// DeletePost hard-deletes a post - author only, no recovery. Cascades
// to delete its comments and any reports against it.
func (c *Client) DeletePost(ctx context.Context, accessToken, postID string) error {
	return c.do(ctx, http.MethodDelete, "/feed/"+postID, accessToken, nil, nil)
}

// ReportPost files a report against a post. reason may be empty.
func (c *Client) ReportPost(ctx context.Context, accessToken, postID, reason string) error {
	return c.do(ctx, http.MethodPost, "/feed/"+postID+"/report", accessToken,
		map[string]string{"reason": reason}, nil)
}

// ListComments fetches a post's comments, oldest first. Same guest/
// anonymization rules as ListFeed.
func (c *Client) ListComments(ctx context.Context, accessToken, postID string) ([]Comment, error) {
	var resp []Comment
	err := c.do(ctx, http.MethodGet, "/feed/"+postID+"/comments", accessToken, nil, &resp)
	return resp, err
}

// CreateComment adds a comment to a post and returns its ID.
func (c *Client) CreateComment(ctx context.Context, accessToken, postID, content string) (string, error) {
	var resp struct {
		ID string `json:"id"`
	}
	err := c.do(ctx, http.MethodPost, "/feed/"+postID+"/comments", accessToken,
		map[string]string{"content": content}, &resp)
	return resp.ID, err
}

// UpdateComment replaces a comment's content - author only.
func (c *Client) UpdateComment(ctx context.Context, accessToken, postID, commentID, content string) error {
	return c.do(ctx, http.MethodPatch, "/feed/"+postID+"/comments/"+commentID, accessToken,
		map[string]string{"content": content}, nil)
}

// DeleteComment hard-deletes a comment - author only.
func (c *Client) DeleteComment(ctx context.Context, accessToken, postID, commentID string) error {
	return c.do(ctx, http.MethodDelete, "/feed/"+postID+"/comments/"+commentID, accessToken, nil, nil)
}

// ReportComment files a report against a comment. reason may be empty.
func (c *Client) ReportComment(ctx context.Context, accessToken, postID, commentID, reason string) error {
	return c.do(ctx, http.MethodPost, "/feed/"+postID+"/comments/"+commentID+"/report", accessToken,
		map[string]string{"reason": reason}, nil)
}
