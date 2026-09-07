// Package query implements the transport-agnostic business logic behind
// every MCP tool (MCP-003): resolving a project's dependency versions
// and performing version-filtered knowledge search. No MCP/HTTP/gRPC
// transport type is imported here — internal/mcp is the only package
// that knows this service exists.
package query

import (
	"context"
	"errors"

	"aleutian-ai/ragctl/internal/backend"
	"aleutian-ai/ragctl/internal/domain"
	"aleutian-ai/ragctl/internal/embedding"
)

// QueryMode selects how SearchKnowledge resolves which version(s) to
// search. Limited to exactly these four, per MCP-002's simplicity
// constraint — no speculative modes.
type QueryMode string

const (
	// ModeProject searches the project's currently active version of
	// Dependency.
	ModeProject QueryMode = "project"
	// ModeLatest searches whichever version holds a "latest"-reason
	// reference for Dependency, independent of any project.
	ModeLatest QueryMode = "latest"
	// ModeCompare searches both the project's active version and the
	// "latest" version, labeling each result with which it came from.
	ModeCompare QueryMode = "compare"
	// ModeAllRetained searches every retained version of Dependency,
	// unfiltered by version — admin/debug use, not for normal retrieval
	// (defeats the version-correctness guarantee on purpose).
	ModeAllRetained QueryMode = "all-retained"
)

// Errors SearchKnowledge/GetProjectDependencies/GetDependencyVersion
// return — distinct from an empty result, per MCP-002's failure-behavior
// requirements.
var (
	ErrProjectNotFound    = errors.New("query: project not found")
	ErrDependencyNotFound = errors.New("query: dependency not found")
	ErrNoActiveGeneration = errors.New("query: no active generation for this version")
)

// Query is one search request.
type Query struct {
	ProjectID  string
	Text       string
	Dependency string // package name; required for all modes except ModeAllRetained's ecosystem-wide case — see SearchKnowledge's doc comment
	Mode       QueryMode
	TopK       int // 0 defaults to defaultTopK
}

const defaultTopK = 10

// ResultChunk is one matched chunk, with its actual retrievable text
// (not just a vector-backend point reference) and the provenance an
// agent needs to judge trust.
type ResultChunk struct {
	ChunkID    string
	Content    string
	Score      float32
	Ecosystem  string
	Dependency string
	Version    string
	Generation string
	SourceType string
	Authority  int
	// TrustClass is derived at query time from SourceType (SEC-001's
	// generation.TrustClassForSourceType) rather than stored on the
	// vector point itself — a vector backend's PointMetadata schema is
	// fixed (VEC-001), and TrustClass is a pure function of SourceType,
	// so deriving it here works retroactively on every already-synced
	// point with no backend schema change or re-replication needed.
	TrustClass domain.TrustClass
}

// SearchResult is SearchKnowledge's output.
type SearchResult struct {
	Chunks []ResultChunk
}

// ProjectDependency is one of a project's resolved dependencies, plus
// whether it currently has a promoted generation to search against.
type ProjectDependency struct {
	Dependency          domain.DependencyVersion
	HasActiveGeneration bool
}

// Provenance is a chunk's full origin — SourceURI in particular isn't
// carried on a vector backend point's metadata (backend.PointMetadata
// has no such field; it's KnowledgeObject-only), so resolving it means
// reading Badger, not just the search result.
type Provenance struct {
	ChunkID     string
	ObjectID    string
	SourceURI   string
	SourceType  string
	LogicalPath string
	Authority   int
	Version     string
	Generation  string
}

// ControlStore is the narrow slice of *bbolt.Store this package needs.
type ControlStore interface {
	ListProjects(ctx context.Context) ([]domain.Project, error)
	GetProject(ctx context.Context, id string) (domain.Project, error)
	GetResolution(ctx context.Context, projectID string) (domain.Resolution, error)
	GetActiveGeneration(ctx context.Context, ecosystem domain.Ecosystem, pkg, backendName string) (domain.Generation, error)
	ListReferences(ctx context.Context, ecosystem domain.Ecosystem, pkg, version string) ([]domain.VersionReference, error)
	ListAllReferences(ctx context.Context) ([]domain.VersionReference, error)
}

// Status is knowledge_status's coarse fleet-wide summary — not a full
// PLAN-001 diff (that needs registry access this package doesn't have,
// plus fresh dependency resolution), just "how much of what's already
// registered has been synced."
type Status struct {
	TotalProjects           int
	TotalDependencies       int
	WithActiveGeneration    int
	WithoutActiveGeneration int
	// Projects lists every registered project's real ID (what
	// project_id must actually be, e.g. "proj_...") alongside its root
	// path — an agent otherwise has no way to discover a project's ID
	// from its filesystem path, since MCP tools take the ID, not the
	// path a user would naturally type or an agent would infer from cwd.
	Projects []ProjectRef
}

// ProjectRef is a registered project's ID and root path — the minimum
// an MCP caller needs to resolve "the project I'm in" to a usable
// project_id for every other tool.
type ProjectRef struct {
	ID   string
	Root string
}

// DataStore is the narrow slice of *badger.Store this package needs.
type DataStore interface {
	GetChunk(ctx context.Context, generationID, chunkID string) (domain.Chunk, error)
	GetKnowledgeObject(ctx context.Context, id string) (domain.KnowledgeObject, error)
	ListGenerationChunks(ctx context.Context, generationID string) ([]domain.Chunk, error)
}

// ReleaseChange is one release-note excerpt GetReleaseChanges found.
type ReleaseChange struct {
	Ecosystem string
	Version   string // the release_version this excerpt documents — always From or To, see GetReleaseChanges' doc comment
	Excerpt   string
}

// Service is ragctl's query business logic — one struct, plain methods,
// no generic pipeline/middleware framework.
//
// embedder wasn't in the ticket's sketched Service struct, but
// VectorBackend.Query needs a query *vector*, and nothing turns
// Query.Text into one without an Embedder — omitting it would leave
// SearchKnowledge unable to actually search. backendName/namespace
// weren't shown either, for the same reason GEN-002/VAL-004 needed
// them: GetActiveGeneration and VectorBackend.Query are both
// backend/namespace-scoped, and a Service has to know which one it's
// searching.
type Service struct {
	control     ControlStore
	data        DataStore
	backend     backend.VectorBackend
	embedder    embedding.Embedder
	namespace   backend.Namespace
	backendName string
}

// New returns a Service wired against the given stores/providers.
func New(control ControlStore, data DataStore, vb backend.VectorBackend, embedder embedding.Embedder, namespace backend.Namespace, backendName string) *Service {
	return &Service{control: control, data: data, backend: vb, embedder: embedder, namespace: namespace, backendName: backendName}
}
