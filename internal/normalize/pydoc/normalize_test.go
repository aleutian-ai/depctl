package pydoc

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aleutian-ai/depctl/internal/domain"
)

func requirePython(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not on PATH")
	}
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

const goldenInit = `"""The widget package does something useful."""

__all__ = ["Do", "Widget", "Reexported"]


def Do(x: str) -> int:
    """Do performs the widget's core action.

    Args:
        x: the input
    """
    return len(x)


class Widget:
    """Widget is the package's main type."""

    def run(self, count: int) -> None:
        """Run executes the widget."""
        pass

    def _helper(self) -> None:
        """Must never appear — private by convention."""
        pass


def _internal() -> None:
    """Must never appear — not exported."""
    pass


from .core import Reexported
`

const goldenCore = `def Reexported() -> str:
    """Reexported is defined in a sibling module and re-exported here."""
    return "ok"
`

func TestExtractGoldenFixturePackage(t *testing.T) {
	requirePython(t)
	dir := t.TempDir()
	writeFile(t, dir, "__init__.py", goldenInit)
	writeFile(t, dir, "core.py", goldenCore)

	mod, err := runExtract(context.Background(), filepath.Join(dir, "__init__.py"), dir)
	if err != nil {
		t.Fatalf("runExtract: %v", err)
	}
	if !strings.Contains(mod.Doc, "widget package does something useful") {
		t.Errorf("module doc = %q", mod.Doc)
	}
	if mod.ExportsDynamic {
		t.Error("ExportsDynamic = true, want false (this fixture's __all__ is a static list)")
	}

	byName := map[string]extractedSymbol{}
	for _, s := range mod.Symbols {
		byName[s.Name] = s
	}

	if _, ok := byName["_internal"]; ok {
		t.Error("_internal (not exported) must never appear")
	}
	if _, ok := byName["_helper"]; ok {
		t.Error("Widget._helper (private convention) must never appear")
	}

	do, ok := byName["Do"]
	if !ok || do.Kind != "function" || !strings.Contains(do.Doc, "performs the widget's core action") || !strings.Contains(do.Signature, "def Do(x: str) -> int:") {
		t.Errorf("Do = %+v, ok=%v", do, ok)
	}
	widget, ok := byName["Widget"]
	if !ok || widget.Kind != "class" || !strings.Contains(widget.Doc, "package's main type") {
		t.Errorf("Widget = %+v, ok=%v", widget, ok)
	}
	run, ok := byName["run"]
	if !ok || run.Kind != "method" || run.Receiver != "Widget" || !strings.Contains(run.Doc, "executes the widget") {
		t.Errorf("run = %+v, ok=%v", run, ok)
	}
	reexp, ok := byName["Reexported"]
	if !ok || !strings.Contains(reexp.Doc, "defined in a sibling module") {
		t.Errorf("Reexported (single-hop re-export) = %+v, ok=%v", reexp, ok)
	}

	wantCount := 4 // Do, Widget, run, Reexported
	if len(mod.Symbols) != wantCount {
		t.Errorf("len(Symbols) = %d, want %d: %+v", len(mod.Symbols), wantCount, mod.Symbols)
	}
}

func TestExtractDynamicAllIsFlaggedNotFabricated(t *testing.T) {
	requirePython(t)
	dir := t.TempDir()
	writeFile(t, dir, "__init__.py", `"""dynamic exports."""

EXTRA = ["Other"]
__all__ = ["Do"] + list(EXTRA)


def Do(x: str) -> int:
    """Do does a thing."""
    return len(x)
`)
	mod, err := runExtract(context.Background(), filepath.Join(dir, "__init__.py"), dir)
	if err != nil {
		t.Fatalf("runExtract: %v", err)
	}
	if !mod.ExportsDynamic {
		t.Error("ExportsDynamic = false, want true — __all__ here is not a static list")
	}
	if len(mod.Symbols) != 1 || mod.Symbols[0].Name != "Do" {
		t.Errorf("Symbols = %+v, want just Do (convention-based fallback)", mod.Symbols)
	}
}

func TestExtractPyiSignatureOverridesButKeepsPyDocstring(t *testing.T) {
	requirePython(t)
	dir := t.TempDir()
	writeFile(t, dir, "__init__.py", `"""pyi override test."""


def Do(x: str) -> int:
    """Real docstring lives only in the .py source."""
    return len(x)
`)
	writeFile(t, dir, "__init__.pyi", "def Do(x: str, verbose: bool = False) -> int: ...\n")

	mod, err := runExtract(context.Background(), filepath.Join(dir, "__init__.py"), dir)
	if err != nil {
		t.Fatalf("runExtract: %v", err)
	}
	if len(mod.Symbols) != 1 {
		t.Fatalf("Symbols = %+v", mod.Symbols)
	}
	do := mod.Symbols[0]
	if !strings.Contains(do.Signature, "verbose") {
		t.Errorf("signature = %q, want the .pyi's signature (with verbose)", do.Signature)
	}
	if !strings.Contains(do.Doc, "Real docstring lives only in the .py source") {
		t.Errorf("doc = %q, want the .py source's docstring retained", do.Doc)
	}
}

func TestSupportsFalseForEmptyDirectory(t *testing.T) {
	dir := t.TempDir()
	n := New()
	if n.Supports(domain.SourceSnapshot{LocalPath: dir}) {
		t.Error("Supports = true for an empty directory")
	}
}

func TestEntryModuleFilePrefersInitPy(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "__init__.py", "")
	writeFile(t, dir, "other.py", "")
	got, ok := entryModuleFile(dir)
	want := filepath.Join(dir, "__init__.py")
	if !ok || got != want {
		t.Errorf("entryModuleFile = %q, %v, want %q, true", got, ok, want)
	}
}
