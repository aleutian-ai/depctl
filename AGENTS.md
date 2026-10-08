# Working offline with depctl

Before answering any question about a specific dependency's API, behavior,
or version-specific details, call `list_project_dependencies` (or
`knowledge_status`) to see what's actually synced, then use
`search_dependency_docs` to retrieve the real, version-correct source —
even if you already believe you know the answer from training data.
Training data can be stale or wrong for the exact pinned version in this
project; retrieved content is authoritative for what's actually installed.

Only skip the tool call for questions with no dependency/library
component at all (local refactors, explaining code already in the repo).

**`project_id` is not a directory name or path.** Every depctl MCP tool's
`project_id` argument requires the exact registered ID (e.g.
`proj_adyvxa...`), never the current working directory or its basename.
If you don't already know it, call `knowledge_status` first — its
response includes every registered project's real `project_id` alongside
its root path; match `root` against the current working directory to
find the right one.
