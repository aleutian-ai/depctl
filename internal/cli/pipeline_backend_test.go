package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"aleutian-ai/ragctl/internal/config"
)

// TestQdrantAPIKeyEnvIsSent is the regression test for vector.api_key_env
// being silently ignored: the README told users to set it for a
// key-protected Qdrant, and nothing ever read it.
func TestQdrantAPIKeyEnvIsSent(t *testing.T) {
	var gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("api-key")
		// Like a real key-protected Qdrant: /healthz is open, the rest isn't.
		if r.URL.Path != "/healthz" && gotKey != "s3cret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	t.Setenv("RAGCTL_TEST_QDRANT_KEY", "s3cret")

	vb, err := buildVectorBackend(config.Config{Vector: config.VectorConfig{Backend: "qdrant", Endpoint: srv.URL, APIKeyEnv: "RAGCTL_TEST_QDRANT_KEY"}})
	if err != nil {
		t.Fatalf("buildVectorBackend: %v", err)
	}
	if err := vb.Health(context.Background()); err != nil {
		t.Fatalf("Health: %v", err)
	}
	if gotKey != "s3cret" {
		t.Errorf("api-key header = %q, want the key from vector.api_key_env", gotKey)
	}

	t.Setenv("RAGCTL_TEST_QDRANT_KEY", "wrong")
	vb, err = buildVectorBackend(config.Config{Vector: config.VectorConfig{Backend: "qdrant", Endpoint: srv.URL, APIKeyEnv: "RAGCTL_TEST_QDRANT_KEY"}})
	if err != nil {
		t.Fatalf("buildVectorBackend: %v", err)
	}
	if err := vb.Health(context.Background()); err == nil {
		t.Error("Health with a wrong key = nil, want an error (doctor must not report a wrong key as healthy)")
	}
}

func TestBuildVectorBackendSelectsPgvector(t *testing.T) {
	vb, err := buildVectorBackend(config.Config{Vector: config.VectorConfig{Backend: "pgvector", Endpoint: "postgres://ragctl@127.0.0.1:1/ragctl"}})
	if err != nil {
		t.Fatalf("buildVectorBackend: %v", err)
	}
	if vb.Name() != "pgvector" {
		t.Errorf("Name = %q, want pgvector", vb.Name())
	}
}

func TestBuildVectorBackendSelectsWeaviate(t *testing.T) {
	vb, err := buildVectorBackend(config.Config{Vector: config.VectorConfig{Backend: "weaviate", Endpoint: "http://127.0.0.1:1"}})
	if err != nil {
		t.Fatalf("buildVectorBackend: %v", err)
	}
	if vb.Name() != "weaviate" {
		t.Errorf("Name = %q, want weaviate", vb.Name())
	}
}

func TestBuildVectorBackendRejectsUnknownBackend(t *testing.T) {
	_, err := buildVectorBackend(config.Config{Vector: config.VectorConfig{Backend: "milvus"}})
	if err == nil || !strings.Contains(err.Error(), "weaviate") {
		t.Fatalf("err = %v, want an unsupported-backend error listing the supported ones", err)
	}
}

func TestBuildVectorBackendMissingSecretEnvNamesTheDaemon(t *testing.T) {
	_, err := buildVectorBackend(config.Config{Vector: config.VectorConfig{Backend: "pgvector", Endpoint: "postgres://x@h/db", APIKeyEnv: "RAGCTL_TEST_UNSET_PG_PASSWORD"}})
	if err == nil || !strings.Contains(err.Error(), "RAGCTL_TEST_UNSET_PG_PASSWORD") || !strings.Contains(err.Error(), "daemon") {
		t.Fatalf("err = %v, want it to name the env var and the daemon's environment", err)
	}
}

func TestRedactDSNHidesPasswords(t *testing.T) {
	if got := redactDSN("postgres://ragctl:hunter2@db:5432/x"); strings.Contains(got, "hunter2") {
		t.Errorf("redactDSN leaked the password: %q", got)
	}
	if got := redactDSN("http://127.0.0.1:6333"); got != "http://127.0.0.1:6333" {
		t.Errorf("redactDSN changed a URL with no password: %q", got)
	}
}

func TestBuildVectorBackendEmbeddedDefaultsToTheDataDir(t *testing.T) {
	isolateEnv(t)
	dataDir, _ := config.DefaultDataDir()
	path, err := embeddedVectorPath(config.Default(dataDir))
	if err != nil {
		t.Fatalf("embeddedVectorPath: %v", err)
	}
	if path != filepath.Join(dataDir, "vectors.db") {
		t.Errorf("path = %q, want <data-dir>/vectors.db", path)
	}
	vb, err := buildVectorBackend(config.Default(dataDir))
	if err != nil || vb.Name() != "embedded" {
		t.Fatalf("buildVectorBackend(embedded) = %v, %v", vb, err)
	}
}

// A config switched from Qdrant keeps its http:// endpoint; that must be
// an explicit error, not a file literally named "http:".
func TestBuildVectorBackendEmbeddedRejectsAURLEndpoint(t *testing.T) {
	_, err := buildVectorBackend(config.Config{Vector: config.VectorConfig{Backend: "embedded", Endpoint: "http://127.0.0.1:6333"}})
	if err == nil || !strings.Contains(err.Error(), "file path") {
		t.Fatalf("err = %v, want a file-path error", err)
	}
}

func TestVectorLocationNeverShowsAPasswordOrAnEmptyPath(t *testing.T) {
	isolateEnv(t)
	pg := config.Config{Vector: config.VectorConfig{Backend: "pgvector", Endpoint: "postgres://ragctl:hunter2@db:5432/x"}}
	if got := vectorLocation(pg); strings.Contains(got, "hunter2") {
		t.Errorf("vectorLocation leaked the password: %q", got)
	}
	if got := vectorLocation(config.Config{Vector: config.VectorConfig{Backend: "embedded"}}); !strings.HasSuffix(got, "vectors.db") {
		t.Errorf("vectorLocation(embedded) = %q, want the vectors.db path", got)
	}
}
