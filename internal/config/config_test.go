package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseSize(t *testing.T) {
	cases := []struct {
		in   string
		want uint64
		err  bool
	}{
		{"10GiB", 10 * GiB, false},
		{"10G", 10 * GiB, false},
		{"512MiB", 512 * MiB, false},
		{"1024", 1024, false},
		{"1024B", 1024, false},
		{"1.5G", uint64(1.5 * float64(GiB)), false},
		{"", 0, true},
		{"abc", 0, true},
		{"-1G", 0, true},
		{"10ZB", 0, true},
	}
	for _, tc := range cases {
		got, err := ParseSize(tc.in)
		if tc.err {
			if err == nil {
				t.Errorf("ParseSize(%q): expected error, got %d", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseSize(%q): unexpected error %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseSize(%q): got %d want %d", tc.in, got, tc.want)
		}
	}
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load(\"\"): %v", err)
	}
	if cfg.Listen.Unix == "" {
		t.Errorf("expected default unix socket, got empty")
	}
	if cfg.Thin.FreeThresholdPercent != 10 {
		t.Errorf("default free threshold = %d, want 10", cfg.Thin.FreeThresholdPercent)
	}
	if cfg.Thin.DefaultInitialSize.Bytes() != 10*GiB {
		t.Errorf("default initial size = %d, want %d", cfg.Thin.DefaultInitialSize, 10*GiB)
	}
}

func TestLoadFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sltvd.yaml")
	yaml := `
listen:
  unix: /tmp/sltvd.sock
storage:
  state_dir: /var/tmp/sltv
  default_vg: vg-test
thin:
  default_initial_size: 5GiB
  free_threshold_percent: 20
  extend_step_percent: 25
  poll_interval: 30s
cluster:
  enabled: true
  node_id: node-a
  etcd:
    endpoints:
      - http://etcd1:2379
      - http://etcd2:2379
    lock_ttl: 5s
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Listen.Unix != "/tmp/sltvd.sock" {
		t.Errorf("listen.unix = %q", cfg.Listen.Unix)
	}
	if cfg.Storage.DefaultVG != "vg-test" {
		t.Errorf("default_vg = %q", cfg.Storage.DefaultVG)
	}
	if cfg.Thin.DefaultInitialSize.Bytes() != 5*GiB {
		t.Errorf("default_initial_size = %d", cfg.Thin.DefaultInitialSize)
	}
	if cfg.Thin.PollInterval != 30*time.Second {
		t.Errorf("poll_interval = %s", cfg.Thin.PollInterval)
	}
	if !cfg.Cluster.Enabled || cfg.Cluster.NodeID != "node-a" {
		t.Errorf("cluster cfg = %+v", cfg.Cluster)
	}
	if len(cfg.Cluster.ETCD.Endpoints) != 2 {
		t.Errorf("etcd endpoints = %v", cfg.Cluster.ETCD.Endpoints)
	}
}

func TestLoadMissing(t *testing.T) {
	_, err := Load("/no/such/sltvd.yaml")
	if !errors.Is(err, ErrFileNotFound) {
		t.Errorf("expected ErrFileNotFound, got %v", err)
	}
}

func TestEnvOverrides(t *testing.T) {
	t.Setenv("SLTV_LISTEN_UNIX", "/run/x.sock")
	t.Setenv("SLTV_DEFAULT_VG", "envvg")
	t.Setenv("SLTV_CLUSTER_ENABLED", "true")
	t.Setenv("SLTV_ETCD_ENDPOINTS", "http://e1:2379, http://e2:2379")
	t.Setenv("SLTV_NODE_ID", "envnode")
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Listen.Unix != "/run/x.sock" {
		t.Errorf("env override Listen.Unix = %q", cfg.Listen.Unix)
	}
	if cfg.Storage.DefaultVG != "envvg" {
		t.Errorf("env override DefaultVG = %q", cfg.Storage.DefaultVG)
	}
	if !cfg.Cluster.Enabled {
		t.Errorf("expected cluster enabled via env")
	}
	if len(cfg.Cluster.ETCD.Endpoints) != 2 {
		t.Errorf("etcd endpoints = %v", cfg.Cluster.ETCD.Endpoints)
	}
}

func TestValidate(t *testing.T) {
	c := Default()
	c.Listen.Unix = ""
	c.Listen.TCP = ""
	if err := c.Validate(); err == nil {
		t.Errorf("expected error when no listener")
	}

	c = Default()
	c.Listen.Unix = ""
	c.Listen.TCP = "0.0.0.0:7443"
	if err := c.Validate(); err == nil {
		t.Errorf("expected error when TCP listen without TLS")
	}

	c = Default()
	c.Thin.FreeThresholdPercent = 0
	if err := c.Validate(); err == nil {
		t.Errorf("expected error for invalid free threshold")
	}

	c = Default()
	c.Cluster.Enabled = true
	if err := c.Validate(); err == nil {
		t.Errorf("expected error: cluster enabled without endpoints")
	}
}

func TestEffectiveNodeID(t *testing.T) {
	c := Default()
	c.Cluster.NodeID = "explicit"
	if got := c.EffectiveNodeID(); got != "explicit" {
		t.Errorf("EffectiveNodeID = %q, want explicit", got)
	}

	c.Cluster.NodeID = ""
	got := c.EffectiveNodeID()
	if got == "" || strings.Contains(got, " ") {
		t.Errorf("EffectiveNodeID hostname fallback = %q", got)
	}
}
