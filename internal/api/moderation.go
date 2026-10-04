package api

import (
	"context"
	"net/http"
)

// ReportedPost is the post a Report targets, if it targets a post.
type ReportedPost struct {
	ID       string `json:"id"`
	AuthorID string `json:"author_id"`
	Title    string `json:"title"`
	Content  string `json:"content"`
}

// ReportedComment is the comment a Report targets, if it targets a
// comment.
type ReportedComment struct {
	ID       string `json:"id"`
	AuthorID string `json:"author_id"`
	Content  string `json:"content"`
}

// Report is one entry in the moderation queue. Exactly one of
// Post/Comment is non-nil, matching whichever the report targets.
type Report struct {
	ID         string           `json:"id"`
	ReporterID string           `json:"reporter_id"`
	Reason     string           `json:"reason"`
	CreatedAt  string           `json:"created_at"`
	Post       *ReportedPost    `json:"post"`
	Comment    *ReportedComment `json:"comment"`
}

// ListReports fetches the full report queue, newest first. Requires
// is_moderator on the calling account - set directly in synq-server's
// Postgres, never through an API (see synq-server's DESIGN.md section
// 5) - and returns a 403 *APIError otherwise, not a 404: these
// endpoints aren't secret, just restricted.
func (c *Client) ListReports(ctx context.Context, accessToken string) ([]Report, error) {
	var resp []Report
	err := c.do(ctx, http.MethodGet, "/moderation/reports", accessToken, nil, &resp)
	return resp, err
}

// DismissReport removes one report from the queue with no action
// taken against the content - it survives.
func (c *Client) DismissReport(ctx context.Context, accessToken, reportID string) error {
	return c.do(ctx, http.MethodDelete, "/moderation/reports/"+reportID, accessToken, nil, nil)
}

// RemoveReportedContent hard-deletes whatever reportID's report
// targets (a post or a comment). Because reports cascade-delete when
// their target is removed, this also clears every OTHER outstanding
// report against that same content, not just this one.
func (c *Client) RemoveReportedContent(ctx context.Context, accessToken, reportID string) error {
	return c.do(ctx, http.MethodDelete, "/moderation/reports/"+reportID+"/content", accessToken, nil, nil)
}
