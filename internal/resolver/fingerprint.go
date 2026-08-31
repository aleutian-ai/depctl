package resolver

import (
	"encoding/base32"
	"encoding/json"
	"sort"

	"github.com/zeebo/blake3"

	"aleutian-ai/ragctl/internal/domain"
)

var fingerprintEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// Fingerprint hashes a dependency list, sorted by (ecosystem, name), so
// that identical dependency sets always produce the same fingerprint
// regardless of the order the source resolver happened to emit them in.
// Shared by every ecosystem resolver rather than reimplemented per package.
func Fingerprint(deps []domain.DependencyVersion) string {
	sorted := make([]domain.DependencyVersion, len(deps))
	copy(sorted, deps)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Dependency.Ecosystem != sorted[j].Dependency.Ecosystem {
			return sorted[i].Dependency.Ecosystem < sorted[j].Dependency.Ecosystem
		}
		return sorted[i].Dependency.Name < sorted[j].Dependency.Name
	})

	// json.Marshal is a deterministic serialization for a fixed struct
	// shape — good enough as fingerprint input without a bespoke codec.
	data, err := json.Marshal(sorted)
	if err != nil {
		// Dependency lists contain only strings/bools; marshal cannot fail.
		panic(err)
	}
	sum := blake3.Sum256(data)
	return "res_" + fingerprintEncoding.EncodeToString(sum[:])
}
