// Package qdrant implements backend.VectorBackend against Qdrant's HTTP
// API (VEC-002) — ragctl's reference vector backend. A small
// hand-written client (net/http + encoding/json), not a generated SDK,
// per the plan's explicit preference for keeping dependency weight low.
package qdrant

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/zeebo/blake3"

	"aleutian-ai/ragctl/internal/backend"
)

// defaultBatchSize bounds how many points go into one upsert request.
const defaultBatchSize = 256

// Client implements backend.VectorBackend against a Qdrant HTTP
// endpoint. One collection per ragctl install (Namespace.Name), filtered
// by metadata for ecosystem/package/version/generation — never one
// collection per dependency version.
type Client struct {
	endpoint   string
	httpClient *http.Client
	batchSize  int
}

// Option configures a Client.
type Option func(*Client)

// WithBatchSize overrides how many points go into one upsert request.
func WithBatchSize(n int) Option {
	return func(c *Client) { c.batchSize = n }
}

// WithHTTPClient overrides the underlying *http.Client (e.g. for tests
// pointing at an httptest.Server).
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) { c.httpClient = h }
}

// New returns a Client targeting endpoint (e.g. "http://127.0.0.1:6333").
func New(endpoint string, opts ...Option) *Client {
	c := &Client{
		endpoint:   endpoint,
		httpClient: http.DefaultClient,
		batchSize:  defaultBatchSize,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Name identifies this backend.
func (c *Client) Name() string { return "qdrant" }

// Capabilities reports what Qdrant supports: vector search and metadata
// filtering (including delete-by-filter), no built-in keyword/hybrid
// search.
func (c *Client) Capabilities(ctx context.Context) (backend.Capabilities, error) {
	return backend.Capabilities{
		VectorSearch:   true,
		MetadataFilter: true,
		DeleteByFilter: true,
	}, nil
}

// Health checks that the Qdrant server itself is reachable, via its
// /healthz endpoint — the interface's Health(ctx) takes no namespace, so
// it can't also confirm a specific collection exists. HealthCollection
// below covers that more specific check for callers that know their
// collection name (e.g. `ragctl doctor`).
func (c *Client) Health(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint+"/healthz", nil)
	if err != nil {
		return &Error{Op: "Health", Kind: ErrBackendRequest, Cause: err}
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return &Error{Op: "Health", Kind: ErrBackendUnavailable, Cause: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return &Error{Op: "Health", Kind: classify(resp.StatusCode), Cause: fmt.Errorf("HTTP %d", resp.StatusCode)}
	}
	return nil
}

// HealthCollection checks that collection exists and is reachable — a
// more specific signal than Health for callers that already know their
// collection name.
func (c *Client) HealthCollection(ctx context.Context, collection string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint+"/collections/"+collection, nil)
	if err != nil {
		return &Error{Op: "Health", Kind: ErrBackendRequest, Cause: err}
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return &Error{Op: "Health", Kind: ErrBackendUnavailable, Cause: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return &Error{Op: "Health", Kind: classify(resp.StatusCode), Cause: fmt.Errorf("HTTP %d", resp.StatusCode)}
	}
	return nil
}

// EnsureNamespace creates ns's collection if it doesn't already exist.
// Idempotent — calling it against an existing collection is a no-op.
func (c *Client) EnsureNamespace(ctx context.Context, ns backend.Namespace) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint+"/collections/"+ns.Name, nil)
	if err != nil {
		return &Error{Op: "EnsureNamespace", Kind: ErrBackendRequest, Cause: err}
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return &Error{Op: "EnsureNamespace", Kind: ErrBackendUnavailable, Cause: err}
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return nil
	}

	distance := ns.Distance
	if distance == "" {
		distance = "Cosine"
	}
	body, err := json.Marshal(createCollectionRequest{
		Vectors: vectorParams{Size: ns.Dimensions, Distance: capitalize(distance)},
	})
	if err != nil {
		return &Error{Op: "EnsureNamespace", Kind: ErrBackendRequest, Cause: err}
	}
	return c.do(ctx, http.MethodPut, "/collections/"+ns.Name, body, "EnsureNamespace", nil)
}

