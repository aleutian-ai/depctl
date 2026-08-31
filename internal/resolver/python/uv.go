package python

import (
	"fmt"

	"github.com/BurntSushi/toml"

	"aleutian-ai/ragctl/internal/domain"
	"aleutian-ai/ragctl/internal/resolver"
)

// uvLockFile is the subset of uv.lock's schema ragctl needs.
type uvLockFile struct {
	Package []uvPackage `toml:"package"`
}

type uvPackage struct {
	Name         string      `toml:"name"`
	Version      string      `toml:"version"`
	Source       uvSource    `toml:"source"`
	Dependencies []uvPkgName `toml:"dependencies"`
}

// uvSource is a oneof: exactly one field is set per package, identifying
// where it came from.
type uvSource struct {
	Registry  string `toml:"registry"`  // normal PyPI (or other index) package
	Virtual   string `toml:"virtual"`   // the project itself (or a workspace member root) — not a dependency
	Editable  string `toml:"editable"`  // local, editable install
	Directory string `toml:"directory"` // local, non-editable install
	Path      string `toml:"path"`      // local path install (older uv.lock revisions)
	Git       string `toml:"git"`       // "<url>#<rev>"
}

type uvPkgName struct {
	Name string `toml:"name"`
}

// resolveUVLock parses uv.lock at root and normalizes it into a
// domain.Resolution.
func resolveUVLock(root string) (domain.Resolution, error) {
	path := lockPath(root, "uv.lock")

	var lock uvLockFile
	if _, err := toml.DecodeFile(path, &lock); err != nil {
		return domain.Resolution{}, resolutionErr(root, fmt.Errorf("parse uv.lock: %w", err))
	}

	deps, err := normalizeUV(lock)
	if err != nil {
		return domain.Resolution{}, resolutionErr(root, err)
	}

	return domain.Resolution{
		Ecosystem:    domain.EcosystemPython,
		LockPath:     path,
		Dependencies: deps,
		Fingerprint:  resolver.Fingerprint(deps),
	}, nil
}

// normalizeUV converts raw uv.lock packages into domain.DependencyVersion:
// the project's own virtual root package(s) are dropped, direct/indirect
// is derived from the virtual root's declared dependency names, and git or
// local (editable/directory/path) provenance is preserved in ResolvedBy.
func normalizeUV(lock uvLockFile) ([]domain.DependencyVersion, error) {
	direct := map[string]bool{}
	for _, p := range lock.Package {
		if p.Source.Virtual != "" {
			for _, d := range p.Dependencies {
				direct[d.Name] = true
			}
		}
	}

	var out []domain.DependencyVersion
	for _, p := range lock.Package {
		if p.Name == "" {
			return nil, fmt.Errorf("uv.lock: package with empty name")
		}
		if p.Source.Virtual != "" {
			continue // the project itself, not a dependency
		}

		resolvedBy := "uv.lock"
		switch {
		case p.Source.Git != "":
			resolvedBy = "uv.lock:git:" + p.Source.Git
		case p.Source.Editable != "":
			resolvedBy = "uv.lock:local:" + p.Source.Editable
		case p.Source.Directory != "":
			resolvedBy = "uv.lock:local:" + p.Source.Directory
		case p.Source.Path != "":
			resolvedBy = "uv.lock:local:" + p.Source.Path
		}

		out = append(out, domain.DependencyVersion{
			Dependency: domain.Dependency{
				Ecosystem: domain.EcosystemPython,
				Name:      p.Name,
				Direct:    direct[p.Name],
			},
			Version:    p.Version,
			ResolvedBy: resolvedBy,
		})
	}
	return out, nil
}
