package fingerprint_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/aleutian-ai/depctl/internal/data/fingerprint"
	"github.com/aleutian-ai/depctl/internal/domain"
	"github.com/aleutian-ai/depctl/internal/normalize/markdown"
)

// TestCRLFAndLFNormalizeToIdenticalFingerprint is the integration test
// HASH-001 itself calls for: Fingerprint trusts that content reaching it
// is already whitespace-normalized by the normalizer that produced it,
// so this spans NORM-002 + HASH-001 rather than testing Fingerprint in
// isolation — a source file that only differs by line-ending style must
// still fingerprint identically once markdown.Normalizer has run.
func TestCRLFAndLFNormalizeToIdenticalFingerprint(t *testing.T) {
	dir := t.TempDir()

	lfPath := filepath.Join(dir, "lf.md")
	crlfPath := filepath.Join(dir, "crlf.md")

	body := "# Title\r\n\r\nSome body text.\r\n\r\n## Section\r\n\r\nMore text.\r\n"
	if err := os.WriteFile(crlfPath, []byte(body), 0o644); err != nil {
		t.Fatalf("write crlf fixture: %v", err)
	}
	lfBody := "# Title\n\nSome body text.\n\n## Section\n\nMore text.\n"
	if err := os.WriteFile(lfPath, []byte(lfBody), 0o644); err != nil {
		t.Fatalf("write lf fixture: %v", err)
	}

	n := markdown.New()

	lfObjs, err := n.Normalize(context.Background(), domain.SourceSnapshot{LocalPath: lfPath, LogicalPath: "doc.md"})
	if err != nil {
		t.Fatalf("normalize lf fixture: %v", err)
	}
	crlfObjs, err := n.Normalize(context.Background(), domain.SourceSnapshot{LocalPath: crlfPath, LogicalPath: "doc.md"})
	if err != nil {
		t.Fatalf("normalize crlf fixture: %v", err)
	}

	lfDigest := fingerprint.Fingerprint("src1", "doc.md", n.Name(), n.Version(), lfObjs[0].Content)
	crlfDigest := fingerprint.Fingerprint("src1", "doc.md", n.Name(), n.Version(), crlfObjs[0].Content)

	if lfDigest != crlfDigest {
		t.Errorf("CRLF and LF variants of equivalent content fingerprinted differently: %x != %x", crlfDigest, lfDigest)
	}
}
