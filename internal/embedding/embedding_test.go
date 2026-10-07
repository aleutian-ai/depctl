package embedding

import (
	"context"
	"strings"
	"testing"
)

// fakeEmbedder is a minimal in-memory Embedder — proves the interface is
// implementable with no provider-specific dependency, and gives
// downstream packages (cache, and later VEC-*/VAL-*) a compile-time
// reference for what a test double looks like.
type fakeEmbedder struct {
	dims int
}

func (f *fakeEmbedder) Name() string    { return "fake" }
func (f *fakeEmbedder) ModelID() string { return "fake-model" }
func (f *fakeEmbedder) Dimensions(ctx context.Context) (int, error) {
	return f.dims, nil
}
func (f *fakeEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = make([]float32, f.dims)
	}
	return out, nil
}

var _ Embedder = (*fakeEmbedder)(nil)

// recordingEmbedder returns 4-value vectors and remembers what it saw.
type recordingEmbedder struct{ seen []string }

func (r *recordingEmbedder) Name() string                                { return "rec" }
func (r *recordingEmbedder) ModelID() string                             { return "model-x" }
func (r *recordingEmbedder) Dimensions(ctx context.Context) (int, error) { return 4, nil }
func (r *recordingEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	r.seen = append(r.seen, texts...)
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = []float32{1, 2, 3, 4}
	}
	return out, nil
}

func TestPromptedPromptsQueriesAndDocumentsDifferently(t *testing.T) {
	rec := &recordingEmbedder{}
	p := &Prompted{Embedder: rec, Prompts: Prompts{Query: "task: code retrieval | query: {q}", Document: "title: {title} | text: {text}", Dimensions: 2}}
	ctx := context.Background()

	q, err := p.EmbedQuery(ctx, "open a pool")
	if err != nil || len(q) != 2 {
		t.Fatalf("EmbedQuery = %v, %v; want a 2-value vector", q, err)
	}
	docs, err := p.EmbedDocuments(ctx, []Document{{Title: "pgxpool.New", Text: "New creates a pool."}, {Text: "A README section."}})
	if err != nil || len(docs) != 2 || len(docs[0]) != 2 {
		t.Fatalf("EmbedDocuments = %v, %v; want two 2-value vectors", docs, err)
	}
	want := []string{
		"task: code retrieval | query: open a pool",
		"title: pgxpool.New | text: New creates a pool.",
		"title: none | text: A README section.",
	}
	if strings.Join(rec.seen, "\n") != strings.Join(want, "\n") {
		t.Errorf("model saw %q, want %q", rec.seen, want)
	}
	if d, _ := p.Dimensions(ctx); d != 2 {
		t.Errorf("Dimensions = %d, want the truncated 2", d)
	}
}

// A config without prompts or a size keeps exactly the old behavior,
// including the identity recorded with its vectors, so existing installs
// don't suddenly look mismatched.
func TestPromptsZeroValueIsTheOldBehavior(t *testing.T) {
	var p Prompts
	if p.QueryText("q") != "q" || p.DocumentText("t", "x") != "x" || p.Identity("nomic-embed-text-v2-moe") != "nomic-embed-text-v2-moe" {
		t.Errorf("zero Prompts changed the text or identity: %q %q %q", p.QueryText("q"), p.DocumentText("t", "x"), p.Identity("nomic-embed-text-v2-moe"))
	}
	withDims := Prompts{Dimensions: 256}
	withPrompts := Prompts{Query: "a {q}"}
	if withDims.Identity("m") == "m" || withPrompts.Identity("m") == "m" || withDims.Identity("m") == withPrompts.Identity("m") {
		t.Error("prompts or a size must change the identity, and differently")
	}
}
