# CLI-001: Configuration model

**Epic:** Configuration and CLI Skeleton
**Status:** done
**Depends on:** STORE-004
**Estimated size:** medium

## Goal
Define and load the global YAML configuration file with explicit defaults and startup validation.

## Non-goals
- No secret management beyond "read from env var reference" (see design's `api_key_env` pattern) — no OS keychain integration in v0.1.
- No per-project `.ragctl.yaml` yet (out of scope for this ticket; can be a follow-up ticket if needed).

## Simplicity constraints
- One flat `Config` struct mirroring the YAML shape 1:1. Do not build a generic config-layering/override framework — env vars are only used for secret *references*, not for overriding arbitrary config keys, in v0.1.
- Validation is a single `Config.Validate() error` method with straightforward checks (non-empty paths, known enum values) — not a validation framework/library.

## Design
Package: `internal/config`

File location: `~/.config/ragctl/config.yaml` (respect `$XDG_CONFIG_HOME` if set, per standard Go conventions — use `os.UserConfigDir()`).

```go
type Config struct {
    Version   int              `yaml:"version"`
    Storage   StorageConfig    `yaml:"storage"`
    Embedding EmbeddingConfig  `yaml:"embedding"`
    Vector    VectorConfig     `yaml:"vector"`
    Retention RetentionConfig  `yaml:"retention"`
    Watch     WatchConfig      `yaml:"watch"`
    Server    ServerConfig     `yaml:"server"`
    Registry  RegistryConfig   `yaml:"registry"`
}

type StorageConfig struct {
    Control ControlStoreConfig `yaml:"control"` // type: bbolt, path
    Data    DataStoreConfig    `yaml:"data"`     // type: badger, path
}

type EmbeddingConfig struct {
    Provider string `yaml:"provider"` // e.g. "ollama"
    Model    string `yaml:"model"`
    Endpoint string `yaml:"endpoint"`
}

type VectorConfig struct {
    Backend    string `yaml:"backend"` // e.g. "qdrant"
    Endpoint   string `yaml:"endpoint"`
    Collection string `yaml:"collection"`
    APIKeyEnv  string `yaml:"api_key_env,omitempty"`
}

type RetentionConfig struct {
    GracePeriod time.Duration `yaml:"grace_period"` // default 336h (14d)
    KeepLatest  bool          `yaml:"keep_latest"`
}

type WatchConfig struct {
    Enabled  bool          `yaml:"enabled"`
    Debounce time.Duration `yaml:"debounce"`
}

type ServerConfig struct {
    MCP  MCPServerConfig  `yaml:"mcp"`
    HTTP HTTPServerConfig `yaml:"http"` // listen addr, default 127.0.0.1:7447
}
```

```go
func Load(path string) (Config, error)   // reads file, applies defaults for missing fields, validates
func Default() Config                     // returns the documented defaults
func (c Config) Validate() error
```

Defaults: `retention.grace_period = 336h`, `watch.debounce = 2s`, `server.http.listen = 127.0.0.1:7447`, `server.mcp.enabled = true`. Never bind server addresses to `0.0.0.0` by default (local-first security default).

Secrets: config never stores raw API keys. `api_key_env` names an environment variable to read at runtime; nothing resolved from it is ever written back to the config file.

CLI command: `ragctl config validate` — loads config, runs `Validate()`, prints actionable errors (which field, what's wrong) or "config OK".

## Inputs / Outputs
- Input: `~/.config/ragctl/config.yaml` (or explicit `--config` path).
- Output: validated `Config` struct in memory; `config validate` also produces human-readable stdout.

## Failure behavior
- Missing file → return `Default()` merged with... actually: missing file is only acceptable for `ragctl init` to create; `Load` on a missing file returns a typed `ErrConfigNotFound` so callers (e.g. `init`) can distinguish "not yet initialized" from "malformed".
- Malformed YAML or failed validation → error message names the exact field and constraint violated (e.g. `retention.grace_period: must be a positive duration`).

## Tests
- Round-trip: write a valid YAML, `Load`, assert struct fields match.
- Default-filling: YAML missing optional fields, `Load`, assert defaults applied.
- Validation failures: invalid backend name, negative durations, empty required paths — each produces a distinct, useful error message.
- Secrets never appear in `config.Marshal()`/re-serialized output even if resolved at runtime.

## Acceptance criteria
- [x] `ragctl config validate` returns useful, field-specific errors on bad config.
- [x] Explicit defaults are documented and applied for all optional fields.
- [x] No API secrets are ever serialized back to the config file.
- [x] Environment variables are used only for secret resolution, not general config override.
