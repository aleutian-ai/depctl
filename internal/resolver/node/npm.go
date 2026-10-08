package node

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/aleutian-ai/depctl/internal/domain"
	"github.com/aleutian-ai/depctl/internal/resolver"
)

// npmLockFile is the subset of package-lock.json's schema (lockfileVersion
// 3, npm 7+) depctl needs.
type npmLockFile struct {
	LockfileVersion int                   `json:"lockfileVersion"`
	Packages        map[string]npmLockPkg `json:"packages"`
}

type npmLockPkg struct {
	Version         string            `json:"version"`
	Resolved        string            `json:"resolved"`
	Link            bool              `json:"link"` // workspace/local symlink
	Dependencies    map[string]string `json:"dependencies"`
	DevDependencies map[string]string `json:"devDependencies"`
}

// resolveNpmLock parses package-lock.json at root and normalizes it into a
// domain.Resolution.
func resolveNpmLock(root string) (domain.Resolution, error) {
	path := lockPath(root, "package-lock.json")

	data, err := os.ReadFile(path)
	if err != nil {
		return domain.Resolution{}, resolutionErr(root, fmt.Errorf("read package-lock.json: %w", err))
	}

	var lock npmLockFile
	if err := json.Unmarshal(data, &lock); err != nil {
		return domain.Resolution{}, resolutionErr(root, fmt.Errorf("parse package-lock.json: %w", err))
	}
	if lock.LockfileVersion != 0 && lock.LockfileVersion < 3 {
		return domain.Resolution{}, resolutionErr(root, fmt.Errorf(
			"package-lock.json lockfileVersion %d is unsupported (only lockfileVersion 3, npm 7+, is supported)", lock.LockfileVersion))
	}

	deps, err := normalizeNpm(lock)
	if err != nil {
		return domain.Resolution{}, resolutionErr(root, err)
	}

	return domain.Resolution{
		Ecosystem:    domain.EcosystemNode,
		LockPath:     path,
		Dependencies: deps,
		Fingerprint:  resolver.Fingerprint(deps),
	}, nil
}

// normalizeNpm converts package-lock.json's packages map into
// domain.DependencyVersion: the root project entry (key "") is dropped,
// direct-ness comes from the root's own dependencies/devDependencies,
// workspace/local symlinks (Link: true) are tagged local, and nested
// "node_modules/a/node_modules/b" keys collapse to the final package name.
func normalizeNpm(lock npmLockFile) ([]domain.DependencyVersion, error) {
	root, ok := lock.Packages[""]
	if !ok {
		return nil, fmt.Errorf("package-lock.json: missing root package entry (key \"\")")
	}
	direct := map[string]bool{}
	for name := range root.Dependencies {
		direct[name] = true
	}
	for name := range root.DevDependencies {
		direct[name] = true
	}

	var out []domain.DependencyVersion
	for key, pkg := range lock.Packages {
		if key == "" {
			continue
		}
		name := npmPackageName(key)
		if name == "" {
			// Not a node_modules/ entry — a workspace member's own key
			// (e.g. "packages/foo"), which is the project itself, not an
			// external dependency.
			continue
		}

		resolvedBy := "package-lock.json"
		version := pkg.Version
		if pkg.Link {
			resolvedBy = "package-lock.json:local:" + pkg.Resolved
		}

		out = append(out, domain.DependencyVersion{
			Dependency: domain.Dependency{
				Ecosystem: domain.EcosystemNode,
				Name:      name,
				Direct:    direct[name],
			},
			Version:    version,
			ResolvedBy: resolvedBy,
		})
	}
	return out, nil
}

// npmPackageName extracts a registry package name from a package-lock.json
// "packages" key, e.g. "node_modules/@types/node" -> "@types/node",
// "node_modules/foo/node_modules/bar" -> "bar". Keys with no node_modules/
// segment (a workspace member's own entry, e.g. "packages/foo") return "".
func npmPackageName(key string) string {
	const marker = "node_modules/"
	idx := strings.LastIndex(key, marker)
	if idx == -1 {
		return ""
	}
	return key[idx+len(marker):]
}
