# GIT-003: Source delta calculation

**Epic:** Git Acquisition
**Status:** planned
**Depends on:** GIT-001
**Estimated size:** small

## Goal
Compute a file-level diff between two resolved commits (old and new dependency versions) as an optimization hint for the normalization pipeline, without treating it as the source of truth for content identity.

## Non-goals
- Does not replace content hashing as the authoritative change signal (HASH-001 remains authoritative — this is purely a hint to reduce work).
- Does not attempt rename-similarity tuning beyond Git's defaults.

## Simplicity constraints
- One `git diff --name-status` call, normalized into a small enum. Do not build a generic patch/diff engine.

## Design
- Package: `internal/source/git`
- ```go
  type FileStatus string
  const (
      FileAdded    FileStatus = "added"
      FileModified FileStatus = "modified"
      FileDeleted  FileStatus = "deleted"
      FileRenamed  FileStatus = "renamed"
  )
  type FileDelta struct {
      Path     string
      OldPath  string // set only for renamed
      Status   FileStatus
  }
  func (c *Cache) Delta(ctx context.Context, repoPath, oldCommit, newCommit string) ([]FileDelta, error)
  ```
- Implementation: run `git diff --name-status <old>..<new>` in the bare repo, parse `A`/`M`/`D`/`R###` prefixes into the enum above (`R###` → `FileRenamed` with old/new paths from the two tab-separated columns).
- The generation builder (GEN-002) may use this list to skip re-normalizing untouched paths, but must still verify via content hash (HASH-001) before trusting reuse — this ticket only produces the raw delta.

## Inputs / Outputs
- Input: repo path, old commit SHA, new commit SHA.
- Output: `[]FileDelta`.

## Failure behavior
- If `oldCommit` is empty/unknown (first-ever sync of a package), return all files as `added` by diffing against Git's empty tree (`git diff --name-status 4b825dc642cb6eb9a060e54bf8d69288fbee4904..<new>`), not an error.
- Malformed diff output (unexpected format) is a typed parse error.

## Tests
- Added/modified/deleted files each correctly classified.
- Renamed file correctly reports both old and new paths.
- Diff against the empty tree (first sync) returns everything as added.

## Acceptance criteria
- [x] All four status types normalized correctly against real fixture commits.
- [x] First-sync (no old commit) case handled without special-casing callers.
