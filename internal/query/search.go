package query

import (
	"context"
	"fmt"

	"aleutian-ai/ragctl/internal/backend"
	"aleutian-ai/ragctl/internal/data/generation"
	"aleutian-ai/ragctl/internal/domain"
)

// Status summarizes fleet-wide sync readiness: every registered
// project's resolved dependencies, tallied by whether each currently
// has an active (promoted) generation.
func (s *Service) Status(ctx context.Context) (Status, error) {
	projects, err := s.control.ListProjects(ctx)
	if err != nil {
		return Status{}, fmt.Errorf("query: list projects: %w", err)
	}

	status := Status{TotalProjects: len(projects)}
	for _, p := range projects {
		status.Projects = append(status.Projects, ProjectRef{ID: p.ID, Root: p.Root})
		resolution, err := s.control.GetResolution(ctx, p.ID)
		if err != nil {
			continue // not yet resolved — nothing to tally for this project
		}
		for _, dv := range resolution.Dependencies {
			status.TotalDependencies++
			if _, err := s.control.GetActiveGeneration(ctx, dv.Dependency.Ecosystem, dv.Dependency.Name, s.backendName); err == nil {
				status.WithActiveGeneration++
			} else {
				status.WithoutActiveGeneration++
			}
		}
	}
	return status, nil
}

// GetProjectDependencies lists projectID's resolved dependencies, each
// flagged with whether it currently has a promoted generation.
func (s *Service) GetProjectDependencies(ctx context.Context, projectID string) ([]ProjectDependency, error) {
	resolution, err := s.getResolution(ctx, projectID)
	if err != nil {
		return nil, err
	}

	deps := make([]ProjectDependency, len(resolution.Dependencies))
	for i, dv := range resolution.Dependencies {
		_, err := s.control.GetActiveGeneration(ctx, dv.Dependency.Ecosystem, dv.Dependency.Name, s.backendName)
		deps[i] = ProjectDependency{Dependency: dv, HasActiveGeneration: err == nil}
	}
	return deps, nil
}

// GetDependencyVersion returns projectID's resolved version of pkg, or
// ErrDependencyNotFound if the project doesn't depend on it.
func (s *Service) GetDependencyVersion(ctx context.Context, projectID, pkg string) (domain.DependencyVersion, error) {
	return s.resolveProjectDependency(ctx, projectID, pkg)
}

// GetProvenance resolves a chunk's full origin, reading through to its
// parent KnowledgeObject for fields (SourceURI, LogicalPath) that a
// vector backend point's metadata alone doesn't carry.
func (s *Service) GetProvenance(ctx context.Context, generationID, chunkID string) (Provenance, error) {
	chunk, err := s.data.GetChunk(ctx, generationID, chunkID)
	if err != nil {
		return Provenance{}, fmt.Errorf("query: get chunk %s/%s: %w", generationID, chunkID, err)
	}
	obj, err := s.data.GetKnowledgeObject(ctx, chunk.ObjectID)
	if err != nil {
		return Provenance{}, fmt.Errorf("query: get object %s: %w", chunk.ObjectID, err)
	}
	return Provenance{
		ChunkID:     chunk.ID,
		ObjectID:    obj.ID,
		SourceURI:   obj.SourceURI,
		SourceType:  obj.SourceType,
		LogicalPath: obj.LogicalPath,
		Authority:   obj.Authority,
		Version:     obj.Dependency.Version,
		Generation:  generationID,
	}, nil
}

// GetReleaseChanges returns release-note excerpts for dependency at
// exactly from and to — NORM-005's release-note normalizer tags each
// versioned section with Metadata["release_version"], and this reads
// those sections directly out of Badger (a metadata scan, not a vector
// search — there's no semantic query here, just "give me the section
// for this exact version").
//
// This deliberately does NOT walk every version between from and to:
// no version-ordering/semver-range utility exists anywhere in this
// codebase (comparing version strings lexically would misorder e.g.
// "v2.0.0" before "v10.0.0"), and building one is out of scope for
// wiring the query service into MCP tools. "from"/"to" are two exact
// version lookups; a caller wanting the full history between them
// currently has to enumerate versions itself.
//
// dependency alone (no project/ecosystem) means this searches every
// ecosystem's active generation for a package of that name — same
// ecosystem-blind-search shape as ModeAllRetained.
func (s *Service) GetReleaseChanges(ctx context.Context, dependency, from, to string) ([]ReleaseChange, error) {
	all, err := s.control.ListAllReferences(ctx)
	if err != nil {
		return nil, fmt.Errorf("query: list all references: %w", err)
	}
	ecosystems := map[domain.Ecosystem]bool{}
	for _, r := range all {
		if r.Package == dependency {
			ecosystems[r.Ecosystem] = true
		}
	}
	if len(ecosystems) == 0 {
		return nil, fmt.Errorf("%w: %s", ErrDependencyNotFound, dependency)
	}

	wantVersions := map[string]bool{from: true, to: true}
	var changes []ReleaseChange
	for eco := range ecosystems {
		gen, err := s.control.GetActiveGeneration(ctx, eco, dependency, s.backendName)
		if err != nil {
			continue
		}
		chunks, err := s.data.ListGenerationChunks(ctx, gen.ID)
		if err != nil {
			continue
		}
		for _, c := range chunks {
			if c.Metadata["content_type"] != "release_note" {
				continue
			}
			version := c.Metadata["release_version"]
			if !wantVersions[version] {
				continue
			}
			changes = append(changes, ReleaseChange{Ecosystem: string(eco), Version: version, Excerpt: string(c.Content)})
		}
	}
	return changes, nil
}

