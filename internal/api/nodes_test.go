package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPresenceCallsUseTheDocumentedRoutes(t *testing.T) {
	var method, path, auth, body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		method, path, auth, body = r.Method, r.URL.Path, r.Header.Get("Authorization"), string(b)
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"share":true,"online":["u1","u2"]}`))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	ctx := context.Background()

	p, err := c.GetPresence(ctx, "tok")
	if err != nil || !p.Share || len(p.Online) != 2 || p.Online[1] != "u2" {
		t.Fatalf("GetPresence: %+v %v", p, err)
	}
	if method != "GET" || path != "/nodes/presence" || auth != "Bearer tok" {
		t.Errorf("GetPresence sent %s %s auth=%q", method, path, auth)
	}

	if err := c.SetPresence(ctx, "tok", false); err != nil {
		t.Fatalf("SetPresence: %v", err)
	}
	if method != "PUT" || path != "/users/me/presence" || body != `{"share":false}` {
		t.Errorf("SetPresence sent %s %s body=%q", method, path, body)
	}
}

func TestDeleteAccountSendsTheConfirmation(t *testing.T) {
	var method, path, auth, body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		method, path, auth, body = r.Method, r.URL.Path, r.Header.Get("Authorization"), string(b)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	if err := NewClient(srv.URL).DeleteAccount(context.Background(), "tok", "alice"); err != nil {
		t.Fatalf("DeleteAccount: %v", err)
	}
	if method != "DELETE" || path != "/users/me" || auth != "Bearer tok" || body != `{"confirm":"alice"}` {
		t.Errorf("sent %s %s auth=%q body=%q", method, path, auth, body)
	}
}
