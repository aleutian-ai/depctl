package registry

import (
	"aleutian-ai/ragctl/internal/domain"
)

// build indexes every loaded manifest by ecosystem+package for O(1)
// lookup. Called once after all sources are merged.
func (r *Registry) build() {
	r.index = map[string]Manifest{}
	for _, m := range r.manifests {
		for _, eco := range m.Match.Ecosystems {
			for _, pkg := range m.Match.Packages {
				r.index[matchKey(eco, pkg)] = m
			}
		}
	}
}

// Match returns the manifest whose match block claims (eco, pkg), if any.
// No match is not an error — it means the package has no known knowledge
// source, which the planner reports rather than treating as a failure.
func (r *Registry) Match(eco domain.Ecosystem, pkg string) (Manifest, bool) {
	m, ok := r.index[matchKey(eco, pkg)]
	return m, ok
}

func matchKey(eco domain.Ecosystem, pkg string) string {
	return string(eco) + "|" + pkg
}

// ManifestNames returns the metadata.name of every loaded manifest.
func (r *Registry) ManifestNames() []string {
	names := make([]string, 0, len(r.manifests))
	for name := range r.manifests {
		names = append(names, name)
	}
	return names
}

// Manifest returns the loaded manifest with the given metadata.name.
func (r *Registry) Manifest(name string) (Manifest, bool) {
	m, ok := r.manifests[name]
	return m, ok
}
