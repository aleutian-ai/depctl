package keyword

import (
	"slices"
	"testing"
)

func TestTokenizeKeepsIdentifiersWholeAndSplit(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []string
	}{
		{"pgxpool.NewWithConfig()", []string{"pgxpool.newwithconfig", "pgxpool", "newwithconfig", "new", "with", "config"}},
		{"NewRandom", []string{"newrandom", "new", "random"}},
		{"http.Client", []string{"http.client", "http", "client"}},
		{"Client.Do", []string{"client.do", "client", "do"}},
		{"snake_case", []string{"snake_case", "snake", "case"}},
		{"kebab-case", []string{"kebab-case", "kebab", "case"}},
		{"github.com/jackc/pgx/v5", []string{"github.com/jackc/pgx/v5", "github", "com", "jackc", "pgx", "v5"}},
		{"HTTPClient", []string{"httpclient", "http", "client"}},
		{"How do I generate a new random UUID?", []string{"generate", "new", "random", "uuid"}},
	} {
		if got := Tokenize(tc.in); !slices.Equal(got, tc.want) {
			t.Errorf("Tokenize(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// "do" is a stopword as a plain word, but never as part of an identifier.
func TestTokenizeNeverDropsIdentifierParts(t *testing.T) {
	if got := Tokenize("Client.Do"); !slices.Contains(got, "do") {
		t.Errorf("Tokenize(Client.Do) = %q, want it to keep \"do\"", got)
	}
	if got := Tokenize("how do you"); len(got) != 0 {
		t.Errorf("Tokenize(\"how do you\") = %q, want nothing (all stopwords)", got)
	}
}
