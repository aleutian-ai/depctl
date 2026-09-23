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

// TestSparsePatternsIncludesGoModForNestedModuleBoundaryDetection covers
// the STRESS-005 finding: generation.normalizeSources needs go.mod
// present in the checked-out worktree to detect a nested Go module
// boundary — without it here, a sparse checkout silently strips away the
// only signal that distinguishes a dependency's own content from a
// sibling module's.
func TestSparsePatternsIncludesGoModForNestedModuleBoundaryDetection(t *testing.T) {
	goPatterns := SparsePatterns(domain.EcosystemGo)
	if !contains(goPatterns, "go.mod") {
		t.Errorf("SparsePatterns(go) = %v, want it to include go.mod", goPatterns)
	}
}

// TestSparsePatternsIncludesPackageJSONForNodeBoundaryDetection covers
// the npm equivalent of the go.mod case above: generation.build's
// discoverNodeSubdir and its walk-time defense both need package.json
// present in the checked-out worktree to find/verify an npm monorepo
// package boundary.
func TestSparsePatternsIncludesPackageJSONForNodeBoundaryDetection(t *testing.T) {
	nodePatterns := SparsePatterns(domain.EcosystemNode)
	if !contains(nodePatterns, "package.json") {
		t.Errorf("SparsePatterns(node) = %v, want it to include package.json", nodePatterns)
	}
	goPatterns := SparsePatterns(domain.EcosystemGo)
	if contains(goPatterns, "package.json") {
		t.Errorf("SparsePatterns(go) = %v, want it to NOT include package.json", goPatterns)
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

func TestScopeToSubdir(t *testing.T) {
	got := ScopeToSubdir([]string{"*.go", "go.mod"}, "billing")
	want := []string{"/billing/**/*.go", "/billing/**/go.mod"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("pattern %d = %q, want %q", i, got[i], want[i])
		}
	}
	if same := ScopeToSubdir([]string{"*.go"}, ""); same[0] != "*.go" {
		t.Errorf("empty subdir changed patterns: %v", same)
	}
}
