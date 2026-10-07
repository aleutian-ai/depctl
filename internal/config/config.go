// Package config defines ragctl's global configuration model: its YAML
// shape, defaults, and validation. Nothing here talks to bbolt, Badger, or
// the network — this package is pure data plus loading/validation logic.
package config

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// ErrConfigNotFound is returned by Load when the config file does not
// exist, so callers (like `ragctl init`) can distinguish "not yet
// initialized" from "malformed config".
var ErrConfigNotFound = errors.New("config file not found")

type Config struct {
	Version       int                 `yaml:"version"`
	Storage       StorageConfig       `yaml:"storage"`
	Embedding     EmbeddingConfig     `yaml:"embedding"`
	Vector        VectorConfig        `yaml:"vector"`
	Retrieval     RetrievalConfig     `yaml:"retrieval"`
	Retention     RetentionConfig     `yaml:"retention"`
	Watch         WatchConfig         `yaml:"watch"`
	Sync          SyncConfig          `yaml:"sync"`
	Server        ServerConfig        `yaml:"server"`
	Daemon        DaemonConfig        `yaml:"daemon"`
	Git           GitConfig           `yaml:"git,omitempty"`
	Fetch         FetchConfig         `yaml:"fetch,omitempty"`
	Log           LogConfig           `yaml:"log,omitempty"`
	Observability ObservabilityConfig `yaml:"observability,omitempty"`
	Export        ExportConfig        `yaml:"export,omitempty"`
}

// ExportConfig groups epic 65's opt-in cross-agent-memory push
// connectors (MEM0-001, GRAPHITI-001, LETTA-001, COGNEE-001) — a zero
// value means none are configured, so `ragctl export <target>` always
// requires an explicit endpoint (here or via its own flags) rather than
// silently defaulting to somewhere data could be sent.
type ExportConfig struct {
	Mem0     Mem0ExportConfig     `yaml:"mem0,omitempty"`
	Graphiti GraphitiExportConfig `yaml:"graphiti,omitempty"`
	Cognee   CogneeExportConfig   `yaml:"cognee,omitempty"`
}

// Mem0ExportConfig configures MEM0-001's connector — the same
// endpoint/api_key_env shape VectorConfig already uses for an unmanaged
// Qdrant, since this is the same kind of "bring your own instance"
// external HTTP target.
type Mem0ExportConfig struct {
	Endpoint  string `yaml:"endpoint,omitempty"`
	APIKeyEnv string `yaml:"api_key_env,omitempty"`
}

// GraphitiExportConfig configures GRAPHITI-001's connector. AuthTokenEnv
// is optional: self-hosted Graphiti has no auth of its own by default,
// so this only matters for a user who's put their own auth in front of
// it via a reverse proxy.
type GraphitiExportConfig struct {
	Endpoint     string `yaml:"endpoint,omitempty"`
	AuthTokenEnv string `yaml:"auth_token_env,omitempty"`
}

// CogneeExportConfig configures COGNEE-001's connector.
type CogneeExportConfig struct {
	Endpoint     string `yaml:"endpoint,omitempty"`
	AuthTokenEnv string `yaml:"auth_token_env,omitempty"`
}

// ObservabilityConfig groups the opt-in observability integrations (OBS-002
// tracing, OBS-003 metrics) — deliberately separate from LogConfig, which
// is always on (OBS-001). A zero value means everything here stays off,
// matching the project's local-first, no-telemetry-by-default posture.
type ObservabilityConfig struct {
	OTel    OTelConfig    `yaml:"otel,omitempty"`
	Metrics MetricsConfig `yaml:"metrics,omitempty"`
}

// OTelConfig configures OBS-002's OpenTelemetry tracing. Off by default —
// Enabled must be explicitly set true, and Endpoint must be non-empty, for
// any span to ever leave the process; see internal/observability/trace.
type OTelConfig struct {
	Enabled  bool   `yaml:"enabled,omitempty"`
	Endpoint string `yaml:"endpoint,omitempty"`
}

