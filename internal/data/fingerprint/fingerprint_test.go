package fingerprint

import (
	"regexp"
	"testing"
)

func TestFingerprintDeterministic(t *testing.T) {
	a := Fingerprint("src1", "README.md", "markdown-normalizer", "v1", []byte("hello"))
	b := Fingerprint("src1", "README.md", "markdown-normalizer", "v1", []byte("hello"))
	if a != b {
		t.Errorf("Fingerprint is not deterministic: %x != %x", a, b)
	}
}

func TestFingerprintDiffersByNormalizerVersion(t *testing.T) {
	a := Fingerprint("src1", "README.md", "markdown-normalizer", "v1", []byte("hello"))
	b := Fingerprint("src1", "README.md", "markdown-normalizer", "v2", []byte("hello"))
	if a == b {
		t.Error("Fingerprint should differ when normalizer version differs, all else equal")
	}
}

func TestFingerprintDiffersByLogicalPathNotByConcatenation(t *testing.T) {
	// Without length-prefixing, "ab"+"c" and "a"+"bc" would collide when
	// naively concatenated. Exercise the same failure shape across the
	// sourceIdentity/logicalPath boundary.
	a := Fingerprint("ab", "c", "n", "v1", []byte("x"))
	b := Fingerprint("a", "bc", "n", "v1", []byte("x"))
	if a == b {
		t.Error("Fingerprint collided across a sourceIdentity/logicalPath boundary shift — length-prefixing is broken")
	}
}

func TestFingerprintDiffersByLogicalPathSameContent(t *testing.T) {
	a := Fingerprint("src1", "docs/a.md", "markdown-normalizer", "v1", []byte("same content"))
	b := Fingerprint("src1", "docs/b.md", "markdown-normalizer", "v1", []byte("same content"))
	if a == b {
		t.Error("Fingerprint should differ for different logical paths, even with identical content")
	}
}

func TestFingerprintNilContentTreatedAsEmpty(t *testing.T) {
	a := Fingerprint("src1", "p", "n", "v1", nil)
	b := Fingerprint("src1", "p", "n", "v1", []byte{})
	if a != b {
		t.Error("nil content should hash identically to empty content")
	}
}

func TestObjectIDDeterministicAndFormatted(t *testing.T) {
	digest := Fingerprint("src1", "README.md", "markdown-normalizer", "v1", []byte("hello"))
	a := ObjectID(digest)
	b := ObjectID(digest)
	if a != b {
		t.Errorf("ObjectID is not deterministic: %s != %s", a, b)
	}

	if !regexp.MustCompile(`^ko_[a-z2-7]+$`).MatchString(a) {
		t.Errorf("ObjectID %q does not match ^ko_[a-z2-7]+$", a)
	}
}

func TestObjectIDNoCollisionsAcrossManyDigests(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 10000; i++ {
		content := []byte{byte(i), byte(i >> 8)}
		digest := Fingerprint("src", "path", "n", "v1", content)
		id := ObjectID(digest)
		if seen[id] {
			t.Fatalf("collision at i=%d: %s", i, id)
		}
		seen[id] = true
	}
}
