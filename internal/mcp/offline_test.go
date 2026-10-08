package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/aleutian-ai/depctl/internal/backend"
	"github.com/aleutian-ai/depctl/internal/backend/qdrant"
	"github.com/aleutian-ai/depctl/internal/control/bbolt"
	"github.com/aleutian-ai/depctl/internal/data/badger"
	"github.com/aleutian-ai/depctl/internal/domain"
	"github.com/aleutian-ai/depctl/internal/embedding"
	"github.com/aleutian-ai/depctl/internal/embedding/ollama"
	"github.com/aleutian-ai/depctl/internal/query"
)

func requireContainerRuntime(t *testing.T) {
	t.Helper()
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("no Docker-API-compatible container runtime reachable")
	}
}

// blockedDialRecorder is a thread-safe log of dial attempts a
// network-blocking RoundTripper rejected, so the test can report every
// offending call site at once rather than failing on the first.
type blockedDialRecorder struct {
	mu       sync.Mutex
	attempts []string
}

func (r *blockedDialRecorder) record(addr string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.attempts = append(r.attempts, addr)
}

func (r *blockedDialRecorder) Attempts() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.attempts...)
}

// newLoopbackOnlyClient returns an *http.Client whose transport rejects
// any dial to a non-loopback address, recording every rejected attempt
// in recorder — the network-blocking RoundTripper MCP-004 asks for. Real
// local services (a real Qdrant container, a fake Ollama httptest
// server) both bind to 127.0.0.1, so this still allows the actual
// production HTTP client code paths (internal/embedding/ollama,
// internal/backend/qdrant) to run for real against them, while any
// attempt to reach an actual external host fails immediately and is
// recorded, not silently retried or hung.
func newLoopbackOnlyClient(recorder *blockedDialRecorder) *http.Client {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, _, err := net.SplitHostPort(addr)
			if err != nil {
				host = addr
			}
			ip := net.ParseIP(host)
			isLoopback := host == "localhost" || (ip != nil && ip.IsLoopback())
			if !isLoopback {
				recorder.record(addr)
				return nil, fmt.Errorf("network access blocked (offline test): attempted dial to %s", addr)
			}
			return dialer.DialContext(ctx, network, addr)
		},
	}
	return &http.Client{Transport: transport, Timeout: 10 * time.Second}
}

