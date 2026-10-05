// Package weaviate implements backend.VectorBackend against Weaviate's
// HTTP API (VEC-011): REST for schema, writes and deletes, GraphQL for
// search and counts (Weaviate has no REST search endpoint). Each
// namespace is its own Weaviate collection ("class"), so ragctl never
// shares one with anything else on the same server.
package weaviate

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/zeebo/blake3"

	"aleutian-ai/ragctl/internal/backend"
)

const (
	defaultTopK = 10
	// batchSize bounds objects per batch write and IDs per delete request.
	batchSize   = 200
	httpTimeout = 60 * time.Second
)

// classNameRE is Weaviate's rule for collection names.
var classNameRE = regexp.MustCompile(`^[A-Z][_0-9A-Za-z]*$`)

// properties are the metadata fields stored on every object. Text fields
// use "field" tokenization so Equal is an exact match on the whole value
// ("v1" must not match "v1.5"), which word tokenization would break.
var properties = []map[string]any{
	textProperty("chunk_id"),
	textProperty("ecosystem"),
	textProperty("dependency"),
	textProperty("version"),
	textProperty("generation"),
	textProperty("source_type"),
	{"name": "authority", "dataType": []string{"int"}},
}

// Client is a VectorBackend backed by one Weaviate server.
type Client struct {
	endpoint   string
	apiKey     string
	httpClient *http.Client
}

// New returns a Client for the Weaviate server at endpoint (e.g.
// http://localhost:8080). apiKey, when non-empty, is sent as a bearer
// token, for a server with API-key authentication enabled.
func New(endpoint, apiKey string) *Client {
	return &Client{
		endpoint:   strings.TrimRight(endpoint, "/"),
		apiKey:     apiKey,
		httpClient: &http.Client{Timeout: httpTimeout},
	}
}

// Name identifies this backend.
func (c *Client) Name() string { return "weaviate" }

// Capabilities reports vector search, metadata filtering, and
// delete-by-filter. No keyword or hybrid search: ragctl stores no text
// in Weaviate for BM25 to search.
func (c *Client) Capabilities(ctx context.Context) (backend.Capabilities, error) {
	return backend.Capabilities{VectorSearch: true, MetadataFilter: true, DeleteByFilter: true}, nil
}

// Health checks the server is ready. With an API key it reads /v1/meta
// instead, because the readiness endpoint is unauthenticated and would
// report a wrong key as healthy.
func (c *Client) Health(ctx context.Context) error {
	path := "/v1/.well-known/ready"
	if c.apiKey != "" {
		path = "/v1/meta"
	}
	return c.do(ctx, http.MethodGet, path, nil, nil)
}

// EnsureNamespace creates ns's collection if it doesn't exist. Idempotent.
// Weaviate fixes the vector dimension at the first write, not here.
func (c *Client) EnsureNamespace(ctx context.Context, ns backend.Namespace) error {
	if ns.Distance != "" && !strings.EqualFold(ns.Distance, "cosine") {
		return fmt.Errorf("weaviate: unsupported distance %q (only cosine)", ns.Distance)
	}
	class, err := className(ns.Name)
	if err != nil {
		return err
	}
	exists, err := c.classExists(ctx, class)
	if err != nil || exists {
		return err
	}
	body := map[string]any{
		"class":             class,
		"description":       "ragctl dependency docs index; managed by ragctl, do not edit",
		"vectorizer":        "none",
		"vectorIndexConfig": map[string]any{"distance": "cosine"},
		"properties":        properties,
	}
	err = c.do(ctx, http.MethodPost, "/v1/schema", body, nil)
	if err != nil && strings.Contains(err.Error(), "already exists") {
		return nil // created concurrently
	}
	return err
}

// Upsert writes or overwrites req.Points. An object's Weaviate ID comes
// from (generation, chunk ID), so the same chunk in two generations is
// two objects. Weaviate batches aren't transactional: on error, earlier
// objects in the request may already be written (a rebuild rewrites them).
func (c *Client) Upsert(ctx context.Context, req backend.UpsertRequest) error {
	class, err := className(req.Namespace)
	if err != nil {
		return err
	}
	for start := 0; start < len(req.Points); start += batchSize {
		end := min(start+batchSize, len(req.Points))
		objects := make([]map[string]any, 0, end-start)
		for _, p := range req.Points[start:end] {
			m := p.Metadata
			objects = append(objects, map[string]any{
				"class":  class,
				"id":     objectID(m.Generation, p.ID),
				"vector": p.Vector,
				"properties": map[string]any{
					"chunk_id": p.ID, "ecosystem": m.Ecosystem, "dependency": m.Dependency, "version": m.Version,
					"generation": m.Generation, "source_type": m.SourceType, "authority": m.Authority,
				},
			})
		}
		// The batch endpoint answers 200 even when objects fail; each
		// object carries its own errors.
		var results []struct {
			ID     string `json:"id"`
			Result struct {
				Errors *struct {
					Error []struct {
						Message string `json:"message"`
					} `json:"error"`
				} `json:"errors"`
			} `json:"result"`
		}
		if err := c.do(ctx, http.MethodPost, "/v1/batch/objects", map[string]any{"objects": objects}, &results); err != nil {
			return err
		}
		for _, r := range results {
			if r.Result.Errors != nil && len(r.Result.Errors.Error) > 0 {
				return fmt.Errorf("weaviate: upsert object %s: %s", r.ID, r.Result.Errors.Error[0].Message)
			}
		}
	}
	return nil
}

