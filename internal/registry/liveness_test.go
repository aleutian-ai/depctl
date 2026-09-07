package registry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
}

func newLivenessFixtureRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	run("add", ".")
	run("commit", "-q", "-m", "initial")
	return dir
}

func TestCheckLivenessGitSourceReachable(t *testing.T) {
	requireGit(t)
	repo := newLivenessFixtureRepo(t)

	result := CheckLiveness(context.Background(), Source{ID: "repository", Type: "git", URL: repo})
	if !result.Reachable {
		t.Errorf("Reachable = false, want true; Error = %s", result.Error)
	}
}

func TestCheckLivenessGitSourceUnreachable(t *testing.T) {
	requireGit(t)

	result := CheckLiveness(context.Background(), Source{ID: "repository", Type: "git", URL: filepath.Join(t.TempDir(), "does-not-exist")})
	if result.Reachable {
		t.Error("Reachable = true, want false for a nonexistent path")
	}
	if result.Error == "" {
		t.Error("Error is empty, want the underlying git failure preserved")
	}
}

func TestCheckLivenessWebsiteSourceReachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	result := CheckLiveness(context.Background(), Source{ID: "docs", Type: "website", URL: srv.URL})
	if !result.Reachable {
		t.Errorf("Reachable = false, want true; Error = %s", result.Error)
	}
}

func TestCheckLivenessWebsiteSourceUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	result := CheckLiveness(context.Background(), Source{ID: "docs", Type: "website", URL: srv.URL})
	if result.Reachable {
		t.Error("Reachable = true, want false for a 404 response")
	}
}

func TestCheckLivenessUnknownSourceTypeReportsError(t *testing.T) {
	result := CheckLiveness(context.Background(), Source{ID: "x", Type: "godoc"})
	if result.Reachable {
		t.Error("Reachable = true, want false for a type with no liveness check implemented")
	}
}
