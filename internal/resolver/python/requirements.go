package python

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/aleutian-ai/depctl/internal/domain"
	"github.com/aleutian-ai/depctl/internal/resolver"
)

var (
	pinnedPattern = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9._-]*)\s*==\s*([^\s;#]+)`)
	namePattern   = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9._-]*)`)
)

// resolveRequirementsTxt parses requirements.txt at root: exact `==` pins
// resolve, everything else (ranges, bare names, include directives) is
// surfaced as a warning rather than guessed.
func resolveRequirementsTxt(root string) (domain.Resolution, error) {
	path := lockPath(root, "requirements.txt")

	data, err := os.ReadFile(path)
	if err != nil {
		return domain.Resolution{}, resolutionErr(root, fmt.Errorf("read requirements.txt: %w", err))
	}

	deps, warnings := parseRequirements(data)
	return domain.Resolution{
		Ecosystem:    domain.EcosystemPython,
		LockPath:     path,
		Dependencies: deps,
		Warnings:     warnings,
		Fingerprint:  resolver.Fingerprint(deps),
	}, nil
}

func parseRequirements(data []byte) ([]domain.DependencyVersion, []string) {
	var deps []domain.DependencyVersion
	var warnings []string

	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "-r ") || strings.HasPrefix(line, "--requirement ") {
			warnings = append(warnings, "skipped include directive (not supported): "+line)
			continue
		}

		if m := pinnedPattern.FindStringSubmatch(line); m != nil {
			deps = append(deps, domain.DependencyVersion{
				Dependency: domain.Dependency{
					Ecosystem: domain.EcosystemPython,
					Name:      m[1],
					Direct:    true,
				},
				Version:    m[2],
				ResolvedBy: "requirements.txt",
			})
			continue
		}

		if m := namePattern.FindStringSubmatch(line); m != nil {
			warnings = append(warnings, fmt.Sprintf("unresolved (not an exact ==pin): %s", m[1]))
			continue
		}

		warnings = append(warnings, fmt.Sprintf("unrecognized requirements.txt line: %q", line))
	}

	return deps, warnings
}
