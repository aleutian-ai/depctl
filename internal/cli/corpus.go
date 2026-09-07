package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"aleutian-ai/ragctl/internal/config"
	"aleutian-ai/ragctl/internal/executil"
)

// corpusPackageJSON/corpusLockPkg mirror exactly what
// internal/resolver/node's static parser reads — a synthetic project
// this command owns entirely, never a project the user scanned
// themselves (CORPUS-001's own simplicity constraint).
type corpusPackageJSON struct {
	Name         string            `json:"name"`
	Version      string            `json:"version"`
	Dependencies map[string]string `json:"dependencies"`
}

type corpusLockFile struct {
	Name            string                   `json:"name"`
	Version         string                   `json:"version"`
	LockfileVersion int                      `json:"lockfileVersion"`
	Packages        map[string]corpusLockPkg `json:"packages"`
}

type corpusLockPkg struct {
	Name         string            `json:"name,omitempty"`
	Version      string            `json:"version"`
	Resolved     string            `json:"resolved,omitempty"`
	Dependencies map[string]string `json:"dependencies,omitempty"`
}

func newCorpusCmd() *cobra.Command {
	corpusCmd := &cobra.Command{
		Use:   "corpus",
		Short: "Manage a synthetic project for indexing local git repos that aren't real dependencies",
	}

	var name, ref string
	var resync bool
	addCmd := &cobra.Command{
		Use:   "add <local-git-path>",
		Short: "Index a local git repo as a synthetic dependency and scan it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCorpusAdd(cmd, args[0], name, ref, resync)
		},
	}
	addCmd.Flags().StringVar(&name, "name", "", "short id for this corpus entry (required)")
	addCmd.Flags().StringVar(&ref, "ref", "HEAD", "git ref to track")
	addCmd.Flags().BoolVar(&resync, "resync", false, "force a resync of an already-added name (bumps its version)")
	corpusCmd.AddCommand(addCmd)

	corpusCmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List corpus entries",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCorpusList(cmd)
		},
	})

	corpusCmd.AddCommand(&cobra.Command{
		Use:   "remove <name>",
		Short: "Remove a corpus entry",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCorpusRemove(cmd, args[0])
		},
	})

	return corpusCmd
}

// corpusDependencyName is the fake node package name a corpus entry is
// registered under, and its registry manifest's metadata.name.
func corpusDependencyName(name string) string {
	return "ragctl-corpus-" + name
}

func corpusProjectDir() (string, error) {
	dataDir, err := config.DefaultDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataDir, "corpus"), nil
}

