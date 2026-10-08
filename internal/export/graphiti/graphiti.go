// Package graphiti is a plain HTTP client for pushing depctl's synced
// dependency knowledge into a user's own Graphiti instance as episodes
// (GRAPHITI-001). Used only from inside the daemon's own
// engine.ExportGraphiti (ADR-011) — never from a CLI process directly.
package graphiti

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

// Client talks to one Graphiti instance's REST API
// (server/graph_service in github.com/getzep/graphiti).
type Client struct {
	baseURL   string
	authToken string
	http      *http.Client
}

// NewClient returns a Client for the Graphiti instance at baseURL.
// authToken is sent as a bearer token when non-empty, for the (likely)
// case a user puts their own auth in front of a service that has none
// of its own by default — see Client's own package doc.
func NewClient(baseURL, authToken string) *Client {
	return &Client{
		baseURL:   strings.TrimRight(baseURL, "/"),
		authToken: authToken,
		http:      &http.Client{Timeout: 30 * time.Second},
	}
}

// episodeMessage is one entry of POST /messages' "messages" array
// (server/graph_service/dto/common.py's Message). RoleType and Role are
// both required by Graphiti's own validation — omitting them was a real
// bug found only against a live server (422 on every export), since the
// fake-server tests accepted any shape. uuid is left unset so the server
// assigns one.
type episodeMessage struct {
	Name              string `json:"name"`
	Content           string `json:"content"`
	RoleType          string `json:"role_type"`
	Role              string `json:"role"`
	Timestamp         string `json:"timestamp"`
	SourceDescription string `json:"source_description,omitempty"`
}

type addMessagesRequest struct {
	GroupID  string           `json:"group_id"`
	Messages []episodeMessage `json:"messages"`
}

// AddEpisode pushes one episode to Graphiti via POST /messages, scoped
// to groupID (depctl's project ID). payload is marshaled to JSON and
// sent as the episode's content — one dependency's full chunk set, per
// GRAPHITI-001's design (an episode per generation, not per chunk:
// Graphiti's own extraction pipeline works over a coherent document).
func (c *Client) AddEpisode(ctx context.Context, groupID, name string, payload any) error {
	content, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("graphiti: encode episode content: %w", err)
	}

	body, err := json.Marshal(addMessagesRequest{
		GroupID: groupID,
		Messages: []episodeMessage{{
			Name:    name,
			Content: string(content),
			// "system": this is reference documentation, not a
			// conversational turn from a user or assistant.
			RoleType:          "system",
			Role:              "depctl",
			Timestamp:         time.Now().UTC().Format(time.RFC3339),
			SourceDescription: "depctl",
		}},
	})
	if err != nil {
		return fmt.Errorf("graphiti: encode add-episode request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/messages", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("graphiti: build add-episode request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.authToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.authToken)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("graphiti: add episode: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("graphiti: add episode: unexpected status %d: %s", resp.StatusCode, strings.TrimSpace(string(detail)))
	}
	return nil
}

// Health does a lightweight pre-flight check via Graphiti's own GET
// /healthcheck (server/graph_service/main.py). An earlier version used
// GET /episodes/{group_id} instead, on the mistaken belief no health
// endpoint existed — against a live server that call always returned
// 422 (it requires a last_n query parameter), and only "passed" because
// any non-5xx counted as healthy. This one requires a real 200.
func (c *Client) Health(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/healthcheck", nil)
	if err != nil {
		return fmt.Errorf("graphiti: build health request: %w", err)
	}
	if c.authToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.authToken)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("graphiti: unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("graphiti: unhealthy: status %d", resp.StatusCode)
	}
	return nil
}
