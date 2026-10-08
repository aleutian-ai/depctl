package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// requireGit skips the test if the git binary isn't on PATH.
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
}

// runGit runs git in dir and fails the test on error, for building
// fixtures — not the code under test, which always goes through
// executil.
func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=depctl-test",
		"GIT_AUTHOR_EMAIL=depctl-test@example.com",
		"GIT_COMMITTER_NAME=depctl-test",
		"GIT_COMMITTER_EMAIL=depctl-test@example.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// newRemoteFixture creates a plain (non-bare) working repo under a fresh
// temp dir with an initial commit, suitable for use as the "remote" side
// of EnsureMirror/FetchTags in tests — a local path stands in for a
// network Git remote.
func newRemoteFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main")
	writeFileT(t, dir, "README.md", "hello\n")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-q", "-m", "initial")
	return dir
}

func writeFileT(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
