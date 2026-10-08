// Package embedding defines depctl's narrow embedding-provider contract
// and the identity metadata that keeps vectors from different
// providers/models from ever being silently mixed. Concrete providers
// (internal/embedding/ollama) and the content-hash cache
// (internal/embedding/cache) build on this package; it has no
// provider-specific code of its own.
package embedding

import (
	"context"
	"fmt"
	"hash/fnv"
	"strings"
	"time"
)

// Embedder turns chunk text into vectors. Implementations batch
// request/response only — no streaming/async API, per EMB-001's
// simplicity constraints.
type Embedder interface {
	// Name identifies the provider (e.g. "ollama").
	Name() string
	// ModelID identifies the specific embedding model in use.
	ModelID() string
	// Dimensions reports the vector length this Embedder produces.
	Dimensions(ctx context.Context) (int, error)
	// Embed returns one vector per input text, in the same order. A
	// failure fails the whole batch — callers retry the batch, there is
	// no partial-batch success in v0.1.
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// EmbeddingIdentity describes the provider/model/normalization that
// produced a set of vectors. Nothing persists it yet: a replica's model
// and dimensions are recorded on domain.BackendReplica, and the
// embedding cache keys on provider and model directly.
type EmbeddingIdentity struct {
	Provider      string
	Model         string
	Dimensions    int
	Normalization string
	CreatedAt     time.Time
}

// Prompts adapts embedding input to what a model was trained with: a
// task prompt for questions, a title/text layout for documents, and
// optionally a shorter vector (a Matryoshka model's leading values carry
// most of the meaning). The zero value embeds text as is, at full size.
type Prompts struct {
	// Query wraps a question; "{q}" marks where it goes.
	Query string
	// Document wraps a chunk; "{title}" and "{text}" mark where its title
	// (qualified symbol or file path) and content go.
	Document string
	// Dimensions keeps only the first N values of each vector; 0 keeps all.
	Dimensions int
}

// QueryText is q as the model should see it.
func (p Prompts) QueryText(q string) string {
	if p.Query == "" {
		return q
	}
	return strings.ReplaceAll(p.Query, "{q}", q)
}

// DocumentText is a chunk as the model should see it.
func (p Prompts) DocumentText(title, text string) string {
	if p.Document == "" {
		return text
	}
	if title == "" {
		title = "none"
	}
	return strings.NewReplacer("{title}", title, "{text}", text).Replace(p.Document)
}

// Identity names what produced a set of vectors: the model, plus the
// prompts and size when set, so vectors from different prompts or sizes
// are never mistaken for compatible. With neither, it is just the model,
// as before these existed.
func (p Prompts) Identity(model string) string {
	if p.Query == "" && p.Document == "" && p.Dimensions == 0 {
		return model
	}
	h := fnv.New32a()
	h.Write([]byte(p.Query + "\x00" + p.Document))
	return fmt.Sprintf("%s [%dd, prompts %08x]", model, p.Dimensions, h.Sum32())
}

// Document is one chunk to embed.
type Document struct {
	Title string
	Text  string
}

// Prompted is an Embedder used with its Prompts: questions and documents
// are prompted differently, and vectors are cut to Prompts.Dimensions.
// Its ModelID is the Prompts identity.
type Prompted struct {
	Embedder
	Prompts Prompts
}

// ModelID is the identity of the vectors this produces (see Identity).
func (p *Prompted) ModelID() string { return p.Prompts.Identity(p.Embedder.ModelID()) }

// Dimensions reports the vector length after truncation.
func (p *Prompted) Dimensions(ctx context.Context) (int, error) {
	if p.Prompts.Dimensions > 0 {
		return p.Prompts.Dimensions, nil
	}
	return p.Embedder.Dimensions(ctx)
}

// EmbedQuery embeds one search question.
func (p *Prompted) EmbedQuery(ctx context.Context, q string) ([]float32, error) {
	vs, err := p.Embedder.Embed(ctx, []string{p.Prompts.QueryText(q)})
	if err != nil {
		return nil, err
	}
	if len(vs) != 1 {
		return nil, fmt.Errorf("embedding: got %d vectors for one question", len(vs))
	}
	return p.truncate(vs[0]), nil
}

// EmbedDocuments embeds chunks, one vector per document, in order.
func (p *Prompted) EmbedDocuments(ctx context.Context, docs []Document) ([][]float32, error) {
	texts := make([]string, len(docs))
	for i, d := range docs {
		texts[i] = p.Prompts.DocumentText(d.Title, d.Text)
	}
	vs, err := p.Embedder.Embed(ctx, texts)
	if err != nil {
		return nil, err
	}
	for i := range vs {
		vs[i] = p.truncate(vs[i])
	}
	return vs, nil
}

// truncate keeps the first Prompts.Dimensions values. No renormalizing:
// every vector store depctl uses compares by cosine, which divides by
// length.
func (p *Prompted) truncate(v []float32) []float32 {
	if n := p.Prompts.Dimensions; n > 0 && n < len(v) {
		return v[:n]
	}
	return v
}