// SearchKnowledge embeds q.Text and searches the vector backend,
// constrained to whichever version(s) q.Mode resolves, returning each
// match's actual chunk content (read from Badger — a vector point alone
// only carries an ID/score/metadata) plus provenance.
//
// q.Dependency is required for every mode except ModeAllRetained's
// ecosystem-unfiltered case: the whole value of "version-correct search"
// depends on knowing exactly which package's exact version to constrain
// to, and backend.Filter has one Dependency/Version field, not a list —
// there is no coherent single Filter for "all of this project's
// dependencies at once."
func (s *Service) SearchKnowledge(ctx context.Context, q Query) (SearchResult, error) {
	switch q.Mode {
	case ModeProject:
		return s.searchProject(ctx, q)
	case ModeLatest:
		return s.searchLatest(ctx, q)
	case ModeCompare:
		return s.searchCompare(ctx, q)
	case ModeAllRetained:
		return s.searchAllRetained(ctx, q)
	default:
		return SearchResult{}, fmt.Errorf("query: unknown mode %q", q.Mode)
	}
}

func (s *Service) searchProject(ctx context.Context, q Query) (SearchResult, error) {
	dep, err := s.resolveProjectDependency(ctx, q.ProjectID, q.Dependency)
	if err != nil {
		return SearchResult{}, err
	}
	// GetActiveGeneration only checks "has *anything* ever been promoted
	// for this ecosystem+package+backend" — active_generations is a
	// single, backend-scoped pointer (whichever generation was most
	// recently promoted, by any project), not a per-project or per-
	// version one. Comparing its Version against dep.Version directly
	// was tried and reverted: it broke the legitimate multi-project case
	// VALID-002 (docs/tickets/completed/45-competitive-validation)
	// exists to prove — Project A resolving v1.0.0 after Project B's
	// v2.0.0 supersedes it as the active pointer is exactly the case
	// where the active pointer's version differs from dep.Version but
	// the search must still succeed (v1.0.0's real content is untouched
	// by v2.0.0's promotion, just no longer "the" active generation).
	if _, err := s.control.GetActiveGeneration(ctx, dep.Dependency.Ecosystem, dep.Dependency.Name, s.backendName); err != nil {
		return SearchResult{}, fmt.Errorf("%w: %s", ErrNoActiveGeneration, dep.Dependency.Name)
	}
	// The real, correctly-scoped check (VALID-001's actual finding): has
	// THIS SPECIFIC VERSION ever itself been successfully promoted —
	// State ACTIVE (currently the pointer) or SUPERSEDED (was the
	// pointer once, since replaced by a newer promotion) — as opposed to
	// FAILED or stuck non-terminal. Unlike the active-pointer check
	// above, this is per-version, so it can't misfire on VALID-002's
	// multi-project scenario the way the reverted attempt did.
	versionGens, err := s.control.ListGenerationsByDependencyVersion(ctx, dep.Dependency.Ecosystem, dep.Dependency.Name, dep.Version)
	if err != nil {
		return SearchResult{}, fmt.Errorf("%w: %s", ErrNoActiveGeneration, dep.Dependency.Name)
	}
	promoted := false
	for _, g := range versionGens {
		if g.State == domain.GenActive || g.State == domain.GenSuperseded {
			promoted = true
			break
		}
	}
	if !promoted {
		return SearchResult{}, fmt.Errorf("%w: %s", ErrNoActiveGeneration, dep.Dependency.Name)
	}
	return s.search(ctx, q.Text, q.TopK, &backend.Filter{
		Ecosystem:  string(dep.Dependency.Ecosystem),
		Dependency: dep.Dependency.Name,
		Version:    dep.Version,
	})
}

func (s *Service) searchLatest(ctx context.Context, q Query) (SearchResult, error) {
	if q.Dependency == "" {
		return SearchResult{}, fmt.Errorf("%w: dependency is required for mode %q", ErrDependencyNotFound, ModeLatest)
	}
	ref, err := s.findLatestReference(ctx, q.Dependency)
	if err != nil {
		return SearchResult{}, err
	}
	return s.search(ctx, q.Text, q.TopK, &backend.Filter{
		Ecosystem:  string(ref.Ecosystem),
		Dependency: ref.Package,
		Version:    ref.Version,
	})
}

