// Package config defines ragctl's global configuration model: its YAML
// shape, defaults, and validation. Nothing here talks to bbolt, Badger, or
// the network — this package is pure data plus loading/validation logic.
package config

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// ErrConfigNotFound is returned by Load when the config file does not
// exist, so callers (like `ragctl init`) can distinguish "not yet
// initialized" from "malformed config".
var ErrConfigNotFound = errors.New("config file not found")

type Config struct {
	Version   int             `yaml:"version"`
	Storage   StorageConfig   `yaml:"storage"`
	Embedding EmbeddingConfig `yaml:"embedding"`
	Vector    VectorConfig    `yaml:"vector"`
	Retention RetentionConfig `yaml:"retention"`
	Watch     WatchConfig     `yaml:"watch"`
	Server    ServerConfig    `yaml:"server"`
	Daemon    DaemonConfig    `yaml:"daemon"`
}

type StorageConfig struct {
	Control ControlStoreConfig `yaml:"control"`
	Data    DataStoreConfig    `yaml:"data"`
}

type ControlStoreConfig struct {
	Type string `yaml:"type"` // "bbolt"
	Path string `yaml:"path"`
}

type DataStoreConfig struct {
	Type string `yaml:"type"` // "badger"
	Path string `yaml:"path"`
}

type EmbeddingConfig struct {
	Provider string `yaml:"provider"`
	Model    string `yaml:"model"`
	Endpoint string `yaml:"endpoint"`
}

type VectorConfig struct {
	Backend    string `yaml:"backend"`
	Endpoint   string `yaml:"endpoint"`
	Collection string `yaml:"collection"`
	APIKeyEnv  string `yaml:"api_key_env,omitempty"`
	// Managed controls whether the daemon starts its own Qdrant
	// container (WATCH-016) when the configured endpoint is
	// unreachable. Defaults to true for fresh installs (see Default);
	// a config file written before this field existed has no
	// "managed" key, which unmarshals to false — so nobody who already
	// points Endpoint at their own real Qdrant gets a surprise second
	// instance competing for the same port.
	Managed bool `yaml:"managed,omitempty"`
}

type RetentionConfig struct {
	GracePeriod time.Duration `yaml:"grace_period"`
	KeepLatest  bool          `yaml:"keep_latest"`
	// OrphanAge is how long a FAILED or stuck-non-terminal generation
	// must be untouched before `ragctl gc --orphans` considers it
	// eligible for cleanup (GC-001) — independent of GracePeriod, which
	// only governs the reference-based path.
	OrphanAge time.Duration `yaml:"orphan_age"`
}

type WatchConfig struct {
	Enabled  bool          `yaml:"enabled"`
	Debounce time.Duration `yaml:"debounce"`
}

// DaemonConfig controls the single-owner daemon (ADR-011). Autostart is
// a pointer so a config file written before the daemon existed — which
// has no daemon key at all, since Load never applies defaults — keeps
// auto-start on rather than silently disabling it.
type DaemonConfig struct {
	Autostart *bool `yaml:"autostart"`
}

// AutostartEnabled reports whether a client may start a daemon itself;
// unset means yes.
func (d DaemonConfig) AutostartEnabled() bool {
	return d.Autostart == nil || *d.Autostart
}

type ServerConfig struct {
	MCP  MCPServerConfig  `yaml:"mcp"`
	HTTP HTTPServerConfig `yaml:"http"`
}

type MCPServerConfig struct {
	Enabled bool `yaml:"enabled"`
	// EnableSyncTool gates the sync_project MCP tool (MCP-003) — on by
	// default. sync_project runs the identical Scheduler.Request path a
	// human's `ragctl sync` uses (bounded, collapsible, mutually
	// exclusive with GC) and touches nothing an agent couldn't already
	// trigger by asking a human to run `ragctl sync`; without it, an
	// agent can register a project (scan_project) but never actually
	// index it, undercutting the whole point of pointing an agent at
	// ragctl in the first place. Set to false for a deliberately
	// read-only MCP session.
	EnableSyncTool bool `yaml:"enable_sync_tool"`
}

