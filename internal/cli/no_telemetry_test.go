package cli

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"testing"

	"aleutian-ai/ragctl/internal/backend"
	"aleutian-ai/ragctl/internal/backend/qdrant"
	bboltstore "aleutian-ai/ragctl/internal/control/bbolt"
	badgerstore "aleutian-ai/ragctl/internal/data/badger"
	"aleutian-ai/ragctl/internal/domain"
	"aleutian-ai/ragctl/internal/embedding"
	"aleutian-ai/ragctl/internal/embedding/ollama"
	"aleutian-ai/ragctl/internal/planner"
	"aleutian-ai/ragctl/internal/source/git"
)

// SEC-005: no-telemetry invariant, enforced not just claimed. See
// docs/tickets/backlog/29-security-hardening/SEC-005-no-telemetry-invariant.md.

// deniedTelemetryModuleSubstrings names known analytics/telemetry SDKs
// that must never enter ragctl's module graph. Matched against the
// lowercased import path of every module actually linked into the
// binary (via runtime/debug.ReadBuildInfo, which reflects real imports,
// not just anything sitting unused in go.sum).
var deniedTelemetryModuleSubstrings = []string{
	"posthog", "segment.io", "segmentio/analytics", "mixpanel", "amplitude",
	"google-analytics", "sentry-go", "getsentry", "bugsnag", "rollbar",
	"datadog", "newrelic", "honeycomb",
}

func TestNoTelemetrySDKInDependencyGraph(t *testing.T) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		t.Fatal("runtime/debug.ReadBuildInfo() returned ok=false — can't verify the module graph")
	}
	for _, dep := range info.Deps {
		lower := strings.ToLower(dep.Path)
		for _, denied := range deniedTelemetryModuleSubstrings {
			if strings.Contains(lower, denied) {
				t.Errorf("found disallowed analytics/telemetry dependency in the real module graph: %s (matched %q) — ragctl promises no telemetry by default; a new dependency like this needs its own explicit ticket, not a quiet addition", dep.Path, denied)
			}
		}
	}
}

// deniedTelemetryHostSubstrings names known telemetry-collector
// hostnames that must never appear as a string literal in ragctl's own
// source — the sibling check to the module-graph one above: a hardcoded
// endpoint could exist even without importing a named SDK for it (a
// bare net/http POST to a collector URL).
var deniedTelemetryHostSubstrings = []string{
	"app.posthog.com", "api.segment.io", "api.mixpanel.com", "api.amplitude.com",
	"sentry.io", "api.bugsnag.com", "api.rollbar.com",
}

func TestNoTelemetryHostLiteralsInSource(t *testing.T) {
	repoRoot := findRepoRootForTest(t)
	selfPath, err := filepath.Abs("no_telemetry_test.go")
	if err != nil {
		t.Fatalf("Abs: %v", err)
	}
	err = filepath.Walk(repoRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			name := info.Name()
			if name == ".git" || name == "node_modules" || name == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		if abs, _ := filepath.Abs(path); abs == selfPath {
			return nil // this file's own denylist literals aren't a real telemetry call
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		lower := strings.ToLower(string(data))
		for _, host := range deniedTelemetryHostSubstrings {
			if strings.Contains(lower, host) {
				t.Errorf("%s: contains a known telemetry-collector hostname literal (%q) — ragctl promises no telemetry by default", path, host)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", repoRoot, err)
	}
}

// findRepoRootForTest walks up from the current package directory to the
// nearest ancestor containing go.mod — this test's own working directory
// is internal/cli, not the repo root, when run via `go test`.
func findRepoRootForTest(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod found walking up from %s", dir)
		}
		dir = parent
	}
}

// dialRecorder wraps a *http.Transport's DialContext, recording every
// address actually dialed — a real assertion surface for "nothing but
// the explicitly-configured endpoints was ever contacted," not a
// heuristic like a timeout.
type dialRecorder struct {
	mu     sync.Mutex
	dialed []string
	dialer net.Dialer
}

func (d *dialRecorder) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	d.mu.Lock()
	d.dialed = append(d.dialed, addr)
	d.mu.Unlock()
	return d.dialer.DialContext(ctx, network, addr)
}

