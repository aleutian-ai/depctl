# Working offline with ragctl

Before answering any question about a specific dependency's API, behavior,
or version-specific details, call `list_project_dependencies` (or
`knowledge_status`) to see what's actually synced, then use
`search_dependency_docs` to retrieve the real, version-correct source —
even if you already believe you know the answer from training data.
Training data can be stale or wrong for the exact pinned version in this
project; retrieved content is authoritative for what's actually installed.

Only skip the tool call for questions with no dependency/library
component at all (local refactors, explaining code already in the repo).
