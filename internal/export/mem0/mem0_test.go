package mem0

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAddMemorySendsExactRequestShape(t *testing.T) {
	var gotPath, gotAuth string
	var gotBody addMemoryRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "test-key")
	err := c.AddMemory(context.Background(), "proj-1", "protobuf is a serialization format", map[string]string{
		"ecosystem":  "go",
		"dependency": "google.golang.org/protobuf",
	})
	if err != nil {
		t.Fatalf("AddMemory: %v", err)
	}

	if gotPath != "/v3/memories/add/" {
		t.Errorf("path = %q, want /v3/memories/add/", gotPath)
	}
	if gotAuth != "Token test-key" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Token test-key")
	}
	if gotBody.Infer {
		t.Error("Infer must be false — this connector requires a synchronous response to report per-chunk success/failure")
	}
	if gotBody.UserID != "proj-1" {
		t.Errorf("UserID = %q, want proj-1", gotBody.UserID)
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
		sawHeader = len(r.Header["Authorization"]) > 0
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "")
	if err := c.AddMemory(context.Background(), "proj-1", "text", nil); err != nil {
		t.Fatalf("AddMemory: %v", err)
	}
	if sawHeader {
		t.Error("Authorization header must be absent when no API key is configured")
	}
}

func TestAddMemoryReturnsErrorOnNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error": "invalid api key"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "bad-key")
	err := c.AddMemory(context.Background(), "proj-1", "text", nil)
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
	if gotPath != "/v1/entities/" {
		t.Errorf("path = %q, want /v1/entities/", gotPath)
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
