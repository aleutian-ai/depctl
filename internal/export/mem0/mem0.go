// Package mem0 is a plain HTTP client for pushing ragctl's synced
// dependency knowledge into a user's own self-hosted Mem0 server as
// tagged memories (MEM0-001). Used only from inside the daemon's own
// engine.ExportMem0 (ADR-011) — never from a CLI process directly.
//
// It targets Mem0's open-source server (server/main.py in
// github.com/mem0ai/mem0), not the hosted Mem0 Platform, whose API has
// different paths and auth. Verified against a real self-hosted server,
// 2026-10-03.
package mem0

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client talks to one self-hosted Mem0 server's REST API.
type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

// NewClient returns a Client for the Mem0 server at baseURL. apiKey may
// be empty for a server running with AUTH_DISABLED.
func NewClient(baseURL, apiKey string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

// message is one entry of an add request's "messages" array.
type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// addMemoryRequest is POST /memories' body (the server's MemoryCreate).
// Infer is always false: ragctl hands over already-chunked, verbatim
// docs, so Mem0 should store them as-is rather than run its LLM fact
// extraction over them. It also keeps the call fast and synchronous, so
// each chunk's success or failure is known immediately.
type addMemoryRequest struct {
	Messages []message         `json:"messages"`
	UserID   string            `json:"user_id"`
	RunID    string            `json:"run_id"`
	Metadata map[string]string `json:"metadata,omitempty"`
	Infer    bool              `json:"infer"`
}

// AddMemory pushes one memory (chunk content, tagged with metadata) to
// Mem0, scoped to userID (ragctl's project ID) and tagged with runID so
// DeleteMemories can later replace exactly this set.
func (c *Client) AddMemory(ctx context.Context, userID, runID, text string, metadata map[string]string) error {
	body, err := json.Marshal(addMemoryRequest{
		Messages: []message{{Role: "user", Content: text}},
		UserID:   userID,
		RunID:    runID,
		Metadata: metadata,
		Infer:    false,
	})
	if err != nil {
		return fmt.Errorf("mem0: encode add-memory request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/memories", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("mem0: build add-memory request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	c.setAuth(req)

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

// Health is a pre-flight check, so a misconfigured endpoint or key fails
// once up front instead of once per chunk. The server has no dedicated
// health route, so this lists entities (GET /entities): cheap, read-only,
// and authenticated, which also proves the API key works. Requires a real
// 200; a 404 here usually means the endpoint is the hosted Platform or
// some other service, not a self-hosted Mem0 server.
func (c *Client) Health(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/entities", nil)
	if err != nil {
		return fmt.Errorf("mem0: build health request: %w", err)
	}
	c.setAuth(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("mem0: unreachable: %w", err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusOK:
		return nil
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("mem0: authentication failed (status %d): set export.mem0.api_key_env to an env var holding the server's API key (sent as X-API-Key)", resp.StatusCode)
	case resp.StatusCode == http.StatusNotFound:
		return fmt.Errorf("mem0: %s/entities not found: is this a self-hosted Mem0 server? (the hosted Mem0 Platform isn't supported)", c.baseURL)
	default:
		return fmt.Errorf("mem0: unhealthy: status %d", resp.StatusCode)
	}
}

// DeleteMemories removes every memory tagged with both userID and runID
// (DELETE /memories, whose filters AND together), so only memories ragctl
// itself wrote for one project's dependency are touched. The self-hosted
// server requires an admin key for deletes.
func (c *Client) DeleteMemories(ctx context.Context, userID, runID string) error {
	q := url.Values{"user_id": {userID}, "run_id": {runID}}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.baseURL+"/memories?"+q.Encode(), nil)
	if err != nil {
		return fmt.Errorf("mem0: build delete request: %w", err)
	}
	c.setAuth(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("mem0: delete previous memories: %w", err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode < 300:
		return nil
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("mem0: replacing previously exported memories needs an admin API key (self-hosted Mem0 requires admin for deletes); status %d", resp.StatusCode)
	default:
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("mem0: delete previous memories: unexpected status %d: %s", resp.StatusCode, strings.TrimSpace(string(detail)))
	}
}

// setAuth sends the API key the way the self-hosted server expects it.
func (c *Client) setAuth(req *http.Request) {
	if c.apiKey != "" {
		req.Header.Set("X-API-Key", c.apiKey)
	}
}
