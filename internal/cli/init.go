package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"aleutian-ai/ragctl/internal/config"
	bboltstore "aleutian-ai/ragctl/internal/control/bbolt"
	badgerstore "aleutian-ai/ragctl/internal/data/badger"
)

func newInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Initialize ragctl's local config and storage directories",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runInit(cmd)
		},
	}
}

// runInit creates everything ragctl needs to operate, if not already
// present. It is safe to run repeatedly: existing files/dirs are left
// untouched, and the summary distinguishes "created" from "already present".
func runInit(cmd *cobra.Command) error {
	dataDir, err := config.DefaultDataDir()
	if err != nil {
		return fmt.Errorf("resolve data dir: %w", err)
	}
	configPath, err := config.DefaultConfigPath()
	if err != nil {
		return fmt.Errorf("resolve config path: %w", err)
	}

	out := cmd.OutOrStdout()
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
		if err := config.Default(dataDir).Save(configPath); err != nil {
			return fmt.Errorf("write config: %w", err)
		}
		reportStatus(out, configPath, false)
	} else if err != nil {
		return fmt.Errorf("stat config %s: %w", configPath, err)
	} else {
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
