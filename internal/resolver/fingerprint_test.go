package resolver

import (
	"testing"

	"github.com/aleutian-ai/depctl/internal/domain"
)

func TestFingerprintStableRegardlessOfOrder(t *testing.T) {
	a := []domain.DependencyVersion{
		{Dependency: domain.Dependency{Ecosystem: domain.EcosystemGo, Name: "example.com/b"}, Version: "v1.0.0"},
		{Dependency: domain.Dependency{Ecosystem: domain.EcosystemGo, Name: "example.com/a"}, Version: "v2.0.0"},
	}
	b := []domain.DependencyVersion{a[1], a[0]}

	if Fingerprint(a) != Fingerprint(b) {
		t.Error("Fingerprint differs by input order, want order-independent")
	}
}

func TestFingerprintDiffersForDifferentDeps(t *testing.T) {
	a := []domain.DependencyVersion{
		{Dependency: domain.Dependency{Ecosystem: domain.EcosystemGo, Name: "example.com/a"}, Version: "v1.0.0"},
	}
	b := []domain.DependencyVersion{
		{Dependency: domain.Dependency{Ecosystem: domain.EcosystemGo, Name: "example.com/a"}, Version: "v2.0.0"},
	}
	if Fingerprint(a) == Fingerprint(b) {
		t.Error("Fingerprint identical for different dependency sets")
	}
}

func TestFingerprintDiffersAcrossEcosystems(t *testing.T) {
	a := []domain.DependencyVersion{
		{Dependency: domain.Dependency{Ecosystem: domain.EcosystemGo, Name: "example"}, Version: "v1.0.0"},
	}
	b := []domain.DependencyVersion{
		{Dependency: domain.Dependency{Ecosystem: domain.EcosystemPython, Name: "example"}, Version: "v1.0.0"},
	}
	if Fingerprint(a) == Fingerprint(b) {
		t.Error("Fingerprint identical across different ecosystems for the same name/version")
	}
}
