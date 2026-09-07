# GIT-005: Local search-path discovery

**Epic:** Local-First Acquisition
**Status:** planned
**Depends on:** GIT-001 (git cache manager), GIT-004 (fast offline failure)
**Estimated size:** medium

## Goal
Before `EnsureMirror` falls back to a network clone for a URL it hasn't mirrored before, check a configured list of local directories for an already-cloned copy of the same repo, and mirror from that instead — zero network, even on the very first sync of a package.

## Non-goals
- No automatic rewriting of registry manifests to point at a discovered local copy — the manifest still declares the real URL; this only changes what `EnsureMirror` mirrors *from* when resolving it. See the epic INDEX's own non-goal on this.
- No fuzzy/partial matching — a candidate directory either is a git repo whose remote resolves to the same host+org+repo as the target URL, or it isn't. No "looks similar" heuristics.

## Simplicity constraints
- Search paths are explicit config (`LocalSearchPaths []string`, e.g. `["~/offline-knowledge"]`), never an implicit scan of the whole filesystem.
- Matching is by asking git itself, not parsing `.git` internals by hand — a worktree-linked directory (e.g. `hack/fetch-corpus`'s own checkouts, which point `.git` at a *file*, not a directory, into ragctl's own mirror cache) still resolves correctly through `git -C <dir> remote get-url origin`, which hand-rolled `.git/config` parsing would get wrong.

## Design options

**Option A — directory-walk auto-discovery (what this ticket specifies).** Walk one level under each search path, run `git -C <candidate> remote get-url origin` per subdirectory (bounded per-call timeout, skip silently on error — not every subdirectory is a git repo), compare the parsed host+org+repo against the target URL. First match wins; result is cached in-memory for the process lifetime so a multi-package sync doesn't re-walk the same directories repeatedly.

  - *Cost:* one `git` subprocess per candidate directory, once per not-yet-mirrored URL. For `~/offline-knowledge`'s ~200 top-level checkouts, worst case (no match found) is ~200 fast subprocess calls — noticeable but not slow (candidate git calls are local, not network).
  - *Benefit:* fully automatic — any repo that happens to already be cloned under a search path is found with zero per-package configuration, including repos nobody thought to register as a corpus entry.

**Option B — explicit mapping file, no directory walk.** A small local file (or `ragctl registry hint <url> <local-path>` command) records known `<url> -> <local path>` pairs directly; `EnsureMirror` checks this map before falling back to network. No git subprocess calls, no directory walking, trivially fast.

  - *Cost:* nothing auto-discovered — only repos someone explicitly registered get the benefit.
  - *Benefit:* dramatically simpler implementation (a lookup table, not a search), and explicit is arguably more honest than a filesystem scan that might match the wrong fork/mirror of a repo by coincidence.

**Recommendation:** ship Option B first if this epic gets built at all — it's a small fraction of Option A's complexity and covers the common case (you already know you have a local copy, e.g. right after running `hack/fetch-corpus`). Only build Option A's directory walk if Option B's manual-registration friction turns out to matter in practice.

## Inputs / Outputs
- Input: a remote URL `EnsureMirror` hasn't mirrored before, plus configured search paths (Option A) or hint map (Option B).
- Output: mirrors from a discovered local path when found; otherwise falls back to GIT-004's probe-then-clone-or-fail-fast behavior, unchanged.

## Tests
- A search path (or hint entry) containing a real local clone of the target repo is found and used; `EnsureMirror` never attempts a network dial.
- A search path with no matching repo falls through to GIT-004's normal offline/online behavior, unaffected.
- (Option A only) A worktree-linked directory (`.git` is a file, not a dir) is matched correctly via the `git remote` subprocess call, not silently skipped.

## Acceptance criteria
- [ ] At least one discovery mechanism (Option A or B) implemented.
- [ ] A first-time sync for a package whose repo already exists under a search path/hint makes zero network calls.
- [ ] Falls through cleanly to GIT-004's behavior when nothing local matches.
