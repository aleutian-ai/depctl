package registry

import (
	"context"
	"embed"
	"fmt"
	"os"
	"path/filepath"
)

//go:embed builtin/*.yaml
var builtinFS embed.FS

// Registry is the merged, matchable set of KnowledgePackage manifests
// loaded from every configured source.
type Registry struct {
	manifests map[string]Manifest // keyed by metadata.name
	index     map[string]Manifest // keyed by ecosystem+"|"+package, built by build()
	Warnings  []string
}

// Loader loads and merges manifests from three sources, in ascending
// priority (later sources win on a metadata.name conflict): the built-in
// registry, the user registry directory, and a project override directory.
type Loader struct {
	UserRegistryDir    string // "" to skip
	ProjectRegistryDir string // "" to skip
}

// NewLoader returns a Loader reading from the given user and project
// registry directories. Either may be "" to skip that source.
func NewLoader(userRegistryDir, projectRegistryDir string) *Loader {
	return &Loader{UserRegistryDir: userRegistryDir, ProjectRegistryDir: projectRegistryDir}
}

// Load merges manifests from all configured sources and builds the
// ecosystem+package match index. A malformed manifest in the user or
// project directory is skipped with a warning; a malformed built-in
// manifest is fatal, since it's compiled in and should never be invalid.
func (l *Loader) Load(ctx context.Context) (*Registry, error) {
	reg := &Registry{manifests: map[string]Manifest{}}

	builtinEntries, err := builtinFS.ReadDir("builtin")
	if err != nil {
		return nil, fmt.Errorf("registry: read embedded builtin dir: %w", err)
	}
	for _, entry := range builtinEntries {
		data, err := builtinFS.ReadFile(filepath.Join("builtin", entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("registry: read builtin manifest %s: %w", entry.Name(), err)
		}
		m, err := ParseManifest(data)
		if err != nil {
			return nil, fmt.Errorf("registry: builtin manifest %s is invalid (this should never happen): %w", entry.Name(), err)
		}
		reg.add(m, "builtin")
	}

	reg.loadDir(l.UserRegistryDir, "user")
	reg.loadDir(l.ProjectRegistryDir, "project")

	reg.build()
	return reg, nil
}

// loadDir walks dir for *.yaml/*.yml manifests, skipping (with a warning)
// any that fail to parse or validate. A missing dir is not an error.
func (r *Registry) loadDir(dir, sourceLabel string) {
	if dir == "" {
		return
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "*.yaml"))
	yml, _ := filepath.Glob(filepath.Join(dir, "*.yml"))
	matches = append(matches, yml...)

	for _, path := range matches {
		data, err := os.ReadFile(path)
		if err != nil {
			r.Warnings = append(r.Warnings, fmt.Sprintf("%s: read %s: %v", sourceLabel, path, err))
			continue
		}
		m, err := ParseManifest(data)
		if err != nil {
			r.Warnings = append(r.Warnings, fmt.Sprintf("%s: skipping invalid manifest %s: %v", sourceLabel, path, err))
			continue
		}
		r.add(m, sourceLabel)
	}
}

func (r *Registry) add(m Manifest, sourceLabel string) {
	if existing, ok := r.manifests[m.Metadata.Name]; ok {
		r.Warnings = append(r.Warnings, fmt.Sprintf(
			"%s manifest %q overrides an earlier one (was from a lower-priority source; had ecosystems %v)",
			sourceLabel, m.Metadata.Name, existing.Match.Ecosystems))
	}
	r.manifests[m.Metadata.Name] = m
}
