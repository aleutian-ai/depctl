package mem0

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAddMemorySendsExactRequestShape(t *testing.T) {
	var gotPath, gotAuth string
	var gotBody addMemoryRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("X-API-Key")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "test-key")
	err := c.AddMemory(context.Background(), "proj-1", "ragctl:google.golang.org/protobuf", "protobuf is a serialization format", map[string]string{
		"ecosystem":  "go",
		"dependency": "google.golang.org/protobuf",
	})
	if err != nil {
		t.Fatalf("AddMemory: %v", err)
	}

	if gotPath != "/memories" {
		t.Errorf("path = %q, want /memories (self-hosted server)", gotPath)
	}
	if gotAuth != "test-key" {
		t.Errorf("X-API-Key = %q, want %q (self-hosted auth header)", gotAuth, "test-key")
	}
	if gotBody.Infer {
		t.Error("Infer must be false — this connector requires a synchronous response to report per-chunk success/failure")
	}
	if gotBody.UserID != "proj-1" {
		t.Errorf("UserID = %q, want proj-1", gotBody.UserID)
	}
	if gotBody.RunID != "ragctl:google.golang.org/protobuf" {
		t.Errorf("RunID = %q, want ragctl:google.golang.org/protobuf", gotBody.RunID)
	}
	if len(gotBody.Messages) != 1 || gotBody.Messages[0].Content != "protobuf is a serialization format" {
		t.Errorf("Messages = %+v, want one message with the chunk content", gotBody.Messages)
	}
	if gotBody.Metadata["dependency"] != "google.golang.org/protobuf" {
		t.Errorf("Metadata[dependency] = %q, want google.golang.org/protobuf", gotBody.Metadata["dependency"])
	}
}

func TestAddMemoryNoAuthHeaderWhenAPIKeyEmpty(t *testing.T) {
	var sawHeader bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawHeader = len(r.Header["X-Api-Key"]) > 0
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "")
	if err := c.AddMemory(context.Background(), "proj-1", "ragctl:dep", "text", nil); err != nil {
		t.Fatalf("AddMemory: %v", err)
	}
	if sawHeader {
		t.Error("X-API-Key header must be absent when no API key is configured")
	}
}

func TestAddMemoryReturnsErrorOnNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error": "invalid api key"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "bad-key")
	err := c.AddMemory(context.Background(), "proj-1", "ragctl:dep", "text", nil)
	if err == nil {
		t.Fatal("expected an error for a 401 response, got nil")
	}
}

func TestHealthUsesRealEntitiesEndpoint(t *testing.T) {
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
	if gotPath != "/entities" {
		t.Errorf("path = %q, want /entities", gotPath)
	}
}

func TestHealthFailsOn5xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "")
	if err := c.Health(context.Background()); err == nil {
		t.Fatal("expected an error for a 500 response, got nil")
	}
}

// TestHealthRequiresA200 guards the live-found gap: against a real
// self-hosted server the old health path 404'd and still "passed",
// because anything below 500 counted as healthy.
func TestHealthRequiresA200(t *testing.T) {
	cases := map[int]string{
		http.StatusUnauthorized: "X-API-Key",
		http.StatusNotFound:     "self-hosted",
		http.StatusBadGateway:   "unhealthy",
	}
	for code, wantInMsg := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(code)
		}))
		err := NewClient(srv.URL, "k").Health(context.Background())
		srv.Close()
		if err == nil {
			t.Errorf("status %d: Health = nil, want an error", code)
			continue
		}
		if !strings.Contains(err.Error(), wantInMsg) {
			t.Errorf("status %d: error %q should mention %q", code, err, wantInMsg)
		}
	}
}

func TestDeleteMemoriesIsScopedToProjectAndRunID(t *testing.T) {
	var gotMethod, gotPath, gotUser, gotRun, gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotUser, gotRun = r.URL.Query().Get("user_id"), r.URL.Query().Get("run_id")
		gotKey = r.Header.Get("X-API-Key")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := NewClient(srv.URL, "k").DeleteMemories(context.Background(), "proj-1", "ragctl:dep"); err != nil {
		t.Fatalf("DeleteMemories: %v", err)
	}
	if gotMethod != http.MethodDelete || gotPath != "/memories" {
		t.Errorf("request = %s %s, want DELETE /memories", gotMethod, gotPath)
	}
	if gotUser != "proj-1" || gotRun != "ragctl:dep" {
		t.Errorf("filters user_id=%q run_id=%q, want proj-1 and ragctl:dep (both, so only ragctl's own memories match)", gotUser, gotRun)
	}
	if gotKey != "k" {
		t.Errorf("X-API-Key = %q, want k", gotKey)
	}
}

func TestDeleteMemoriesNonAdminKeyExplainsWhy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	err := NewClient(srv.URL, "user-key").DeleteMemories(context.Background(), "proj-1", "ragctl:dep")
	if err == nil || !strings.Contains(err.Error(), "admin") {
		t.Fatalf("err = %v, want it to say deletes need an admin key", err)
	}
}