type HTTPServerConfig struct {
	Listen string `yaml:"listen"`
}

// autostartDefault backs DaemonConfig.Autostart in Default; a pointer
// field needs an addressable value.
var autostartDefault = true

// Default returns the documented v0.1 defaults, rooted at the given data
// directory (control.db and badger/ live under it).
func Default(dataDir string) Config {
	return Config{
		Version: 1,
		Storage: StorageConfig{
			Control: ControlStoreConfig{Type: "bbolt", Path: dataDir + "/control.db"},
			Data:    DataStoreConfig{Type: "badger", Path: dataDir + "/badger"},
		},
		Embedding: EmbeddingConfig{
			Provider: "ollama",
			Model:    "nomic-embed-text-v2-moe",
			Endpoint: "http://127.0.0.1:11434",
		},
		Vector: VectorConfig{
			Backend:    "qdrant",
			Endpoint:   "http://127.0.0.1:6333",
			Collection: "ragctl",
			Managed:    true,
		},
		Retention: RetentionConfig{
			GracePeriod: 336 * time.Hour, // 14 days
			KeepLatest:  true,
			OrphanAge:   24 * time.Hour,
		},
		Watch: WatchConfig{
			Enabled:  true,
			Debounce: 2 * time.Second,
		},
		Server: ServerConfig{
			MCP:  MCPServerConfig{Enabled: true, EnableSyncTool: true},
			HTTP: HTTPServerConfig{Listen: "127.0.0.1:7447"},
		},
		Daemon: DaemonConfig{Autostart: &autostartDefault},
	}
}

// Load reads and validates the config file at path. It returns
// ErrConfigNotFound (wrapped, errors.Is-compatible) if the file does not
// exist.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Config{}, fmt.Errorf("%w: %s", ErrConfigNotFound, path)
		}
		return Config{}, fmt.Errorf("read config %s: %w", path, err)
	}

	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return Config{}, fmt.Errorf("parse config %s: %w", path, err)
	}

	if err := c.Validate(); err != nil {
		return Config{}, fmt.Errorf("invalid config %s: %w", path, err)
	}

	return c, nil
}

// Validate checks straightforward invariants: non-empty required paths and
// known enum-like values. It never binds server addresses to 0.0.0.0 by
// default; that's a config-time policy, not enforced here.
func (c Config) Validate() error {
	if c.Storage.Control.Path == "" {
		return errors.New("storage.control.path: must not be empty")
	}
	if c.Storage.Data.Path == "" {
		return errors.New("storage.data.path: must not be empty")
	}
	if c.Retention.GracePeriod < 0 {
		return errors.New("retention.grace_period: must be a non-negative duration")
	}
	if c.Retention.OrphanAge < 0 {
		return errors.New("retention.orphan_age: must be a non-negative duration")
	}
	if c.Watch.Debounce < 0 {
		return errors.New("watch.debounce: must be a non-negative duration")
	}
	return nil
}

// Save writes c to path as YAML, creating the parent directory if needed.
// It never serializes resolved secrets — only the api_key_env reference,
// which is already the only secret-shaped field on Config.
func (c Config) Save(path string) error {
	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write config %s: %w", path, err)
	}
	return nil
}

// Fingerprint hashes c's canonical YAML encoding — not the on-disk
// bytes, so cosmetic differences (comments, key order, whitespace) never
// register as a change. The daemon computes this once at startup
// (runDaemonRun) and reports it over /v1/health; every ensureDaemon call
// and `ragctl doctor` compare it against a fresh load's fingerprint to
// warn when config.yaml has changed since the daemon started and hasn't
// taken effect yet (config is loaded once for the daemon's whole
// lifetime — see docs/internal/daemon.md).
func (c Config) Fingerprint() (string, error) {
	data, err := yaml.Marshal(c)
	if err != nil {
		return "", fmt.Errorf("marshal config: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
