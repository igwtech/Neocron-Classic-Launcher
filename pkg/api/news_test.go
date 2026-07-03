package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchNews(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.String()
		fmt.Fprint(w, `[{"id":5,"title":"Patch Notes 0.0.15","body":"<p>New patch!</p>","createdAt":"2026-04-13T23:33:59.974Z"},
		               {"id":4,"title":"Older","body":"<p>x</p>","createdAt":"2026-04-13T19:48:46.621Z"}]`)
	}))
	defer srv.Close()

	posts, err := FetchNews(context.Background(), srv.URL, 10, "ua")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/news?limit=10" {
		t.Errorf("request path = %q, want /news?limit=10", gotPath)
	}
	if len(posts) != 2 {
		t.Fatalf("got %d posts, want 2", len(posts))
	}
	if posts[0].Title != "Patch Notes 0.0.15" || posts[0].ID.String() != "5" ||
		posts[0].CreatedAt == "" || posts[0].Body == "" {
		t.Errorf("post[0] = %+v", posts[0])
	}
}

func TestFetchNewsDefaultsLimit(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.String()
		fmt.Fprint(w, `[]`)
	}))
	defer srv.Close()
	if _, err := FetchNews(context.Background(), srv.URL, 0, "ua"); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/news?limit=10" {
		t.Errorf("default limit path = %q, want /news?limit=10", gotPath)
	}
}
