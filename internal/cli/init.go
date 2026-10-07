package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"aleutian-ai/ragctl/internal/config"
	bboltstore "aleutian-ai/ragctl/internal/control/bbolt"
	badgerstore "aleutian-ai/ragctl/internal/data/badger"
)

func newInitCmd() *cobra.Command {
	var vectorBackend, retrievalMode string
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize ragctl's local config and storage directories",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runInit(cmd, vectorBackend, retrievalMode)
		},
	}
	cmd.Flags().StringVar(&retrievalMode, "retrieval-mode", "",
		`how search works, for a new config: "auto" (default; hybrid keyword + semantic search when Ollama is available, keyword search otherwise), "vector" (always require Ollama), or "keyword" (never use an embedding model)`)
	cmd.Flags().StringVar(&vectorBackend, "vector-backend", "",
		`vector store for a new config: "embedded" (default; a file in the data dir, no service or container) or "qdrant" (a local Qdrant, which ragctl starts in a container if none is running). To use your own Qdrant, pgvector or Weaviate, edit the vector section of the config.`)
	return cmd
}

// runInit creates everything ragctl needs to operate, if not already
// present. It is safe to run repeatedly: existing files/dirs are left
// untouched, and the summary distinguishes "created" from "already present".
func runInit(cmd *cobra.Command, vectorBackend, retrievalMode string) error {
	switch vectorBackend {
	case "", "qdrant", "embedded":
	default:
		return fmt.Errorf("--vector-backend %q: use \"qdrant\" or \"embedded\" (for your own Qdrant, pgvector or Weaviate, edit the vector section of the config)", vectorBackend)
	}
	switch retrievalMode {
	case "", config.RetrievalAuto, config.RetrievalVector, config.RetrievalKeyword:
	default:
		return fmt.Errorf("--retrieval-mode %q: use auto, vector or keyword", retrievalMode)
	}
	// init is one of the only commands that opens the stores itself, so
	// it has to refuse while the daemon owns them (ADR-011).
	if err := requireNoDaemon(cmd.Context()); err != nil {
		return err
	}
	return initStores(cmd.OutOrStdout(), vectorBackend, retrievalMode)
}

// initStores is runInit's actual work, factored out so ensureInitialized
// can run the identical logic silently (to os.Stderr) when a command or
// MCP tool call needs the stores and they don't exist yet — auto-init on
// first use rather than a hard "run `ragctl init` first" refusal, since
// init asks no interactive questions and is safe to run repeatedly (even
// racing itself: every step here is idempotent). vectorBackend, when
// set, chooses the vector store for a newly written config; an existing
// config is never rewritten, so asking for a different one is an error.
func initStores(out io.Writer, vectorBackend, retrievalMode string) error {
	dataDir, err := config.DefaultDataDir()
	if err != nil {
		return fmt.Errorf("resolve data dir: %w", err)
	}
	configPath, err := config.DefaultConfigPath()
	if err != nil {
		return fmt.Errorf("resolve config path: %w", err)
	}

	gitDir := filepath.Join(dataDir, "git")
	registryDir := filepath.Join(dataDir, "registry")
	controlPath, err := controlDBPath()
	if err != nil {
		return fmt.Errorf("resolve control db path: %w", err)
	}
	badgerDir, err := badgerDirPath()
	if err != nil {
		return fmt.Errorf("resolve badger dir: %w", err)
	}

	// Plain dirs: just MkdirAll and report existed-before. dataDir and
	// configPath's parent are the same directory on macOS (see
	// config.DefaultDataDir), so dedupe before reporting.
	for _, dir := range dedupe(dataDir, gitDir, registryDir, filepath.Dir(configPath)) {
		existed := dirExists(dir)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
		reportStatus(out, dir, existed)
	}

	// Config file: write defaults only if absent, never overwrite.
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		cfg := config.Default(dataDir)
		if vectorBackend == "qdrant" {
			cfg.Vector.QdrantDefaults()
		}
		if retrievalMode != "" {
			cfg.Retrieval.Mode = retrievalMode
		}
		if err := cfg.Save(configPath); err != nil {
			return fmt.Errorf("write config: %w", err)
		}
		reportStatus(out, configPath, false)
	} else if err != nil {
		return fmt.Errorf("stat config %s: %w", configPath, err)
	} else {
		if vectorBackend != "" {
			existing, err := config.Load(configPath)
			if err != nil {
				return fmt.Errorf("read config %s: %w", configPath, err)
			}
			if existing.Vector.Backend != vectorBackend {
				return fmt.Errorf("%s already exists with vector.backend %q; init never rewrites it, so edit vector.backend there to switch to %q", configPath, existing.Vector.Backend, vectorBackend)
			}
		}
		if retrievalMode != "" {
			existing, err := config.Load(configPath)
			if err != nil {
				return fmt.Errorf("read config %s: %w", configPath, err)
			}
			if have := existing.Retrieval.ModeOrDefault(); have != retrievalMode {
				return fmt.Errorf("%s already exists with retrieval.mode %q; init never rewrites it, so edit retrieval.mode there to switch to %q", configPath, have, retrievalMode)
			}
		}
		reportStatus(out, configPath, true)
	}

	// bbolt control DB: Open+Close creates the file and its buckets.
	controlExisted := fileExists(controlPath)
	control, err := bboltstore.Open(controlPath)
	if err != nil {
		return fmt.Errorf("open control db: %w", err)
	}
	if err := control.Close(); err != nil {
		return fmt.Errorf("close control db: %w", err)
	}
	reportStatus(out, controlPath, controlExisted)

	// Badger data dir: Open+Close creates the directory contents.
	badgerExisted := dirNonEmpty(badgerDir)
	data, err := badgerstore.Open(badgerDir)
	if err != nil {
		return fmt.Errorf("open badger store: %w", err)
	}
	if err := data.Close(); err != nil {
		return fmt.Errorf("close badger store: %w", err)
	}
	reportStatus(out, badgerDir, badgerExisted)

	return nil
}

func reportStatus(out interface{ Write([]byte) (int, error) }, path string, existed bool) {
	status := "created"
	if existed {
		status = "already present"
	}
	fmt.Fprintf(out, "%-12s %s\n", status, path)
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func dirNonEmpty(path string) bool {
	entries, err := os.ReadDir(path)
	return err == nil && len(entries) > 0
}

// dedupe returns dirs with duplicates removed, preserving first-seen order.
func dedupe(dirs ...string) []string {
	seen := make(map[string]bool, len(dirs))
	out := make([]string, 0, len(dirs))
	for _, d := range dirs {
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	return out
}
