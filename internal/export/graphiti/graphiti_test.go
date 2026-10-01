package graphiti

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAddEpisodeSendsExactRequestShape(t *testing.T) {
	var gotPath, gotAuth string
	var gotBody addMessagesRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "test-token")
	payload := map[string]any{"dependency": "google.golang.org/protobuf", "version": "v1.36.11"}
	if err := c.AddEpisode(context.Background(), "proj-1", "google.golang.org/protobuf", payload); err != nil {
		t.Fatalf("AddEpisode: %v", err)
	}

	if gotPath != "/messages" {
		t.Errorf("path = %q, want /messages", gotPath)
	}
	if gotAuth != "Bearer test-token" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer test-token")
	}
	if gotBody.GroupID != "proj-1" {
		t.Errorf("GroupID = %q, want proj-1", gotBody.GroupID)
	}
	if len(gotBody.Messages) != 1 || gotBody.Messages[0].Name != "google.golang.org/protobuf" {
		t.Fatalf("Messages = %+v, want one message named for the dependency", gotBody.Messages)
	}
	var content map[string]any
	if err := json.Unmarshal([]byte(gotBody.Messages[0].Content), &content); err != nil {
		t.Fatalf("Content is not valid JSON: %v", err)
	}
	if content["dependency"] != "google.golang.org/protobuf" {
		t.Errorf("Content[dependency] = %v, want google.golang.org/protobuf", content["dependency"])
	}
	if gotBody.Messages[0].Timestamp == "" {
		t.Error("Timestamp must not be empty")
	}
	// Both are required by Graphiti's own Message model — a live server
	// rejects the request with 422 without them.
	if gotBody.Messages[0].RoleType != "system" {
		t.Errorf("RoleType = %q, want system", gotBody.Messages[0].RoleType)
	}
	if gotBody.Messages[0].Role == "" {
		t.Error("Role must not be empty")
	}
}

func TestAddEpisodeNoAuthHeaderWhenTokenEmpty(t *testing.T) {
	var sawHeader bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawHeader = len(r.Header["Authorization"]) > 0
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "")
	if err := c.AddEpisode(context.Background(), "proj-1", "dep", map[string]string{"a": "b"}); err != nil {
		t.Fatalf("AddEpisode: %v", err)
	}
	if sawHeader {
		t.Error("Authorization header must be absent when no token is configured — Graphiti has no auth of its own by default")
	}
}

func TestAddEpisodeReturnsErrorOnNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "")
	if err := c.AddEpisode(context.Background(), "proj-1", "dep", map[string]string{}); err == nil {
		t.Fatal("expected an error for a 500 response, got nil")
	}
}

func TestHealthUsesRealHealthcheckEndpoint(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "")
	if err := c.Health(context.Background()); err != nil {
		t.Fatalf("Health: %v", err)
	}
	if gotPath != "/healthcheck" {
		t.Errorf("path = %q, want /healthcheck", gotPath)
	}
}

// TestHealthRequiresA200 guards the exact live-found bug: a 4xx (the old
// /episodes check always got 422) must not count as healthy.
func TestHealthRequiresA200(t *testing.T) {
	for _, code := range []int{http.StatusUnprocessableEntity, http.StatusNotFound, http.StatusInternalServerError} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(code)
		}))
		c := NewClient(srv.URL, "")
		if err := c.Health(context.Background()); err == nil {
			t.Errorf("status %d: expected an error, got nil", code)
		}
		srv.Close()
	}
}
