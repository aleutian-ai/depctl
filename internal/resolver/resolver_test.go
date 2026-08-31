package resolver

import (
	"context"
	"testing"

	"aleutian-ai/ragctl/internal/domain"
)

// fakeResolver is an in-memory Resolver for testing Registry behavior,
// independent of any real ecosystem implementation.
type fakeResolver struct {
	name    string
	detects bool
}

func (f fakeResolver) Name() string { return f.name }

func (f fakeResolver) Detect(ctx context.Context, root string) (bool, error) {
	return f.detects, nil
}

func (f fakeResolver) Resolve(ctx context.Context, root string) (domain.Resolution, error) {
	return domain.Resolution{
		Ecosystem:   domain.Ecosystem(f.name),
		Fingerprint: "fixed-fingerprint-" + f.name,
	}, nil
}

func TestRegistryDetectAllOrdering(t *testing.T) {
	go1 := fakeResolver{name: "go", detects: true}
	python := fakeResolver{name: "python", detects: false}
	node := fakeResolver{name: "node", detects: true}

	reg := NewRegistry(go1, python, node)
	matched, err := reg.DetectAll(context.Background(), "/some/root")
	if err != nil {
		t.Fatalf("DetectAll: %v", err)
	}

	if len(matched) != 2 {
		t.Fatalf("got %d matches, want 2: %+v", len(matched), matched)
	}
	if matched[0].Name() != "go" || matched[1].Name() != "node" {
		t.Errorf("matched order = [%s, %s], want [go, node] (registration order)", matched[0].Name(), matched[1].Name())
	}
}

func TestRegistryDetectAllNoMatches(t *testing.T) {
	reg := NewRegistry(fakeResolver{name: "python", detects: false})
	matched, err := reg.DetectAll(context.Background(), "/some/root")
	if err != nil {
		t.Fatalf("DetectAll: %v", err)
	}
	if len(matched) != 0 {
		t.Errorf("got %d matches, want 0", len(matched))
	}
}

func TestResolutionFingerprintStable(t *testing.T) {
	r := fakeResolver{name: "go", detects: true}
	a, err := r.Resolve(context.Background(), "/root")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	b, err := r.Resolve(context.Background(), "/root")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if a.Fingerprint != b.Fingerprint {
		t.Errorf("Fingerprint not stable: %q vs %q", a.Fingerprint, b.Fingerprint)
	}
}
