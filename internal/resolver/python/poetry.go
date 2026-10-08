package python

import (
	"fmt"

	"github.com/BurntSushi/toml"

	"github.com/aleutian-ai/depctl/internal/domain"
	"github.com/aleutian-ai/depctl/internal/resolver"
)

// poetryLockFile is the subset of poetry.lock's schema depctl needs.
type poetryLockFile struct {
	Package []poetryPackage `toml:"package"`
}

type poetryPackage struct {
	Name    string        `toml:"name"`
	Version string        `toml:"version"`
	Source  *poetrySource `toml:"source"` // absent for plain PyPI packages
}

type poetrySource struct {
	Type              string `toml:"type"` // "git", "directory", "file", "url", "legacy"
	URL               string `toml:"url"`
	Reference         string `toml:"reference"`
	ResolvedReference string `toml:"resolved_reference"`
}

// resolvePoetryLock parses poetry.lock at root and normalizes it into a
// domain.Resolution.
//
// Unlike uv.lock, poetry.lock doesn't expose which packages are direct
// project dependencies vs. transitive ones, so every entry is left
// Direct: false here.
func resolvePoetryLock(root string) (domain.Resolution, error) {
	path := lockPath(root, "poetry.lock")

	var lock poetryLockFile
	if _, err := toml.DecodeFile(path, &lock); err != nil {
		return domain.Resolution{}, resolutionErr(root, fmt.Errorf("parse poetry.lock: %w", err))
	}

	deps, err := normalizePoetry(lock)
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

func normalizePoetry(lock poetryLockFile) ([]domain.DependencyVersion, error) {
	var out []domain.DependencyVersion
	for _, p := range lock.Package {
		if p.Name == "" {
			return nil, fmt.Errorf("poetry.lock: package with empty name")
		}

		resolvedBy := "poetry.lock"
		if p.Source != nil {
			switch p.Source.Type {
			case "git":
				rev := p.Source.ResolvedReference
				if rev == "" {
					rev = p.Source.Reference
				}
				resolvedBy = "poetry.lock:git:" + p.Source.URL + "#" + rev
			case "directory":
				resolvedBy = "poetry.lock:local:" + p.Source.URL
			case "file", "url":
				resolvedBy = "poetry.lock:" + p.Source.Type + ":" + p.Source.URL
			}
		}

		out = append(out, domain.DependencyVersion{
			Dependency: domain.Dependency{
				Ecosystem: domain.EcosystemPython,
				Name:      p.Name,
			},
			Version:    p.Version,
			ResolvedBy: resolvedBy,
		})
	}
	return out, nil
}
