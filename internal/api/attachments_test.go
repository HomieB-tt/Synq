package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// recorder is a fake synq-server that remembers the last request.
type recorder struct {
	method, path, query, auth, ctype string
	contentLength                    int64
	body                             []byte
}

func newServer(t *testing.T, status int, response string) (*Client, *recorder) {
	t.Helper()
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.method, rec.path, rec.query = r.Method, r.URL.Path, r.URL.RawQuery
		rec.auth, rec.ctype, rec.contentLength = r.Header.Get("Authorization"), r.Header.Get("Content-Type"), r.ContentLength
		rec.body, _ = io.ReadAll(r.Body)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(response))
	}))
	t.Cleanup(srv.Close)
	return NewClient(srv.URL), rec
}

func jsonBody(t *testing.T, rec *recorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.body, &m); err != nil {
		t.Fatalf("request body %q is not JSON: %v", rec.body, err)
	}
	return m
}

func TestCreateSessionHeartbeatTerminate(t *testing.T) {
	c, rec := newServer(t, 200, `{"session_id":"ses-1","expires_at":"2026-10-04T17:30:00Z"}`)
	s, err := c.CreateSession(context.Background(), "tok")
	if err != nil || s.ID != "ses-1" {
		t.Fatalf("CreateSession = %+v, %v", s, err)
	}
	if rec.method != "POST" || rec.path != "/sessions" || rec.auth != "Bearer tok" {
		t.Errorf("request = %+v", rec)
	}

	if err := c.Heartbeat(context.Background(), "tok", "ses-1"); err != nil || rec.path != "/sessions/ses-1/heartbeat" {
		t.Errorf("Heartbeat: %v, path %s", err, rec.path)
	}

	if err := c.TerminateSession(context.Background(), "tok", "ses-1", "user_quit"); err != nil {
		t.Fatal(err)
	}
	b := jsonBody(t, rec)
	if rec.path != "/sessions/ses-1/terminate" || b["reason"] != "user_quit" || b["idempotency_key"] != "terminate:ses-1" {
		t.Errorf("terminate request: %s %v", rec.path, b)
	}
}

func TestHeartbeatNotFoundIsDetectable(t *testing.T) {
	c, _ := newServer(t, 404, `{"error":"session not found or not active"}`)
	err := c.Heartbeat(context.Background(), "tok", "ses-1")
	if !IsNotFound(err) {
		t.Errorf("err = %v, want a 404 APIError", err)
	}
}

func TestOfferAttachment(t *testing.T) {
	c, rec := newServer(t, 200, `{"attachment_id":"att-1","blob_id":"blob-1"}`)
	resp, err := c.OfferAttachment(context.Background(), "tok", OfferRequest{
		RecipientID: "usr-2", SessionID: "ses-1", BlobSize: 99,
		CiphertextSHA256: "sha256:" + strings.Repeat("a", 64),
		Encryption:       EncryptionMetadata{Algorithm: "AES-256-GCM-chunked", Chunk: 65536},
	})
	if err != nil || resp.AttachmentID != "att-1" {
		t.Fatalf("OfferAttachment = %+v, %v", resp, err)
	}
	b := jsonBody(t, rec)
	if rec.path != "/attachments/offer" || b["recipient_id"] != "usr-2" || b["session_id"] != "ses-1" || b["blob_size"] != float64(99) {
		t.Errorf("offer request: %v", b)
	}
	// Nothing secret goes in the server-visible metadata.
	if raw, _ := json.Marshal(b["encryption_metadata"]); strings.Contains(string(raw), "key") && !strings.Contains(string(raw), "chunk") {
		t.Errorf("metadata looks like it carries a key: %s", raw)
	}
}

func TestOfferNotFoundIsDetectable(t *testing.T) {
	c, _ := newServer(t, 404, `{"error":"not found"}`)
	if _, err := c.OfferAttachment(context.Background(), "tok", OfferRequest{}); !IsNotFound(err) {
		t.Errorf("err = %v", err)
	}
}

func TestUploadSendsExactContentLengthAndBody(t *testing.T) {
	c, rec := newServer(t, 204, ``)
	payload := bytes.Repeat([]byte("c"), 300_000)
	// A plain io.Reader, like a real file stream: net/http can't work out
	// the length of one of these by itself.
	err := c.UploadAttachment(context.Background(), "tok", "att-1", io.MultiReader(bytes.NewReader(payload)), int64(len(payload)))
	if err != nil {
		t.Fatal(err)
	}
	if rec.method != "POST" || rec.path != "/attachments/att-1/upload" || rec.auth != "Bearer tok" {
		t.Errorf("request = %+v", rec)
	}
	if rec.contentLength != int64(len(payload)) {
		t.Errorf("Content-Length = %d, want %d (chunked uploads defeat the server's size check)", rec.contentLength, len(payload))
	}
	if !bytes.Equal(rec.body, payload) {
		t.Error("body was altered in transit")
	}
	if rec.ctype != "application/octet-stream" {
		t.Errorf("Content-Type = %q", rec.ctype)
	}
}

