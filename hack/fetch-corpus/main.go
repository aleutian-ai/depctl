// Command fetch-corpus populates a local, real-world repo corpus (e.g.
// ~/offline-knowledge) for exercising depctl's resolvers and acquisition
// layer against genuine projects rather than synthetic fixtures. It
// replaces ad hoc shell clone loops with a single shared bare mirror per
// repository (internal/source/git) plus a persistent worktree
// checkout, so re-runs are fast, idempotent, and don't re-download
// history that's already cached. Not part of the depctl binary — a dev
// tool, run via `go run ./hack/fetch-corpus`.
package main

import (
	"bufio"
	"bytes"
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/aleutian-ai/depctl/internal/config"
	"github.com/aleutian-ai/depctl/internal/executil"
	git "github.com/aleutian-ai/depctl/internal/source/git"
)

// repoSpec is one manifest line: a repo to fetch, grouped under an
// arbitrary ecosystem/category subdirectory of the corpus root.
type repoSpec struct {
	Ecosystem string
	Slug      string // "org/repo"
}

func main() {
	manifestPath := flag.String("manifest", "hack/fetch-corpus/repos.txt", "path to the repo list (\"<ecosystem> <org>/<repo>\" per line)")
	corpusRoot := flag.String("root", defaultCorpusRoot(), "directory to check working copies out into, one subdir per ecosystem")
	mirrorRoot := flag.String("mirror-root", defaultMirrorRoot(), "directory for shared bare mirrors (defaults to depctl's own git cache, so this doubles as cache warming)")
	concurrency := flag.Int("concurrency", 6, "number of repos to fetch concurrently")
	backupDir := flag.String("backup", "", "if set, rsync the corpus root's contents there after fetching (additive only — never deletes, e.g. a /Volumes/... external drive)")
	flag.Parse()

	specs, err := readManifest(*manifestPath)
	if err != nil {
		log.Fatalf("read manifest: %v", err)
	}

	cache := git.NewCache(*mirrorRoot)
	sem := make(chan struct{}, *concurrency)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var failed []string

	for _, spec := range specs {
		wg.Add(1)
		sem <- struct{}{}
		go func(spec repoSpec) {
			defer wg.Done()
			defer func() { <-sem }()

			if err := fetchOne(cache, *corpusRoot, spec); err != nil {
				mu.Lock()
				failed = append(failed, fmt.Sprintf("%s/%s: %v", spec.Ecosystem, spec.Slug, err))
				mu.Unlock()
				fmt.Printf("FAIL  %-10s %s: %v\n", spec.Ecosystem, spec.Slug, err)
			}
		}(spec)
	}
	wg.Wait()

	exitCode := 0
	if len(failed) > 0 {
		fmt.Fprintf(os.Stderr, "\n%d of %d repos failed:\n", len(failed), len(specs))
		for _, f := range failed {
			fmt.Fprintln(os.Stderr, "  "+f)
		}
		exitCode = 1
	}

	if *backupDir != "" {
		fmt.Printf("\nbacking up %s -> %s\n", *corpusRoot, *backupDir)
		if err := backupCorpus(*corpusRoot, *backupDir); err != nil {
			fmt.Fprintf(os.Stderr, "backup failed: %v\n", err)
			exitCode = 1
		} else {
			fmt.Println("backup complete")
		}
	}

	os.Exit(exitCode)
}

// backupCorpus rsyncs corpusRoot's contents into dst, additively — it
// never passes --delete, so files already at dst that no longer exist in
// corpusRoot are left alone rather than removed.
func backupCorpus(corpusRoot, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return fmt.Errorf("mkdir backup dest: %w", err)
	}

	// A trailing slash on the source makes rsync copy corpusRoot's
	// *contents* into dst, rather than nesting a corpusRoot-named
	// directory inside it.
	src := strings.TrimSuffix(corpusRoot, string(filepath.Separator)) + string(filepath.Separator)

	result, err := executil.Run(context.Background(), executil.RunOptions{
		Args:    []string{"rsync", "-a", "--stats", src, dst},
		Timeout: 2 * time.Hour,
	})
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("rsync exited %d: %s", result.ExitCode, bytes.TrimSpace(result.Stderr))
	}
	fmt.Print(string(result.Stdout))
	return nil
}

// fetchOne ensures a shared bare mirror exists and is up to date, then
// materializes a persistent HEAD checkout under corpusRoot if one isn't
// already there. Unlike git.Cache.MaterializeWorktree (which is scoped to
// disposable normalization worktrees), the checkout here is meant to
// persist, so it's registered against the mirror directly rather than
// through that temp-dir-and-cleanup API.
func fetchOne(cache *git.Cache, corpusRoot string, spec repoSpec) error {
	name := path.Base(spec.Slug)
	targetDir := filepath.Join(corpusRoot, spec.Ecosystem, name)

	if info, err := os.Stat(targetDir); err == nil && info.IsDir() {
		fmt.Printf("SKIP  %-10s %s (already checked out)\n", spec.Ecosystem, spec.Slug)
		return nil
	}

	ctx := context.Background()
	url := "https://github.com/" + spec.Slug + ".git"

	repoPath, err := cache.EnsureMirror(ctx, url)
	if err != nil {
		return fmt.Errorf("mirror: %w", err)
	}
	if err := cache.FetchTags(ctx, repoPath); err != nil {
		return fmt.Errorf("fetch tags: %w", err)
	}

	commit, err := cache.ResolveRef(ctx, repoPath, "HEAD")
	if err != nil {
		return fmt.Errorf("resolve HEAD: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(targetDir), 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	result, err := executil.Run(ctx, executil.RunOptions{
		Args:    []string{"git", "-C", repoPath, "worktree", "add", "--detach", targetDir, commit},
		Timeout: 60 * time.Second,
	})
	if err != nil {
		return fmt.Errorf("worktree add: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("worktree add: exit %d: %s", result.ExitCode, bytes.TrimSpace(result.Stderr))
	}

	fmt.Printf("OK    %-10s %s @ %s\n", spec.Ecosystem, spec.Slug, commit[:12])
	return nil
}

// readManifest parses lines of "<ecosystem> <org>/<repo>"; blank lines
// and lines starting with # are ignored.
func readManifest(path string) ([]repoSpec, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var specs []repoSpec
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return nil, fmt.Errorf("malformed line %q: want \"<ecosystem> <org>/<repo>\"", line)
		}
		specs = append(specs, repoSpec{Ecosystem: fields[0], Slug: fields[1]})
	}
	return specs, scanner.Err()
}

func defaultCorpusRoot() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "./offline-knowledge"
	}
	return filepath.Join(home, "offline-knowledge")
}

// defaultMirrorRoot points at depctl's own git cache location
// (config.DefaultDataDir()/git — the same path GIT-001 uses), so running
// this tool also warms the cache depctl's own acquisition step will use.
func defaultMirrorRoot() string {
	dataDir, err := config.DefaultDataDir()
	if err != nil {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, ".local", "share", "depctl", "git")
	}
	return filepath.Join(dataDir, "git")
}
