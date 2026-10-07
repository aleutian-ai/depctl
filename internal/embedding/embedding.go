// Package embedding defines ragctl's narrow embedding-provider contract
// and the identity metadata that keeps vectors from different
// providers/models from ever being silently mixed. Concrete providers
// (internal/embedding/ollama) and the content-hash cache
// (internal/embedding/cache) build on this package; it has no
// provider-specific code of its own.
package embedding

import (
	"context"
	"time"
)

// Embedder turns chunk text into vectors. Implementations batch
// request/response only — no streaming/async API, per EMB-001's
// simplicity constraints.
type Embedder interface {
	// Name identifies the provider (e.g. "ollama").
	Name() string
	// ModelID identifies the specific embedding model in use.
	ModelID() string
	// Dimensions reports the vector length this Embedder produces.
	Dimensions(ctx context.Context) (int, error)
	// Embed returns one vector per input text, in the same order. A
	// failure fails the whole batch — callers retry the batch, there is
	// no partial-batch success in v0.1.
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// EmbeddingIdentity describes the provider/model/normalization that
// produced a set of vectors. Nothing persists it yet: a replica's model
// and dimensions are recorded on domain.BackendReplica, and the
// embedding cache keys on provider and model directly.
type EmbeddingIdentity struct {
	Provider      string
	Model         string
	Dimensions    int
	Normalization string
	CreatedAt     time.Time
}