// Delete removes points matching req.IDs (in every generation) or
// req.Filter (a union), as separate requests.
func (c *Client) Delete(ctx context.Context, req backend.DeleteRequest) error {
	class, err := className(req.Namespace)
	if err != nil {
		return err
	}
	for start := 0; start < len(req.IDs); start += batchSize {
		end := min(start+batchSize, len(req.IDs))
		where := map[string]any{"path": []string{"chunk_id"}, "operator": "ContainsAny", "valueTextArray": req.IDs[start:end]}
		if err := c.deleteWhere(ctx, class, where); err != nil {
			return err
		}
	}
	if where := whereFilter(req.Filter); where != nil {
		return c.deleteWhere(ctx, class, where)
	}
	return nil
}

// Query returns the TopK points nearest req.Vector, constrained by
// req.Filter. Score is cosine similarity (1 - Weaviate's cosine
// distance), so higher is better, matching the other backends.
func (c *Client) Query(ctx context.Context, req backend.QueryRequest) (backend.QueryResult, error) {
	class, err := className(req.Namespace)
	if err != nil {
		return backend.QueryResult{}, err
	}
	topK := req.TopK
	if topK <= 0 {
		topK = defaultTopK
	}
	vec, _ := json.Marshal(req.Vector)
	args := fmt.Sprintf("nearVector: {vector: %s}, limit: %d", vec, topK)
	if where := whereFilter(req.Filter); where != nil {
		args += ", where: " + graphQLValue(where)
	}
	q := fmt.Sprintf(`{ Get { %s(%s) { chunk_id ecosystem dependency version generation source_type authority _additional { distance } } } }`, class, args)

	var data struct {
		Get map[string][]struct {
			ChunkID    string `json:"chunk_id"`
			Ecosystem  string `json:"ecosystem"`
			Dependency string `json:"dependency"`
			Version    string `json:"version"`
			Generation string `json:"generation"`
			SourceType string `json:"source_type"`
			Authority  int    `json:"authority"`
			Additional struct {
				Distance float32 `json:"distance"`
			} `json:"_additional"`
		} `json:"Get"`
	}
	if err := c.graphQL(ctx, q, &data); err != nil {
		return backend.QueryResult{}, err
	}
	var res backend.QueryResult
	for _, o := range data.Get[class] {
		res.Points = append(res.Points, backend.ScoredPoint{
			ID:    o.ChunkID,
			Score: 1 - o.Additional.Distance,
			Metadata: backend.PointMetadata{
				Ecosystem: o.Ecosystem, Dependency: o.Dependency, Version: o.Version,
				Generation: o.Generation, SourceType: o.SourceType, Authority: o.Authority,
			},
		})
	}
	return res, nil
}

// Count reports exactly how many points in namespace match filter.
func (c *Client) Count(ctx context.Context, namespace string, filter *backend.Filter) (int, error) {
	class, err := className(namespace)
	if err != nil {
		return 0, err
	}
	target := class
	if where := whereFilter(filter); where != nil {
		target += "(where: " + graphQLValue(where) + ")"
	}
	var data struct {
		Aggregate map[string][]struct {
			Meta struct {
				Count int `json:"count"`
			} `json:"meta"`
		} `json:"Aggregate"`
	}
	if err := c.graphQL(ctx, fmt.Sprintf(`{ Aggregate { %s { meta { count } } } }`, target), &data); err != nil {
		return 0, err
	}
	rows := data.Aggregate[class]
	if len(rows) == 0 {
		return 0, nil
	}
	return rows[0].Meta.Count, nil
}

func (c *Client) classExists(ctx context.Context, class string) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint+"/v1/schema/"+class, nil)
	if err != nil {
		return false, err
	}
	c.authorize(req)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("weaviate: unreachable: %w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	default:
		return false, statusError(resp)
	}
}

// deleteWhere deletes every object in class matching where. Weaviate caps
// one batch delete at its query limit (10,000 by default), so it repeats
// until a pass deletes fewer than the cap.
func (c *Client) deleteWhere(ctx context.Context, class string, where map[string]any) error {
	body := map[string]any{"match": map[string]any{"class": class, "where": where}, "output": "minimal"}
	for {
		var out struct {
			Results struct {
				Matches    int `json:"matches"`
				Limit      int `json:"limit"`
				Successful int `json:"successful"`
				Failed     int `json:"failed"`
			} `json:"results"`
		}
		if err := c.do(ctx, http.MethodDelete, "/v1/batch/objects", body, &out); err != nil {
			return err
		}
		r := out.Results
		if r.Failed > 0 {
			return fmt.Errorf("weaviate: delete from %s: %d of %d objects failed", class, r.Failed, r.Matches)
		}
		if r.Limit == 0 || r.Matches < r.Limit || r.Successful == 0 {
			return nil
		}
	}
}

