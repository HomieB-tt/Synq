package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// newTestServer and newTestClient together stand in for a real
// synq-server for every test in this package - nothing here ever
// makes a network call to a real deployment. See
// synq-server/scripts/smoke_test.py's own doc comment on why a real
// server (even a disposable one) is the wrong tool for exercising a
// client library's request-building/response-parsing logic in
// isolation.
func newTestServer(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return NewClient(srv.URL)
}

func ctx(t *testing.T) context.Context {
	t.Helper()
	c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return c
}

func TestRegisterVerifySendsExactFieldsAndParsesTokens(t *testing.T) {
	var gotPath, gotAuth string
	var gotBody map[string]string

	client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(TokenPair{AccessToken: "access-123", RefreshToken: "refresh-456"})
	})

	tokens, err := client.RegisterVerify(ctx(t), "alice", "pubkeyhex", "boxkeyhex", "sighex")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if gotPath != "/auth/register/verify" {
		t.Errorf("path = %q, want /auth/register/verify", gotPath)
	}
	if gotAuth != "" {
		t.Errorf("expected no Authorization header on register/verify (pre-auth), got %q", gotAuth)
	}
	wantBody := map[string]string{
		"username": "alice", "pub_key": "pubkeyhex", "box_pub_key": "boxkeyhex", "signature": "sighex",
	}
	for k, v := range wantBody {
		if gotBody[k] != v {
			t.Errorf("request body[%q] = %q, want %q", k, gotBody[k], v)
		}
	}
	if tokens.AccessToken != "access-123" || tokens.RefreshToken != "refresh-456" {
		t.Errorf("got tokens %+v, want access-123/refresh-456", tokens)
	}
}

func TestLogoutAllSendsBearerTokenAndNoBody(t *testing.T) {
	var gotAuth, gotMethod string
	var bodyWasEmpty bool

	client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotMethod = r.Method
		buf := make([]byte, 1)
		n, _ := r.Body.Read(buf)
		bodyWasEmpty = n == 0
		w.WriteHeader(http.StatusNoContent)
	})

	if err := client.LogoutAll(ctx(t), "my-access-token"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotAuth != "Bearer my-access-token" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer my-access-token")
	}
	if !bodyWasEmpty {
		t.Error("expected an empty body for LogoutAll")
	}
}

func TestListFeedBuildsQueryParamsAndParsesArray(t *testing.T) {
	var gotQuery string

	client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]Post{
			{ID: "p1", Author: "node", Category: CategoryGeneral},
		})
	})

	posts, err := client.ListFeed(ctx(t), "", 10, 20)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotQuery != "limit=10&offset=20" {
		t.Errorf("query = %q, want limit=10&offset=20", gotQuery)
	}
	if len(posts) != 1 || posts[0].ID != "p1" || posts[0].Author != "node" {
		t.Errorf("got posts %+v", posts)
	}
}

func TestReportCommentInterpolatesBothIDsInPath(t *testing.T) {
	var gotPath string

	client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	})

	if err := client.ReportComment(ctx(t), "tok", "post-1", "comment-2", "spam"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "/feed/post-1/comments/comment-2/report"
	if gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
}

func TestErrorResponseBecomesAPIError(t *testing.T) {
	client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "already connected"})
	})

	err := client.SendNodeRequest(ctx(t), "tok", "bob")
	if err == nil {
		t.Fatal("expected an error")
	}

	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("expected *APIError, got %T: %v", err, err)
	}
	if apiErr.StatusCode != http.StatusConflict {
		t.Errorf("StatusCode = %d, want 409", apiErr.StatusCode)
	}
	if apiErr.Message != "already connected" {
		t.Errorf("Message = %q, want %q", apiErr.Message, "already connected")
	}
}

func TestIsNotFoundIsUnauthorizedIsRateLimited(t *testing.T) {
	notFound := &APIError{StatusCode: http.StatusNotFound}
	unauthorized := &APIError{StatusCode: http.StatusUnauthorized}
	rateLimited := &APIError{StatusCode: http.StatusTooManyRequests}
	other := &APIError{StatusCode: http.StatusInternalServerError}

	if !IsNotFound(notFound) || IsNotFound(unauthorized) || IsNotFound(other) {
		t.Error("IsNotFound did not discriminate correctly")
	}
	if !IsUnauthorized(unauthorized) || IsUnauthorized(notFound) || IsUnauthorized(other) {
		t.Error("IsUnauthorized did not discriminate correctly")
	}
	if !IsRateLimited(rateLimited) || IsRateLimited(notFound) || IsRateLimited(other) {
		t.Error("IsRateLimited did not discriminate correctly")
	}
	if IsNotFound(nil) || IsUnauthorized(nil) || IsRateLimited(nil) {
		t.Error("Is* helpers should return false for a nil error, not panic")
	}
	// A non-*APIError (e.g. a network-level error) should never match.
	if IsNotFound(context.DeadlineExceeded) {
		t.Error("Is* helpers should not match a non-*APIError")
	}
}

func TestGetPublicKeysEscapesUsernameInPath(t *testing.T) {
	var gotPath string

	client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(PublicKeys{Username: "weird user"})
	})

	_, err := client.GetPublicKeys(ctx(t), "tok", "weird user")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// url.PathEscape turns a space into %20, not "+" (that's query-string
	// escaping) - confirms the right escaping function was used for a
	// path segment specifically.
	if gotPath != "/users/weird%20user/keys" {
		t.Errorf("path = %q, want /users/weird%%20user/keys", gotPath)
	}
}