// TestRealSyncMakesNoUnexpectedNetworkCalls is SEC-005's runtime check:
// a real syncVersion run — real git (a local fixture repo, so zero
// network I/O on the git side), real bbolt/Badger stores, and REAL HTTP
// clients (internal/embedding/ollama, internal/backend/qdrant) pointed
// at two httptest servers standing in for the embedder and vector
// backend — with every outbound TCP dial recorded via a custom
// http.Transport installed as http.DefaultTransport for the duration.
// Asserts every single dial landed on one of the two explicitly
// configured fixture endpoints, nothing else — the real proof that a
// normal sync makes no call beyond what the user's own config named.
func TestRealSyncMakesNoUnexpectedNetworkCalls(t *testing.T) {
	requireGitForAtomicPromotionTest(t)
	ctx := context.Background()

	store, err := bboltstore.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatalf("bbolt.Open: %v", err)
	}
	defer store.Close()
	badgerStore, err := badgerstore.Open(filepath.Join(t.TempDir(), "badger"))
	if err != nil {
		t.Fatalf("badger.Open: %v", err)
	}
	defer badgerStore.Close()

	repoDir := atomicPromotionFixtureRepo(t)
	reg := atomicPromotionTestRegistry(t, repoDir)
	gitCache := git.NewCache(t.TempDir())

	embedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input []string `json:"input"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		vecs := make([][]float32, len(req.Input))
		for i := range vecs {
			vecs[i] = []float32{0.1, 0.2, 0.3, 0.4}
		}
		json.NewEncoder(w).Encode(struct {
			Embeddings [][]float32 `json:"embeddings"`
		}{Embeddings: vecs})
	}))
	defer embedSrv.Close()

	vecSrv := newFakeQdrantServer(t)
	defer vecSrv.Close()

	embedHost := mustHost(t, embedSrv.URL)
	vecHost := mustHost(t, vecSrv.URL)
	allowed := map[string]bool{embedHost: true, vecHost: true}

	recorder := &dialRecorder{}
	origTransport := http.DefaultTransport
	http.DefaultTransport = &http.Transport{DialContext: recorder.DialContext}
	defer func() { http.DefaultTransport = origTransport }()

	embedder := ollama.New(embedSrv.URL, "test-model")
	vb := qdrant.New(vecSrv.URL)
	ns := backend.Namespace{Name: "sec005", Dimensions: 4}

	dep := domain.DependencyVersion{
		Dependency: domain.Dependency{Ecosystem: domain.EcosystemGo, Name: "example.com/widget"},
		Version:    "v1.0.0",
	}
	action := planner.Action{Kind: planner.ActionSyncVersion, ProjectID: "proj_sec005", Dependency: dep}
	if err := syncVersion(ctx, store, badgerStore, gitCache, &embedding.Prompted{Embedder: embedder}, vb, ns, reg, action, false); err != nil {
		t.Fatalf("syncVersion: %v", err)
	}

	recorder.mu.Lock()
	dialed := append([]string(nil), recorder.dialed...)
	recorder.mu.Unlock()

	if len(dialed) == 0 {
		t.Fatal("no dials recorded at all — the dial hook probably isn't actually wired into the real HTTP clients, this test would pass vacuously")
	}
	for _, addr := range dialed {
		if !allowed[addr] {
			t.Errorf("real sync dialed an unexpected address: %s (allowed: %v)", addr, allowed)
		}
	}
}

// newFakeQdrantServer is a minimal, in-memory, wire-compatible-enough
// stand-in for real Qdrant: it remembers every upserted point and replays
// all of them (ignoring real vector similarity) on search, which is
// enough for a real syncVersion run's post-write validation step (VAL-002
// checking the new generation is genuinely queryable) to find something,
// without needing to reimplement real ANN search.
func newFakeQdrantServer(t *testing.T) *httptest.Server {
	t.Helper()
	type point struct {
		ID      any            `json:"id"`
		Payload map[string]any `json:"payload"`
	}
	var mu sync.Mutex
	var points []point

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet:
			w.WriteHeader(http.StatusNotFound)
		case strings.HasSuffix(r.URL.Path, "/points") && r.Method == http.MethodPut:
			var req struct {
				Points []point `json:"points"`
			}
			json.NewDecoder(r.Body).Decode(&req)
			mu.Lock()
			points = append(points, req.Points...)
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{}`))
		case strings.HasSuffix(r.URL.Path, "/points/search"):
			mu.Lock()
			results := make([]map[string]any, len(points))
			for i, p := range points {
				results[i] = map[string]any{"id": p.ID, "score": float32(1.0), "payload": p.Payload}
			}
			mu.Unlock()
			json.NewEncoder(w).Encode(map[string]any{"result": results})
		default:
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{}`))
		}
	}))
}

func mustHost(t *testing.T, rawURL string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse %q: %v", rawURL, err)
	}
	return u.Host
}
