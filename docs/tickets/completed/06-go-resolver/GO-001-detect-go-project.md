# GO-001: Detect Go project

**Epic:** Go Resolver
**Status:** planned
**Depends on:** RES-001
**Estimated size:** small

## Goal
Implement the `Resolver.Detect` method for the Go ecosystem: given a directory root, report whether it is a Go module root.

## Non-goals
- Does not resolve dependency versions (GO-002).
- Does not handle Go workspaces (`go.work`) specially — a workspace root without its own `go.mod` is not treated as a project root in v0.1.

## Simplicity constraints
- Detection is a single `os.Stat` check for `go.mod` in the given directory — no upward directory search, no heuristics on file content.
- Do not attempt to parse `go.mod` in this ticket; that begins in GO-002.

## Design
- Package: `internal/resolver/golang`
- Type `GoResolver struct{}` implementing the `Resolver` interface from RES-001:
  ```go
  func (r *GoResolver) Name() string { return "go" }
  func (r *GoResolver) Detect(ctx context.Context, root string) (bool, error)
  ```
- `Detect` returns `true` iff `filepath.Join(root, "go.mod")` exists and is a regular file. Any `os.Stat` error other than "not exist" is returned as an error.

## Inputs / Outputs
- Input: absolute project root path.
- Output: boolean detected + error.

## Failure behavior
- Permission errors or other I/O errors on stat are returned to the caller, not swallowed.
- Missing file is not an error — it's `(false, nil)`.

## Tests
- Fixture with `go.mod` at root → detected.
- Fixture with nested module under a subdirectory (e.g. `testdata/projects/go-simple/`) → root itself not detected unless `go.mod` is directly present.
- Empty directory → not detected, no error.
- Directory without read permission → error returned.

## Acceptance criteria
- [ ] `Detect` correctly identifies Go module roots per nested-module fixtures in `testdata/projects/`.
- [ ] No network or subprocess calls made during detection.
