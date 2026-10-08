package node

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/aleutian-ai/depctl/internal/domain"
	"github.com/aleutian-ai/depctl/internal/resolver"
)

// bunLockFile is the subset of bun.lock's schema (the text/JSONC format
// introduced in Bun 1.1.39, default since 1.2 — not the older binary
// bun.lockb) depctl needs. "packages" values are heterogeneous arrays
// (element 0 is always the resolved "name@version"/"name@github:..."
// specifier string; the remaining elements — a metadata object, then an
// integrity/cache-key string — vary by entry and aren't needed here),
// so each entry is decoded as []any and only its first element is read.
type bunLockFile struct {
	LockfileVersion int                     `json:"lockfileVersion"`
	Workspaces      map[string]bunWorkspace `json:"workspaces"`
	Packages        map[string][]any        `json:"packages"`
}

type bunWorkspace struct {
	Dependencies    map[string]string `json:"dependencies"`
	DevDependencies map[string]string `json:"devDependencies"`
}

// resolveBunLock parses bun.lock at root and normalizes it into a
// domain.Resolution. Unlike yarn.lock, bun.lock's own "workspaces"
// field already carries each workspace's direct dependencies (the same
// shape pnpm-lock.yaml's "importers" has) — no separate package.json
// read needed, unlike yarn.go's own resolveYarnLock.
func resolveBunLock(root string) (domain.Resolution, error) {
	path := lockPath(root, "bun.lock")

	raw, err := os.ReadFile(path)
	if err != nil {
		return domain.Resolution{}, resolutionErr(root, fmt.Errorf("read bun.lock: %w", err))
	}
	// bun.lock is JSONC (comments + trailing commas allowed) — strip that
	// down to plain JSON with a minimal hand-rolled pass before decoding
	// with encoding/json, rather than pulling in a third-party JSONC
	// dependency for this alone (matching yarn.go's own Classic-parser
	// precedent: hand-roll a small format-specific parser first).
	data := stripJSONC(raw)

	var lock bunLockFile
	if err := json.Unmarshal(data, &lock); err != nil {
		return domain.Resolution{}, resolutionErr(root, fmt.Errorf("parse bun.lock: %w", err))
	}

	direct := map[string]bool{}
	for _, ws := range lock.Workspaces {
		for name := range ws.Dependencies {
			direct[name] = true
		}
		for name := range ws.DevDependencies {
			direct[name] = true
		}
	}

	var out []domain.DependencyVersion
	for key, entry := range lock.Packages {
		if len(entry) == 0 {
			continue
		}
		specifier, ok := entry[0].(string)
		if !ok {
			continue
		}
		name, version := bunSplitSpecifier(specifier, key)
		if name == "" || version == "" {
			continue
		}
		out = append(out, domain.DependencyVersion{
			Dependency: domain.Dependency{
				Ecosystem: domain.EcosystemNode,
				Name:      name,
				Direct:    direct[name],
			},
			Version:    version,
			ResolvedBy: "bun.lock",
		})
	}

	return domain.Resolution{
		Ecosystem:    domain.EcosystemNode,
		LockPath:     path,
		Dependencies: out,
		Fingerprint:  resolver.Fingerprint(out),
	}, nil
}

// stripJSONC strips JSONC's two additions over plain JSON — line/block
// comments and trailing commas — down to parseable JSON, correctly
// skipping over string literals throughout (so a URL like
// "http://example.com" inside a string value is never mistaken for the
// start of a line comment).
func stripJSONC(data []byte) []byte {
	var out []byte
	inString := false
	escaped := false
	for i := 0; i < len(data); i++ {
		c := data[i]
		if inString {
			out = append(out, c)
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch {
		case c == '"':
			inString = true
			out = append(out, c)
		case c == '/' && i+1 < len(data) && data[i+1] == '/':
			for i < len(data) && data[i] != '\n' {
				i++
			}
			out = append(out, '\n')
		case c == '/' && i+1 < len(data) && data[i+1] == '*':
			i += 2
			for i+1 < len(data) && !(data[i] == '*' && data[i+1] == '/') {
				i++
			}
			i++ // land on the '/' the loop above stopped just before
			out = append(out, ' ')
		default:
			out = append(out, c)
		}
	}
	return stripTrailingCommas(out)
}

// stripTrailingCommas removes a comma immediately followed by only
// whitespace and a closing '}' or ']' — the other JSONC allowance
// encoding/json doesn't accept.
func stripTrailingCommas(data []byte) []byte {
	var out []byte
	inString := false
	escaped := false
	for i := 0; i < len(data); i++ {
		c := data[i]
		if inString {
			out = append(out, c)
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		if c == '"' {
			inString = true
			out = append(out, c)
			continue
		}
		if c == ',' {
			j := i + 1
			for j < len(data) && (data[j] == ' ' || data[j] == '\t' || data[j] == '\n' || data[j] == '\r') {
				j++
			}
			if j < len(data) && (data[j] == '}' || data[j] == ']') {
				continue // drop the trailing comma
			}
		}
		out = append(out, c)
	}
	return out
}

// bunSplitSpecifier splits a packages[key][0] specifier like
// "is-odd@3.0.1" or "@types/node@20.11.0" into name/version — same
// last-'@'-splits-correctly reasoning as yarn.go's descriptor helpers.
// A non-registry specifier (e.g. a github: reference, "pkg@github:
// owner/repo#commit") still splits at the same point; the "version"
// half is then that raw ref string, not a semver — an accepted, real
// limitation for non-registry dependencies (see this ticket's Non-goals
// — only the exact-version-per-registry-entry shape is in scope). key
// (the "packages" map's own key, always the plain package name) is used
// as a fallback name if the specifier itself doesn't split cleanly,
// rather than dropping the entry outright.
func bunSplitSpecifier(specifier, key string) (name, version string) {
	at := strings.LastIndex(specifier, "@")
	if at <= 0 {
		return key, ""
	}
	return specifier[:at], specifier[at+1:]
}
