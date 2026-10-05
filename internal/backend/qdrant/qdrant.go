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
	"time"

	"github.com/zeebo/blake3"

	"aleutian-ai/ragctl/internal/backend"
)

// defaultBatchSize bounds how many points go into one upsert request.
const defaultBatchSize = 256

// defaultHTTPTimeout bounds one request to Qdrant. http.DefaultClient has
// no timeout at all — an unreachable/stopped Qdrant (a killed container,
// a down podman VM) would otherwise hang a request forever, which, run
// inside the daemon's scheduler, can keep the whole daemon process alive
// indefinitely even after Shutdown is requested (see internal/daemon's
// Scheduler.execute).
const defaultHTTPTimeout = 60 * time.Second

// Client implements backend.VectorBackend against a Qdrant HTTP
// endpoint. One collection per ragctl install (Namespace.Name), filtered
// by metadata for ecosystem/package/version/generation — never one
// collection per dependency version.
type Client struct {
	endpoint   string
	httpClient *http.Client
	batchSize  int
	apiKey     string
}

// Option configures a Client.
type Option func(*Client)

// WithBatchSize overrides how many points go into one upsert request.
func WithBatchSize(n int) Option {
	return func(c *Client) { c.batchSize = n }
}

// WithAPIKey authenticates every request with Qdrant's api-key header,
// for a server started with an API key (QDRANT__SERVICE__API_KEY).
func WithAPIKey(key string) Option {
	return func(c *Client) { c.apiKey = key }
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
		httpClient: &http.Client{Timeout: defaultHTTPTimeout},
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
// collection name (e.g. `ragctl doctor`). With an API key it checks
// /collections instead: Qdrant serves /healthz without auth, so a wrong
// key would otherwise look healthy until the first sync failed.
func (c *Client) Health(ctx context.Context) error {
	path := "/healthz"
	if c.apiKey != "" {
		path = "/collections"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint+path, nil)
	if err != nil {
		return &Error{Op: "Health", Kind: ErrBackendRequest, Cause: err}
	}
	c.authorize(req)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return &Error{Op: "Health", Kind: ErrBackendUnavailable, Cause: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return &Error{Op: "Health", Kind: ErrBackendRequest, Cause: fmt.Errorf("HTTP %d: the server wants an API key, or rejected the one from vector.api_key_env", resp.StatusCode)}
	}
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
	c.authorize(req)
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
	c.authorize(req)
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
			ID:      pointID(p.Metadata.Generation, p.ID),
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
//
// IDs and Filter are sent as two separate requests, never combined into
// one selector body: Qdrant's points_delete API takes a "one of
// {points, filter}" selector, and empirically (verified against a real
// v1.13.1 server, not just its docs) sending both fields in one request
// body silently deletes only by ID and drops the filter entirely — a
// real bug caught by an independent adversarial review of this package,
// not by the original unit tests, which only asserted on the outgoing
// request's JSON shape and never checked against a live server what
// that shape actually does.
func (c *Client) Delete(ctx context.Context, req backend.DeleteRequest) error {
	// req.IDs are ragctl chunk IDs. A point's own ID also depends on its
	// generation now (see pointID), which an ID-only delete doesn't know,
	// so each chunk is deleted by its "_id" payload instead — removing it
	// from every generation that holds it, same as when one chunk was
	// one point.
	for _, id := range req.IDs {
		body, err := json.Marshal(pointsSelector{Filter: &qdrantFilter{Must: []matchCondition{{Key: "_id", Match: matchValue{Value: id}}}}})
		if err != nil {
			return &Error{Op: "Delete", Kind: ErrBackendRequest, Cause: err}
		}
		if err := c.do(ctx, http.MethodPost, "/collections/"+req.Namespace+"/points/delete", body, "Delete", nil); err != nil {
			return err
		}
	}
	if req.Filter != nil {
		body, err := json.Marshal(pointsSelector{Filter: filterFrom(req.Filter)})
		if err != nil {
			return &Error{Op: "Delete", Kind: ErrBackendRequest, Cause: err}
		}
		if err := c.do(ctx, http.MethodPost, "/collections/"+req.Namespace+"/points/delete", body, "Delete", nil); err != nil {
			return err
		}
	}
	return nil
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

// Count reports exactly how many points in namespace match filter, via
// Qdrant's dedicated `points/count` endpoint (`exact: true` — an
// approximate count would defeat the point: POINT-003 needs to tell
// "zero points" apart from "a few points," not a ballpark).
func (c *Client) Count(ctx context.Context, namespace string, filter *backend.Filter) (int, error) {
	cr := countRequest{Exact: true}
	if filter != nil {
		cr.Filter = filterFrom(filter)
	}
	body, err := json.Marshal(cr)
	if err != nil {
		return 0, &Error{Op: "Count", Kind: ErrBackendRequest, Cause: err}
	}
	var parsed countResponse
	if err := c.do(ctx, http.MethodPost, "/collections/"+namespace+"/points/count", body, "Count", &parsed); err != nil {
		return 0, err
	}
	return parsed.Result.Count, nil
}

// do performs one HTTP round-trip and decodes a JSON response into out
// (if non-nil), classifying failures per Error's Kind.
func (c *Client) do(ctx context.Context, method, path string, body []byte, op string, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.endpoint+path, bytes.NewReader(body))
	if err != nil {
		return &Error{Op: op, Kind: ErrBackendRequest, Cause: err}
	}
	c.authorize(req)
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
// hex string) from a generation ID and a ragctl chunk ID — Qdrant only
// accepts unsigned integers or UUIDs as point IDs, and ragctl's chunk IDs
// ("chk_...") are neither. The generation is part of the key because
// chunk IDs are content-derived: two generations holding identical
// content (sibling modules of one monorepo, or a file unchanged between
// two versions) would otherwise share one point, and the later upsert
// would overwrite the earlier generation's payload, leaving it with no
// searchable points.
func pointID(generationID, chunkID string) string {
	sum := blake3.Sum256([]byte(generationID + "\x00" + chunkID))
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

// authorize sets the api-key header when the client has a key.
func (c *Client) authorize(req *http.Request) {
	if c.apiKey != "" {
		req.Header.Set("api-key", c.apiKey)
	}
}
