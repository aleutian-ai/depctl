package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func requireGitForCorpus(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
}

func newCorpusFixtureRepo(t *testing.T) string {
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

func TestCorpusAddScansOneNewDependency(t *testing.T) {
	isolateEnv(t)
	requireGitForCorpus(t)
	requireGo(t)
	runInitForTest(t)
	useRealRagctlBinary(t)
	repo := newCorpusFixtureRepo(t)

	cmd := NewRootCmd()
	cmd.SetArgs([]string{"corpus", "add", repo, "--name", "widget"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("corpus add: %v", err)
	}
	if !strings.Contains(out.String(), "resolved") {
		t.Errorf("expected corpus add to trigger a scan, got:\n%s", out.String())
	}

	dir, err := corpusProjectDir()
	if err != nil {
		t.Fatalf("corpusProjectDir: %v", err)
	}
	pkg, _, err := loadCorpusFiles(dir)
	if err != nil {
		t.Fatalf("loadCorpusFiles: %v", err)
	}
	if len(pkg.Dependencies) != 1 || pkg.Dependencies["ragctl-corpus-widget"] != "1" {
		t.Errorf("Dependencies = %+v, want exactly ragctl-corpus-widget=1", pkg.Dependencies)
	}

	regDir, _ := userRegistryDirPath()
	if _, err := os.Stat(filepath.Join(regDir, "ragctl-corpus-widget.yaml")); err != nil {
		t.Errorf("registry manifest not written: %v", err)
	}
}

func TestCorpusAddWithoutResyncIsNoOp(t *testing.T) {
	isolateEnv(t)
	requireGitForCorpus(t)
	requireGo(t)
	runInitForTest(t)
	useRealRagctlBinary(t)
	repo := newCorpusFixtureRepo(t)

	add := func() string {
		cmd := NewRootCmd()
		cmd.SetArgs([]string{"corpus", "add", repo, "--name", "widget"})
		var out bytes.Buffer
		cmd.SetOut(&out)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("corpus add: %v", err)
		}
		return out.String()
	}
	add()
	second := add()
	if !strings.Contains(second, "already added") {
		t.Errorf("second add without --resync should be a no-op, got:\n%s", second)
	}

	dir, _ := corpusProjectDir()
	pkg, _, _ := loadCorpusFiles(dir)
	if pkg.Dependencies["ragctl-corpus-widget"] != "1" {
		t.Errorf("version changed on a no-op add: %+v", pkg.Dependencies)
	}
}

func TestCorpusAddResyncBumpsVersion(t *testing.T) {
	isolateEnv(t)
	requireGitForCorpus(t)
	requireGo(t)
	runInitForTest(t)
	useRealRagctlBinary(t)
	repo := newCorpusFixtureRepo(t)

	run := func(extraArgs ...string) {
		cmd := NewRootCmd()
		cmd.SetArgs(append([]string{"corpus", "add", repo, "--name", "widget"}, extraArgs...))
		cmd.SetOut(new(bytes.Buffer))
		if err := cmd.Execute(); err != nil {
			t.Fatalf("corpus add: %v", err)
		}
	}
	run()
	run("--resync")

	dir, _ := corpusProjectDir()
	pkg, _, _ := loadCorpusFiles(dir)
	if pkg.Dependencies["ragctl-corpus-widget"] == "1" {
		t.Error("--resync did not change the version")
	}
}

func TestCorpusRemoveDropsEntry(t *testing.T) {
	isolateEnv(t)
	requireGitForCorpus(t)
	requireGo(t)
	runInitForTest(t)
	useRealRagctlBinary(t)
	repo := newCorpusFixtureRepo(t)

	add := NewRootCmd()
	add.SetArgs([]string{"corpus", "add", repo, "--name", "widget"})
	add.SetOut(new(bytes.Buffer))
	if err := add.Execute(); err != nil {
		t.Fatalf("corpus add: %v", err)
	}

	remove := NewRootCmd()
	remove.SetArgs([]string{"corpus", "remove", "widget"})
	remove.SetOut(new(bytes.Buffer))
	if err := remove.Execute(); err != nil {
		t.Fatalf("corpus remove: %v", err)
	}

	dir, _ := corpusProjectDir()
	pkg, lock, _ := loadCorpusFiles(dir)
	if _, ok := pkg.Dependencies["ragctl-corpus-widget"]; ok {
		t.Error("dependency still present after remove")
	}
	if _, ok := lock.Packages["node_modules/ragctl-corpus-widget"]; ok {
		t.Error("lockfile entry still present after remove")
	}
	regDir, _ := userRegistryDirPath()
	if _, err := os.Stat(filepath.Join(regDir, "ragctl-corpus-widget.yaml")); !os.IsNotExist(err) {
		t.Errorf("registry manifest still present after remove: err=%v", err)
	}
}

func TestCorpusAddRejectsNonGitDirectory(t *testing.T) {
	isolateEnv(t)
	requireGitForCorpus(t)
	runInitForTest(t)

	cmd := NewRootCmd()
	cmd.SetArgs([]string{"corpus", "add", t.TempDir(), "--name", "widget"})
	cmd.SetOut(new(bytes.Buffer))
	if err := cmd.Execute(); err == nil {
		t.Fatal("corpus add on a non-git directory succeeded, want an error")
	}
}
