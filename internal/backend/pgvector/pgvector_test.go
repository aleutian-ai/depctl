package pgvector

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/aleutian-ai/depctl/internal/backend"
	"github.com/aleutian-ai/depctl/internal/backend/conformance"
)

// requireContainerRuntime skips when no Docker-API-compatible runtime is
// reachable (same convention as the Qdrant adapter's tests).
func requireContainerRuntime(t *testing.T) {
	t.Helper()
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("no Docker-API-compatible container runtime reachable")
	}
}

// startPostgres runs a real Postgres with pgvector available and returns
// its DSN (without the password, which is passed separately, the way
// depctl's config supplies it).
func startPostgres(t *testing.T) (dsn, password string) {
	t.Helper()
	requireContainerRuntime(t)
	t.Setenv("TESTCONTAINERS_RYUK_DISABLED", "true") // rootless Podman has no "bridge" network for ryuk
	ctx := context.Background()
	password = "pgvector-test"
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "pgvector/pgvector:pg17",
			ExposedPorts: []string{"5432/tcp"},
			Env:          map[string]string{"POSTGRES_PASSWORD": password},
			// The server logs "ready" once for its init pass, then again
			// for real.
			WaitingFor: wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(90 * time.Second),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start postgres container: %v", err)
	}
	t.Cleanup(func() {
		if err := c.Terminate(context.Background()); err != nil {
			t.Logf("terminate postgres container: %v", err)
		}
	})
	host, err := c.Host(ctx)
	if err != nil {
		t.Fatalf("container host: %v", err)
	}
	port, err := c.MappedPort(ctx, "5432")
	if err != nil {
		t.Fatalf("mapped port: %v", err)
	}
	return fmt.Sprintf("postgres://postgres@%s:%s/postgres?sslmode=disable", host, port.Port()), password
}

// TestPgvectorConformance runs the shared VectorBackend suite (VEC-010)
// against a real Postgres + pgvector.
func TestPgvectorConformance(t *testing.T) {
	dsn, password := startPostgres(t)
	conformance.Run(t, func(t *testing.T) backend.VectorBackend {
		a, err := New(dsn, password)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		return a
	})
}

// TestPgvectorSpecifics covers what VEC-014 adds beyond the shared suite.
func TestPgvectorSpecifics(t *testing.T) {
	dsn, password := startPostgres(t)
	ctx := context.Background()
	a, err := New(dsn, password)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ns := backend.Namespace{Name: "depctl-specifics", Dimensions: 4, Distance: "cosine"}
	if err := a.EnsureNamespace(ctx, ns); err != nil {
		t.Fatalf("EnsureNamespace: %v", err)
	}

	t.Run("ReEnsuringWithADifferentDimensionIsAClearError", func(t *testing.T) {
		bad := ns
		bad.Dimensions = 768
		if err := a.EnsureNamespace(ctx, bad); !errors.Is(err, ErrDimensionMismatch) {
			t.Fatalf("EnsureNamespace with a new dimension = %v, want ErrDimensionMismatch", err)
		}
	})

	t.Run("WrongSizedVectorWritesNothing", func(t *testing.T) {
		ok := backend.Point{ID: "ok", Vector: []float32{1, 0, 0, 0}, Metadata: backend.PointMetadata{Ecosystem: "go", Dependency: "d", Version: "v1", Generation: "g"}}
		bad := backend.Point{ID: "bad", Vector: []float32{1, 0}, Metadata: ok.Metadata}
		if err := a.Upsert(ctx, backend.UpsertRequest{Namespace: ns.Name, Points: []backend.Point{ok, bad}}); err == nil {
			t.Fatal("Upsert with a 2-dim vector into a 4-dim namespace succeeded, want an error")
		}
		if n, err := a.Count(ctx, ns.Name, nil); err != nil || n != 0 {
			t.Errorf("Count after the failed batch = %d (err %v), want 0: the whole batch must roll back", n, err)
		}
	})

	t.Run("UnsupportedDistanceIsRejected", func(t *testing.T) {
		if err := a.EnsureNamespace(ctx, backend.Namespace{Name: "depctl-dot", Dimensions: 4, Distance: "dot"}); err == nil {
			t.Fatal("EnsureNamespace with distance \"dot\" succeeded, want an error")
		}
	})

	t.Run("NewReusesOnePoolPerDatabase", func(t *testing.T) {
		b, err := New(dsn, password)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if b.pool != a.pool {
			t.Error("a second New for the same database opened a second pool")
		}
	})
}
