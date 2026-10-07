// Package ollama implements ragctl's embedding.Embedder against a local
// Ollama HTTP endpoint (EMB-002) — the only embedding provider, and an
// optional one: keyword search works without it.
package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// defaultBatchSize bounds how many texts go into one /api/embed request —
// sequential requests, no concurrent fan-out, per EMB-002's simplicity
// constraints.
const defaultBatchSize = 16

// dimensionProbeText is embedded once, on first Dimensions call, purely
// to measure the model's vector length.
const dimensionProbeText = "ragctl-dimension-probe"

var retryBackoffs = []time.Duration{200 * time.Millisecond, 400 * time.Millisecond, 800 * time.Millisecond}

// Client implements embedding.Embedder against Ollama's /api/embed
// endpoint via a small hand-written HTTP client — no generated SDK, no
// third-party Ollama client dependency.
type Client struct {
	endpoint   string
	model      string
	httpClient *http.Client
	batchSize  int

	dimsOnce sync.Once
	dims     int
	dimsErr  error
}

// Option configures a Client.
type Option func(*Client)

// WithBatchSize overrides the number of texts sent per /api/embed
// request.
func WithBatchSize(n int) Option {
	return func(c *Client) { c.batchSize = n }
}

// WithHTTPClient overrides the underlying *http.Client (e.g. for tests
// pointing at an httptest.Server with a custom timeout).
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) { c.httpClient = h }
}

// New returns a Client targeting endpoint (e.g. "http://127.0.0.1:11434")
// for model (e.g. "nomic-embed-text").
func New(endpoint, model string, opts ...Option) *Client {
	c := &Client{
		endpoint:   endpoint,
		model:      model,
		httpClient: &http.Client{Timeout: 60 * time.Second},
		batchSize:  defaultBatchSize,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Name identifies this provider for content-identity/embedding-cache
// keys.
func (c *Client) Name() string { return "ollama" }

// ModelID identifies the specific embedding model in use.
func (c *Client) ModelID() string { return c.model }

// Dimensions reports the model's vector length, probed once (embedding a
// fixed short string) and cached thereafter.
func (c *Client) Dimensions(ctx context.Context) (int, error) {
	c.dimsOnce.Do(func() {
		vecs, err := c.embedBatch(ctx, []string{dimensionProbeText})
		if err != nil {
			c.dimsErr = fmt.Errorf("ollama: dimension probe: %w", err)
			return
		}
		c.dims = len(vecs[0])
	})
	return c.dims, c.dimsErr
}

// Embed returns one vector per input text, in the same order, batching
// requests to at most c.batchSize texts each.
func (c *Client) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	var out [][]float32
	for start := 0; start < len(texts); start += c.batchSize {
		end := min(start+c.batchSize, len(texts))
		vecs, err := c.embedBatch(ctx, texts[start:end])
		if err != nil {
			return nil, err
		}
		out = append(out, vecs...)
	}

	if len(out) > 0 && c.dims == 0 {
		// A concurrent/earlier Dimensions() probe may not have run yet;
		// seed it from this batch so later mismatch checks have
		// something to compare against without a redundant HTTP call.
		c.dimsOnce.Do(func() { c.dims = len(out[0]) })
	}
	for i, v := range out {
		if c.dims != 0 && len(v) != c.dims {
			return nil, fmt.Errorf("ollama: embed: vector %d has %d dimensions, want %d", i, len(v), c.dims)
		}
	}
	return out, nil
}

// Reachable reports whether Ollama itself is responding at c's
// endpoint, independent of whether c's model is pulled — callers use
// this to tell "Ollama isn't running" from "Ollama is up but missing
// this model" and act on each differently.
func (c *Client) Reachable(ctx context.Context) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint+"/api/tags", nil)
	if err != nil {
		return false
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

type tagsResponse struct {
	Models []struct {
		Name  string `json:"name"`
		Model string `json:"model"`
	} `json:"models"`
}

// ModelPulled reports whether c.model is already present in Ollama's
// local model list, without downloading or embedding anything.
func (c *Client) ModelPulled(ctx context.Context) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint+"/api/tags", nil)
	if err != nil {
		return false, fmt.Errorf("ollama: build request: %w", err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("ollama: request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, fmt.Errorf("ollama: read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("ollama: request failed %d: %s", resp.StatusCode, bytes.TrimSpace(body))
	}
	var parsed tagsResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return false, fmt.Errorf("ollama: decode response: %w", err)
	}
	for _, m := range parsed.Models {
		if modelMatches(m.Name, c.model) || modelMatches(m.Model, c.model) {
			return true, nil
		}
	}
	return false, nil
}

