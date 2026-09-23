package qdrant

import (
	"context"
	"fmt"
	"os/exec"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"aleutian-ai/ragctl/internal/backend"
)

// requireContainerRuntime skips the test if no Docker-API-compatible
// container runtime is reachable (Docker Desktop, or Podman's Docker
// socket compatibility) — same "skip rather than fail" convention as
// internal/source/git's requireGit for a missing git binary.
func requireContainerRuntime(t *testing.T) {
	t.Helper()
	cmd := exec.Command("docker", "info")
	if err := cmd.Run(); err != nil {
		t.Skip("no Docker-API-compatible container runtime reachable")
	}
}

// TestQdrantIntegrationVersionFilteredQuery spins up a real Qdrant
// container and exercises the full round trip VEC-002 exists for:
// upserting two versions of the same package's chunks into one shared
// collection, then confirming a version-filtered query returns only the
// requested version's points — the mechanism that lets retrieval stay
// constrained to an exact dependency version.
func TestQdrantIntegrationVersionFilteredQuery(t *testing.T) {
	requireContainerRuntime(t)
	// testcontainers' reaper ("ryuk") sidecar needs a "bridge" network,
	// which rootless Podman's Docker-API compatibility layer doesn't
	// provide (only "podman") — disabling it is safe here since the test
	// already explicitly Terminate()s the container in t.Cleanup; ryuk is
	// only a backstop for a killed/crashed process, not this test's
	// primary cleanup path.
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

	c := New(fmt.Sprintf("http://%s:%s", host, port.Port()))

	ns := backend.Namespace{Name: "ragctl", Dimensions: 4, Distance: "cosine"}
	if err := c.EnsureNamespace(ctx, ns); err != nil {
		t.Fatalf("EnsureNamespace: %v", err)
	}
	// Idempotent re-call must be a no-op, not an error.
	if err := c.EnsureNamespace(ctx, ns); err != nil {
		t.Fatalf("second EnsureNamespace: %v", err)
	}

	err = c.Upsert(ctx, backend.UpsertRequest{
		Namespace: ns.Name,
		Points: []backend.Point{
			{
				ID:     "chk_v67_1",
				Vector: []float32{1, 0, 0, 0},
				Metadata: backend.PointMetadata{
					Ecosystem: "go", Dependency: "grpc-go", Version: "v1.67.0", Generation: "gen_a", SourceType: "git", Authority: 100,
				},
			},
			{
				ID:     "chk_v68_1",
				Vector: []float32{1, 0, 0, 0},
				Metadata: backend.PointMetadata{
					Ecosystem: "go", Dependency: "grpc-go", Version: "v1.68.0", Generation: "gen_b", SourceType: "git", Authority: 100,
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	result, err := c.Query(ctx, backend.QueryRequest{
		Namespace: ns.Name,
		Vector:    []float32{1, 0, 0, 0},
		TopK:      10,
		Filter:    &backend.Filter{Dependency: "grpc-go", Version: "v1.67.0"},
	})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(result.Points) != 1 {
		t.Fatalf("got %d points, want exactly 1 (version-filtered)", len(result.Points))
	}
	if result.Points[0].ID != "chk_v67_1" {
		t.Errorf("matched point ID = %s, want chk_v67_1", result.Points[0].ID)
	}
	if result.Points[0].Metadata.Version != "v1.67.0" {
		t.Errorf("matched point version = %s, want v1.67.0", result.Points[0].Metadata.Version)
	}

	if err := c.Delete(ctx, backend.DeleteRequest{Namespace: ns.Name, IDs: []string{"chk_v67_1", "chk_v68_1"}}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	result, err = c.Query(ctx, backend.QueryRequest{Namespace: ns.Name, Vector: []float32{1, 0, 0, 0}, TopK: 10})
	if err != nil {
		t.Fatalf("Query after delete: %v", err)
	}
	if len(result.Points) != 0 {
		t.Errorf("got %d points after delete, want 0", len(result.Points))
	}
}

// TestQdrantIntegrationDeleteByIDsAndFilterTogether is a regression test
// for a real bug an adversarial review caught: sending both req.IDs and
// req.Filter on one DeleteRequest silently deleted only by ID and
// dropped the filter, because Qdrant's points_delete selector is a "one
// of {points, filter}" and combining both into a single request body
// only ever applies whichever field the server's untagged deserializer
// matches first. Delete now issues two separate requests; this proves
// both actually take effect against a real server, not just that the
// combined-request JSON was shaped correctly (which the old,
// insufficient unit test verified and which passed even with the bug
// present).
func TestQdrantIntegrationDeleteByIDsAndFilterTogether(t *testing.T) {
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
	c := New(fmt.Sprintf("http://%s:%s", host, port.Port()))

	ns := backend.Namespace{Name: "ragctl", Dimensions: 4, Distance: "cosine"}
	if err := c.EnsureNamespace(ctx, ns); err != nil {
		t.Fatalf("EnsureNamespace: %v", err)
	}

	err = c.Upsert(ctx, backend.UpsertRequest{
		Namespace: ns.Name,
		Points: []backend.Point{
			{ID: "chk_by_id", Vector: []float32{1, 0, 0, 0}, Metadata: backend.PointMetadata{Ecosystem: "go", Dependency: "widget", Version: "v1.0.0", Generation: "gen_a"}},
			{ID: "chk_by_filter_1", Vector: []float32{1, 0, 0, 0}, Metadata: backend.PointMetadata{Ecosystem: "go", Dependency: "widget", Version: "v0.9.0", Generation: "gen_old"}},
			{ID: "chk_by_filter_2", Vector: []float32{1, 0, 0, 0}, Metadata: backend.PointMetadata{Ecosystem: "go", Dependency: "widget", Version: "v0.9.0", Generation: "gen_old"}},
			{ID: "chk_survivor", Vector: []float32{1, 0, 0, 0}, Metadata: backend.PointMetadata{Ecosystem: "go", Dependency: "widget", Version: "v1.0.0", Generation: "gen_a"}},
		},
	})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	// One call, deleting one point by explicit ID AND two more via a
	// filter that matches neither of those IDs — both must take effect.
	err = c.Delete(ctx, backend.DeleteRequest{
		Namespace: ns.Name,
		IDs:       []string{"chk_by_id"},
		Filter:    &backend.Filter{Generation: "gen_old"},
	})
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}

	result, err := c.Query(ctx, backend.QueryRequest{Namespace: ns.Name, Vector: []float32{1, 0, 0, 0}, TopK: 10})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(result.Points) != 1 || result.Points[0].ID != "chk_survivor" {
		t.Fatalf("remaining points = %+v, want exactly [chk_survivor]", result.Points)
	}
}

// TestQdrantIntegrationCountAgainstARealCollection is POINT-003's own
// proof point: Count must return the real, exact number of points for a
// generation, including zero for a generation with none at all — not a
// TopK-bounded approximation via Query, which would report "1" for a
// generation with 50 points once TopK caps it, or worse, "0" only by
// accident if no query vector happened to be similar enough to surface
// any of them.
func TestQdrantIntegrationCountAgainstARealCollection(t *testing.T) {
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
	c := New(fmt.Sprintf("http://%s:%s", host, port.Port()))

	ns := backend.Namespace{Name: "ragctl", Dimensions: 4, Distance: "cosine"}
	if err := c.EnsureNamespace(ctx, ns); err != nil {
		t.Fatalf("EnsureNamespace: %v", err)
	}

	err = c.Upsert(ctx, backend.UpsertRequest{
		Namespace: ns.Name,
		Points: []backend.Point{
			{ID: "chk_1", Vector: []float32{1, 0, 0, 0}, Metadata: backend.PointMetadata{Ecosystem: "go", Dependency: "widget", Version: "v1.0.0", Generation: "gen_has_points"}},
			{ID: "chk_2", Vector: []float32{0, 1, 0, 0}, Metadata: backend.PointMetadata{Ecosystem: "go", Dependency: "widget", Version: "v1.0.0", Generation: "gen_has_points"}},
			{ID: "chk_3", Vector: []float32{0, 0, 1, 0}, Metadata: backend.PointMetadata{Ecosystem: "go", Dependency: "other", Version: "v2.0.0", Generation: "gen_other"}},
		},
	})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	n, err := c.Count(ctx, ns.Name, &backend.Filter{Generation: "gen_has_points"})
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if n != 2 {
		t.Errorf("Count(gen_has_points) = %d, want 2", n)
	}

	n, err = c.Count(ctx, ns.Name, &backend.Filter{Generation: "gen_no_points_at_all"})
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if n != 0 {
		t.Errorf("Count(gen_no_points_at_all) = %d, want 0 — the exact case POINT-003 needs to detect", n)
	}

	n, err = c.Count(ctx, ns.Name, nil)
	if err != nil {
		t.Fatalf("Count with nil filter: %v", err)
	}
	if n != 3 {
		t.Errorf("Count(nil filter) = %d, want 3 (the whole namespace)", n)
	}
}
