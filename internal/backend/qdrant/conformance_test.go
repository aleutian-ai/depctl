package qdrant

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/aleutian-ai/depctl/internal/backend"
	"github.com/aleutian-ai/depctl/internal/backend/conformance"
)

// TestQdrantConformance runs the shared VectorBackend suite (VEC-010)
// against a real Qdrant. One container backs every subtest; the suite
// gives each subtest its own collection.
func TestQdrantConformance(t *testing.T) {
	requireContainerRuntime(t)
	t.Setenv("TESTCONTAINERS_RYUK_DISABLED", "true") // see TestQdrantIntegrationVersionFilteredQuery
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
	endpoint := fmt.Sprintf("http://%s:%s", host, port.Port())

	conformance.Run(t, func(t *testing.T) backend.VectorBackend { return New(endpoint) })
}
