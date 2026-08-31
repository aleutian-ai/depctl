package project

import "testing"

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
