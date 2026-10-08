package mcp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/aleutian-ai/depctl/internal/domain"
	"github.com/aleutian-ai/depctl/internal/query"
	"github.com/aleutian-ai/depctl/internal/symbolgraph"
	"github.com/aleutian-ai/depctl/internal/symbolgraph/gopackages"
)

type fakeCallSiteResolver struct {
	bundle    *symbolgraph.EvidenceBundle
	err       error
	calledQ   string
	calledLoc symbolgraph.CallSite
}

func (f *fakeCallSiteResolver) ResolveEvidence(ctx context.Context, projectID string, site symbolgraph.CallSite, queryText string) (*symbolgraph.EvidenceBundle, error) {
	f.calledLoc = site
	f.calledQ = queryText
	return f.bundle, f.err
}

func TestExplainCallSiteHandlerReturnsSymbolAndEvidence(t *testing.T) {
	resolver := &fakeCallSiteResolver{
		bundle: &symbolgraph.EvidenceBundle{
			Symbol:     symbolgraph.ExternalSymbolRef{Ecosystem: "go", Module: "go.etcd.io/bbolt", Package: "bbolt", QualifiedName: "(*Tx).Bucket"},
			Dependency: domain.DependencyVersion{Dependency: domain.Dependency{Name: "go.etcd.io/bbolt"}, Version: "1.3.11"},
			Result:     query.SearchResult{Chunks: []query.ResultChunk{{ChunkID: "chunk_1", Content: "Bucket returns..."}}},
		},
	}
	handler := explainCallSiteHandler(resolver, jitDeps{})

	_, out, err := handler(context.Background(), nil, ExplainCallSiteIn{ProjectID: "proj_1", File: "client.go", Line: 12, Column: 9})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if out.Symbol == nil {
		t.Fatal("out.Symbol = nil, want a populated ResolvedSymbol")
	}
	if out.Symbol.Module != "go.etcd.io/bbolt" || out.Symbol.Version != "1.3.11" || out.Symbol.QualifiedName != "(*Tx).Bucket" {
		t.Errorf("out.Symbol = %+v, unexpected", out.Symbol)
	}
	if len(out.Chunks) != 1 || out.Chunks[0].ChunkID != "chunk_1" {
		t.Errorf("out.Chunks = %+v, want the resolver's one chunk", out.Chunks)
	}
	if out.Note != securityNote {
		t.Errorf("out.Note = %q, want securityNote", out.Note)
	}
	if resolver.calledLoc != (symbolgraph.CallSite{File: "client.go", Line: 12, Column: 9}) {
		t.Errorf("resolver called with %+v, want the input call site", resolver.calledLoc)
	}
}

func TestExplainCallSiteHandlerInternalCallSiteReturnsNote(t *testing.T) {
	resolver := &fakeCallSiteResolver{bundle: nil}
	handler := explainCallSiteHandler(resolver, jitDeps{})

	_, out, err := handler(context.Background(), nil, ExplainCallSiteIn{ProjectID: "proj_1", File: "main.go", Line: 5, Column: 2})
	if err != nil {
		t.Fatalf("handler: %v, want nil error for an internal call site", err)
	}
	if out.Symbol != nil || len(out.Chunks) != 0 {
		t.Errorf("out = %+v, want empty Symbol/Chunks for an internal call site", out)
	}
	if out.Note == "" {
		t.Error("out.Note is empty, want an explanation that this call site is internal")
	}
}

func TestExplainCallSiteHandlerNilResolverReportsNotConfigured(t *testing.T) {
	handler := explainCallSiteHandler(nil, jitDeps{})

	_, _, err := handler(context.Background(), nil, ExplainCallSiteIn{ProjectID: "proj_1", File: "client.go", Line: 1, Column: 1})
	if err == nil {
		t.Fatal("handler succeeded, want a not-configured error")
	}
}

func TestExplainCallSiteHandlerMapsDependencyNotResolvedError(t *testing.T) {
	resolver := &fakeCallSiteResolver{err: symbolgraph.ErrDependencyNotResolved}
	handler := explainCallSiteHandler(resolver, jitDeps{})

	_, _, err := handler(context.Background(), nil, ExplainCallSiteIn{ProjectID: "proj_1", File: "client.go", Line: 1, Column: 1})
	if !errors.Is(err, symbolgraph.ErrDependencyNotResolved) {
		t.Fatalf("handler err = %v, want wrapping ErrDependencyNotResolved", err)
	}
}

