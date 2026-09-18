package mcp

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// VERIFY-002: a mechanical check that every tool registerTools actually
// registers is mentioned in the docs that describe ragctl's MCP surface
// — this session's own documentation sweep found README.md,
// docs/internal/mcp.md, and docs/features/query-serving.md each
// independently drift from the real tool list at different points. Not
// a general freshness detector (see the ticket's own non-goals) — just
// the one class of drift ("this tool isn't mentioned at all") that's
// fully mechanical to catch.

// registeredToolNames mirrors registerTools' own tool list
// (internal/mcp/tools.go) — kept as a literal here so a forgotten doc
// update shows up as a test failure. Adding a ninth tool means adding
// its name here too; this test can't derive the list itself without
// real added complexity (reflecting over the SDK's tool registry) for a
// small marginal gain over one more literal string.
var registeredToolNames = []string{
	"search_dependency_docs", "get_dependency_version", "list_project_dependencies",
	"get_release_changes", "knowledge_status", "sync_project", "scan_project",
	"explain_call_site",
}

// docsCheckedForToolSurface are the docs whose actual job is to be an
// exhaustive tool reference. README.md is deliberately excluded: its
// "Using ragctl through an MCP agent" section intentionally names only
// a representative subset ("a handful of tools... and read-only lookups
// like list_project_dependencies") for a skimming reader, not an
// exhaustive list — an earlier draft of this ticket assumed README.md
// belonged in this check too, but running the check for real showed
// that assumption conflicts with README.md's actual intended style
// (forcing it to name every tool would turn a deliberately short
// section into a table README.md's other two sibling docs already own).
var docsCheckedForToolSurface = []string{
	"docs/internal/mcp.md",
	"docs/features/query-serving.md",
}

func findRepoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller: could not determine this test file's own path")
	}
	dir := filepath.Dir(thisFile)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("findRepoRoot: walked up to filesystem root without finding go.mod")
		}
		dir = parent
	}
}

func TestDocsListEveryRegisteredTool(t *testing.T) {
	repoRoot := findRepoRoot(t)
	for _, doc := range docsCheckedForToolSurface {
		data, err := os.ReadFile(filepath.Join(repoRoot, doc))
		if err != nil {
			t.Fatalf("read %s: %v", doc, err)
		}
		content := string(data)
		for _, tool := range registeredToolNames {
			if !strings.Contains(content, tool) {
				t.Errorf("%s does not mention tool %q — registered in internal/mcp/tools.go but missing from this doc", doc, tool)
			}
		}
	}
}
