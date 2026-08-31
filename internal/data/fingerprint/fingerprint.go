// Package fingerprint provides ragctl's single deterministic
// content-identity primitive: a BLAKE3-based Fingerprint over a
// KnowledgeObject's identifying inputs, and the ObjectID string derived
// from it. Every other subsystem that needs to detect "has this content
// actually changed" or "what's this object's storage key" goes through
// this package rather than hashing ad hoc.
package fingerprint

import (
	"encoding/base32"
	"encoding/binary"
	"hash"
	"strings"

	"github.com/zeebo/blake3"
)

// objectIDEncoding is lowercase, unpadded base32 — chosen for
// human-inspectable, storage-key-safe IDs. This is deliberately
// different from internal/resolver.Fingerprint's uppercase convention:
// that one is an internal no-op-detection signal, never surfaced as a
// stored key or shown to a user, so case never mattered there.
var objectIDEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// Fingerprint computes a deterministic BLAKE3 digest over a
// KnowledgeObject's content-identity inputs — source identity, logical
// path, normalizer name, normalizer version, and normalized content —
// hashed in that fixed order with each field length-prefixed so that
// e.g. "ab"+"c" and "a"+"bc" can never collide into the same digest.
//
// content is expected to already be whitespace-normalized by whichever
// Normalizer produced it (consistent line endings, trailing whitespace
// trimmed) before it reaches this function — Fingerprint does not
// re-normalize. Whitespace policy is owned by normalizers, not here, so
// this function stays a pure hash over its inputs.
//
// Bumping a normalizer's Version() is how a parsing-logic change signals
// "re-normalize and re-fingerprint this" even when the underlying source
// bytes haven't changed — normalizerVersion is part of the hash input
// specifically so that happens automatically.
func Fingerprint(sourceIdentity, logicalPath, normalizerName, normalizerVersion string, content []byte) [32]byte {
	h := blake3.New()
	writeLengthPrefixed(h, []byte(sourceIdentity))
	writeLengthPrefixed(h, []byte(logicalPath))
	writeLengthPrefixed(h, []byte(normalizerName))
	writeLengthPrefixed(h, []byte(normalizerVersion))
	writeLengthPrefixed(h, content)

	var digest [32]byte
	copy(digest[:], h.Sum(nil))
	return digest
}

// ObjectID derives a deterministic, human-inspectable KnowledgeObject ID
// from a Fingerprint digest: "ko_" followed by lowercase, unpadded
// base32. Used as the Badger key suffix for obj/<object-id>.
func ObjectID(digest [32]byte) string {
	return "ko_" + strings.ToLower(objectIDEncoding.EncodeToString(digest[:]))
}

// writeLengthPrefixed writes a uvarint length followed by b, so
// concatenating variable-length fields can never produce an ambiguous
// hash input (e.g. "ab"+"c" vs "a"+"bc").
func writeLengthPrefixed(h hash.Hash, b []byte) {
	var lenBuf [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(lenBuf[:], uint64(len(b)))
	h.Write(lenBuf[:n])
	h.Write(b)
}
