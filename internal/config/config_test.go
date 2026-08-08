package config

import (
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	t.Setenv("PANEL_DATA_DIR", "/data")
	c, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if c.ListenAddr != ":8080" {
		t.Errorf("ListenAddr = %q, want %q", c.ListenAddr, ":8080")
	}
	if c.Network != "mtpanel_net" {
		t.Errorf("Network = %q, want %q", c.Network, "mtpanel_net")
	}
	if c.NetworkSubnet != "172.28.0.0/16" {
		t.Errorf("NetworkSubnet = %q, want %q", c.NetworkSubnet, "172.28.0.0/16")
	}
	if c.TelemtImage != "ghcr.io/telemt/telemt:latest" {
		t.Errorf("TelemtImage = %q", c.TelemtImage)
	}
	if c.PollInterval != 5*time.Second {
		t.Errorf("PollInterval = %v, want 5s", c.PollInterval)
	}
	if got, want := len(c.ReservedPorts), 2; got != want {
		t.Fatalf("len(ReservedPorts) = %d, want %d", got, want)
	}
}

func TestLoadOverrides(t *testing.T) {
	t.Setenv("PANEL_DATA_DIR", "/tmp/x")
	t.Setenv("PANEL_LISTEN", ":9000")
	t.Setenv("PANEL_POLL_INTERVAL", "12s")
	t.Setenv("TELEMT_IMAGE", "ghcr.io/telemt/telemt:v1")
	c, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if c.ListenAddr != ":9000" {
		t.Errorf("ListenAddr = %q", c.ListenAddr)
	}
	if c.PollInterval != 12*time.Second {
		t.Errorf("PollInterval = %v", c.PollInterval)
	}
	if c.TelemtImage != "ghcr.io/telemt/telemt:v1" {
		t.Errorf("TelemtImage = %q", c.TelemtImage)
	}
}

func TestLoadRejectsBadInterval(t *testing.T) {
	t.Setenv("PANEL_DATA_DIR", "/data")
	t.Setenv("PANEL_POLL_INTERVAL", "banana")
	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want error for unparseable interval")
	}
}