func (s *Service) searchCompare(ctx context.Context, q Query) (SearchResult, error) {
	projectResult, projectErr := s.searchProject(ctx, q)
	latestResult, latestErr := s.searchLatest(ctx, q)
	if projectErr != nil && latestErr != nil {
		return SearchResult{}, fmt.Errorf("compare: project search: %v; latest search: %v", projectErr, latestErr)
	}
	var merged SearchResult
	merged.Chunks = append(merged.Chunks, projectResult.Chunks...)
	merged.Chunks = append(merged.Chunks, latestResult.Chunks...)
	return merged, nil
}

func (s *Service) searchAllRetained(ctx context.Context, q Query) (SearchResult, error) {
	if q.Dependency == "" {
		return SearchResult{}, fmt.Errorf("%w: dependency is required", ErrDependencyNotFound)
	}
	// No ecosystem is known without a project or a reference to derive
	// it from — search across every reference for this package name,
	// regardless of ecosystem, and let per-version filters narrow within
	// the results the backend actually returns.
	all, err := s.control.ListAllReferences(ctx)
	if err != nil {
		return SearchResult{}, fmt.Errorf("query: list all references: %w", err)
	}
	versions := map[string]domain.VersionReference{}
	for _, r := range all {
		if r.Package == q.Dependency {
			versions[string(r.Ecosystem)+"|"+r.Version] = r
		}
	}
	if len(versions) == 0 {
		return SearchResult{}, fmt.Errorf("%w: %s", ErrDependencyNotFound, q.Dependency)
	}

	var merged SearchResult
	for _, r := range versions {
		result, err := s.search(ctx, q.Text, q.TopK, &backend.Filter{Ecosystem: string(r.Ecosystem), Dependency: r.Package, Version: r.Version})
		if err != nil {
			continue
		}
		merged.Chunks = append(merged.Chunks, result.Chunks...)
	}
	return merged, nil
}

// search embeds text once and queries the backend under filter,
// resolving each match's actual chunk content from Badger.
func (s *Service) search(ctx context.Context, text string, topK int, filter *backend.Filter) (SearchResult, error) {
	if topK <= 0 {
		topK = defaultTopK
	}
	vectors, err := s.embedder.Embed(ctx, []string{text})
	if err != nil {
		return SearchResult{}, fmt.Errorf("query: embed query text: %w", err)
	}
	result, err := s.backend.Query(ctx, backend.QueryRequest{
		Namespace: s.namespace.Name,
		Vector:    vectors[0],
		TopK:      topK,
		Filter:    filter,
	})
	if err != nil {
		return SearchResult{}, fmt.Errorf("query: backend query: %w", err)
	}

	chunks := make([]ResultChunk, 0, len(result.Points))
	for _, p := range result.Points {
		chunk, err := s.data.GetChunk(ctx, p.Metadata.Generation, p.ID)
		if err != nil {
			// A point the backend returned but Badger no longer has
			// (e.g. GC raced a query) is skipped, not fatal to the
			// whole search.
			continue
		}
		chunks = append(chunks, ResultChunk{
			ChunkID:    p.ID,
			Content:    string(chunk.Content),
			Score:      p.Score,
			Ecosystem:  p.Metadata.Ecosystem,
			Dependency: p.Metadata.Dependency,
			Version:    p.Metadata.Version,
			Generation: p.Metadata.Generation,
			SourceType: p.Metadata.SourceType,
			Authority:  p.Metadata.Authority,
			TrustClass: generation.TrustClassForSourceType(p.Metadata.SourceType),
		})
	}
	return SearchResult{Chunks: chunks}, nil
}

func (s *Service) getResolution(ctx context.Context, projectID string) (domain.Resolution, error) {
	if _, err := s.control.GetProject(ctx, projectID); err != nil {
		return domain.Resolution{}, fmt.Errorf("%w: %s", ErrProjectNotFound, projectID)
	}
	resolution, err := s.control.GetResolution(ctx, projectID)
	if err != nil {
		return domain.Resolution{}, fmt.Errorf("%w: project %s has no resolution", ErrDependencyNotFound, projectID)
	}
	return resolution, nil
}

func (s *Service) resolveProjectDependency(ctx context.Context, projectID, pkg string) (domain.DependencyVersion, error) {
	resolution, err := s.getResolution(ctx, projectID)
	if err != nil {
		return domain.DependencyVersion{}, err
	}
	for _, dv := range resolution.Dependencies {
		if dv.Dependency.Name == pkg {
			return dv, nil
		}
	}
	return domain.DependencyVersion{}, fmt.Errorf("%w: %s", ErrDependencyNotFound, pkg)
}

func (s *Service) findLatestReference(ctx context.Context, pkg string) (domain.VersionReference, error) {
	all, err := s.control.ListAllReferences(ctx)
	if err != nil {
		return domain.VersionReference{}, fmt.Errorf("query: list all references: %w", err)
	}
	for _, r := range all {
		if r.Package == pkg && r.Reason == domain.ReferenceReasonLatest {
			return r, nil
		}
	}
	return domain.VersionReference{}, fmt.Errorf("%w: no \"latest\" reference recorded for %s", ErrDependencyNotFound, pkg)
}
