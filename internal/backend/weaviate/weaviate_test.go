package weaviate

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"aleutian-ai/ragctl/internal/backend"
	"aleutian-ai/ragctl/internal/backend/conformance"
)

const testAPIKey = "weaviate-test-key"

// requireContainerRuntime skips when no Docker-API-compatible runtime is
// reachable (same convention as the Qdrant adapter's tests).
func requireContainerRuntime(t *testing.T) {
	t.Helper()
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("no Docker-API-compatible container runtime reachable")
	}
}

// startWeaviate runs a real Weaviate with API-key auth on and returns its
// endpoint. queryLimit, when non-empty, lowers Weaviate's per-request
// result cap (QUERY_MAXIMUM_RESULTS, 10000 by default).
func startWeaviate(t *testing.T, queryLimit string) string {
	t.Helper()
	requireContainerRuntime(t)
	t.Setenv("TESTCONTAINERS_RYUK_DISABLED", "true") // rootless Podman has no "bridge" network for ryuk
	ctx := context.Background()
	env := map[string]string{
		"AUTHENTICATION_ANONYMOUS_ACCESS_ENABLED": "false",
		"AUTHENTICATION_APIKEY_ENABLED":           "true",
		"AUTHENTICATION_APIKEY_ALLOWED_KEYS":      testAPIKey,
		"AUTHENTICATION_APIKEY_USERS":             "ragctl",
		"DEFAULT_VECTORIZER_MODULE":               "none",
		"PERSISTENCE_DATA_PATH":                   "/var/lib/weaviate",
		"CLUSTER_HOSTNAME":                        "node1",
	}
	if queryLimit != "" {
		env["QUERY_MAXIMUM_RESULTS"] = queryLimit
	}
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "cr.weaviate.io/semitechnologies/weaviate:1.32.4",
			ExposedPorts: []string{"8080/tcp"},
			Env:          env,
			WaitingFor:   wait.ForHTTP("/v1/.well-known/ready").WithPort("8080/tcp").WithStartupTimeout(90 * time.Second),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start weaviate container: %v", err)
	}
	t.Cleanup(func() {
		if err := c.Terminate(context.Background()); err != nil {
			t.Logf("terminate weaviate container: %v", err)
		}
	})
	host, err := c.Host(ctx)
	if err != nil {
		t.Fatalf("container host: %v", err)
	}
	port, err := c.MappedPort(ctx, "8080")
	if err != nil {
		t.Fatalf("mapped port: %v", err)
	}
	return fmt.Sprintf("http://%s:%s", host, port.Port())
}

// TestWeaviateConformance runs the shared VectorBackend suite (VEC-010)
// against a real Weaviate.
func TestWeaviateConformance(t *testing.T) {
	endpoint := startWeaviate(t, "")
	conformance.Run(t, func(t *testing.T) backend.VectorBackend { return New(endpoint, testAPIKey) })
}

// TestWeaviateSpecifics covers what VEC-011 adds beyond the shared suite.
// Its container caps results at 5 per request, so deletes must page.
func TestWeaviateSpecifics(t *testing.T) {
	endpoint := startWeaviate(t, "5")
	ctx := context.Background()
	c := New(endpoint, testAPIKey)
	ns := backend.Namespace{Name: "ragctl-specifics", Dimensions: 4, Distance: "cosine"}
	if err := c.EnsureNamespace(ctx, ns); err != nil {
		t.Fatalf("EnsureNamespace: %v", err)
	}
	meta := backend.PointMetadata{Ecosystem: "go", Dependency: "d", Version: "v1", Generation: "g"}

	t.Run("WrongKeyIsUnhealthy", func(t *testing.T) {
		err := New(endpoint, "wrong").Health(ctx)
		if err == nil || !strings.Contains(err.Error(), "API key") {
			t.Fatalf("Health with a wrong key = %v, want an API-key error", err)
		}
	})

	t.Run("WrongSizedVectorIsAnError", func(t *testing.T) {
		if err := c.Upsert(ctx, backend.UpsertRequest{Namespace: ns.Name, Points: []backend.Point{{ID: "ok", Vector: []float32{1, 0, 0, 0}, Metadata: meta}}}); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
		bad := backend.Point{ID: "bad", Vector: []float32{1, 0}, Metadata: meta}
		if err := c.Upsert(ctx, backend.UpsertRequest{Namespace: ns.Name, Points: []backend.Point{bad}}); err == nil {
			t.Fatal("Upsert of a 2-dim vector into a 4-dim collection succeeded, want an error")
		}
	})

	// Filter values go into GraphQL text; quotes and backslashes must be
	// escaped, not break (or rewrite) the query.
	t.Run("FilterValuesAreEscaped", func(t *testing.T) {
		odd := meta
		odd.Version = `v"1\x`
		if err := c.Upsert(ctx, backend.UpsertRequest{Namespace: ns.Name, Points: []backend.Point{{ID: "odd", Vector: []float32{0, 1, 0, 0}, Metadata: odd}}}); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
		res, err := c.Query(ctx, backend.QueryRequest{Namespace: ns.Name, Vector: []float32{0, 1, 0, 0}, TopK: 5, Filter: &backend.Filter{Version: odd.Version}})
		if err != nil {
			t.Fatalf("Query: %v", err)
		}
		if len(res.Points) != 1 || res.Points[0].ID != "odd" {
			t.Errorf("Query by version %q = %+v, want only [odd]", odd.Version, res.Points)
		}
	})

	t.Run("DeleteByFilterPagesPastTheQueryLimit", func(t *testing.T) {
		paged := backend.Namespace{Name: "ragctl-paged", Dimensions: 4, Distance: "cosine"}
		if err := c.EnsureNamespace(ctx, paged); err != nil {
			t.Fatalf("EnsureNamespace: %v", err)
		}
		var points []backend.Point
		for i := range 12 {
			points = append(points, backend.Point{ID: fmt.Sprintf("p%d", i), Vector: []float32{1, float32(i), 0, 0}, Metadata: meta})
		}
		if err := c.Upsert(ctx, backend.UpsertRequest{Namespace: paged.Name, Points: points}); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
		if err := c.Delete(ctx, backend.DeleteRequest{Namespace: paged.Name, Filter: &backend.Filter{Version: "v1"}}); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if n, err := c.Count(ctx, paged.Name, nil); err != nil || n != 0 {
			t.Errorf("Count after deleting 12 objects with a 5-result cap = %d (err %v), want 0", n, err)
		}
	})

	t.Run("UnsupportedDistanceIsRejected", func(t *testing.T) {
		if err := c.EnsureNamespace(ctx, backend.Namespace{Name: "ragctl-dot", Dimensions: 4, Distance: "dot"}); err == nil {
			t.Fatal("EnsureNamespace with distance \"dot\" succeeded, want an error")
		}
	})
}

func TestClassName(t *testing.T) {
	for in, want := range map[string]string{
		"ragctl-d7b3d605":  "Ragctl_d7b3d605",
		"conformance_ab12": "Conformance_ab12",
		"Ragctl_d7b3d605":  "Ragctl_d7b3d605",
		"ragctl.v2-a1":     "Ragctl_v2_a1",
	} {
		if got, err := className(in); err != nil || got != want {
			t.Errorf("className(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "1ragctl", "ragctl space", "ragctl/x"} {
		if _, err := className(bad); err == nil {
			t.Errorf("className(%q) succeeded, want an error", bad)
		}
	}
}
