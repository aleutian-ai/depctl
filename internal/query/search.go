package query

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"aleutian-ai/ragctl/internal/backend"
	"aleutian-ai/ragctl/internal/data/generation"
	"aleutian-ai/ragctl/internal/domain"
	"aleutian-ai/ragctl/internal/observability"
	"aleutian-ai/ragctl/internal/observability/trace"
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
			if _, err := s.control.GetActiveGeneration(ctx, dv.Dependency.Ecosystem, dv.Dependency.Name, dv.Version, s.backendName); err == nil {
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
		_, err := s.control.GetActiveGeneration(ctx, dv.Dependency.Ecosystem, dv.Dependency.Name, dv.Version, s.backendName)
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
// exactly from and to, read directly out of Badger by
// Metadata["release_version"] (a metadata scan, not a search). The
// release-note normalizer tags each release-note file with its first
// version-like heading only, so a version is found when it leads a
// release-notes file (typically the newest entry of a changelog), not
// when it's an older entry further down.
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
		// Release notes are cumulative, so the newer version's own
		// generation usually carries both sections; fall back to the
		// older one's if only that version is active.
		gen, err := s.control.GetActiveGeneration(ctx, eco, dependency, to, s.backendName)
		if err != nil {
			gen, err = s.control.GetActiveGeneration(ctx, eco, dependency, from, s.backendName)
		}
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

// SearchKnowledge searches the index for q.Text (embedding it first when
// the Service has an embedder), constrained to whichever version(s)
// q.Mode resolves, returning each match's actual chunk content (read
// from Badger — a point alone only carries an ID/score/metadata) plus
// provenance.
//
// q.Dependency is required for every mode (ModeAllRetained still
// searches every ecosystem's versions of it): the whole value of "version-correct search"
// depends on knowing exactly which package's exact version to constrain
// to, and backend.Filter has one Dependency/Version field, not a list —
// there is no coherent single Filter for "all of this project's
// dependencies at once."
func (s *Service) SearchKnowledge(ctx context.Context, q Query) (SearchResult, error) {
	ctx, end := trace.StartSpan(ctx, "query")
	defer end()
	start := time.Now()
	logger := observability.FromContext(ctx).With(
		observability.KeyProjectID, q.ProjectID,
		observability.KeyDependency, q.Dependency,
		observability.KeyBackend, s.backendName,
	)

	var result SearchResult
	var err error
	switch q.Mode {
	case ModeProject:
		result, err = s.searchProject(ctx, q)
	case ModeLatest:
		result, err = s.searchLatest(ctx, q)
	case ModeCompare:
		result, err = s.searchCompare(ctx, q)
	case ModeAllRetained:
		result, err = s.searchAllRetained(ctx, q)
	default:
		return SearchResult{}, fmt.Errorf("query: unknown mode %q", q.Mode)
	}
	if err != nil {
		trace.RecordError(ctx, err)
		logger.Error("query failed", observability.KeyStage, "query", observability.KeyDurationMS, time.Since(start).Milliseconds(), "mode", string(q.Mode), "error", err)
		return result, err
	}
	logger.Info("query completed",
		observability.KeyStage, "query",
		observability.KeyDurationMS, time.Since(start).Milliseconds(),
		observability.KeyRetrievalTopK, q.TopK,
		observability.KeyRetrievalCount, len(result.Chunks),
		"mode", string(q.Mode),
	)
	return result, nil
}

func (s *Service) searchProject(ctx context.Context, q Query) (SearchResult, error) {
	dep, err := s.resolveProjectDependency(ctx, q.ProjectID, q.Dependency)
	if err != nil {
		return SearchResult{}, err
	}
	// Active generations are per version (ADR-012), so this asks exactly
	// "is the version this project resolves currently served?". It
	// replaces an earlier workaround for the one-pointer-per-dependency
	// model, which treated any ever-promoted (ACTIVE or SUPERSEDED)
	// generation of the version as searchable so VALID-002's two-project
	// case still worked.
	gen, err := s.control.GetActiveGeneration(ctx, dep.Dependency.Ecosystem, dep.Dependency.Name, dep.Version, s.backendName)
	if err != nil {
		return SearchResult{}, fmt.Errorf("%w: %s", ErrNoActiveGeneration, dep.Dependency.Name)
	}
	// Scoped to the active generation itself, not just the version, so a
	// rebuild's not-yet-collected predecessor can never mix stale chunks
	// into results.
	return s.search(ctx, q.Text, q.TopK, &backend.Filter{
		Ecosystem:  string(dep.Dependency.Ecosystem),
		Dependency: dep.Dependency.Name,
		Version:    dep.Version,
		Generation: gen.ID,
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
	// Scoped to the version's active generation, as in searchProject, so
	// a rebuild's not-yet-collected predecessor never mixes in.
	gen, err := s.control.GetActiveGeneration(ctx, ref.Ecosystem, ref.Package, ref.Version, s.backendName)
	if err != nil {
		return SearchResult{}, fmt.Errorf("%w: %s %s", ErrNoActiveGeneration, ref.Package, ref.Version)
	}
	return s.search(ctx, q.Text, q.TopK, &backend.Filter{
		Ecosystem:  string(ref.Ecosystem),
		Dependency: ref.Package,
		Version:    ref.Version,
		Generation: gen.ID,
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
		// Only versions with an active generation, and only that
		// generation (see searchLatest).
		gen, err := s.control.GetActiveGeneration(ctx, r.Ecosystem, r.Package, r.Version, s.backendName)
		if err != nil {
			continue
		}
		result, err := s.search(ctx, q.Text, q.TopK, &backend.Filter{Ecosystem: string(r.Ecosystem), Dependency: r.Package, Version: r.Version, Generation: gen.ID})
		if err != nil {
			continue
		}
		merged.Chunks = append(merged.Chunks, result.Chunks...)
	}
	return merged, nil
}

// search queries the backend under filter, with text embedded once when
// the service has an embedder (a keyword-only service has none), and
// resolves each match's actual chunk content from Badger.
func (s *Service) search(ctx context.Context, text string, topK int, filter *backend.Filter) (SearchResult, error) {
	if topK <= 0 {
		topK = defaultTopK
	}
	var vector []float32
	if s.embedder != nil {
		v, err := s.embedder.EmbedQuery(ctx, text)
		if err != nil {
			return SearchResult{}, fmt.Errorf("query: embed query text: %w", err)
		}
		vector = v
	}
	result, err := s.backend.Query(ctx, backend.QueryRequest{
		Namespace: s.namespace.Name,
		Vector:    vector,
		Text:      text,
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
			Breadcrumb: breadcrumb(p.Metadata.Dependency, p.Metadata.Version, chunk.Metadata),
		})
	}
	return SearchResult{Chunks: chunks}, nil
}

// breadcrumb builds a display string identifying a chunk's structural
// position: "dependency@version" plus whatever structural segments the
// chunk's own metadata carries (a Markdown section path, or a symbol
// chunk's file/symbol), per STRUCT-003's promoted fields. Never empty —
// a chunk with neither kind of structural metadata still gets a valid
// "dependency@version".
func breadcrumb(dependency, version string, metadata map[string]string) string {
	head := dependency
	if version != "" {
		head += "@" + version
	}

	var segments []string
	if raw := metadata["section_path"]; raw != "" && raw != "null" {
		var path []string
		if err := json.Unmarshal([]byte(raw), &path); err == nil {
			segments = path
		}
	} else if symbol := metadata["symbol"]; symbol != "" {
		if sourcePath := metadata["source_path"]; sourcePath != "" {
			segments = append(segments, sourcePath)
		}
		segments = append(segments, symbol)
	}

	if len(segments) == 0 {
		return head
	}
	return head + " > " + strings.Join(segments, " > ")
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