// fakeOllamaServer stands in for a local Ollama instance — a real
// httptest.Server (real HTTP, real loopback socket), not a fake
// embedding.Embedder, so internal/embedding/ollama's actual client code
// runs for real in this test, exactly as it would in production.
func fakeOllamaServer(t *testing.T, dims int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input []string `json:"input"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		vecs := make([][]float32, len(req.Input))
		for i := range vecs {
			v := make([]float32, dims)
			v[0] = 1
			vecs[i] = v
		}
		json.NewEncoder(w).Encode(map[string]any{"embeddings": vecs})
	}))
}

// TestOfflineSearchDependencyDocsAndGetDependencyVersion is MCP-004: the
// release-blocking proof that depctl serve answers version-correct MCP
// queries with zero *external* network access once knowledge has been
// synced. It uses real production HTTP clients (internal/embedding/
// ollama, internal/backend/qdrant) against real local services (a real
// Qdrant container, a real httptest Ollama fake) — both loopback-only —
// behind a RoundTripper that rejects and records any dial to anything
// else. If that happened, the query below would either fail outright or
// this test's final assertion on blockedDialRecorder would catch it.
func TestOfflineSearchDependencyDocsAndGetDependencyVersion(t *testing.T) {
	requireContainerRuntime(t)
	t.Setenv("TESTCONTAINERS_RYUK_DISABLED", "true")
	ctx := context.Background()

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "qdrant/qdrant:v1.13.1",
			ExposedPorts: []string{"6333/tcp"},
			WaitingFor:   wait.ForHTTP("/healthz").WithPort("6333/tcp").WithStartupTimeout(60 * time.Second),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start qdrant container: %v", err)
	}
	t.Cleanup(func() {
		if err := container.Terminate(context.Background()); err != nil {
			t.Logf("terminate qdrant container: %v", err)
		}
	})
	host, err := container.Host(ctx)
	if err != nil {
		t.Fatalf("container host: %v", err)
	}
	port, err := container.MappedPort(ctx, "6333")
	if err != nil {
		t.Fatalf("mapped port: %v", err)
	}
	qdrantURL := fmt.Sprintf("http://%s:%s", host, port.Port())

	const dims = 4
	ollamaSrv := fakeOllamaServer(t, dims)
	defer ollamaSrv.Close()

	recorder := &blockedDialRecorder{}
	blockedClient := newLoopbackOnlyClient(recorder)

	embedder := ollama.New(ollamaSrv.URL, "test-model", ollama.WithHTTPClient(blockedClient))
	vb := qdrant.New(qdrantURL, qdrant.WithHTTPClient(blockedClient))
	ns := backend.Namespace{Name: "depctl", Dimensions: dims, Distance: "cosine"}
	if err := vb.EnsureNamespace(ctx, ns); err != nil {
		t.Fatalf("EnsureNamespace: %v", err)
	}

	// --- Pre-sync fixture: write directly into local stores (test
	// doubles), not via a live sync — per MCP-004's own design.
	store, err := bbolt.Open(t.TempDir() + "/control.db")
	if err != nil {
		t.Fatalf("bbolt.Open: %v", err)
	}
	defer store.Close()
	badgerStore, err := badger.Open(t.TempDir() + "/badger")
	if err != nil {
		t.Fatalf("badger.Open: %v", err)
	}
	defer badgerStore.Close()

	dep := domain.DependencyVersion{
		Dependency: domain.Dependency{Ecosystem: domain.EcosystemGo, Name: "google.golang.org/grpc"},
		Version:    "v1.67.0",
	}
	if err := store.PutProject(ctx, domain.Project{ID: "proj_1", Root: "/repo"}); err != nil {
		t.Fatalf("PutProject: %v", err)
	}
	if err := store.PutResolution(ctx, "proj_1", domain.Resolution{Dependencies: []domain.DependencyVersion{dep}}); err != nil {
		t.Fatalf("PutResolution: %v", err)
	}

	gen := domain.Generation{ID: "gen_1", Dependency: dep, State: domain.GenReady}
	if err := store.PromoteGeneration(ctx, gen, "qdrant"); err != nil {
		t.Fatalf("PromoteGeneration: %v", err)
	}

	obj := domain.KnowledgeObject{
		ID: "ko_1", Dependency: dep, SourceURI: "https://github.com/grpc/grpc-go", SourceType: "git",
		LogicalPath: "README.md", Authority: 100, Content: []byte("grpc retry semantics documentation"),
	}
	if err := badgerStore.PutKnowledgeObject(ctx, obj); err != nil {
		t.Fatalf("PutKnowledgeObject: %v", err)
	}
	chunk := domain.Chunk{ID: "chk_1", ObjectID: obj.ID, Content: obj.Content}
	if err := badgerStore.PutChunk(ctx, gen.ID, chunk); err != nil {
		t.Fatalf("PutChunk: %v", err)
	}

	// The chunk's vector point — embedded via the real (loopback-only)
	// Ollama fake, upserted into the real Qdrant container.
	vecs, err := embedder.Embed(ctx, []string{string(chunk.Content)})
	if err != nil {
		t.Fatalf("Embed (fixture setup): %v", err)
	}
	err = vb.Upsert(ctx, backend.UpsertRequest{
		Namespace: ns.Name,
		Points: []backend.Point{{
			ID: chunk.ID, Vector: vecs[0],
			Metadata: backend.PointMetadata{
				Ecosystem: string(dep.Dependency.Ecosystem), Dependency: dep.Dependency.Name, Version: dep.Version,
				Generation: gen.ID, SourceType: obj.SourceType, Authority: obj.Authority,
			},
		}},
	})
	if err != nil {
		t.Fatalf("Upsert (fixture setup): %v", err)
	}

	// --- The actual offline query, over a real MCP client/server
	// session (in-memory transport — no subprocess, no real stdio pipe,
	// per MCP-004's speed guidance; the protocol serialization path
	// between a real client and real server is still exercised for
	// real).
	svc := query.New(store, badgerStore, vb, &embedding.Prompted{Embedder: embedder}, ns, "qdrant")
	server := New(Deps{Query: svc})

	t1, t2 := sdkmcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, t1)
	if err != nil {
		t.Fatalf("server.Connect: %v", err)
	}
	defer serverSession.Close()
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "offline-test-client", Version: "v0.0.0"}, nil)
	clientSession, err := client.Connect(ctx, t2, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	defer clientSession.Close()

	searchRes, err := clientSession.CallTool(ctx, &sdkmcp.CallToolParams{
		Name: "search_dependency_docs",
		Arguments: map[string]any{
			"project_id": "proj_1",
			"query":      "how does grpc retry work",
			"dependency": "google.golang.org/grpc",
		},
	})
	if err != nil {
		t.Fatalf("CallTool search_dependency_docs: %v", err)
	}
	if searchRes.IsError {
		t.Fatalf("search_dependency_docs returned an error result: %+v", searchRes.Content)
	}
	searchText := resultText(t, searchRes)
	if !strings.Contains(searchText, "grpc retry semantics documentation") {
		t.Errorf("search result missing expected chunk content:\n%s", searchText)
	}
	if !strings.Contains(searchText, `"version":"v1.67.0"`) {
		t.Errorf("search result missing expected version v1.67.0:\n%s", searchText)
	}
	if !strings.Contains(searchText, `"generation":"gen_1"`) {
		t.Errorf("search result missing expected generation gen_1:\n%s", searchText)
	}

	versionRes, err := clientSession.CallTool(ctx, &sdkmcp.CallToolParams{
		Name:      "get_dependency_version",
		Arguments: map[string]any{"project_id": "proj_1", "package": "google.golang.org/grpc"},
	})
	if err != nil {
		t.Fatalf("CallTool get_dependency_version: %v", err)
	}
	if versionRes.IsError {
		t.Fatalf("get_dependency_version returned an error result: %+v", versionRes.Content)
	}
	versionText := resultText(t, versionRes)
	if !strings.Contains(versionText, `"version":"v1.67.0"`) {
		t.Errorf("get_dependency_version result missing v1.67.0:\n%s", versionText)
	}

	if attempts := recorder.Attempts(); len(attempts) != 0 {
		t.Fatalf("blocked (non-loopback) network dial attempted during offline query: %v", attempts)
	}
}

func resultText(t *testing.T, res *sdkmcp.CallToolResult) string {
	t.Helper()
	if len(res.Content) == 0 {
		t.Fatal("result has no content")
	}
	tc, ok := res.Content[0].(*sdkmcp.TextContent)
	if !ok {
		t.Fatalf("result.Content[0] = %T, want *sdkmcp.TextContent", res.Content[0])
	}
	return tc.Text
}

// TestLoopbackOnlyClientBlocksNonLoopbackDials proves the network-block
// mechanism the main offline test relies on actually fires — not just
// that it was never triggered. Without this, a bug making
// newLoopbackOnlyClient permissive (e.g. an inverted condition) would
// make TestOfflineSearchDependencyDocsAndGetDependencyVersion pass for
// the wrong reason: not because nothing tried to leave the loopback
// interface, but because nothing was actually blocking it.
func TestLoopbackOnlyClientBlocksNonLoopbackDials(t *testing.T) {
	recorder := &blockedDialRecorder{}
	client := newLoopbackOnlyClient(recorder)

	// example.com resolves to a real, well-known non-loopback address —
	// this must never actually complete a TCP handshake to it.
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.com", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	_, err = client.Do(req)
	if err == nil {
		t.Fatal("request to a non-loopback host succeeded, want it blocked")
	}
	if !strings.Contains(err.Error(), "network access blocked") {
		t.Errorf("error = %v, want it to mention the block", err)
	}
	if attempts := recorder.Attempts(); len(attempts) != 1 || !strings.Contains(attempts[0], "example.com") {
		t.Errorf("recorder.Attempts() = %v, want exactly one attempt naming example.com", attempts)
	}
}
