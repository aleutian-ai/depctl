package golang

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"aleutian-ai/ragctl/internal/executil"
)

// skipWorkspaceScanDirs are directory names never worth descending into
// while looking for sibling go.mod files — large and never themselves a
// separate Go module worth adding to the synthesized workspace.
var skipWorkspaceScanDirs = map[string]bool{
	".git":         true,
	"vendor":       true,
	"node_modules": true,
}

// synthesizeWorkspace builds a temporary go.work covering every go.mod
// found under root's repository (the nearest ancestor containing .git),
// so a submodule whose own go.mod replace directives don't fully resolve
// its siblings (see unresolvedLocalReplaceMarker) can resolve via the
// workspace instead. Returns the go.work path and a cleanup func; err is
// non-nil if no repo root was found or fewer than two go.mod files exist
// under it (a workspace of one module can't help).
func synthesizeWorkspace(ctx context.Context, root string) (goWorkPath string, cleanup func(), err error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", nil, fmt.Errorf("resolve %s: %w", root, err)
	}

	repoRoot, err := findRepoRoot(absRoot)
	if err != nil {
		return "", nil, err
	}

	roots, err := findGoModRoots(repoRoot)
	if err != nil {
		return "", nil, err
	}
	if len(roots) < 2 {
		return "", nil, fmt.Errorf("only %d go.mod under %s, a workspace wouldn't add coverage", len(roots), repoRoot)
	}

	version, err := goToolchainVersion(ctx, absRoot)
	if err != nil {
		return "", nil, err
	}

	dir, err := os.MkdirTemp("", "ragctl-goworkspace-")
	if err != nil {
		return "", nil, fmt.Errorf("create workspace scratch dir: %w", err)
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "go %s\n\nuse (\n", version)
	for _, r := range roots {
		fmt.Fprintf(&sb, "\t%q\n", r)
	}
	sb.WriteString(")\n")

	goWorkPath = filepath.Join(dir, "go.work")
	if err := os.WriteFile(goWorkPath, []byte(sb.String()), 0o644); err != nil {
		os.RemoveAll(dir)
		return "", nil, fmt.Errorf("write %s: %w", goWorkPath, err)
	}

	return goWorkPath, func() { os.RemoveAll(dir) }, nil
}

// findRepoRoot walks up from start looking for a .git entry (directory for
// a normal clone, file for a worktree), returning the first ancestor that
// has one.
func findRepoRoot(start string) (string, error) {
	dir := start
	for {
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no .git found above %s", start)
		}
		dir = parent
	}
}

// findGoModRoots walks repoRoot for every directory containing a go.mod,
// skipping skipWorkspaceScanDirs.
func findGoModRoots(repoRoot string) ([]string, error) {
	var roots []string
	err := filepath.WalkDir(repoRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipWorkspaceScanDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() == "go.mod" {
			roots = append(roots, filepath.Dir(path))
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk %s for go.mod roots: %w", repoRoot, err)
	}
	return roots, nil
}

// goToolchainVersion returns the running toolchain's version (e.g.
// "1.26.8"), for the go.work `go` directive — the workspace itself doesn't
// need to match any module's requirement exactly, only be a valid
// directive; the actual per-module version check still happens via
// GOTOOLCHAIN=local as normal.
func goToolchainVersion(ctx context.Context, dir string) (string, error) {
	result, err := executil.Run(ctx, executil.RunOptions{
		Dir:     dir,
		Args:    []string{"go", "env", "GOVERSION"},
		Timeout: defaultListTimeout,
	})
	if err != nil {
		return "", fmt.Errorf("go env GOVERSION: %w", err)
	}
	if result.ExitCode != 0 {
		return "", fmt.Errorf("go env GOVERSION exited %d: %s", result.ExitCode, result.Stderr)
	}
	return strings.TrimPrefix(strings.TrimSpace(string(result.Stdout)), "go"), nil
}
