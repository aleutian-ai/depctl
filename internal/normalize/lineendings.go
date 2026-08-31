package normalize

import "bytes"

// NormalizeLineEndings converts CRLF and lone-CR line endings to LF.
// Every content-type normalizer calls this before setting
// KnowledgeObject.Content, since HASH-001's fingerprint function trusts
// that normalized content is already whitespace-normalized rather than
// re-normalizing it itself — this is what makes that trust assumption
// actually hold.
func NormalizeLineEndings(b []byte) []byte {
	b = bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))
	b = bytes.ReplaceAll(b, []byte("\r"), []byte("\n"))
	return b
}
