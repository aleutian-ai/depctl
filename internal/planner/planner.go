// Package planner computes the diff between a project's current
// dependency resolution and its recorded state (references, active
// generations), producing typed actions for `ragctl plan`/`ragctl sync`
// to report or execute. See docs/tickets/completed/15-planner-sync.
package planner

import (
	"context"
	"sort"

	"aleutian-ai/ragctl/internal/domain"
	"aleutian-ai/ragctl/internal/registry"
)

// ActionKind identifies what Plan wants done for one dependency.
type ActionKind string

const (
	ActionAddReference  ActionKind = "ADD_REFERENCE"
	ActionDropReference ActionKind = "DROP_REFERENCE"
	ActionSyncVersion   ActionKind = "SYNC_VERSION"
	ActionRetainVersion ActionKind = "RETAIN_VERSION"
	ActionGCCandidate   ActionKind = "GC_CANDIDATE"
	ActionNoop          ActionKind = "NOOP"
)

// Action is one unit of planned work for one project+dependency.
type Action struct {
	Kind       ActionKind
	ProjectID  string
	Dependency domain.DependencyVersion
	Reason     string
}

// GenerationKey deterministically identifies a dependency version for
// the activeGenerations lookup Plan takes — callers build this map by
// checking bbolt.Store.GetActiveGeneration for each dependency version
// they care about before calling Plan, so Plan itself never touches
// storage.
func GenerationKey(dep domain.DependencyVersion) string {
	return string(dep.Dependency.Ecosystem) + "|" + dep.Dependency.Name + "|" + dep.Version
}

func referenceKey(ecosystem domain.Ecosystem, pkg string) string {
	return string(ecosystem) + "|" + pkg
}

// Plan diffs resolution (a project's freshly resolved dependencies)
// against current (that project's previously recorded VersionReferences)
// and returns the actions needed to bring recorded state in line.
//
// Plan is a pure function: no bbolt/network access happens inside it.
// Two inputs the original design sketch didn't show had to be added for
// that to hold: reg (an already-loaded *registry.Registry — REG-002's
// loading is I/O, but Match() itself is a pure in-memory lookup) and
// activeGenerations (whether a given dependency version already has a
// promoted generation — Plan can't determine that itself without
// touching bbolt, so the caller resolves it via GetActiveGeneration and
// passes the answer in as a map).
//
// GC_CANDIDATE/RETAIN_VERSION are emitted as provisional markers only:
// per PLAN-001's own non-goals, real retention/GC eligibility (does any
// *other* project still reference this version?) needs epic 16's
// cross-project reference counting, which doesn't exist yet. Every
// version this project drops is marked GC_CANDIDATE unconditionally —
// the correct, conservative default until RET-001 can check other
// projects. RETAIN_VERSION is never emitted by this ticket's Plan (a
// single project's diff structurally can't prove another project still
// needs a version); it exists in the ActionKind enum for RET-001 to
// produce once it has fleet-wide reference data.
//
// noSource holds versions sync has recorded as having no docs source
// (PLAN-005). A version needs building whenever it has no active
// generation, including when its reference is unchanged (a previous
// build failed), unless it is known to have no source and the registry
// still has no manifest for it.
func Plan(ctx context.Context, project domain.Project, resolution domain.Resolution, current []domain.VersionReference, reg *registry.Registry, activeGenerations, noSource map[string]bool) ([]Action, error) {
	currentByKey := make(map[string]domain.VersionReference, len(current))
	for _, r := range current {
		currentByKey[referenceKey(r.Ecosystem, r.Package)] = r
	}
	seen := make(map[string]bool, len(resolution.Dependencies))

	var actions []Action
	for _, dv := range resolution.Dependencies {
		key := referenceKey(dv.Dependency.Ecosystem, dv.Dependency.Name)
		seen[key] = true

		reason := ""
		_, mapped := reg.Match(dv.Dependency.Ecosystem, dv.Dependency.Name)
		if !mapped {
			reason = "no knowledge source mapped"
		}
		genKey := GenerationKey(dv)
		needsBuild := !activeGenerations[genKey] && (mapped || !noSource[genKey])

		prior, hadPrior := currentByKey[key]
		switch {
		case !hadPrior:
			actions = append(actions, Action{Kind: ActionAddReference, ProjectID: project.ID, Dependency: dv, Reason: reason})
			if needsBuild {
				actions = append(actions, Action{Kind: ActionSyncVersion, ProjectID: project.ID, Dependency: dv, Reason: reason})
			}
		case prior.Version != dv.Version:
			oldDep := domain.DependencyVersion{Dependency: dv.Dependency, Version: prior.Version}
			actions = append(actions,
				Action{Kind: ActionDropReference, ProjectID: project.ID, Dependency: oldDep, Reason: "version changed"},
				Action{Kind: ActionGCCandidate, ProjectID: project.ID, Dependency: oldDep, Reason: "no longer referenced by this project (provisional — see RET-001)"},
				// Without this, nothing ever marks the new version as
				// referenced: RET-001's grace-period/GC logic sees zero
				// references for the project's now-current version and
				// would eventually reap it as orphaned, even though it's
				// exactly what this project depends on.
				Action{Kind: ActionAddReference, ProjectID: project.ID, Dependency: dv, Reason: reason},
			)
			if needsBuild {
				actions = append(actions, Action{Kind: ActionSyncVersion, ProjectID: project.ID, Dependency: dv, Reason: reason})
			}
		case needsBuild:
			// Reference unchanged but this exact version was never
			// successfully built (an earlier build failed). Without this,
			// it stayed unbuilt forever (OPS-005's "referenced but never
			// built", PLAN-005).
			actions = append(actions, Action{Kind: ActionSyncVersion, ProjectID: project.ID, Dependency: dv, Reason: "referenced but not built — retrying"})
		default:
			actions = append(actions, Action{Kind: ActionNoop, ProjectID: project.ID, Dependency: dv})
		}
	}

	// Dependencies referenced before but absent from the new resolution
	// (e.g. removed from go.mod). Sorted for deterministic output — map
	// iteration order isn't.
	var droppedKeys []string
	for key := range currentByKey {
		if !seen[key] {
			droppedKeys = append(droppedKeys, key)
		}
	}
	sort.Strings(droppedKeys)
	for _, key := range droppedKeys {
		prior := currentByKey[key]
		dep := domain.DependencyVersion{
			Dependency: domain.Dependency{Ecosystem: prior.Ecosystem, Name: prior.Package},
			Version:    prior.Version,
		}
		actions = append(actions,
			Action{Kind: ActionDropReference, ProjectID: project.ID, Dependency: dep, Reason: "dependency removed"},
			Action{Kind: ActionGCCandidate, ProjectID: project.ID, Dependency: dep, Reason: "no longer referenced by this project (provisional — see RET-001)"},
		)
	}

	return actions, nil
}