// e2eControlStore/e2eQueryService are minimal symbolgraph.ControlStore/
// QueryService fakes, distinct from this package's own QueryService
// fakes (a different interface shape) — used only to prove the real
// chain (gopackages.Provider -> symbolgraph.Resolver ->
// explainCallSiteHandler) end to end, not through a mocked resolver.
type e2eControlStore struct{ resolution domain.Resolution }

func (s *e2eControlStore) GetResolution(ctx context.Context, projectID string) (domain.Resolution, error) {
	return s.resolution, nil
}

type e2eQueryService struct{ result query.SearchResult }

func (s *e2eQueryService) SearchKnowledge(ctx context.Context, q query.Query) (query.SearchResult, error) {
	return s.result, nil
}

// TestExplainCallSiteHandlerEndToEndWithRealGoPackagesProvider proves
// the full, real chain — a real go/packages-based SymbolProvider
// (GRAPH-003) resolving an actual call site in a fixture Go module,
// joined through a real symbolgraph.Resolver (GRAPH-002) — not just the
// MCP handler exercised against a fake resolver (the other tests in
// this file).
func TestExplainCallSiteHandlerEndToEndWithRealGoPackagesProvider(t *testing.T) {
	root := t.TempDir()
	depRoot := filepath.Join(root, "dep")
	mustWriteFile(t, filepath.Join(depRoot, "go.mod"), "module example.com/dep\n\ngo 1.21\n")
	mustWriteFile(t, filepath.Join(depRoot, "dep.go"), "package dep\n\nfunc Greet(name string) string { return \"hello \" + name }\n")

	appRoot := filepath.Join(root, "app")
	mustWriteFile(t, filepath.Join(appRoot, "go.mod"),
		"module example.com/app\n\ngo 1.21\n\nrequire example.com/dep v0.0.0\n\nreplace example.com/dep => ../dep\n")
	mustWriteFile(t, filepath.Join(appRoot, "main.go"),
		"package main\n\nimport \"example.com/dep\"\n\nfunc main() {\n\tdep.Greet(\"world\")\n}\n")

	provider := gopackages.New(appRoot)
	control := &e2eControlStore{resolution: domain.Resolution{Dependencies: []domain.DependencyVersion{
		{Dependency: domain.Dependency{Ecosystem: domain.EcosystemGo, Name: "example.com/dep"}, Version: "v0.0.0"},
	}}}
	queries := &e2eQueryService{result: query.SearchResult{Chunks: []query.ResultChunk{{ChunkID: "chunk_1", Content: "Greet returns a greeting"}}}}
	resolver := symbolgraph.New(provider, control, queries)

	handler := explainCallSiteHandler(resolver, jitDeps{})
	_, out, err := handler(context.Background(), nil, ExplainCallSiteIn{ProjectID: "proj_1", File: "main.go", Line: 6, Column: 6})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if out.Symbol == nil {
		t.Fatal("out.Symbol = nil, want a resolved symbol for a real external call site")
	}
	if out.Symbol.Module != "example.com/dep" || out.Symbol.QualifiedName != "Greet" {
		t.Errorf("out.Symbol = %+v, want Module=example.com/dep QualifiedName=Greet", out.Symbol)
	}
	if out.Symbol.Version != "v0.0.0" {
		t.Errorf("out.Symbol.Version = %q, want v0.0.0 (from the fixture resolution)", out.Symbol.Version)
	}
	if len(out.Chunks) != 1 || out.Chunks[0].ChunkID != "chunk_1" {
		t.Errorf("out.Chunks = %+v, want the fake query service's one chunk", out.Chunks)
	}
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestExplainCallSiteHandlerPassesQueryTextThrough(t *testing.T) {
	resolver := &fakeCallSiteResolver{bundle: &symbolgraph.EvidenceBundle{}}
	handler := explainCallSiteHandler(resolver, jitDeps{})

	_, _, err := handler(context.Background(), nil, ExplainCallSiteIn{ProjectID: "proj_1", File: "client.go", Line: 1, Column: 1, Query: "how do transactions work"})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if resolver.calledQ != "how do transactions work" {
		t.Errorf("resolver.calledQ = %q, want the caller's query text passed through unchanged", resolver.calledQ)
	}
}
