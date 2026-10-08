package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aleutian-ai/depctl/internal/config"
)

func TestEmbeddingReadinessCheckReadyNilIsAlwaysReady(t *testing.T) {
	var r *embeddingReadiness
	if err := r.checkReady(); err != nil {
		t.Errorf("nil readiness checkReady() = %v, want nil", err)
	}
}

func TestEmbeddingReadinessCheckReadyPerState(t *testing.T) {
	cases := []struct {
		state       embeddingState
		wantErr     bool
		wantContain string
	}{
		{embeddingStateUnknown, false, ""},
		{embeddingStateReady, false, ""},
		{embeddingStateChecking, true, "checking"},
		{embeddingStatePulling, true, "downloaded"},
		{embeddingStateUnreachable, true, "unreachable"},
		{embeddingStateError, true, "not ready"},
	}
	for _, c := range cases {
		r := newEmbeddingReadiness()
		r.set(c.state, "detail-text")
		err := r.checkReady()
		if c.wantErr && err == nil {
			t.Errorf("state %s: checkReady() = nil, want an error", c.state)
			continue
		}
		if !c.wantErr && err != nil {
			t.Errorf("state %s: checkReady() = %v, want nil", c.state, err)
			continue
		}
		if c.wantErr && !strings.Contains(err.Error(), c.wantContain) {
			t.Errorf("state %s: checkReady() = %q, want it to mention %q", c.state, err, c.wantContain)
		}
	}
}

func TestCheckEmbeddingReadinessUnreachable(t *testing.T) {
	cfg := config.Default("/data")
	cfg.Embedding.Endpoint = "http://127.0.0.1:1" // nothing listens here
	readiness := newEmbeddingReadiness()

	checkEmbeddingReadiness(context.Background(), cfg, readiness, func(string, ...any) {})

	state, detail := readiness.get()
	if state != embeddingStateUnreachable {
		t.Errorf("state = %s, want unreachable", state)
	}
	if detail == "" {
		t.Error("detail is empty, want it to name the endpoint")
	}
}

func TestCheckEmbeddingReadinessAlreadyPulled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"models": []map[string]string{{"name": "nomic-embed-text-v2-moe:latest"}},
		})
	}))
	defer srv.Close()

	cfg := config.Default("/data")
	cfg.Embedding.Endpoint = srv.URL
	readiness := newEmbeddingReadiness()

	checkEmbeddingReadiness(context.Background(), cfg, readiness, func(string, ...any) {})

	state, _ := readiness.get()
	if state != embeddingStateReady {
		t.Errorf("state = %s, want ready (model already pulled)", state)
	}
}

func TestCheckEmbeddingReadinessPullsMissingModelThenReady(t *testing.T) {
	pulled := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			if pulled {
				json.NewEncoder(w).Encode(map[string]any{
					"models": []map[string]string{{"name": "nomic-embed-text-v2-moe:latest"}},
				})
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"models": []any{}})
		case "/api/pull":
			pulled = true
			json.NewEncoder(w).Encode(map[string]string{"status": "success"})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	cfg := config.Default("/data")
	cfg.Embedding.Endpoint = srv.URL
	readiness := newEmbeddingReadiness()

	var logLines []string
	checkEmbeddingReadiness(context.Background(), cfg, readiness, func(format string, args ...any) {
		logLines = append(logLines, format)
	})

	state, _ := readiness.get()
	if state != embeddingStateReady {
		t.Errorf("state = %s, want ready after a successful pull", state)
	}
	if !pulled {
		t.Error("PullModel's /api/pull endpoint was never hit")
	}
	if len(logLines) == 0 {
		t.Error("expected at least one progress log line during the pull")
	}
}

func TestCheckEmbeddingReadinessNonOllamaProviderIsReadyImmediately(t *testing.T) {
	cfg := config.Default("/data")
	cfg.Embedding.Provider = "openai-compatible-future-provider"
	readiness := newEmbeddingReadiness()

	checkEmbeddingReadiness(context.Background(), cfg, readiness, func(string, ...any) {})

	state, _ := readiness.get()
	if state != embeddingStateReady {
		t.Errorf("state = %s, want ready (nothing here understands a non-ollama provider's readiness)", state)
	}
}
