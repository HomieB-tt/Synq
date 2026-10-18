package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestListCommentsSendsPagingOnlyWhenAsked(t *testing.T) {
	var gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":"c1","content":"hi","author":"amy"}]`))
	}))
	defer srv.Close()
	c := NewClient(srv.URL)

	cs, err := c.ListComments(context.Background(), "", "p1", 0, 0)
	if err != nil || len(cs) != 1 || cs[0].Author != "amy" {
		t.Fatalf("defaults: %+v %v", cs, err)
	}
	if gotPath != "/feed/p1/comments" || gotQuery != "" {
		t.Errorf("defaults sent %q ? %q, want no query", gotPath, gotQuery)
	}

	if _, err := c.ListComments(context.Background(), "", "p1", 50, 100); err != nil {
		t.Fatal(err)
	}
	if gotQuery != "limit=50&offset=100" {
		t.Errorf("query = %q, want limit=50&offset=100", gotQuery)
	}
}
