// Package config defines the on-disk configuration for sltvd and a
// loader that reads YAML files and applies SLTV_* environment overrides.
//
// The schema is intentionally small and explicit; see
// docs/configuration.md for a human-readable reference and
// deploy/examples/sltvd.yaml for a commented sample.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// DefaultPath is the conventional system location for sltvd's
// configuration file.
const DefaultPath = "/etc/sltv/sltvd.yaml"

// Config is the root configuration object for sltvd.
type Config struct {
	Listen   ListenConfig   `yaml:"listen"`
	TLS      TLSConfig      `yaml:"tls"`
	Storage  StorageConfig  `yaml:"storage"`
	Libvirt  LibvirtConfig  `yaml:"libvirt"`
	Thin     ThinConfig     `yaml:"thin"`
	Cluster  ClusterConfig  `yaml:"cluster"`
	Log      LogConfig      `yaml:"log"`
}

// ListenConfig describes how the gRPC server is exposed.
type ListenConfig struct {
	// Unix is the path to a UNIX domain socket. Empty disables the socket.
	Unix string `yaml:"unix"`
	// TCP is the optional TCP listen address (e.g. "0.0.0.0:7443").
	// Requires TLS to be configured.
	TCP string `yaml:"tcp"`
}

// TLSConfig holds paths to PEM-encoded credentials. Used both for the
// gRPC TCP listener and for talking to ETCD.
type TLSConfig struct {
	Cert string `yaml:"cert"`
	Key  string `yaml:"key"`
	CA   string `yaml:"ca"`
}

// StorageConfig governs local persistent state and default volume groups.
type StorageConfig struct {
	StateDir  string `yaml:"state_dir"`
	DefaultVG string `yaml:"default_vg"`
}

// LibvirtConfig is the libvirt connection URI.
type LibvirtConfig struct {
	URI string `yaml:"uri"`
}

// ThinConfig configures thin-provisioning defaults and the auto-extend
// loop. All percentages are integers in [0, 100]. Sizes are byte counts
// once parsed.
type ThinConfig struct {
	// DefaultInitialSize is the LV size used at creation when the user
	// does not pass an explicit size for a thin disk. Accepts strings
	// like "10GiB" or numeric byte counts in YAML.
	DefaultInitialSize HumanSize `yaml:"default_initial_size"`
	// FreeThresholdPercent is the free-percentage below which the daemon
	// auto-extends a thin LV.
	FreeThresholdPercent int `yaml:"free_threshold_percent"`
	// ExtendStepPercent is the percentage of the current LV size to add
	// when auto-extending.
	ExtendStepPercent int `yaml:"extend_step_percent"`
	// PollInterval is how often the auto-extend manager scans VMs.
	PollInterval time.Duration `yaml:"poll_interval"`
}

// ClusterConfig configures ETCD-backed clustering. When Enabled is
// false, sltvd runs as an isolated, single-host service.
type ClusterConfig struct {
	Enabled bool        `yaml:"enabled"`
	NodeID  string      `yaml:"node_id"`
	ETCD    ETCDConfig  `yaml:"etcd"`
}

// ETCDConfig describes how to talk to ETCD.
type ETCDConfig struct {
	Endpoints []string      `yaml:"endpoints"`
	LockTTL   time.Duration `yaml:"lock_ttl"`
	TLS       TLSConfig     `yaml:"tls"`
}

// LogConfig controls structured logging.
type LogConfig struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
}

// Default returns a Config populated with the documented defaults.
// It is the starting point for both the YAML loader and runtime
// fallbacks.
func Default() Config {
	return Config{
		Listen: ListenConfig{
			Unix: "/run/sltv/sltvd.sock",
		},
		Storage: StorageConfig{
			StateDir:  "/var/lib/sltv",
			DefaultVG: "vg-sltv",
		},
		Libvirt: LibvirtConfig{
			URI: "qemu:///system",
		},
		Thin: ThinConfig{
			DefaultInitialSize:   HumanSize(10 * GiB),
			FreeThresholdPercent: 10,
			ExtendStepPercent:    10,
			PollInterval:         15 * time.Second,
		},
		Cluster: ClusterConfig{
			Enabled: false,
			ETCD: ETCDConfig{
				LockTTL: 10 * time.Second,
			},
		},
		Log: LogConfig{
			Level:  "info",
			Format: "json",
		},
	}
}

