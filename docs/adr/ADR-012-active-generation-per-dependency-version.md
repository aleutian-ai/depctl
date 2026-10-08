# ADR-012: Active generations are unique per dependency version

**Status:** Accepted
**Date:** 2026-10-02

## Context

depctl's central promise is version-correct knowledge for every project it tracks, including retaining an older version that another project still uses. The control store didn't model that. The active-generation pointer was keyed by `(ecosystem, dependency, backend)`, with no version, so at most one version of a dependency could be active at a time. Promoting `uuid` v1.5.0 superseded v1.6.0, even when another project still depended on v1.6.0.

`VEC-016`'s real-container run (2026-10-01) exposed it. A project moved from `uuid` v1.6.0 to v1.5.0, and `plan` reported "up to date" while only v1.6.0 was indexed. The planner's caller (`internal/cli/plan.go`) asked "does this dependency have an active generation?" when it needed to ask "does this *version*?". The version-less lookup made the wrong question the easy one to ask. GC's own eligibility check made the same lookup and compared versions by hand (`internal/retention/gc_planner.go`), which is why the bug went unnoticed.

Fixing only the planner's comparison would have made things worse. Two projects on different versions of one dependency, a normal situation, would rebuild and supersede each other on every sync, and each project's searches would fail whenever the other synced last.

## Decision

**An active generation is unique per dependency version, not per dependency.** Several versions of the same dependency may be active at the same time when different projects reference them.

```text
(ecosystem, dependency, version, backend) → active generation
```

- The active-generation pointer is keyed by `ecosystem|dependency|version|backend`.
- **There is no version-less API.** `GetActiveGeneration`, `PromoteGeneration`'s supersede step, and `ClearActiveGeneration` all take the exact version. A version-less convenience lookup is how this class of bug comes back, so none is provided.
- Promoting a generation supersedes only the previously active generation **for that same version** (a rebuild). A different version's active generation is untouched.
- Whether an old version stays or goes is decided only by references and retention (epic 16): GC removes a version once no project references it and its grace period has expired. Promotion no longer implicitly retires other versions. **Being active is not a reason to keep a version.** GC's old "an active version is never eligible" rule only worked because promoting a newer version used to make the old one inactive; under this ADR it would make every unreferenced version immortal (found in epic 66's real-container run). GC therefore clears a version's active pointer as its first deletion step.
- Project search is scoped to the exact active **generation**, not just the version, so a rebuild's not-yet-collected predecessor never mixes stale chunks into results.

Existing installs are migrated once, in place, when the control store is opened. Each old-format pointer names a generation, and that generation's record carries its version, so the pointer is rewritten under the new key. The migration is idempotent; a pointer whose generation record is missing is left for `depctl doctor` to report, as before.

## Consequences

- Two projects on different versions of a dependency are both served correctly, and repeated syncs of either are no-ops.
- Agents searching for a project's dependency get that project's exact version or an explicit "no active generation for this version", never another version's docs.
- Callers that previously asked "is anything active for this dependency?" now have to name the version they care about. Every call site has a resolved version available; where a caller genuinely wants all versions (status, doctor), it lists pointers instead.
- More versions can be active at once, so the vector store can hold more points than before. That's the intended cost of the promise: retained versions are exactly the ones some project still references.
- The migration is control-store schema version 2. Once an install is migrated, an older depctl binary refuses to open it (`ErrUnsupportedSchemaVersion`) rather than misreading the new keys. Downgrading means restoring a pre-migration `control.db` backup.

## Related

- `VEC-016` (epic 25): the real-container run that found this.
- Epic 66: the implementation tickets, which also fix the planner's retry gap for referenced-but-unbuilt versions and the vector-readiness recheck.
- Epic 16 (retention and GC): unchanged, and now the sole owner of version lifetime.
