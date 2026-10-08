package node

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/aleutian-ai/depctl/internal/domain"
	"github.com/aleutian-ai/depctl/internal/resolver"
)

// pnpmLockFile is the subset of pnpm-lock.yaml's schema (current,
// lockfileVersion 9.x) depctl needs.
type pnpmLockFile struct {
	Importers map[string]pnpmImporter `yaml:"importers"`
	Packages  map[string]any          `yaml:"packages"` // only key names matter
}

type pnpmImporter struct {
	Dependencies    map[string]pnpmDepRef `yaml:"dependencies"`
	DevDependencies map[string]pnpmDepRef `yaml:"devDependencies"`
}

type pnpmDepRef struct {
	Specifier string `yaml:"specifier"`
	Version   string `yaml:"version"`
}

// resolvePnpmLock parses pnpm-lock.yaml at root and normalizes it into a
// domain.Resolution.
//
// Workspace members that depend on each other never appear under
// `packages:` (only externally-resolved packages do), so cross-workspace
// dependencies are naturally excluded rather than needing an explicit
// local/link check.
func resolvePnpmLock(root string) (domain.Resolution, error) {
	path := lockPath(root, "pnpm-lock.yaml")

	data, err := os.ReadFile(path)
	if err != nil {
		return domain.Resolution{}, resolutionErr(root, fmt.Errorf("read pnpm-lock.yaml: %w", err))
	}

	var lock pnpmLockFile
	if err := yaml.Unmarshal(data, &lock); err != nil {
		return domain.Resolution{}, resolutionErr(root, fmt.Errorf("parse pnpm-lock.yaml: %w", err))
	}

	deps, err := normalizePnpm(lock)
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

func normalizePnpm(lock pnpmLockFile) ([]domain.DependencyVersion, error) {
	direct := map[string]bool{}
	for _, importer := range lock.Importers {
		for name := range importer.Dependencies {
			direct[name] = true
		}
		for name := range importer.DevDependencies {
			direct[name] = true
		}
	}

	var out []domain.DependencyVersion
	for key := range lock.Packages {
		name, version, err := splitPnpmKey(key)
		if err != nil {
			return nil, err
		}
		out = append(out, domain.DependencyVersion{
			Dependency: domain.Dependency{
				Ecosystem: domain.EcosystemNode,
				Name:      name,
				Direct:    direct[name],
			},
			Version:    version,
			ResolvedBy: "pnpm-lock.yaml",
		})
	}
	return out, nil
}

// splitPnpmKey splits a `packages:` key such as "left-pad@1.3.0" or
// "@types/node@20.11.0" into name and version. A peer-dependency suffix in
// parentheses, e.g. "is-odd@3.0.1(is-number@6.0.0)", is stripped from the
// version. Older lockfile revisions prefix keys with "/"; that's tolerated
// too.
func splitPnpmKey(key string) (name, version string, err error) {
	key = strings.TrimPrefix(key, "/")
	// Strip a peer-dependency suffix, e.g. "is-odd@3.0.1(is-number@6.0.0)",
	// before splitting on '@' — otherwise the suffix's own '@' would be
	// picked up as the split point instead of the real name/version one.
	if paren := strings.IndexByte(key, '('); paren != -1 {
		key = key[:paren]
	}
	at := strings.LastIndex(key, "@")
	if at <= 0 {
		return "", "", fmt.Errorf("pnpm-lock.yaml: could not split package key %q into name@version", key)
	}
	return key[:at], key[at+1:], nil
}
