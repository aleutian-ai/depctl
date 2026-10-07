package retention

import (
	"context"
	"fmt"
	"time"

	"aleutian-ai/ragctl/internal/domain"
)

// GCCandidate is one (ecosystem, package, version) tuple RET-003 has
// determined is safe to delete.
type GCCandidate struct {
	Ecosystem domain.Ecosystem
	Package   string
	Version   string
	Reason    string // e.g. "grace_expired"
}

// dependencyVersion is a bare (ecosystem, package, version) tuple, used
// to dedupe candidates discovered from multiple reference rows.
type dependencyVersion struct {
	ecosystem domain.Ecosystem
	pkg       string
	version   string
}

func (v dependencyVersion) key() string {
	return string(v.ecosystem) + "|" + v.pkg + "|" + v.version
}

// PlanGC computes every dependency version eligible for garbage
// collection. A version is eligible only if ALL hold:
//   - no "project"-reason reference (no registered project currently
//     resolves to it)
//   - no "latest"-reason reference
//   - no "manual_pin"-reason reference
//   - a "grace_period" reference exists and now is past its expiry
//
// Being active does not protect a version (ADR-012, see below); gc.Run
// clears the active pointer itself. backendName is currently unused.
//
// Pure over store reads: no deletion happens here (RET-004).
//
// The candidate set itself is discovered via ListAllReferences rather
// than "iterating the dependency_versions bucket" the design sketch
// names — that bucket (STORE-001's fuller relational split) was never
// built; every (ecosystem, package, version) a project has ever
// resolved to already has at least one reference row once PLAN-003
// records it, so the references bucket is the actual source of that
// enumeration in this codebase.
func PlanGC(ctx context.Context, store ControlStore, backendName string, gracePeriod time.Duration, now time.Time) ([]GCCandidate, error) {
	gracePeriod = EffectiveGracePeriod(gracePeriod)

	all, err := store.ListAllReferences(ctx)
	if err != nil {
		return nil, fmt.Errorf("retention: list all references: %w", err)
	}

	seen := map[string]dependencyVersion{}
	for _, r := range all {
		v := dependencyVersion{ecosystem: r.Ecosystem, pkg: r.Package, version: r.Version}
		seen[v.key()] = v
	}

	var candidates []GCCandidate
	for _, v := range seen {
		refs, err := store.ListReferences(ctx, v.ecosystem, v.pkg, v.version)
		if err != nil {
			return nil, fmt.Errorf("retention: list references %s/%s/%s: %w", v.ecosystem, v.pkg, v.version, err)
		}

		var graceRef *domain.VersionReference
		blocked := false
		for i := range refs {
			switch refs[i].Reason {
			case domain.ReferenceReasonProject, domain.ReferenceReasonLatest, domain.ReferenceReasonManualPin:
				blocked = true
			case domain.ReferenceReasonGracePeriod:
				graceRef = &refs[i]
			}
		}
		if blocked || graceRef == nil {
			continue
		}
		if !now.After(GraceExpiry(*graceRef, gracePeriod)) {
			continue
		}

		// No "active means keep" exclusion (ADR-012): references and the
		// grace period alone decide a version's lifetime. Under the old
		// one-pointer model an unreferenced old version stopped being
		// active once a newer one was promoted; now each version stays
		// active until GC retires it, so excluding active versions here
		// would make every unreferenced version immortal. gc.Run clears
		// the version's active pointer as its first step.

		candidates = append(candidates, GCCandidate{
			Ecosystem: v.ecosystem,
			Package:   v.pkg,
			Version:   v.version,
			Reason:    "grace_expired",
		})
	}

	return candidates, nil
}