func runCorpusAdd(cmd *cobra.Command, path, name, ref string, resync bool) error {
	if name == "" {
		return fmt.Errorf("--name is required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", path, err)
	}
	result, err := executil.Run(context.Background(), executil.RunOptions{
		Dir: abs, Args: []string{"git", "rev-parse", "--is-inside-work-tree"}, Timeout: 10 * time.Second,
	})
	if err != nil || result.ExitCode != 0 {
		return fmt.Errorf("%s is not a git repository", abs)
	}

	dir, err := corpusProjectDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create corpus project dir: %w", err)
	}
	pkg, lock, err := loadCorpusFiles(dir)
	if err != nil {
		return err
	}

	depName := corpusDependencyName(name)
	_, exists := pkg.Dependencies[depName]
	if exists && !resync {
		fmt.Fprintf(cmd.OutOrStdout(), "%s already added — pass --resync to force a resync\n", name)
		return nil
	}

	version := "1"
	if exists {
		version = time.Now().UTC().Format("20060102150405")
	}
	pkg.Dependencies[depName] = version
	lock.Packages["node_modules/"+depName] = corpusLockPkg{Version: version, Resolved: "local"}
	lock.Packages[""] = corpusLockPkg{Name: pkg.Name, Version: pkg.Version, Dependencies: pkg.Dependencies}

	if err := writeCorpusFiles(dir, pkg, lock); err != nil {
		return err
	}

	regDir, err := userRegistryDirPath()
	if err != nil {
		return fmt.Errorf("resolve user registry dir: %w", err)
	}
	if err := os.MkdirAll(regDir, 0o755); err != nil {
		return fmt.Errorf("create registry dir: %w", err)
	}
	manifestPath := filepath.Join(regDir, depName+".yaml")
	manifest := fmt.Sprintf(`apiVersion: ragctl.dev/v1alpha1
kind: KnowledgePackage
metadata:
  name: %s
match:
  ecosystems: [node]
  packages: [%s]
version:
  strategy: none
sources:
  - id: repository
    type: git
    url: %s
    ref: %s
    authority: 100
`, depName, depName, abs, ref)
	if err := os.WriteFile(manifestPath, []byte(manifest), 0o644); err != nil {
		return fmt.Errorf("write registry manifest: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "added %s -> %s (version %s)\n", name, abs, version)
	return runScan(cmd, dir)
}

func runCorpusList(cmd *cobra.Command) error {
	dir, err := corpusProjectDir()
	if err != nil {
		return err
	}
	pkg, _, err := loadCorpusFiles(dir)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	for depName, version := range pkg.Dependencies {
		fmt.Fprintf(out, "%-30s version=%s\n", depName, version)
	}
	fmt.Fprintf(out, "\n%d corpus entries\n", len(pkg.Dependencies))
	return nil
}

func runCorpusRemove(cmd *cobra.Command, name string) error {
	dir, err := corpusProjectDir()
	if err != nil {
		return err
	}
	pkg, lock, err := loadCorpusFiles(dir)
	if err != nil {
		return err
	}
	depName := corpusDependencyName(name)
	if _, ok := pkg.Dependencies[depName]; !ok {
		return fmt.Errorf("no corpus entry named %q", name)
	}
	delete(pkg.Dependencies, depName)
	delete(lock.Packages, "node_modules/"+depName)
	lock.Packages[""] = corpusLockPkg{Name: pkg.Name, Version: pkg.Version, Dependencies: pkg.Dependencies}

	if err := writeCorpusFiles(dir, pkg, lock); err != nil {
		return err
	}

	regDir, err := userRegistryDirPath()
	if err != nil {
		return fmt.Errorf("resolve user registry dir: %w", err)
	}
	if err := os.Remove(filepath.Join(regDir, depName+".yaml")); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove registry manifest: %w", err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "removed %s\n", name)
	return nil
}

// loadCorpusFiles reads the synthetic project's package.json/
// package-lock.json, creating empty ones if this is the first entry.
func loadCorpusFiles(dir string) (corpusPackageJSON, corpusLockFile, error) {
	pkgPath := filepath.Join(dir, "package.json")
	lockPath := filepath.Join(dir, "package-lock.json")

	pkg := corpusPackageJSON{Name: "ragctl-corpus", Version: "1.0.0", Dependencies: map[string]string{}}
	if data, err := os.ReadFile(pkgPath); err == nil {
		if err := json.Unmarshal(data, &pkg); err != nil {
			return corpusPackageJSON{}, corpusLockFile{}, fmt.Errorf("parse %s: %w", pkgPath, err)
		}
	} else if !os.IsNotExist(err) {
		return corpusPackageJSON{}, corpusLockFile{}, fmt.Errorf("read %s: %w", pkgPath, err)
	}

	lock := corpusLockFile{Name: pkg.Name, Version: pkg.Version, LockfileVersion: 3, Packages: map[string]corpusLockPkg{}}
	if data, err := os.ReadFile(lockPath); err == nil {
		if err := json.Unmarshal(data, &lock); err != nil {
			return corpusPackageJSON{}, corpusLockFile{}, fmt.Errorf("parse %s: %w", lockPath, err)
		}
	} else if !os.IsNotExist(err) {
		return corpusPackageJSON{}, corpusLockFile{}, fmt.Errorf("read %s: %w", lockPath, err)
	}
	return pkg, lock, nil
}

func writeCorpusFiles(dir string, pkg corpusPackageJSON, lock corpusLockFile) error {
	pkgData, err := json.MarshalIndent(pkg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal package.json: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "package.json"), pkgData, 0o644); err != nil {
		return fmt.Errorf("write package.json: %w", err)
	}

	lockData, err := json.MarshalIndent(lock, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal package-lock.json: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "package-lock.json"), lockData, 0o644); err != nil {
		return fmt.Errorf("write package-lock.json: %w", err)
	}
	return nil
}
