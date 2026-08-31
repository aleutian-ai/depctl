// Package config defines ragctl's global configuration model: its YAML
// shape, defaults, and validation. Nothing here talks to bbolt, Badger, or
// the network — this package is pure data plus loading/validation logic.
package config

import (
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
}

type RetentionConfig struct {
	GracePeriod time.Duration `yaml:"grace_period"`
	KeepLatest  bool          `yaml:"keep_latest"`
}

type WatchConfig struct {
	Enabled  bool          `yaml:"enabled"`
	Debounce time.Duration `yaml:"debounce"`
}

type ServerConfig struct {
	MCP  MCPServerConfig  `yaml:"mcp"`
	HTTP HTTPServerConfig `yaml:"http"`
}

type MCPServerConfig struct {
	Enabled bool `yaml:"enabled"`
}

type HTTPServerConfig struct {
	Listen string `yaml:"listen"`
}

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
			Model:    "nomic-embed-text",
			Endpoint: "http://127.0.0.1:11434",
		},
		Vector: VectorConfig{
			Backend:    "qdrant",
			Endpoint:   "http://127.0.0.1:6333",
			Collection: "ragctl",
		},
		Retention: RetentionConfig{
			GracePeriod: 336 * time.Hour, // 14 days
			KeepLatest:  true,
		},
		Watch: WatchConfig{
			Enabled:  true,
			Debounce: 2 * time.Second,
		},
		Server: ServerConfig{
			MCP:  MCPServerConfig{Enabled: true},
			HTTP: HTTPServerConfig{Listen: "127.0.0.1:7447"},
		},
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
