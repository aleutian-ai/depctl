package symbolgraph

import (
	"context"
	"errors"
	"testing"

	"github.com/aleutian-ai/depctl/internal/domain"
	"github.com/aleutian-ai/depctl/internal/query"
)

type stubControlStore struct {
	resolution domain.Resolution
	err        error
}

func (s *stubControlStore) GetResolution(ctx context.Context, projectID string) (domain.Resolution, error) {
	return s.resolution, s.err
}

type stubQueryService struct {
	result query.SearchResult
	err    error
	lastQ  query.Query
}

func (s *stubQueryService) SearchKnowledge(ctx context.Context, q query.Query) (query.SearchResult, error) {
	s.lastQ = q
	return s.result, s.err
}

func fixtureResolution() domain.Resolution {
	return domain.Resolution{
		Ecosystem: domain.EcosystemGo,
		Dependencies: []domain.DependencyVersion{
			{Dependency: domain.Dependency{Ecosystem: domain.EcosystemGo, Name: "go.etcd.io/bbolt"}, Version: "1.3.11"},
		},
	}
}

func TestResolveEvidenceEndToEnd(t *testing.T) {
	site := CallSite{File: "client.go", Line: 12, Column: 9}
	ref := ExternalSymbolRef{Ecosystem: "go", Module: "go.etcd.io/bbolt", Package: "bbolt", QualifiedName: "(*Tx).Bucket"}
	provider := &fakeProvider{refs: map[CallSite]ExternalSymbolRef{site: ref}}
	control := &stubControlStore{resolution: fixtureResolution()}
	wantResult := query.SearchResult{Chunks: []query.ResultChunk{{ChunkID: "chunk_1", Content: "Bucket returns..."}}}
	queries := &stubQueryService{result: wantResult}

	r := New(provider, control, queries)
	bundle, err := r.ResolveEvidence(context.Background(), "proj_1", site, "how does Bucket work")
	if err != nil {
		t.Fatalf("ResolveEvidence: %v", err)
	}
	if bundle == nil {
		t.Fatal("ResolveEvidence returned nil bundle, want a populated one")
	}
	if bundle.Symbol != ref {
		t.Errorf("bundle.Symbol = %+v, want %+v", bundle.Symbol, ref)
	}
	if bundle.Dependency.Dependency.Name != "go.etcd.io/bbolt" || bundle.Dependency.Version != "1.3.11" {
		t.Errorf("bundle.Dependency = %+v, want the fixture's bbolt@1.3.11", bundle.Dependency)
	}
	if len(bundle.Result.Chunks) != 1 || bundle.Result.Chunks[0].ChunkID != "chunk_1" {
		t.Errorf("bundle.Result = %+v, want the stub's chunk", bundle.Result)
	}
	if queries.lastQ.Dependency != "go.etcd.io/bbolt" || queries.lastQ.Mode != query.ModeProject {
		t.Errorf("SearchKnowledge called with %+v, want Dependency=go.etcd.io/bbolt Mode=project", queries.lastQ)
	}
}

func TestResolveEvidenceInternalCallSiteReturnsNilNil(t *testing.T) {
	site := CallSite{File: "main.go", Line: 5, Column: 2}
	provider := &fakeProvider{}
	r := New(provider, &stubControlStore{}, &stubQueryService{})

	bundle, err := r.ResolveEvidence(context.Background(), "proj_1", site, "anything")
	if err != nil {
		t.Fatalf("ResolveEvidence: %v, want nil error for an internal call site", err)
	}
	if bundle != nil {
		t.Errorf("ResolveEvidence bundle = %+v, want nil for an internal call site", bundle)
	}
}

func TestResolveEvidenceDependencyNotResolved(t *testing.T) {
	site := CallSite{File: "client.go", Line: 12, Column: 9}
	ref := ExternalSymbolRef{Ecosystem: "go", Module: "example.com/not-a-dependency", Package: "foo"}
	provider := &fakeProvider{refs: map[CallSite]ExternalSymbolRef{site: ref}}
	control := &stubControlStore{resolution: fixtureResolution()} // doesn't contain example.com/not-a-dependency
	r := New(provider, control, &stubQueryService{})

	_, err := r.ResolveEvidence(context.Background(), "proj_1", site, "anything")
	if !errors.Is(err, ErrDependencyNotResolved) {
		t.Fatalf("ResolveEvidence err = %v, want wrapping ErrDependencyNotResolved", err)
	}
}

func TestResolveEvidenceProviderErrorPropagated(t *testing.T) {
	site := CallSite{File: "broken.go", Line: 1, Column: 1}
	wantErr := errors.New("backing index unreachable")
	provider := &fakeProvider{errs: map[CallSite]error{site: wantErr}}
	r := New(provider, &stubControlStore{}, &stubQueryService{})

	_, err := r.ResolveEvidence(context.Background(), "proj_1", site, "anything")
	if !errors.Is(err, wantErr) {
		t.Fatalf("ResolveEvidence err = %v, want wrapping %v", err, wantErr)
	}
}

func TestResolveEvidenceProjectNotFoundPropagatedDistinctly(t *testing.T) {
	site := CallSite{File: "client.go", Line: 12, Column: 9}
	ref := ExternalSymbolRef{Ecosystem: "go", Module: "go.etcd.io/bbolt"}
	provider := &fakeProvider{refs: map[CallSite]ExternalSymbolRef{site: ref}}
	wantErr := errors.New("project not found")
	control := &stubControlStore{err: wantErr}
	r := New(provider, control, &stubQueryService{})

	_, err := r.ResolveEvidence(context.Background(), "proj_missing", site, "anything")
	if !errors.Is(err, wantErr) {
		t.Fatalf("ResolveEvidence err = %v, want wrapping %v", err, wantErr)
	}
	if errors.Is(err, ErrDependencyNotResolved) {
		t.Error("ResolveEvidence err wraps ErrDependencyNotResolved for a project-not-found failure — these must stay distinct")
	}
}

func TestResolveEvidenceSearchKnowledgeErrorPropagated(t *testing.T) {
	site := CallSite{File: "client.go", Line: 12, Column: 9}
	ref := ExternalSymbolRef{Ecosystem: "go", Module: "go.etcd.io/bbolt"}
	provider := &fakeProvider{refs: map[CallSite]ExternalSymbolRef{site: ref}}
	control := &stubControlStore{resolution: fixtureResolution()}
	wantErr := errors.New("no active generation")
	queries := &stubQueryService{err: wantErr}
	r := New(provider, control, queries)

	_, err := r.ResolveEvidence(context.Background(), "proj_1", site, "anything")
	if !errors.Is(err, wantErr) {
		t.Fatalf("ResolveEvidence err = %v, want wrapping %v", err, wantErr)
	}
}