// Load reads YAML from path on top of Default() and then applies
// SLTV_* environment overrides. Missing files yield ErrFileNotFound; an
// empty path means "skip the file and apply defaults+env".
func Load(path string) (Config, error) {
	cfg := Default()
	if path != "" {
		raw, err := os.ReadFile(path)
		switch {
		case err == nil:
			if err := yaml.Unmarshal(raw, &cfg); err != nil {
				return Config{}, fmt.Errorf("parse %s: %w", path, err)
			}
		case errors.Is(err, os.ErrNotExist):
			return Config{}, fmt.Errorf("%w: %s", ErrFileNotFound, path)
		default:
			return Config{}, fmt.Errorf("read %s: %w", path, err)
		}
	}
	applyEnv(&cfg)
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// ErrFileNotFound is returned by Load when the given config path does
// not exist on disk. Callers can handle it specially (e.g. to fall back
// to defaults) using errors.Is.
var ErrFileNotFound = errors.New("config file not found")

// Validate enforces invariants the loader cannot express in the schema
// alone, returning a single descriptive error on the first violation.
func (c Config) Validate() error {
	if c.Listen.Unix == "" && c.Listen.TCP == "" {
		return errors.New("listen: at least one of unix or tcp must be set")
	}
	if c.Listen.TCP != "" {
		if c.TLS.Cert == "" || c.TLS.Key == "" {
			return errors.New("listen.tcp requires tls.cert and tls.key")
		}
	}
	if c.Storage.StateDir == "" {
		return errors.New("storage.state_dir must not be empty")
	}
	if c.Thin.FreeThresholdPercent <= 0 || c.Thin.FreeThresholdPercent >= 100 {
		return errors.New("thin.free_threshold_percent must be in (0, 100)")
	}
	if c.Thin.ExtendStepPercent <= 0 || c.Thin.ExtendStepPercent > 1000 {
		return errors.New("thin.extend_step_percent must be in (0, 1000]")
	}
	if c.Thin.PollInterval < time.Second {
		return errors.New("thin.poll_interval must be >= 1s")
	}
	if c.Thin.DefaultInitialSize <= 0 {
		return errors.New("thin.default_initial_size must be > 0")
	}
	if c.Cluster.Enabled {
		if len(c.Cluster.ETCD.Endpoints) == 0 {
			return errors.New("cluster.etcd.endpoints must be set when cluster.enabled=true")
		}
		if c.Cluster.ETCD.LockTTL < time.Second {
			return errors.New("cluster.etcd.lock_ttl must be >= 1s")
		}
	}
	return nil
}

// EffectiveNodeID returns the cluster node id, defaulting to the
// machine hostname when Cluster.NodeID is empty.
func (c Config) EffectiveNodeID() string {
	if c.Cluster.NodeID != "" {
		return c.Cluster.NodeID
	}
	host, err := os.Hostname()
	if err != nil || host == "" {
		return "sltvd"
	}
	return host
}

// applyEnv overlays well-known SLTV_* environment variables on top of
// cfg. This is intentionally a small, explicit mapping rather than a
// reflection-based scheme so the supported overrides are easy to
// document and review.
func applyEnv(cfg *Config) {
	if v, ok := os.LookupEnv("SLTV_LISTEN_UNIX"); ok {
		cfg.Listen.Unix = v
	}
	if v, ok := os.LookupEnv("SLTV_LISTEN_TCP"); ok {
		cfg.Listen.TCP = v
	}
	if v, ok := os.LookupEnv("SLTV_STATE_DIR"); ok {
		cfg.Storage.StateDir = v
	}
	if v, ok := os.LookupEnv("SLTV_DEFAULT_VG"); ok {
		cfg.Storage.DefaultVG = v
	}
	if v, ok := os.LookupEnv("SLTV_LIBVIRT_URI"); ok {
		cfg.Libvirt.URI = v
	}
	if v, ok := os.LookupEnv("SLTV_NODE_ID"); ok {
		cfg.Cluster.NodeID = v
	}
	if v, ok := os.LookupEnv("SLTV_CLUSTER_ENABLED"); ok {
		cfg.Cluster.Enabled = parseBool(v)
	}
	if v, ok := os.LookupEnv("SLTV_ETCD_ENDPOINTS"); ok && v != "" {
		cfg.Cluster.ETCD.Endpoints = splitCSV(v)
	}
	if v, ok := os.LookupEnv("SLTV_LOG_LEVEL"); ok {
		cfg.Log.Level = v
	}
	if v, ok := os.LookupEnv("SLTV_LOG_FORMAT"); ok {
		cfg.Log.Format = v
	}
}

func parseBool(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "t", "true", "y", "yes", "on":
		return true
	default:
		return false
	}
}

