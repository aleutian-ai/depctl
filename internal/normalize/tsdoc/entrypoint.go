package tsdoc

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// packageJSONFields is the subset of package.json entryDeclarationFile
// needs to find a package's published declaration entry point.
type packageJSONFields struct {
	Types   string `json:"types"`
	Typings string `json:"typings"` // older alias for "types"
	Main    string `json:"main"`
}

// entryDeclarationFile resolves dir's published .d.ts entry point:
// package.json's own "types"/"typings" field when present (the
// authoritative signal — that field exists specifically for this),
// otherwise a same-basename .d.ts sitting next to "main", otherwise the
// first .d.ts file found directly in dir. Bounded and non-recursive by
// design — no bundler-level `exports`-map resolution, matching NORM-008's
// own Non-goals.
func entryDeclarationFile(dir string) (string, bool) {
	var pkg packageJSONFields
	if data, err := os.ReadFile(filepath.Join(dir, "package.json")); err == nil {
		_ = json.Unmarshal(data, &pkg)
	}

	types := pkg.Types
	if types == "" {
		types = pkg.Typings
	}
	if types != "" {
		if p := filepath.Join(dir, filepath.FromSlash(types)); fileExists(p) {
			return p, true
		}
	}
	if pkg.Main != "" {
		mainPath := filepath.FromSlash(pkg.Main)
		p := filepath.Join(dir, strings.TrimSuffix(mainPath, filepath.Ext(mainPath))+".d.ts")
		if fileExists(p) {
			return p, true
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".d.ts") {
			return filepath.Join(dir, e.Name()), true
		}
	}
	return "", false
}

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}
