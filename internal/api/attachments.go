package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
)

// EncryptionMetadata is the opaque blob the offer carries for the
// server to store. The server never interprets it, and this client puts
// nothing secret in it: the file key and filename travel inside the
// end-to-end encrypted chat channel instead (see internal/transfer).
type EncryptionMetadata struct {
	Algorithm string `json:"algorithm"`
	Chunk     int    `json:"chunk_size"`
}

// OfferRequest is the body of POST /attachments/offer.
type OfferRequest struct {
	RecipientID      string             `json:"recipient_id"`
	SessionID        string             `json:"session_id"`
	BlobSize         int64              `json:"blob_size"`
	CiphertextSHA256 string             `json:"ciphertext_sha256"`
	Encryption       EncryptionMetadata `json:"encryption_metadata"`
}

// OfferResponse is what the server returns for an offer.
type OfferResponse struct {
	AttachmentID string `json:"attachment_id"`
	BlobID       string `json:"blob_id"`
}

// OfferAttachment registers an attachment for recipientID, who must be
// an ACTIVE node of the caller. A 404 means the server won't say whether
// that is because they aren't one, or don't exist.
func (c *Client) OfferAttachment(ctx context.Context, accessToken string, req OfferRequest) (*OfferResponse, error) {
	var resp OfferResponse
	if err := c.do(ctx, http.MethodPost, "/attachments/offer", accessToken, req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// UploadAttachment streams size bytes from body as the attachment's
// ciphertext. size must equal the offer's blob_size exactly; the server
// rejects anything else. The body is streamed, never read into memory.
func (c *Client) UploadAttachment(ctx context.Context, accessToken, attachmentID string, body io.Reader, size int64) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.BaseURL+"/attachments/"+attachmentID+"/upload", io.NopCloser(body))
	if err != nil {
		return fmt.Errorf("api: build upload request: %w", err)
	}
	// Set explicitly: for an arbitrary reader net/http would otherwise
	// fall back to chunked encoding with no Content-Length, which the
	// server's early size check can't use.
	req.ContentLength = size
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := c.stream.Do(req)
	if err != nil {
		return fmt.Errorf("api: upload: %w", err)
	}
	defer resp.Body.Close()
	return statusError(resp)
}

// ClaimAttachment moves an offered attachment to CLAIMED for this
// recipient. 409 means the sender's upload hasn't finished yet.
func (c *Client) ClaimAttachment(ctx context.Context, accessToken, attachmentID, sessionID string) error {
	return c.do(ctx, http.MethodPost, "/attachments/"+attachmentID+"/claim", accessToken,
		map[string]string{
			"session_id":      sessionID,
			"idempotency_key": "claim:" + attachmentID + ":" + sessionID,
		}, nil)
}

// DownloadStart is the response to StartDownload.
type DownloadStart struct {
	DownloadID string `json:"download_id"`
	BlobSize   int64  `json:"blob_size"`
}

// StartDownload begins (or, with a higher attempt number, restarts) the
// download. attempt feeds the idempotency key, so a retry after a
// failure uses a new one rather than replaying the old result.
func (c *Client) StartDownload(ctx context.Context, accessToken, attachmentID, sessionID string, attempt int) (*DownloadStart, error) {
	var ds DownloadStart
	err := c.do(ctx, http.MethodPost, "/attachments/"+attachmentID+"/download/start", accessToken,
		map[string]string{
			"session_id":      sessionID,
			"idempotency_key": "download:" + attachmentID + ":" + sessionID + ":" + strconv.Itoa(attempt),
		}, &ds)
	if err != nil {
		return nil, err
	}
	return &ds, nil
}

// DownloadBlob opens the ciphertext stream. The caller must Close it,
// and must check it read exactly the size StartDownload reported - a
// stream that ends early is not an error at this layer.
func (c *Client) DownloadBlob(ctx context.Context, accessToken, attachmentID, downloadID string) (io.ReadCloser, error) {
	q := url.Values{"download_id": {downloadID}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.BaseURL+"/attachments/"+attachmentID+"/blob?"+q.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("api: build download request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := c.stream.Do(req)
	if err != nil {
		return nil, fmt.Errorf("api: download: %w", err)
	}
	if err := statusError(resp); err != nil {
		resp.Body.Close()
		return nil, err
	}
	return resp.Body, nil
}

// CompleteDownload tells the server the ciphertext arrived intact and
// authenticated. The server checks size and hash against the sender's
// commitment and refuses (400) if either differs. Call it only after the
// file has been decrypted successfully: the request asserts exactly that.
func (c *Client) CompleteDownload(ctx context.Context, accessToken, attachmentID, sessionID, downloadID string, receivedBytes int64, ciphertextSHA256 string) error {
	return c.do(ctx, http.MethodPost, "/attachments/"+attachmentID+"/download/complete", accessToken,
		map[string]any{
			"session_id":              sessionID,
			"download_id":             downloadID,
			"received_bytes":          receivedBytes,
			"ciphertext_sha256":       ciphertextSHA256,
			"plaintext_authenticated": true,
			"idempotency_key":         "complete:" + attachmentID + ":" + downloadID,
		}, nil)
}

// CancelDownload abandons an in-progress download.
func (c *Client) CancelDownload(ctx context.Context, accessToken, attachmentID, sessionID, downloadID string) error {
	return c.do(ctx, http.MethodPost, "/attachments/"+attachmentID+"/download/cancel", accessToken,
		map[string]string{
			"session_id":      sessionID,
			"download_id":     downloadID,
			"idempotency_key": "cancel:" + attachmentID + ":" + downloadID,
		}, nil)
}

// MarkOpened records that the recipient viewed the attachment. Only
// valid once it is AVAILABLE (409 otherwise).
func (c *Client) MarkOpened(ctx context.Context, accessToken, attachmentID, sessionID string) error {
	return c.do(ctx, http.MethodPost, "/attachments/"+attachmentID+"/open", accessToken,
		map[string]string{
			"session_id":      sessionID,
			"idempotency_key": "open:" + attachmentID + ":" + sessionID,
		}, nil)
}

// IgnoreAttachment records that the recipient declined to keep it.
func (c *Client) IgnoreAttachment(ctx context.Context, accessToken, attachmentID, sessionID string) error {
	return c.do(ctx, http.MethodPost, "/attachments/"+attachmentID+"/ignore", accessToken,
		map[string]string{
			"session_id":      sessionID,
			"idempotency_key": "ignore:" + attachmentID + ":" + sessionID,
		}, nil)
}

// statusError converts a non-2xx response into an *APIError, reading
// only a bounded amount of the body.
func statusError(resp *http.Response) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	msg := string(body)
	var parsed errorResponse
	if json.Unmarshal(body, &parsed) == nil && parsed.Error != "" {
		msg = parsed.Error
	}
	if msg == "" {
		msg = http.StatusText(resp.StatusCode)
	}
	return &APIError{StatusCode: resp.StatusCode, Message: msg}
}

// IsConflict reports whether err is an *APIError with a 409 status.
func IsConflict(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusConflict
}