func TestUploadErrorsCarryTheStatus(t *testing.T) {
	for status, check := range map[int]func(error) bool{404: IsNotFound, 409: IsConflict} {
		c, _ := newServer(t, status, `{"error":"nope"}`)
		err := c.UploadAttachment(context.Background(), "tok", "att-1", strings.NewReader("x"), 1)
		if !check(err) {
			t.Errorf("status %d: err = %v", status, err)
		}
	}
}

func TestClaimAndStartDownload(t *testing.T) {
	c, rec := newServer(t, 204, ``)
	if err := c.ClaimAttachment(context.Background(), "tok", "att-1", "ses-2"); err != nil {
		t.Fatal(err)
	}
	b := jsonBody(t, rec)
	if rec.path != "/attachments/att-1/claim" || b["session_id"] != "ses-2" || b["idempotency_key"] != "claim:att-1:ses-2" {
		t.Errorf("claim: %s %v", rec.path, b)
	}

	c, rec = newServer(t, 200, `{"download_id":"dl-1","blob_size":4096}`)
	ds, err := c.StartDownload(context.Background(), "tok", "att-1", "ses-2", 3)
	if err != nil || ds.DownloadID != "dl-1" || ds.BlobSize != 4096 {
		t.Fatalf("StartDownload = %+v, %v", ds, err)
	}
	if b := jsonBody(t, rec); b["idempotency_key"] != "download:att-1:ses-2:3" {
		t.Errorf("idempotency key = %v", b["idempotency_key"])
	}
}

func TestDownloadBlobStreamsAndEscapesTheQuery(t *testing.T) {
	c, rec := newServer(t, 200, "ciphertext-bytes")
	rc, err := c.DownloadBlob(context.Background(), "tok", "att-1", "dl 1&x=y")
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	if string(got) != "ciphertext-bytes" {
		t.Errorf("body = %q", got)
	}
	if rec.method != "GET" || rec.path != "/attachments/att-1/blob" || rec.query != "download_id=dl+1%26x%3Dy" {
		t.Errorf("request = %+v", rec)
	}
}

func TestDownloadBlobErrorIsAnAPIError(t *testing.T) {
	c, _ := newServer(t, 409, `{"error":"attachment is in state CLAIMED, expected DOWNLOADING"}`)
	if _, err := c.DownloadBlob(context.Background(), "tok", "att-1", "dl-1"); !IsConflict(err) {
		t.Errorf("err = %v", err)
	}
}

func TestCompleteSaysAuthenticatedAndCarriesTheSession(t *testing.T) {
	c, rec := newServer(t, 204, ``)
	hash := "sha256:" + strings.Repeat("b", 64)
	if err := c.CompleteDownload(context.Background(), "tok", "att-1", "ses-2", "dl-1", 4096, hash); err != nil {
		t.Fatal(err)
	}
	b := jsonBody(t, rec)
	if rec.path != "/attachments/att-1/download/complete" ||
		b["session_id"] != "ses-2" || b["download_id"] != "dl-1" || b["received_bytes"] != float64(4096) ||
		b["ciphertext_sha256"] != hash || b["plaintext_authenticated"] != true {
		t.Errorf("complete: %v", b)
	}
}

// The server's open, ignore and cancel handlers all require a session_id
// and an idempotency_key even though API.md's examples omit them; sending
// bodyless requests gets a 422.
func TestOpenIgnoreCancelCarryTheFieldsTheServerRequires(t *testing.T) {
	ctx := context.Background()
	cases := map[string]func(*Client) error{
		"/attachments/att-1/open":            func(c *Client) error { return c.MarkOpened(ctx, "tok", "att-1", "ses-2") },
		"/attachments/att-1/ignore":          func(c *Client) error { return c.IgnoreAttachment(ctx, "tok", "att-1", "ses-2") },
		"/attachments/att-1/download/cancel": func(c *Client) error { return c.CancelDownload(ctx, "tok", "att-1", "ses-2", "dl-1") },
	}
	for path, call := range cases {
		c, rec := newServer(t, 204, ``)
		if err := call(c); err != nil {
			t.Errorf("%s: %v", path, err)
			continue
		}
		b := jsonBody(t, rec)
		if rec.path != path || b["session_id"] != "ses-2" || b["idempotency_key"] == "" || b["idempotency_key"] == nil {
			t.Errorf("%s: request %s %v", path, rec.path, b)
		}
	}
}
