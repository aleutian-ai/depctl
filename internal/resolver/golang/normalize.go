package golang

import (
	"fmt"

	"github.com/aleutian-ai/depctl/internal/domain"
)

// normalize converts raw go list modules into the domain's canonical
// DependencyVersion list: the main module is dropped, replace directives
// are resolved to their target version (or flagged local when the replace
// target has no version, i.e. a filesystem path), and provenance is kept in
// ResolvedBy.
func normalize(modules []goModule) ([]domain.DependencyVersion, error) {
	var out []domain.DependencyVersion
	for _, m := range modules {
		if m.Main {
			continue
		}
		if m.Path == "" {
			return nil, fmt.Errorf("golang: module with empty Path in go list output")
		}

		version := m.Version
		resolvedBy := "go-list"
		if m.Replace != nil {
			if m.Replace.Version == "" {
				resolvedBy = "local:" + m.Replace.Path
			} else {
				version = m.Replace.Version
				resolvedBy = "replace:" + m.Replace.Path + "@" + m.Replace.Version
			}
		}

		out = append(out, domain.DependencyVersion{
			Dependency: domain.Dependency{
				Ecosystem: domain.EcosystemGo,
				Name:      m.Path,
				Direct:    !m.Indirect,
			},
			Version:    version,
			ResolvedBy: resolvedBy,
		})
	}
	return out, nil
}