// MetricsConfig configures OBS-003's Prometheus /metrics endpoint. Off by
// default; Listen defaults to localhost-only when unset, matching the
// project's local-first security defaults — see
// MetricsConfig.ListenOrDefault.
type MetricsConfig struct {
	Enabled bool   `yaml:"enabled,omitempty"`
	Listen  string `yaml:"listen,omitempty"`
}

// DefaultMetricsListen is used whenever Listen is unset — localhost-only,
// never 0.0.0.0, so enabling metrics never exposes the endpoint beyond the
// local machine unless an operator deliberately overrides it.
const DefaultMetricsListen = "127.0.0.1:9090"

// ListenOrDefault returns m.Listen, or DefaultMetricsListen if unset.
func (m MetricsConfig) ListenOrDefault() string {
	if m.Listen == "" {
		return DefaultMetricsListen
	}
	return m.Listen
}

// LogConfig configures OBS-001's structured logging. A zero value means
// human-readable text at info level, to stderr — the same default a
// terminal user gets today; JSON is opt-in for log-aggregation use.
type LogConfig struct {
	// JSON switches the log handler from human-readable text to JSON
	// lines — for piping to a log aggregator, not interactive use.
	JSON bool `yaml:"json,omitempty"`
	// Level is one of "debug", "info", "warn", "error" — empty means
	// "info".
	Level string `yaml:"level,omitempty"`
}

