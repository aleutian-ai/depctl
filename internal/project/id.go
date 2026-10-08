package project

import (
	"encoding/base32"
	"strings"

	"github.com/zeebo/blake3"
)

// idEncoding is unpadded, lowercase base32 — compact and safe to use
// directly in bbolt keys or filenames.
var idEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// ProjectID deterministically derives a project ID from its canonical
// absolute root path: "proj_" + base32(BLAKE3(canonicalRoot)).
//
// v0.1 policy: moving or renaming a project directory produces a new
// project ID on the next scan. The old project's dependency references and
// retention reasons are not automatically transferred — there is no
// "depctl project move" command in v0.1. A moved project is simply
// registered as a new project the next time it's scanned.
func ProjectID(canonicalRoot string) string {
	sum := blake3.Sum256([]byte(canonicalRoot))
	return "proj_" + strings.ToLower(idEncoding.EncodeToString(sum[:]))
}

// CanonicalRoot normalizes root the same way Scan does, for callers (like
// `depctl scan`) computing a ProjectID outside of a scan.
func CanonicalRoot(root string) (string, error) {
	return canonicalize(root)
}
