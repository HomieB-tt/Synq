package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestValidateServerURL(t *testing.T) {
	tests := []struct {
		url           string
		allowInsecure bool
		wantErr       bool
	}{
		{"https://synq.example.com", false, false},
		{"http://localhost:8080", false, false},
		{"http://127.0.0.1:8080", false, false},
		{"http://[::1]:8080", false, false},
		{"http://synq.example.com", false, true},
		{"http://192.168.1.20:8080", false, true},
		{"http://192.168.1.20:8080", true, false},
		{"ftp://synq.example.com", false, true},
		{"synq.example.com:8080", false, true},
		{"https://", false, true},
		{"", false, true},
	}
	for _, tc := range tests {
		err := ValidateServerURL(tc.url, tc.allowInsecure)
		if (err != nil) != tc.wantErr {
			t.Errorf("ValidateServerURL(%q, %v) error = %v, wantErr %v", tc.url, tc.allowInsecure, err, tc.wantErr)
		}
	}
}

func TestPing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()
	d, err := NewClient(srv.URL + "/").Ping(context.Background())
	if err != nil || d <= 0 {
		t.Fatalf("Ping = %v, %v", d, err)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
	}))
	defer bad.Close()
	if _, err := NewClient(bad.URL).Ping(context.Background()); err == nil {
		t.Fatal("503 should be an error")
	}
}
