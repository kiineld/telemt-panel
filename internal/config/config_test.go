package config

import (
	"strings"
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

// TestValidateHostDataDir covers Finding 6: docker-compose.yml's
// PANEL_HOST_DATA_DIR=${PWD}/data collapses to the literal string "/data"
// when $PWD is unset or empty — non-empty, so a plain "is it set" check
// would miss it, but it happens to coincide with the panel's own
// in-container data directory, which is the signal this checks for.
func TestValidateHostDataDir(t *testing.T) {
	cases := []struct {
		name          string
		hostDataDir   string
		containerDir  string
		wantWarning   bool
		wantSubstring string
	}{
		{"unset", "", "/data", true, "unset"},
		{"collapsed to container dir", "/data", "/data", true, "matches the panel's own in-container data directory"},
		{"relative path", "data", "/data", true, "not an absolute path"},
		{"plausible host path", "/opt/telemt-panel/data", "/data", false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ValidateHostDataDir(c.hostDataDir, c.containerDir)
			if c.wantWarning && got == "" {
				t.Fatalf("ValidateHostDataDir(%q, %q) = \"\", want a warning", c.hostDataDir, c.containerDir)
			}
			if !c.wantWarning && got != "" {
				t.Errorf("ValidateHostDataDir(%q, %q) = %q, want no warning for a plausible path", c.hostDataDir, c.containerDir, got)
			}
			if c.wantSubstring != "" && !strings.Contains(got, c.wantSubstring) {
				t.Errorf("ValidateHostDataDir(%q, %q) = %q, want it to mention %q", c.hostDataDir, c.containerDir, got, c.wantSubstring)
			}
		})
	}
}
