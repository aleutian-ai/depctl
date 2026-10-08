package node

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/aleutian-ai/depctl/internal/domain"
	"github.com/aleutian-ai/depctl/internal/resolver"
)

// yarnEntry is one resolved package parsed out of yarn.lock, either
// format.
type yarnEntry struct {
	name    string
	version string
}

// resolveYarnLock parses yarn.lock (Classic v1 or Berry) at root and
// normalizes it into a domain.Resolution.
//
// Unlike npm/pnpm, yarn.lock (both formats) carries no reliable,
// uniformly-present "these are the project's own direct dependencies"
// section to read the way package-lock.json's root entry or
// pnpm-lock.yaml's importers do (Berry's own workspace self-entries
// come closest, but Classic has nothing analogous at all) — so
// direct-ness is read from package.json's own dependencies/
// devDependencies instead, uniformly across both formats. Detect
// already confirmed package.json exists before Resolve was ever called.
func resolveYarnLock(root string) (domain.Resolution, error) {
	path := lockPath(root, "yarn.lock")

	data, err := os.ReadFile(path)
	if err != nil {
		return domain.Resolution{}, resolutionErr(root, fmt.Errorf("read yarn.lock: %w", err))
	}

	entries, err := parseYarnLock(data)
	if err != nil {
		return domain.Resolution{}, resolutionErr(root, err)
	}

	direct, err := readPackageJSONDirectNames(root)
	if err != nil {
		return domain.Resolution{}, resolutionErr(root, err)
	}

	var out []domain.DependencyVersion
	for _, e := range entries {
		out = append(out, domain.DependencyVersion{
			Dependency: domain.Dependency{
				Ecosystem: domain.EcosystemNode,
				Name:      e.name,
				Direct:    direct[e.name],
			},
			Version:    e.version,
			ResolvedBy: "yarn.lock",
		})
	}

	return domain.Resolution{
		Ecosystem:    domain.EcosystemNode,
		LockPath:     path,
		Dependencies: out,
		Fingerprint:  resolver.Fingerprint(out),
	}, nil
}

// parseYarnLock picks Classic vs Berry by the presence of Berry's own
// "__metadata:" key — present in every Berry lockfile (it records the
// lockfile schema version), absent from every Classic one.
func parseYarnLock(data []byte) ([]yarnEntry, error) {
	if bytes.Contains(data, []byte("__metadata:")) {
		return parseYarnBerry(data)
	}
	return parseYarnClassic(data), nil
}

// parseYarnClassic hand-parses Yarn Classic's lockfile format: a custom
// (non-YAML, non-JSON) text format, not worth a third-party dependency
// for — one unindented "descriptor[, descriptor...]:" header line per
// block, followed by indented fields, the only one of which depctl
// needs being `version "x.y.z"`. A block's first descriptor's package
// name is what's recorded; every descriptor in one block resolves to
// the same package by construction, so the rest are redundant for
// depctl's purposes.
func parseYarnClassic(data []byte) []yarnEntry {
	var entries []yarnEntry
	lines := strings.Split(string(data), "\n")
	for i := 0; i < len(lines); i++ {
		line := strings.TrimRight(lines[i], "\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, " ") || !strings.HasSuffix(line, ":") {
			continue // not a block header
		}
		header := strings.TrimSuffix(line, ":")
		first := strings.TrimSpace(strings.SplitN(header, ",", 2)[0])
		first = strings.Trim(first, `"`)
		name := yarnDescriptorName(first)
		if name == "" {
			continue
		}
		var version string
		for i+1 < len(lines) {
			next := lines[i+1]
			if next == "" || !strings.HasPrefix(next, " ") {
				break
			}
			i++
			field := strings.TrimSpace(next)
			if v, ok := strings.CutPrefix(field, "version "); ok {
				version = strings.Trim(v, `"`)
			}
		}
		if version != "" {
			entries = append(entries, yarnEntry{name: name, version: version})
		}
	}
	return entries
}

// yarnDescriptorName splits a Classic descriptor like "is-odd@^3.0.0" or
// "@types/node@^20.0.0" into just the package name — the last '@'
// always separates name from range, including for scoped names, since a
// scoped name's own leading '@' is never the last one.
func yarnDescriptorName(descriptor string) string {
	at := strings.LastIndex(descriptor, "@")
	if at <= 0 {
		return ""
	}
	return descriptor[:at]
}

// yarnBerryEntry is the subset of one Berry lockfile package block
// depctl needs. linkType "soft" marks a workspace-local package (the
// project's own self-entry, or a sibling workspace reached via the
// `workspace:` protocol) — never a real published version (its own
// "version" is a placeholder like "0.0.0-use.local"), so these are
// skipped entirely rather than kept with a local marker the way npm.go
// keeps symlinked packages; unlike npm's Link case, there is no real
// resolvable version to report here at all.
type yarnBerryEntry struct {
	Version  string `yaml:"version"`
	LinkType string `yaml:"linkType"`
}

func parseYarnBerry(data []byte) ([]yarnEntry, error) {
	var raw map[string]yarnBerryEntry
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse yarn.lock (Berry): %w", err)
	}

	var entries []yarnEntry
	for key, pkg := range raw {
		if key == "__metadata" || pkg.LinkType == "soft" {
			continue
		}
		// A Berry key groups one or more comma-separated descriptors,
		// e.g. `"is-odd@npm:^3.0.0, is-odd@npm:^3.0.1":` — every
		// descriptor in one block resolves to the same package, so only
		// the first is needed, mirroring parseYarnClassic's own
		// reasoning.
		first := strings.TrimSpace(strings.SplitN(key, ",", 2)[0])
		first = strings.Trim(first, `"`)
		name := yarnBerryDescriptorName(first)
		if name == "" || pkg.Version == "" {
			continue
		}
		entries = append(entries, yarnEntry{name: name, version: pkg.Version})
	}
	return entries, nil
}

// yarnBerryDescriptorName splits a Berry descriptor like
// "is-odd@npm:^3.0.0" or "@types/node@npm:^20.0.0" into just the
// package name — same last-'@'-splits-correctly reasoning as Classic's
// yarnDescriptorName, since Berry descriptors add a protocol prefix
// (npm:/workspace:/patch:/etc.) after the '@', not another '@' before it.
func yarnBerryDescriptorName(descriptor string) string {
	at := strings.LastIndex(descriptor, "@")
	if at <= 0 {
		return ""
	}
	return descriptor[:at]
}

// packageJSONDeps is the subset of package.json readPackageJSONDirectNames
// needs.
type packageJSONDeps struct {
	Dependencies    map[string]string `json:"dependencies"`
	DevDependencies map[string]string `json:"devDependencies"`
}

// readPackageJSONDirectNames reads root's package.json and returns the
// set of names declared under "dependencies"/"devDependencies" — see
// resolveYarnLock's own doc comment for why yarn.lock alone can't
// answer this the way npm/pnpm's lockfiles can.
func readPackageJSONDirectNames(root string) (map[string]bool, error) {
	data, err := os.ReadFile(filepath.Join(root, "package.json"))
	if err != nil {
		return nil, fmt.Errorf("read package.json: %w", err)
	}
	var pkg packageJSONDeps
	if err := json.Unmarshal(data, &pkg); err != nil {
		return nil, fmt.Errorf("parse package.json: %w", err)
	}
	direct := make(map[string]bool, len(pkg.Dependencies)+len(pkg.DevDependencies))
	for name := range pkg.Dependencies {
		direct[name] = true
	}
	for name := range pkg.DevDependencies {
		direct[name] = true
	}
	return direct, nil
}
