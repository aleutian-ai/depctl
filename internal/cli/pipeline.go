// pipeline.go wires together the concrete providers (embedder, vector
// backend, Git cache) that generation.Build/Replicate and
// validate/promote need — shared between `ragctl plan` (just needs the
// configured backend's name) and `ragctl sync` (needs the whole
// pipeline running for real).
package cli

import (
	"errors"
	"fmt"
	"os"

	"aleutian-ai/ragctl/internal/backend"
	"aleutian-ai/ragctl/internal/backend/pgvector"
	"aleutian-ai/ragctl/internal/backend/qdrant"
	"aleutian-ai/ragctl/internal/backend/weaviate"
	"aleutian-ai/ragctl/internal/config"
	"aleutian-ai/ragctl/internal/data/badger"
	"aleutian-ai/ragctl/internal/embedding"
	"aleutian-ai/ragctl/internal/embedding/cache"
	"aleutian-ai/ragctl/internal/embedding/ollama"
	"aleutian-ai/ragctl/internal/source/git"
)

// loadRagctlConfig loads config.yaml from its default location, falling
// back to documented defaults if the file doesn't exist yet — `plan`
// only needs the configured backend's name, which shouldn't require
// `ragctl init` to have run first.
func loadRagctlConfig() (config.Config, error) {
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
	cfg, err := loadRagctlConfig()
	if err != nil {
		return "", err
	}
	return cfg.Vector.Backend, nil
}

// buildEmbedder constructs the configured Embedder wrapped in a
// content-hash cache backed by badgerStore. "ollama" is the only
// supported provider in v0.1, matching EMB-002's scope.
func buildEmbedder(cfg config.Config, badgerStore *badger.Store) (embedding.Embedder, error) {
	if cfg.Embedding.Provider != "ollama" {
		return nil, fmt.Errorf("unsupported embedding provider %q (only \"ollama\" is implemented)", cfg.Embedding.Provider)
	}
	client := ollama.New(cfg.Embedding.Endpoint, cfg.Embedding.Model)
	return cache.New(client, badgerStore), nil
}

// buildVectorBackend constructs the configured VectorBackend: "qdrant"
// (the default), "pgvector" (VEC-014) or "weaviate" (VEC-011).
func buildVectorBackend(cfg config.Config) (backend.VectorBackend, error) {
	// vector.api_key_env names the env var holding the secret: Qdrant's
	// or Weaviate's API key, or pgvector's database password. It's read by whichever
	// process builds the backend (the daemon, normally), not the shell
	// running a ragctl command.
	var secret string
	if env := cfg.Vector.APIKeyEnv; env != "" {
		secret = os.Getenv(env)
		if secret == "" {
			return nil, fmt.Errorf("vector.api_key_env names %s, but it isn't set in the ragctl daemon's environment; export it, then run `ragctl daemon stop` so the next command starts a daemon that sees it", env)
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
	default:
		return nil, fmt.Errorf("unsupported vector backend %q (supported: \"qdrant\", \"pgvector\", \"weaviate\")", cfg.Vector.Backend)
	}
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