// graphQL runs query and decodes its "data" into out. GraphQL reports
// errors in the body with HTTP 200, so those are checked here.
func (c *Client) graphQL(ctx context.Context, query string, out any) error {
	var resp struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := c.do(ctx, http.MethodPost, "/v1/graphql", map[string]string{"query": query}, &resp); err != nil {
		return err
	}
	if len(resp.Errors) > 0 {
		return fmt.Errorf("weaviate: graphql: %s", resp.Errors[0].Message)
	}
	if err := json.Unmarshal(resp.Data, out); err != nil {
		return fmt.Errorf("weaviate: decode graphql response: %w", err)
	}
	return nil
}

// do sends body as JSON and decodes a 2xx response into out (if non-nil).
func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("weaviate: encode request: %w", err)
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.endpoint+path, reader)
	if err != nil {
		return fmt.Errorf("weaviate: %w", err)
	}
	c.authorize(req)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("weaviate: unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return statusError(resp)
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("weaviate: decode %s %s response: %w", method, path, err)
	}
	return nil
}

func (c *Client) authorize(req *http.Request) {
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
}

func statusError(resp *http.Response) error {
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("weaviate: HTTP %d: the server wants an API key, or rejected the one from vector.api_key_env", resp.StatusCode)
	}
	return fmt.Errorf("weaviate: HTTP %d: %s", resp.StatusCode, bytes.TrimSpace(msg))
}

// className maps a ragctl namespace (e.g. "ragctl-d7b3d605") to a valid
// Weaviate collection name ("Ragctl_d7b3d605"): Weaviate requires a
// leading capital and allows only letters, digits and underscores.
func className(namespace string) (string, error) {
	var b strings.Builder
	for i, r := range namespace {
		switch {
		case i == 0 && r >= 'a' && r <= 'z':
			b.WriteRune(r - 'a' + 'A')
		case r == '-' || r == '.':
			b.WriteByte('_')
		default:
			b.WriteRune(r)
		}
	}
	name := b.String()
	if !classNameRE.MatchString(name) {
		return "", fmt.Errorf("weaviate: namespace %q can't be a Weaviate collection name", namespace)
	}
	return name, nil
}

// whereFilter builds a Weaviate where clause ANDing f's non-empty fields,
// or nil when nothing filters.
func whereFilter(f *backend.Filter) map[string]any {
	if f == nil {
		return nil
	}
	var operands []map[string]any
	for _, kv := range [][2]string{{"ecosystem", f.Ecosystem}, {"dependency", f.Dependency}, {"version", f.Version}, {"generation", f.Generation}} {
		if kv[1] != "" {
			operands = append(operands, map[string]any{"path": []string{kv[0]}, "operator": "Equal", "valueText": kv[1]})
		}
	}
	switch len(operands) {
	case 0:
		return nil
	case 1:
		return operands[0]
	default:
		return map[string]any{"operator": "And", "operands": operands}
	}
}

// graphQLValue renders a where clause as a GraphQL input value: like JSON,
// but with unquoted keys and the operator as a bare enum.
func graphQLValue(v any) string {
	switch v := v.(type) {
	case map[string]any:
		var parts []string
		for _, k := range []string{"operator", "path", "valueText", "operands"} {
			val, ok := v[k]
			if !ok {
				continue
			}
			if k == "operator" {
				parts = append(parts, k+": "+val.(string))
			} else {
				parts = append(parts, k+": "+graphQLValue(val))
			}
		}
		return "{" + strings.Join(parts, ", ") + "}"
	case []map[string]any:
		parts := make([]string, len(v))
		for i, o := range v {
			parts[i] = graphQLValue(o)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	default:
		b, _ := json.Marshal(v) // strings and string slices: JSON escaping is valid GraphQL
		return string(b)
	}
}

// objectID derives a deterministic UUID (Weaviate requires one) from a
// generation and chunk ID, so the same chunk in two generations is two
// objects. Same scheme as the Qdrant adapter's point IDs.
func objectID(generationID, chunkID string) string {
	sum := blake3.Sum256([]byte(generationID + "\x00" + chunkID))
	b := sum[:16]
	b[6] = (b[6] & 0x0f) | 0x50 // version 5 (name-based)
	b[8] = (b[8] & 0x3f) | 0x80 // RFC 4122 variant
	h := hex.EncodeToString(b)
	return fmt.Sprintf("%s-%s-%s-%s-%s", h[0:8], h[8:12], h[12:16], h[16:20], h[20:32])
}

func textProperty(name string) map[string]any {
	return map[string]any{"name": name, "dataType": []string{"text"}, "tokenization": "field", "indexSearchable": false}
}
