package normalize

import (
	"testing"

	"aleutian-ai/ragctl/internal/domain"
)

func TestSparsePatternsCoversMarkdownPlaintextLicense(t *testing.T) {
	patterns := SparsePatterns(domain.EcosystemNode)
	want := []string{"*.md", "*.mdx", "*.txt", "*.rst", "LICENSE*", "license*"}
	for _, w := range want {
		found := false
		for _, p := range patterns {
			if p == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("SparsePatterns(node) = %v, missing %q", patterns, w)
		}
	}
}

func TestSparsePatternsIncludesGoOnlyForGoEcosystem(t *testing.T) {
	goPatterns := SparsePatterns(domain.EcosystemGo)
	if !contains(goPatterns, "*.go") {
		t.Errorf("SparsePatterns(go) = %v, want it to include *.go", goPatterns)
	}
	nodePatterns := SparsePatterns(domain.EcosystemNode)
	if contains(nodePatterns, "*.go") {
		t.Errorf("SparsePatterns(node) = %v, want it to NOT include *.go", nodePatterns)
	}
}

func contains(patterns []string, p string) bool {
	for _, x := range patterns {
		if x == p {
			return true
		}
	}
	return false
}
