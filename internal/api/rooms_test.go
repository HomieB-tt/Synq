package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRoomCallsUseTheDocumentedRoutes(t *testing.T) {
	type seen struct{ method, path, auth, body string }
	var got seen
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = seen{r.Method, r.URL.Path, r.Header.Get("Authorization"), string(b)}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/rooms":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"r1","name":"standup","owner_id":"u1","status":"JOINED","members":[{"user_id":"u1","username":"amy","status":"JOINED"}],"max_members":16}`))
		case r.Method == http.MethodGet && r.URL.Path == "/rooms":
			_, _ = w.Write([]byte(`[{"id":"r1","name":"standup","owner_id":"u1","status":"INVITED"}]`))
		case r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"id":"r1","name":"standup","owner_id":"u1","status":"JOINED","members":[],"max_members":16}`))
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	ctx := context.Background()

	room, err := c.CreateRoom(ctx, "tok", "standup")
	if err != nil || room.ID != "r1" || room.MaxMembers != 16 || len(room.Members) != 1 || room.Members[0].Username != "amy" {
		t.Fatalf("CreateRoom: %+v %v", room, err)
	}
	if got.method != "POST" || got.path != "/rooms" || got.auth != "Bearer tok" {
		t.Errorf("CreateRoom sent %+v", got)
	}
	var body map[string]string
	if err := json.Unmarshal([]byte(got.body), &body); err != nil || body["name"] != "standup" {
		t.Errorf("CreateRoom body = %q", got.body)
	}

	rows, err := c.ListRooms(ctx, "tok")
	if err != nil || len(rows) != 1 || rows[0].Status != "INVITED" || rows[0].OwnerID != "u1" {
		t.Fatalf("ListRooms: %+v %v", rows, err)
	}

	if _, err := c.GetRoom(ctx, "tok", "r1"); err != nil || got.path != "/rooms/r1" {
		t.Fatalf("GetRoom: %v, path %q", err, got.path)
	}

	steps := []struct {
		name           string
		call           func() error
		method, path   string
		wantBodyHasKey string
	}{
		{"invite", func() error { return c.InviteToRoom(ctx, "tok", "r1", "bob") }, "POST", "/rooms/r1/invite", "username"},
		{"accept", func() error { return c.AcceptRoomInvite(ctx, "tok", "r1") }, "POST", "/rooms/r1/accept", ""},
		{"leave", func() error { return c.LeaveRoom(ctx, "tok", "r1") }, "POST", "/rooms/r1/leave", ""},
		{"end", func() error { return c.EndRoom(ctx, "tok", "r1") }, "DELETE", "/rooms/r1", ""},
	}
	for _, s := range steps {
		if err := s.call(); err != nil {
			t.Errorf("%s: %v", s.name, err)
			continue
		}
		if got.method != s.method || got.path != s.path {
			t.Errorf("%s sent %s %s, want %s %s", s.name, got.method, got.path, s.method, s.path)
		}
		if s.wantBodyHasKey != "" {
			var m map[string]string
			if json.Unmarshal([]byte(got.body), &m) != nil || m[s.wantBodyHasKey] == "" {
				t.Errorf("%s body = %q", s.name, got.body)
			}
		}
	}
}

func TestRoomIDsAreEscapedInThePath(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	if err := NewClient(srv.URL).EndRoom(context.Background(), "tok", "a/b?c"); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/rooms/a%2Fb%3Fc" {
		t.Errorf("path = %q", gotPath)
	}
}

func TestRoomErrorsSurfaceTheServerMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":"a room holds at most 16 people"}`))
	}))
	defer srv.Close()
	err := NewClient(srv.URL).InviteToRoom(context.Background(), "tok", "r1", "bob")
	if err == nil || !contains(err.Error(), "at most 16") {
		t.Fatalf("err = %v", err)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
