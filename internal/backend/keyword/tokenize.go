package keyword

import (
	"strings"
	"unicode"
)

// maxTokenLen drops anything longer (minified code, base64 blobs): no one
// searches for it, and it bloats the index.
const maxTokenLen = 64

// stopwords are common English words that carry no meaning in a question
// like "how do I create a new client". Only whole plain words are
// dropped; identifier parts never are.
var stopwords = map[string]bool{
	"a": true, "an": true, "and": true, "are": true, "as": true, "at": true, "be": true, "by": true,
	"can": true, "do": true, "does": true, "for": true, "from": true, "how": true, "i": true,
	"if": true, "in": true, "into": true, "is": true, "it": true, "its": true, "me": true, "my": true,
	"of": true, "on": true, "or": true, "should": true, "so": true, "that": true, "the": true,
	"their": true, "then": true, "there": true, "these": true, "this": true, "to": true, "use": true,
	"was": true, "we": true, "what": true, "when": true, "where": true, "which": true, "who": true,
	"why": true, "will": true, "with": true, "you": true, "your": true,
}

// Tokenize turns text into search terms, keeping programming identifiers
// intact as well as split up. A word is a run of letters, digits and the
// joiners . _ - /, so "pgxpool.NewWithConfig()" yields:
//
//	pgxpool.newwithconfig  (the whole identifier)
//	pgxpool, newwithconfig (split on joiners)
//	new, with, config      (camelCase parts)
//
// Everything is lowercased so matching is case-insensitive; the whole
// identifier term still keeps "pgxpool.newwithconfig" distinct from text
// that merely mentions "new", "with" and "config".
func Tokenize(text string) []string {
	var out []string
	for _, word := range strings.FieldsFunc(text, func(r rune) bool { return !isWordRune(r) }) {
		word = strings.Trim(word, "._-/")
		if word == "" {
			continue
		}
		whole := strings.ToLower(word)
		parts := strings.FieldsFunc(word, func(r rune) bool { return strings.ContainsRune("._-/", r) })
		if len(parts) == 1 && !hasInnerCase(word) {
			// A plain word: the only kind that can be a stopword.
			if !stopwords[whole] {
				out = appendToken(out, whole)
			}
			continue
		}
		out = appendToken(out, whole)
		for _, p := range parts {
			lp := strings.ToLower(p)
			if len(parts) > 1 {
				out = appendToken(out, lp)
			}
			if camel := splitCamel(p); len(camel) > 1 {
				for _, c := range camel {
					out = appendToken(out, strings.ToLower(c))
				}
			}
		}
	}
	return out
}

func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("._-/", r)
}

func appendToken(out []string, t string) []string {
	if len(t) < 2 || len(t) > maxTokenLen {
		return out
	}
	return append(out, t)
}

// hasInnerCase reports a lower-to-upper change inside s ("NewRandom").
func hasInnerCase(s string) bool {
	return len(splitCamel(s)) > 1
}

// splitCamel splits "NewWithConfig" into New, With, Config, and keeps
// acronyms together: "HTTPClient" is HTTP, Client.
func splitCamel(s string) []string {
	rs := []rune(s)
	var parts []string
	start := 0
	for i := 1; i < len(rs); i++ {
		lowerToUpper := unicode.IsLower(rs[i-1]) && unicode.IsUpper(rs[i])
		acronymEnd := unicode.IsUpper(rs[i-1]) && unicode.IsUpper(rs[i]) && i+1 < len(rs) && unicode.IsLower(rs[i+1])
		if lowerToUpper || acronymEnd {
			parts = append(parts, string(rs[start:i]))
			start = i
		}
	}
	return append(parts, string(rs[start:]))
}
