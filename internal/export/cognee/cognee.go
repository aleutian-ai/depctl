// Package cognee is a plain HTTP client for pushing depctl's synced
// dependency knowledge into a user's own Cognee instance as a dataset
// for its own ECL (extract-cognify-load) pipeline (COGNEE-001). Used
// only from inside the daemon's own engine.ExportCognee (ADR-011) —
// never from a CLI process directly.
package cognee

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"
)

// addTimeout and healthTimeout bound the two cheap, fast calls. cognify
// gets its own, much longer bound — see cognifyTimeout's own doc.
const (
	addTimeout    = 30 * time.Second
	healthTimeout = 10 * time.Second
	// cognifyTimeout is long because POST /api/v1/cognify turned out to
	// be fully synchronous in current Cognee (confirmed live against a
	// real self-hosted container, 2026-10-01), not a fire-and-forget
	// trigger as Cognee's own docs implied: the HTTP response doesn't
	// return until the whole extraction pipeline finishes. A real,
	// one-chunk test run against a local Ollama model took 2m41s. The
	// Client has no default timeout of its own (see NewClient) — every
	// call bounds itself via context so a caller's own, shorter ctx
	// still wins; this is only the ceiling when the caller's ctx allows
	// more.
	cognifyTimeout = 10 * time.Minute
)

// Client talks to one Cognee instance's REST API.
type Client struct {
	baseURL   string
	authToken string
	http      *http.Client
}

// NewClient returns a Client for the Cognee instance at baseURL.
// authToken is sent as a bearer token when non-empty, for the (likely)
// case a user puts their own auth in front of a self-hosted server that
// has none of its own by default. The underlying http.Client has no
// blanket Timeout — each method bounds its own call via context instead,
// since Add/Health and Cognify need very different bounds (see
// cognifyTimeout's doc).
func NewClient(baseURL, authToken string) *Client {
	return &Client{
		baseURL:   strings.TrimRight(baseURL, "/"),
		authToken: authToken,
		http:      &http.Client{},
	}
}

// Add uploads content into datasetName via POST /api/v1/add. Confirmed
// (2026-10-01, docs.cognee.ai/guides/deploy-rest-api-server) this
// endpoint takes multipart form data — a "data" file field plus a
// "datasetName" field — not a JSON body as originally assumed; filename
// is cosmetic (Cognee reads the file's content, not its name).
func (c *Client) Add(ctx context.Context, datasetName, filename string, content []byte) error {
	ctx, cancel := context.WithTimeout(ctx, addTimeout)
	defer cancel()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if err := w.WriteField("datasetName", datasetName); err != nil {
		return fmt.Errorf("cognee: write datasetName field: %w", err)
	}
	part, err := w.CreateFormFile("data", filename)
	if err != nil {
		return fmt.Errorf("cognee: create form file: %w", err)
	}
	if _, err := part.Write(content); err != nil {
		return fmt.Errorf("cognee: write form file content: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("cognee: close multipart writer: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/v1/add", &buf)
	if err != nil {
		return fmt.Errorf("cognee: build add request: %w", err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	if c.authToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.authToken)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("cognee: add: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("cognee: add: unexpected status %d: %s", resp.StatusCode, strings.TrimSpace(string(detail)))
	}
	return nil
}

type cognifyRequest struct {
	Datasets  []string `json:"datasets"`
	ChunkSize int      `json:"chunk_size,omitempty"`
}

// Cognify triggers Cognee's own ECL pipeline over datasetName via POST
// /api/v1/cognify. Confirmed live (2026-10-01, see cognifyTimeout's doc)
// that this call is fully synchronous: it does not return until the
// whole extraction pipeline finishes, contrary to Cognee's own docs
// (which describe it as a fire-and-forget trigger) — budget real time
// for this call, not a quick round trip.
func (c *Client) Cognify(ctx context.Context, datasetName string, chunkSize int) error {
	ctx, cancel := context.WithTimeout(ctx, cognifyTimeout)
	defer cancel()
	body, err := json.Marshal(cognifyRequest{Datasets: []string{datasetName}, ChunkSize: chunkSize})
	if err != nil {
		return fmt.Errorf("cognee: encode cognify request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/v1/cognify", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("cognee: build cognify request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.authToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.authToken)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("cognee: cognify: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("cognee: cognify: unexpected status %d: %s", resp.StatusCode, strings.TrimSpace(string(detail)))
	}
	return nil
}

// Health does a lightweight pre-flight check that baseURL is reachable
// and responding, via the real, documented GET /api/v1/datasets
// endpoint (confirmed, 2026-10-01) — Cognee has no dedicated health
// endpoint.
func (c *Client) Health(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, healthTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/v1/datasets", nil)
	if err != nil {
		return fmt.Errorf("cognee: build health request: %w", err)
	}
	if c.authToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.authToken)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("cognee: unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 500 {
		return fmt.Errorf("cognee: unhealthy: status %d", resp.StatusCode)
	}
	return nil
}
