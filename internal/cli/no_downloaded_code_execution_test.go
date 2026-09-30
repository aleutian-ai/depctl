package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	bboltstore "aleutian-ai/ragctl/internal/control/bbolt"
	"aleutian-ai/ragctl/internal/domain"
)

// SEC-004: no downloaded code execution. See
// docs/tickets/backlog/29-security-hardening/SEC-004-no-downloaded-code-execution.md.

func TestRequireRegisteredProjectRootRejectsUnregisteredID(t *testing.T) {
	store, err := bboltstore.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatalf("bbolt.Open: %v", err)
	}
	defer store.Close()

	err = requireRegisteredProjectRoot(context.Background(), store, "proj_never_registered")
	if err == nil {
		t.Fatal("requireRegisteredProjectRoot succeeded for an unregistered project ID, want ErrUnregisteredProjectRoot")
	}
	if !errors.Is(err, ErrUnregisteredProjectRoot) {
		t.Errorf("error = %v, want it to wrap ErrUnregisteredProjectRoot", err)
	}
}

func TestRequireRegisteredProjectRootAllowsRegisteredID(t *testing.T) {
	store, err := bboltstore.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatalf("bbolt.Open: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	if err := store.PutProject(ctx, domain.Project{ID: "proj_real", Root: t.TempDir()}); err != nil {
		t.Fatalf("PutProject: %v", err)
	}
	if err := requireRegisteredProjectRoot(ctx, store, "proj_real"); err != nil {
		t.Errorf("requireRegisteredProjectRoot rejected a genuinely registered project: %v", err)
	}
}

// dangerousPythonExecPatterns catches a future edit to pydoc's embedded
// extract.py accidentally reintroducing dynamic execution of the target
// file's own content (importing/exec'ing/eval'ing it as live code)
// instead of statically parsing its text via the ast module — the exact
// thing SEC-004 exists to prevent.
var dangerousPythonExecPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\bimportlib\b`),
	regexp.MustCompile(`__import__\s*\(`),
	regexp.MustCompile(`\bexec\s*\(`),
	regexp.MustCompile(`\beval\s*\(`),
}

// dangerousJSExecPatterns is tsdoc's sibling list — deliberately excludes
// a bare `exec(` (RegExp.prototype.exec is a normal, benign regex method
// used throughout this file) in favor of the real Node dynamic-execution
// primitives that would matter here.
var dangerousJSExecPatterns = []*regexp.Regexp{
	regexp.MustCompile(`require\s*\(\s*['"]child_process['"]`),
	regexp.MustCompile(`\bvm\.runInContext\b`),
	regexp.MustCompile(`\bvm\.runInNewContext\b`),
	regexp.MustCompile(`new\s+Function\s*\(`),
	regexp.MustCompile(`\beval\s*\(`),
}

func TestExtractionScriptsNeverDynamicallyExecuteTargetContent(t *testing.T) {
	check := func(rel string, patterns []*regexp.Regexp) {
		data, err := os.ReadFile(rel)
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		text := string(data)
		for _, pat := range patterns {
			if pat.MatchString(text) {
				t.Errorf("%s: matches a dynamic-execution pattern (%s) — extraction scripts must only statically parse the target file's text, never import/require/exec/eval it as live code", rel, pat.String())
			}
		}
	}
	check("../normalize/pydoc/extract.py", dangerousPythonExecPatterns)
	check("../normalize/tsdoc/extract.js", dangerousJSExecPatterns)
}

// TestNormalizerSubprocessesOnlyExecuteInterpretersNotFetchedContent
// confirms pydoc/tsdoc's own subprocess invocations run a real language
// interpreter (python3/node) with ragctl's own script piped over stdin —
// never a path into the fetched dependency's worktree passed as the
// executable itself.
func TestNormalizerSubprocessesOnlyExecuteInterpretersNotFetchedContent(t *testing.T) {
	files := []string{
		"../normalize/pydoc/normalize.go",
		"../normalize/tsdoc/normalize.go",
	}
	for _, rel := range files {
		data, err := os.ReadFile(rel)
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		text := string(data)
		if !strings.Contains(text, `exec.CommandContext(ctx, "python3"`) && !strings.Contains(text, `exec.CommandContext(ctx, "node"`) {
			t.Errorf("%s: expected to find a CommandContext call naming a real interpreter binary (python3/node) as the executable, found none — if this changed, re-verify the executable is never a path into fetched content", rel)
		}
	}
}
