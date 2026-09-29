package pydoc

import (
	"os"
	"path/filepath"
	"strings"
)

// entryModuleFile resolves dir's Python entry module: __init__.py (a
// real package) when present, otherwise the single .py file directly in
// dir, otherwise the first .py file found. Bounded and non-recursive by
// design — matching tsdoc's own entry-point-resolution precedent; deeper
// submodules are only ever reached through a single-hop relative-import
// re-export (extract.py's own scan/resolve_reexport), never a blind walk.
func entryModuleFile(dir string) (string, bool) {
	initPath := filepath.Join(dir, "__init__.py")
	if fileExists(initPath) {
		return initPath, true
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}
	var pyFiles []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".py") {
			pyFiles = append(pyFiles, e.Name())
		}
	}
	if len(pyFiles) == 0 {
		return "", false
	}
	if len(pyFiles) == 1 {
		return filepath.Join(dir, pyFiles[0]), true
	}
	// Several top-level .py files with no __init__.py: not a real
	// package layout (could be a flat script collection) — pick the
	// first, alphabetically, for determinism rather than guessing intent.
	first := pyFiles[0]
	for _, f := range pyFiles[1:] {
		if f < first {
			first = f
		}
	}
	return filepath.Join(dir, first), true
}

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}
