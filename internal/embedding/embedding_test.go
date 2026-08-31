package embedding

import "context"

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
