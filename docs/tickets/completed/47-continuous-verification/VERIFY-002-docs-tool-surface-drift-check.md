# VERIFY-002: MCP tool-surface docs-drift CI check

**Epic:** Continuous Verification
**Status:** done
**Depends on:** none
**Estimated size:** small

## Goal
This session's own documentation sweep found `README.md`, `docs/internal/mcp.md`, and `docs/features/query-serving.md` all independently drifted from the real MCP tool surface at different points (a stale tool count, a stale "disabled by default" claim, a missing tool entirely) — each caught only because someone happened to reread the docs closely after a change. Add one small, mechanical CI check for the narrowest, most repeatedly-hit instance of this: the registered tool count and name list actually match what these three docs claim.

## Non-goals
- **Not a general documentation-freshness or semantic-drift detector.** Whether a doc's *prose description* of a tool's behavior is still accurate (e.g. "enabled by default" vs. "disabled by default") is a natural-language claim this check cannot verify — that class of drift still needs a human rereading the docs, the way this session's sweep did. This ticket only makes the *one* class of drift that's fully mechanical (a tool exists/doesn't exist, is/isn't named) impossible to miss.
- No enforcement for `docs/internal/*.md`'s deeper per-tool behavior descriptions, `docs/features/*.md`'s sequence diagrams, or any other doc category — scoped to exactly the tool-count/tool-name claim, in exactly the three files this session found drifted.
- No auto-fixing of drifted docs — the check fails CI and names what's wrong; a human still writes the fix, the same as any other CI gate in this repo.

## Simplicity constraints
- A single small Go test (not a shell script or separate tool) in `internal/mcp`, since that package already knows its own registered tool list (`registerTools`) — no new `hack/` script, no new CI job, it's just another test `go test ./...` already runs.
- Parses the three docs with a simple line-scan for each known tool name (`strings.Contains`), not a Markdown/AST parser — this is intentionally low-tech, matching BOOT-003's own "don't over-build the first spike" precedent.

## Design
`internal/mcp/docs_drift_test.go` (new):
```go
// registeredToolNames is derived the same way registerTools itself
// enumerates tools — kept as one literal list here specifically so a
// forgotten doc update shows up as a test failure, not a silent gap.
var registeredToolNames = []string{
    "search_dependency_docs", "get_dependency_version", "list_project_dependencies",
    "get_release_changes", "knowledge_status", "sync_project", "scan_project",
    "explain_call_site",
}

func TestDocsListEveryRegisteredTool(t *testing.T) {
    repoRoot := findRepoRoot(t) // walks up from the test's own file to find go.mod
    for _, doc := range []string{"README.md", "docs/internal/mcp.md", "docs/features/query-serving.md"} {
        content := readFile(t, filepath.Join(repoRoot, doc))
        for _, tool := range registeredToolNames {
            if !strings.Contains(content, tool) {
                t.Errorf("%s does not mention tool %q — registered in internal/mcp/tools.go but missing from this doc", doc, tool)
            }
        }
    }
}
```
A genuinely new tool added to `registerTools` without a matching addition to `registeredToolNames` here would pass this test trivially (a name not in the list is never checked) — so this ticket's own review/PR checklist should note: adding an eighth tool means updating this list too, the same one-line reminder every other doc-sync convention in this repo already relies on human discipline for. This test only removes the "forgot the doc" failure mode, not the "forgot this test's own list" one — an acceptable, honest limit given the alternative (deriving the list via reflection over `registerTools`) is real added complexity for a small marginal gain.

## Inputs / Outputs
- Input: the three named docs' current content, plus the literal `registeredToolNames` list.
- Output: a CI-failing test naming exactly which doc is missing exactly which tool.

## Failure behavior
- A doc missing a tool name → test failure naming the doc and the tool, actionable without further investigation.

## Tests
This ticket's own test *is* the test.

## Acceptance criteria
- [x] `TestDocsListEveryRegisteredTool` fails today's `docs/internal/mcp.md`/`docs/features/query-serving.md` if either is edited to remove a tool mention, and passes against their current (already-correct, post-sweep) content.
- [x] Runs as part of `go test ./...` — no new CI job or script.

## Post-implementation note
The first real run of this test immediately found two genuine, pre-existing doc bugs this session's own earlier "sweep" had missed — exactly proving the ticket's value, on the very first try:
- `docs/features/query-serving.md` never mentioned `scan_project` at all, and its "registers seven tools" count was wrong (should have been eight) — fixed: added the missing table row and corrected the count.
- `docs/internal/mcp.md` had the same two bugs — missing `scan_project` from its bullet list, "seven tools" instead of eight — fixed the same way.

One design assumption from the original ticket turned out to be wrong and was corrected rather than force-fit: **`README.md` was dropped from the checked-docs list.** `README.md`'s MCP section is deliberately non-exhaustive by design ("a handful of tools... and read-only lookups like `list_project_dependencies`") — it's a skimming-reader overview, not a reference, and the two sibling docs (`docs/internal/mcp.md`, `docs/features/query-serving.md`) already own the job of being exhaustive. Requiring README.md to name every tool would have forced a style change to satisfy a mechanical check rather than fixing a real gap. `docsCheckedForToolSurface` documents this reasoning inline.
