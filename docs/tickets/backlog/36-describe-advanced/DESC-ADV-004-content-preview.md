# DESC-ADV-004: Content preview

**Epic:** Describe — Advanced
**Status:** planned
**Depends on:** DESC-001
**Estimated size:** small

## Goal
`ragctl describe <ecosystem>/<package> --preview` shows 1-2 sample chunks per source, so a human can sanity-check what's actually in the corpus without a separate MCP `search_dependency_docs` call.

## Non-goals
No relevance ranking/query — this is "show me any representative sample," not search. A real query still goes through MCP.

## Simplicity constraints
Reads directly from Badger (`ListGenerationChunks`, first N per source) — no vector query, no embedding call, so `--preview` stays fully offline even without Qdrant reachable.

## Acceptance criteria
- [ ] `--preview` shows N sample chunks (content + trust class + source) per declared source with an active generation.
- [ ] Works with the vector backend down (reads Badger only).
