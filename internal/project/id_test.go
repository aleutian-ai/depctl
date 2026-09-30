package project

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProjectIDDeterministic(t *testing.T) {
	a := ProjectID("/Users/jin/src/foo")
	b := ProjectID("/Users/jin/src/foo")
	if a != b {
		t.Errorf("ProjectID not deterministic: %s != %s", a, b)
	}
}

func TestProjectIDGoldenValue(t *testing.T) {
	// Golden value pinned so an accidental change to the hash input or
	// encoding is caught by CI rather than silently changing every
	// project's ID on upgrade.
	got := ProjectID("/Users/jin/src/foo")
	want := "proj_45na2schjt6o6mta3hqew66ge4iji3lftpdqjhvgrjhidwxw7a4q"
	if got != want {
		t.Errorf("ProjectID golden value changed:\n got:  %s\n want: %s", got, want)
	}
}

func TestProjectIDDifferentPathsDiffer(t *testing.T) {
	seen := map[string]bool{}
	paths := []string{
		"/a/b/c", "/a/b/d", "/a/b", "/x/y/z", "/", "/a/b/c/",
	}
	for _, p := range paths {
		id := ProjectID(p)
		if seen[id] {
			t.Errorf("collision for path %q -> %s", p, id)
		}
		seen[id] = true
	}
}

func TestCanonicalRootNormalizesTrailingSlashAndDot(t *testing.T) {
	a, err := CanonicalRoot("/tmp/foo/")
	if err != nil {
		t.Fatalf("CanonicalRoot: %v", err)
	}
	b, err := CanonicalRoot("/tmp/foo/.")
	if err != nil {
		t.Fatalf("CanonicalRoot: %v", err)
	}
	if a != b {
		t.Errorf("expected same canonical root, got %q and %q", a, b)
	}
	if ProjectID(a) != ProjectID(b) {
		t.Error("expected same ProjectID for equivalent paths")
	}
}

// TestCanonicalRootResolvesSymlinkedAncestor is PROJ-002's regression
// test: found live when `ragctl scan /tmp/foo` (a literal, absolute path
// through macOS's symlinked /tmp -> /private/tmp) and `ragctl serve`'s
// own startup scan (relative ".", which os.Getwd() resolves via the
// symlink-resolving getcwd() syscall) registered the SAME physical
// directory as two different projects with two different IDs.
// filepath.Abs alone never catches this — it's a no-op Clean for an
// already-absolute path — so this test creates a real symlink (not
// relying on macOS's own /tmp, to stay portable) and confirms both the
// symlinked and real spellings resolve to one canonical root and one
// ProjectID.
func TestCanonicalRootResolvesSymlinkedAncestor(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	viaSymlink, err := CanonicalRoot(link)
	if err != nil {
		t.Fatalf("CanonicalRoot(symlink path): %v", err)
	}
	viaReal, err := CanonicalRoot(real)
	if err != nil {
		t.Fatalf("CanonicalRoot(real path): %v", err)
	}
	if viaSymlink != viaReal {
		t.Errorf("canonical roots diverge for the same physical directory:\n via symlink: %q\n via real:    %q", viaSymlink, viaReal)
	}
	if ProjectID(viaSymlink) != ProjectID(viaReal) {
		t.Error("expected the same ProjectID for the symlinked and real spellings of one directory")
	}
}