// Upsert writes req.Points in batches of c.batchSize.
func (c *Client) Upsert(ctx context.Context, req backend.UpsertRequest) error {
	for start := 0; start < len(req.Points); start += c.batchSize {
		end := min(start+c.batchSize, len(req.Points))
		if err := c.upsertBatch(ctx, req.Namespace, req.Points[start:end]); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) upsertBatch(ctx context.Context, collection string, points []backend.Point) error {
	qPoints := make([]qdrantPoint, len(points))
	for i, p := range points {
		qPoints[i] = qdrantPoint{
			ID:      pointID(p.ID),
			Vector:  p.Vector,
			Payload: payloadFrom(p.Metadata, p.ID),
		}
	}
	body, err := json.Marshal(upsertRequest{Points: qPoints})
	if err != nil {
		return &Error{Op: "Upsert", Kind: ErrBackendRequest, Cause: err}
	}
	return c.do(ctx, http.MethodPut, "/collections/"+collection+"/points", body, "Upsert", nil)
}

// Delete removes points by ID, by Filter, or both.
func (c *Client) Delete(ctx context.Context, req backend.DeleteRequest) error {
	sel := pointsSelector{}
	if len(req.IDs) > 0 {
		sel.Points = make([]string, len(req.IDs))
		for i, id := range req.IDs {
			sel.Points[i] = pointID(id)
		}
	}
	if req.Filter != nil {
		sel.Filter = filterFrom(req.Filter)
	}
	body, err := json.Marshal(sel)
	if err != nil {
		return &Error{Op: "Delete", Kind: ErrBackendRequest, Cause: err}
	}
	return c.do(ctx, http.MethodPost, "/collections/"+req.Namespace+"/points/delete", body, "Delete", nil)
}

// Query returns the TopK nearest points to req.Vector, constrained by
// req.Filter.
func (c *Client) Query(ctx context.Context, req backend.QueryRequest) (backend.QueryResult, error) {
	sr := searchRequest{
		Vector:      req.Vector,
		Limit:       req.TopK,
		WithPayload: true,
	}
	if req.Filter != nil {
		sr.Filter = filterFrom(req.Filter)
	}
	body, err := json.Marshal(sr)
	if err != nil {
		return backend.QueryResult{}, &Error{Op: "Query", Kind: ErrBackendRequest, Cause: err}
	}

	var parsed searchResponse
	if err := c.do(ctx, http.MethodPost, "/collections/"+req.Namespace+"/points/search", body, "Query", &parsed); err != nil {
		return backend.QueryResult{}, err
	}

	result := backend.QueryResult{Points: make([]backend.ScoredPoint, len(parsed.Result))}
	for i, r := range parsed.Result {
		result.Points[i] = backend.ScoredPoint{
			ID:       originalID(r.Payload),
			Score:    r.Score,
			Metadata: metadataFrom(r.Payload),
		}
	}
	return result, nil
}

// do performs one HTTP round-trip and decodes a JSON response into out
// (if non-nil), classifying failures per Error's Kind.
func (c *Client) do(ctx context.Context, method, path string, body []byte, op string, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.endpoint+path, bytes.NewReader(body))
	if err != nil {
		return &Error{Op: op, Kind: ErrBackendRequest, Cause: err}
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return &Error{Op: op, Kind: ErrBackendUnavailable, Cause: err}
	}
	defer resp.Body.Close()

	respBody, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return &Error{Op: op, Kind: ErrBackendUnavailable, Cause: readErr}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &Error{Op: op, Kind: classify(resp.StatusCode), Cause: fmt.Errorf("HTTP %d: %s", resp.StatusCode, bytes.TrimSpace(respBody))}
	}
	if out != nil {
		if err := json.Unmarshal(respBody, out); err != nil {
			return &Error{Op: op, Kind: ErrBackendRequest, Cause: fmt.Errorf("decode response: %w", err)}
		}
	}
	return nil
}

// pointID derives a deterministic Qdrant-valid point ID (a UUID-shaped
// hex string) from a ragctl chunk ID — Qdrant only accepts unsigned
// integers or UUIDs as point IDs, and ragctl's chunk IDs ("chk_...") are
// neither.
func pointID(chunkID string) string {
	sum := blake3.Sum256([]byte(chunkID))
	b := sum[:16]
	b[6] = (b[6] & 0x0f) | 0x50 // version 5 (name-based)
	b[8] = (b[8] & 0x3f) | 0x80 // RFC 4122 variant
	hexStr := hex.EncodeToString(b)
	return fmt.Sprintf("%s-%s-%s-%s-%s", hexStr[0:8], hexStr[8:12], hexStr[12:16], hexStr[16:20], hexStr[20:32])
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
