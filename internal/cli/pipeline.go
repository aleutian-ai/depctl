// pipeline.go builds the configured providers (embedder, vector store,
// Git cache) that sync needs; `depctl plan` only needs the configured
// vector backend's name. The keyword index is built in retrieval.go.

package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/aleutian-ai/depctl/internal/backend"
	"github.com/aleutian-ai/depctl/internal/backend/embedded"
	"github.com/aleutian-ai/depctl/internal/backend/pgvector"
	"github.com/aleutian-ai/depctl/internal/backend/qdrant"
	"github.com/aleutian-ai/depctl/internal/backend/weaviate"
	"github.com/aleutian-ai/depctl/internal/config"
	"github.com/aleutian-ai/depctl/internal/data/badger"
	"github.com/aleutian-ai/depctl/internal/embedding"
	"github.com/aleutian-ai/depctl/internal/embedding/cache"
	"github.com/aleutian-ai/depctl/internal/embedding/ollama"
	"github.com/aleutian-ai/depctl/internal/source/git"
)

// loadDepctlConfig loads config.yaml from its default location, falling
// back to documented defaults if the file doesn't exist yet — `plan`
// only needs the configured backend's name, which shouldn't require
// `depctl init` to have run first.
func loadDepctlConfig() (config.Config, error) {
	path, err := config.DefaultConfigPath()
	if err != nil {
		return config.Config{}, fmt.Errorf("resolve config path: %w", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		if errors.Is(err, config.ErrConfigNotFound) {
			dataDir, dErr := config.DefaultDataDir()
			if dErr != nil {
				return config.Config{}, fmt.Errorf("resolve data dir: %w", dErr)
			}
			return config.Default(dataDir), nil
		}
		return config.Config{}, err
	}
	return cfg, nil
}

// configuredVectorBackendName returns just the backend name from
// config, without constructing a client — all `computePlans` needs to
// check GetActiveGeneration.
func configuredVectorBackendName() (string, error) {
	cfg, err := loadDepctlConfig()
	if err != nil {
		return "", err
	}
	return cfg.Vector.Backend, nil
}

// buildEmbedder constructs the configured Embedder wrapped in a
// content-hash cache backed by badgerStore. "ollama" is the only
// supported provider.
func buildEmbedder(cfg config.Config, badgerStore *badger.Store) (*embedding.Prompted, error) {
	if cfg.Embedding.Provider != "ollama" {
		return nil, fmt.Errorf("unsupported embedding provider %q (only \"ollama\" is implemented)", cfg.Embedding.Provider)
	}
	client := ollama.New(cfg.Embedding.Endpoint, cfg.Embedding.Model)
	return &embedding.Prompted{Embedder: cache.New(client, badgerStore), Prompts: cfg.Embedding.Prompts()}, nil
}

// buildVectorBackend constructs the configured vector store: "embedded"
// (the default for fresh installs; a file in the data dir, no service),
// "qdrant", "pgvector" or "weaviate".
func buildVectorBackend(cfg config.Config) (backend.VectorBackend, error) {
	// vector.api_key_env names the env var holding the secret: Qdrant's
	// or Weaviate's API key, or pgvector's database password. It's read by whichever
	// process builds the backend (the daemon, normally), not the shell
	// running a depctl command.
	var secret string
	if env := cfg.Vector.APIKeyEnv; env != "" {
		secret = os.Getenv(env)
		if secret == "" {
			return nil, fmt.Errorf("vector.api_key_env names %s, but it isn't set in the depctl daemon's environment; export it, then run `depctl daemon stop` so the next command starts a daemon that sees it", env)
		}
	}
	switch cfg.Vector.Backend {
	case "qdrant":
		var opts []qdrant.Option
		if secret != "" {
			opts = append(opts, qdrant.WithAPIKey(secret))
		}
		return qdrant.New(cfg.Vector.Endpoint, opts...), nil
	case "pgvector":
		return pgvector.New(cfg.Vector.Endpoint, secret)
	case "weaviate":
		return weaviate.New(cfg.Vector.Endpoint, secret), nil
	case "embedded":
		path, err := embeddedVectorPath(cfg)
		if err != nil {
			return nil, err
		}
		return embedded.New(path), nil
	default:
		return nil, fmt.Errorf("unsupported vector backend %q (supported: \"qdrant\", \"pgvector\", \"weaviate\", \"embedded\")", cfg.Vector.Backend)
	}
}

// embeddedVectorPath is where the embedded backend keeps its file:
// vector.endpoint if set (a file path), else vectors.db next to the
// configured control.db (the data dir, by default).
func embeddedVectorPath(cfg config.Config) (string, error) {
	if cfg.Vector.Endpoint == "" {
		return filepath.Join(filepath.Dir(cfg.Storage.Control.Path), "vectors.db"), nil
	}
	if strings.Contains(cfg.Vector.Endpoint, "://") {
		return "", fmt.Errorf("vector.endpoint is %q, but the embedded backend takes a file path; remove vector.endpoint to use the default <data-dir>/vectors.db", cfg.Vector.Endpoint)
	}
	return cfg.Vector.Endpoint, nil
}

// buildGitCache returns a git.Cache rooted under the configured data
// directory's "git" subdirectory, matching GIT-001's documented layout,
// with cfg.Git's optional local-seed fallback tiers (GIT-006/GIT-007)
// configured — both empty by default, zero behavior change unless a
// user explicitly sets them — and cfg.Fetch's per-repository mirror size
// cap (SEC-003).
func buildGitCache(cfg config.Config) (*git.Cache, error) {
	dataDir, err := config.DefaultDataDir()
	if err != nil {
		return nil, fmt.Errorf("resolve data dir: %w", err)
	}
	return git.NewCache(dataDir+"/git",
		git.WithExternalMirrorRoots(cfg.Git.MirrorSearchPaths),
		git.WithCheckoutSearchRoots(cfg.Git.CheckoutSearchPaths),
		git.WithMaxMirrorBytes(cfg.Fetch.MaxSourceTotalBytesOrDefault()),
	), nil
}