// modelMatches compares a pulled model's name (which always carries a
// tag, e.g. "nomic-embed-text-v2-moe:latest") against the configured
// model name, which typically omits the ":latest" tag.
func modelMatches(pulled, configured string) bool {
	return pulled == configured || pulled == configured+":latest"
}

type pullRequest struct {
	Model string `json:"model"`
}

type pullStatusLine struct {
	Status string `json:"status"`
	Error  string `json:"error"`
}

// PullModel downloads c.model into Ollama, relaying each distinct
// status Ollama itself reports (e.g. "pulling manifest", "verifying
// sha256 digest", "success") to out as the download progresses — not
// every line, since Ollama reports a completed/total byte count on
// nearly every line during the download phase, which would flood a
// plain line-oriented writer. Uses its own long-lived HTTP client
// rather than c.httpClient, since a model pull can run far longer than
// the short per-embed-request timeout that client is tuned for; the
// caller's ctx is the real bound.
func (c *Client) PullModel(ctx context.Context, out io.Writer) error {
	body, err := json.Marshal(pullRequest{Model: c.model})
	if err != nil {
		return fmt.Errorf("ollama: encode pull request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+"/api/pull", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("ollama: build pull request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	pullClient := &http.Client{}
	resp, err := pullClient.Do(req)
	if err != nil {
		return fmt.Errorf("ollama: pull request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("ollama: pull failed %d: %s", resp.StatusCode, bytes.TrimSpace(respBody))
	}

	dec := json.NewDecoder(resp.Body)
	lastStatus := ""
	for {
		var line pullStatusLine
		if err := dec.Decode(&line); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("ollama: decode pull response: %w", err)
		}
		if line.Error != "" {
			return fmt.Errorf("ollama: pull: %s", line.Error)
		}
		if line.Status != "" && line.Status != lastStatus {
			fmt.Fprintf(out, "  %s\n", line.Status)
			lastStatus = line.Status
		}
	}
}

type embedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embedResponse struct {
	Embeddings [][]float32 `json:"embeddings"`
}

// embedBatch sends one /api/embed request for texts, retrying transient
// failures (5xx, connection errors, timeouts) a small fixed number of
// times with short exponential backoff. Non-transient errors (4xx,
// malformed response) fail immediately with no retry.
func (c *Client) embedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	var lastErr error
	for attempt := 0; ; attempt++ {
		vecs, transient, err := c.doEmbed(ctx, texts)
		if err == nil {
			return vecs, nil
		}
		lastErr = err
		if !transient || attempt >= len(retryBackoffs) {
			return nil, lastErr
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(retryBackoffs[attempt]):
		}
	}
}

// doEmbed performs a single HTTP round-trip, reporting whether a failure
// is transient (worth retrying) or not.
func (c *Client) doEmbed(ctx context.Context, texts []string) (vecs [][]float32, transient bool, err error) {
	body, err := json.Marshal(embedRequest{Model: c.model, Input: texts})
	if err != nil {
		return nil, false, fmt.Errorf("ollama: encode request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+"/api/embed", bytes.NewReader(body))
	if err != nil {
		return nil, false, fmt.Errorf("ollama: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		// Connection refused, timeout, DNS failure, etc. are all
		// transient — the caller may just not have Ollama up yet.
		return nil, true, fmt.Errorf("ollama: request: %w", err)
	}
	defer resp.Body.Close()

	respBody, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return nil, true, fmt.Errorf("ollama: read response: %w", readErr)
	}

	if resp.StatusCode >= 500 {
		return nil, true, fmt.Errorf("ollama: server error %d: %s", resp.StatusCode, bytes.TrimSpace(respBody))
	}
	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("ollama: request failed %d: %s", resp.StatusCode, bytes.TrimSpace(respBody))
	}

	var parsed embedResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, false, fmt.Errorf("ollama: decode response: %w", err)
	}
	if len(parsed.Embeddings) != len(texts) {
		return nil, false, fmt.Errorf("ollama: response has %d embeddings, want %d", len(parsed.Embeddings), len(texts))
	}
	return parsed.Embeddings, false, nil
}