func splitCSV(v string) []string {
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Size unit constants in IEC bytes. Used by HumanSize and consumers.
const (
	KiB uint64 = 1 << 10
	MiB uint64 = 1 << 20
	GiB uint64 = 1 << 30
	TiB uint64 = 1 << 40
)

// HumanSize is a uint64 byte count that supports YAML strings such as
// "10GiB", "512MiB", "1024" (raw bytes), or numeric YAML values.
type HumanSize uint64

// UnmarshalYAML implements yaml.Unmarshaler.
func (s *HumanSize) UnmarshalYAML(value *yaml.Node) error {
	switch value.Tag {
	case "!!int":
		n, err := strconv.ParseUint(value.Value, 10, 64)
		if err != nil {
			return err
		}
		*s = HumanSize(n)
		return nil
	case "!!str":
		n, err := ParseSize(value.Value)
		if err != nil {
			return err
		}
		*s = HumanSize(n)
		return nil
	default:
		return fmt.Errorf("config: cannot decode size from %q (tag=%s)", value.Value, value.Tag)
	}
}

// MarshalYAML emits the raw byte count so round-tripping is lossless.
func (s HumanSize) MarshalYAML() (any, error) {
	return uint64(s), nil
}

// Bytes returns the size as a uint64 byte count.
func (s HumanSize) Bytes() uint64 { return uint64(s) }

// ParseSize parses strings such as "10GiB", "512MiB", "10G", "1024",
// returning the size in bytes. It accepts both IEC (KiB, MiB, GiB, TiB)
// and the common short forms (K, M, G, T) interpreted as their IEC
// counterparts. The empty string is an error.
func ParseSize(in string) (uint64, error) {
	s := strings.TrimSpace(in)
	if s == "" {
		return 0, errors.New("empty size")
	}
	// Find the split between number and unit.
	i := 0
	for i < len(s) {
		c := s[i]
		if (c >= '0' && c <= '9') || c == '.' {
			i++
			continue
		}
		break
	}
	num := strings.TrimSpace(s[:i])
	unit := strings.TrimSpace(s[i:])
	if num == "" {
		return 0, fmt.Errorf("size %q: missing number", in)
	}
	val, err := strconv.ParseFloat(num, 64)
	if err != nil {
		return 0, fmt.Errorf("size %q: %w", in, err)
	}
	if val < 0 {
		return 0, fmt.Errorf("size %q: must be non-negative", in)
	}
	mult := uint64(1)
	switch strings.ToUpper(unit) {
	case "", "B":
		mult = 1
	case "K", "KB", "KIB":
		mult = KiB
	case "M", "MB", "MIB":
		mult = MiB
	case "G", "GB", "GIB":
		mult = GiB
	case "T", "TB", "TIB":
		mult = TiB
	default:
		return 0, fmt.Errorf("size %q: unknown unit %q", in, unit)
	}
	return uint64(val * float64(mult)), nil
}
