// Package mem0 is a plain HTTP client for pushing ragctl's synced
// dependency knowledge into a user's own Mem0 instance as tagged
// memories (MEM0-001). Used only from inside the daemon's own
// engine.ExportMem0 (ADR-011) — never from a CLI process directly.
package mem0

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client talks to one Mem0 instance's REST API.
type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

// NewClient returns a Client for the Mem0 instance at baseURL. apiKey
// may be empty for an instance with no auth configured.
func NewClient(baseURL, apiKey string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

// message is one entry of an add-memories request's "messages" array.
type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// addMemoryRequest is POST /v3/memories/add/'s request body (confirmed
// against docs.mem0.ai/api-reference/memory/add-memories, 2026-09-30).
// Infer is always false: the endpoint is asynchronous by default
// (returns an event_id to poll), which this connector's per-chunk
// success/failure reporting can't use — infer:false makes the call
// synchronous instead.
type addMemoryRequest struct {
	Messages []message         `json:"messages"`
	UserID   string            `json:"user_id"`
	Metadata map[string]string `json:"metadata,omitempty"`
	Infer    bool              `json:"infer"`
}

// AddMemory pushes one memory (chunk content, tagged with metadata) to
// Mem0, scoped to userID (ragctl's project ID).
func (c *Client) AddMemory(ctx context.Context, userID, text string, metadata map[string]string) error {
	body, err := json.Marshal(addMemoryRequest{
		Messages: []message{{Role: "user", Content: text}},
		UserID:   userID,
		Metadata: metadata,
		Infer:    false,
	})
	if err != nil {
		return fmt.Errorf("mem0: encode add-memory request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v3/memories/add/", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("mem0: build add-memory request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Token "+c.apiKey)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("mem0: add memory: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("mem0: add memory: unexpected status %d: %s", resp.StatusCode, strings.TrimSpace(string(detail)))
	}
	return nil
}

// Health does a lightweight pre-flight check that baseURL is reachable
// and responding, so a misconfigured endpoint fails once up front
// instead of once per chunk. Mem0 has no dedicated health/ping endpoint
// (confirmed absent from its API reference, 2026-09-30), so this uses
// GET /v1/entities/ — the cheapest real, documented, read-only endpoint
// available, listing the account's own entities rather than any ragctl
// data.
func (c *Client) Health(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/entities/", nil)
	if err != nil {
		return fmt.Errorf("mem0: build health request: %w", err)
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Token "+c.apiKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("mem0: unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 500 {
		return fmt.Errorf("mem0: unhealthy: status %d", resp.StatusCode)
	}
	return nil
}
