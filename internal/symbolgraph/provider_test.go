package symbolgraph

import (
	"context"
	"errors"
	"testing"
)

// fakeProvider is a trivial SymbolProvider test double, proving the
// interface is implementable and its contract (ok=false for an internal
// symbol, a populated ExternalSymbolRef for an external one, error
// propagation) round-trips correctly through a real caller.
type fakeProvider struct {
	refs map[CallSite]ExternalSymbolRef
	errs map[CallSite]error
}

var _ SymbolProvider = (*fakeProvider)(nil)

func (f *fakeProvider) Resolve(ctx context.Context, site CallSite) (ExternalSymbolRef, bool, error) {
	if err, ok := f.errs[site]; ok {
		return ExternalSymbolRef{}, false, err
	}
	ref, ok := f.refs[site]
	return ref, ok, nil
}

func TestFakeProviderResolvesKnownExternalSymbol(t *testing.T) {
	site := CallSite{File: "client.go", Line: 12, Column: 9}
	want := ExternalSymbolRef{
		Ecosystem:     "go",
		Module:        "go.etcd.io/bbolt",
		Package:       "bbolt",
		QualifiedName: "(*Tx).Bucket",
	}
	p := &fakeProvider{refs: map[CallSite]ExternalSymbolRef{site: want}}

	got, ok, err := p.Resolve(context.Background(), site)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !ok {
		t.Fatal("Resolve ok = false, want true for a known external symbol")
	}
	if got != want {
		t.Errorf("Resolve = %+v, want %+v", got, want)
	}
}

func TestFakeProviderReturnsNotOKForInternalSymbol(t *testing.T) {
	site := CallSite{File: "main.go", Line: 5, Column: 2}
	p := &fakeProvider{}

	got, ok, err := p.Resolve(context.Background(), site)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if ok {
		t.Errorf("Resolve ok = true, want false for an unregistered/internal call site (got %+v)", got)
	}
	if got != (ExternalSymbolRef{}) {
		t.Errorf("Resolve returned non-zero ref %+v when ok=false", got)
	}
}

func TestFakeProviderPropagatesError(t *testing.T) {
	site := CallSite{File: "broken.go", Line: 1, Column: 1}
	wantErr := errors.New("backing index unreachable")
	p := &fakeProvider{errs: map[CallSite]error{site: wantErr}}

	_, ok, err := p.Resolve(context.Background(), site)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Resolve err = %v, want %v", err, wantErr)
	}
	if ok {
		t.Error("Resolve ok = true alongside a non-nil error, want false")
	}
}