// SlogLevel parses Level into a slog.Level, defaulting to
// slog.LevelInfo for an empty or unrecognized value rather than
// erroring — a typo'd log level should degrade to a sane default, never
// prevent the daemon from starting.
func (l LogConfig) SlogLevel() slog.Level {
	switch strings.ToLower(l.Level) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// FetchConfig bounds worst-case resource consumption from untrusted
// external fetches (SEC-004: registry metadata, Go vanity-import
// resolution; git mirror size) — a zero value on any field means
// "unset, use the built-in default," the same "0 unmarshals as unset"
// convention Sync.MaxConcurrency already establishes, so an existing
// config.yaml with no "fetch" key at all sees no behavior change beyond
// the defaults already applying. MaxWebsitePageSize/MaxDecompressedSize
// from this ticket's original design are deliberately not included yet
// — there's no website-acquisition or decompression code path in the
// codebase for them to bound (backlog epic 24); add them when that
// ships, not speculatively now.
type FetchConfig struct {
	// MaxFileSize bounds a single external HTTP response body (registry
	// metadata, vanity-import meta tags) — default 10 MiB.
	MaxFileSize int64 `yaml:"max_file_size,omitempty"`
	// MaxRedirects bounds how many redirect hops a single external fetch
	// will follow before failing — default 5.
	MaxRedirects int `yaml:"max_redirects,omitempty"`
	// MaxSourceTotalBytes bounds one dependency's real, on-disk git
	// mirror size — default 500 MiB.
	MaxSourceTotalBytes int64 `yaml:"max_source_total_bytes,omitempty"`
}

// Defaults, applied wherever a zero FetchConfig field is used — see
// FetchConfig's own doc comment for the "0 means unset" convention.
const (
	DefaultMaxFetchFileSize         int64 = 10 * 1024 * 1024 // 10 MiB
	DefaultMaxFetchRedirects        int   = 5
	DefaultMaxFetchSourceTotalBytes int64 = 500 * 1024 * 1024 // 500 MiB
)

// MaxFileSizeOrDefault returns f.MaxFileSize, or DefaultMaxFetchFileSize
// if unset (<= 0).
func (f FetchConfig) MaxFileSizeOrDefault() int64 {
	if f.MaxFileSize <= 0 {
		return DefaultMaxFetchFileSize
	}
	return f.MaxFileSize
}

// MaxRedirectsOrDefault returns f.MaxRedirects, or
// DefaultMaxFetchRedirects if unset (<= 0).
func (f FetchConfig) MaxRedirectsOrDefault() int {
	if f.MaxRedirects <= 0 {
		return DefaultMaxFetchRedirects
	}
	return f.MaxRedirects
}

// MaxSourceTotalBytesOrDefault returns f.MaxSourceTotalBytes, or
// DefaultMaxFetchSourceTotalBytes if unset (<= 0).
func (f FetchConfig) MaxSourceTotalBytesOrDefault() int64 {
	if f.MaxSourceTotalBytes <= 0 {
		return DefaultMaxFetchSourceTotalBytes
	}
	return f.MaxSourceTotalBytes
}

// GitConfig configures internal/source/git.Cache's optional local-seed
// fallback tiers (epic 43) — both empty by default, so an existing
// config file with no "git" key at all sees no behavior change.
type GitConfig struct {
	// MirrorSearchPaths (GIT-006): external directories laid out
	// identically to ragctl's own mirror cache convention
	// (<host>/<org>/<repo>.git) — e.g. a backup of another machine's
	// ragctl data dir. Checked by exact path, in order, before a
	// network clone; the first hit wins.
	MirrorSearchPaths []string `yaml:"mirror_search_paths,omitempty"`
	// CheckoutSearchPaths (GIT-007): external directories containing
	// arbitrary real git working-tree checkouts (e.g. a personal
	// corpus like ~/offline-knowledge) — discovered by walking each
	// root and matching a checkout's own "origin" remote against the
	// dependency being acquired, not by path convention.
	CheckoutSearchPaths []string `yaml:"checkout_search_paths,omitempty"`
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

// RetrievalConfig chooses how search finds chunks (LOCAL-001/002).
type RetrievalConfig struct {
	// Mode is "auto", "vector" or "keyword". auto (the default for fresh
	// installs) always builds a keyword index and adds vectors when the
	// embedder is ready; search uses vectors when it can and keyword
	// search otherwise. vector requires the embedder, as before; keyword
	// never uses it. Empty, in a config written before this existed,
	// means vector, so nothing changes for an existing install.
	Mode string `yaml:"mode,omitempty"`
}

// Retrieval modes.
const (
	RetrievalAuto    = "auto"
	RetrievalVector  = "vector"
	RetrievalKeyword = "keyword"
)

// ModeOrDefault resolves an empty Mode to vector (see Mode).
func (r RetrievalConfig) ModeOrDefault() string {
	if r.Mode == "" {
		return RetrievalVector
	}
	return r.Mode
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

// SyncConfig controls RunSync's bulk-sync worker pool (epic 53/
// COORD-003) — irrelevant to a JIT single-dependency sync, which is
// always one action regardless of this setting.
type SyncConfig struct {
	// MaxConcurrency bounds how many SYNC_VERSION actions one RunSync
	// call processes at once. Default 2, deliberately conservative:
	// real concurrent throughput against GitHub (clone rate-limiting)
	// and Ollama (GPU-bound embed throughput) was unmeasured at the
	// time this shipped — see epic 53/COORD-003's own benchmark note
	// for the real numbers this default was chosen from.
	MaxConcurrency int `yaml:"max_concurrency"`
	// MaxTotalConcurrency bounds how many SYNC_VERSION actions run at
	// once across the whole daemon, not just one project's own RunSync
	// call. SCOPE-002's own known risk: ambient sync fires per project on
	// first registration, and each project's RunSync spins up its own
	// MaxConcurrency workers — scanning a directory that registers many
	// projects at once (a real polyglot monorepo: mem0's 22 sub-projects)
	// multiplies concurrency to projects x MaxConcurrency with no daemon-
	// wide bound, all contending for the same GitHub/Ollama/Qdrant
	// capacity. Default 4, deliberately close to MaxConcurrency's own
	// default rather than a large number — the goal is "bounded and
	// visible," not "as fast as possible"; see RunSync's own doc comment
	// for where this is actually enforced.
	MaxTotalConcurrency int `yaml:"max_total_concurrency"`
	// DisableAmbient stops the daemon starting a full sync automatically
	// when a project is first registered (SCOPE-002). Off by default — the
	// full sync is the deliberate default — for contexts that want only
	// explicit or just-in-time syncs, such as CI and tests.
	DisableAmbient bool `yaml:"disable_ambient"`
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

// ServerConfig is deliberately MCP-only — a Streamable HTTP transport
// remains a documented future option on the MCP SDK (see docs/internal/
// cli.md), not built, so no HTTPServerConfig-shaped field exists here
// (CFG-001, epic 61): an unconsumed config field that looks functional
// but silently does nothing is worse than no field at all. Add one back
// only alongside a real consumer.
type ServerConfig struct {
	MCP MCPServerConfig `yaml:"mcp"`
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

// autostartDefault backs DaemonConfig.Autostart in Default; a pointer
// field needs an addressable value.
var autostartDefault = true

// defaultCollectionName returns a per-install-unique Qdrant collection
// name — SAFE-001 (epic 61): a plain, identical "ragctl" literal on
// every fresh install meant that two independent instances (a real
// install and an isolated/test one) sharing one Qdrant server would
// silently commingle data the moment either one synced, since ambient
// sync (SCOPE-002) fires automatically with no separate opt-in step.
// Live-found: this happened for real, twice, during this session's own
// live-verification work. A random suffix (not hostname-based — the
// real incidents here were two installs on the *same* machine, in
// different isolated $HOME directories) makes every fresh `ragctl init`
// distinct by default, on any machine, without requiring the operator
// to think about it. Never changes an existing config.yaml's already-
// saved collection name — Load never re-derives defaults for a field
// that's already on disk (see this package's own "no implicit
// defaulting" convention), so this only affects installs from here on.
func defaultCollectionName() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failing is effectively unheard-of on any real
		// platform ragctl runs on; fall back to the old plain name
		// rather than a zero-value/panic, since a collection name still
		// has to be something.
		return "ragctl"
	}
	return "ragctl-" + hex.EncodeToString(b[:])
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
			Model:    "nomic-embed-text-v2-moe",
			Endpoint: "http://127.0.0.1:11434",
		},
		// The embedded backend (VEC-015) is the default: one file in the
		// data dir, no vector service or container to run. QdrantDefaults
		// switches a config to ragctl-managed Qdrant.
		Vector: VectorConfig{
			Backend:    "embedded",
			Collection: defaultCollectionName(),
		},
		Retrieval: RetrievalConfig{Mode: RetrievalAuto},
		Retention: RetentionConfig{
			GracePeriod: 336 * time.Hour, // 14 days
			KeepLatest:  true,
			OrphanAge:   24 * time.Hour,
		},
		Watch: WatchConfig{
			Enabled:  true,
			Debounce: 2 * time.Second,
		},
		Sync: SyncConfig{
			MaxConcurrency:      2,
			MaxTotalConcurrency: 4,
		},
		Server: ServerConfig{
			MCP: MCPServerConfig{Enabled: true, EnableSyncTool: true},
		},
		Daemon: DaemonConfig{Autostart: &autostartDefault},
	}
}

// QdrantDefaults switches v to a local Qdrant at the standard port that
// ragctl starts itself (WATCH-016) if nothing is running there.
func (v *VectorConfig) QdrantDefaults() {
	v.Backend, v.Endpoint, v.Managed = "qdrant", "http://127.0.0.1:6333", true
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
	if c.Sync.MaxConcurrency < 0 {
		return errors.New("sync.max_concurrency: must be non-negative")
	}
	if c.Sync.MaxTotalConcurrency < 0 {
		return errors.New("sync.max_total_concurrency: must be non-negative")
	}
	if c.Observability.OTel.Enabled && c.Observability.OTel.Endpoint == "" {
		return errors.New("observability.otel.endpoint: must be set when observability.otel.enabled is true")
	}
	switch c.Retrieval.Mode {
	case "", RetrievalAuto, RetrievalVector, RetrievalKeyword:
	default:
		return fmt.Errorf("retrieval.mode %q: must be auto, vector or keyword", c.Retrieval.Mode)
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
