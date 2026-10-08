package golang

import (
	"context"
	"testing"

	"github.com/aleutian-ai/depctl/internal/domain"
)

func TestResolveEndToEnd(t *testing.T) {
	requireGo(t)
	offlineEnv(t)

	base := t.TempDir()
	depDir := base + "/foolocal"
	writeGoMod(t, depDir, "module example.com/foo\n\ngo 1.21\n")

	root := base + "/main"
	writeGoMod(t, root, "module example.com/main\n\ngo 1.21\n\nrequire example.com/foo v0.0.0\n\nreplace example.com/foo => ../foolocal\n")

	res, err := New().Resolve(context.Background(), root)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Ecosystem != domain.EcosystemGo {
		t.Errorf("Ecosystem = %q, want go", res.Ecosystem)
	}
	if len(res.Dependencies) != 1 || res.Dependencies[0].Dependency.Name != "example.com/foo" {
		t.Fatalf("Dependencies = %+v, want exactly example.com/foo", res.Dependencies)
	}
	if res.Fingerprint == "" {
		t.Error("Fingerprint is empty")
	}

	res2, err := New().Resolve(context.Background(), root)
	if err != nil {
		t.Fatalf("second Resolve: %v", err)
	}
	if res.Fingerprint != res2.Fingerprint {
		t.Errorf("fingerprint not stable across identical resolves: %q vs %q", res.Fingerprint, res2.Fingerprint)
	}
}
