// Package config loads panel settings from the environment.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Config holds every runtime setting the panel needs. All fields have
// working defaults so the panel boots with an empty environment.
type Config struct {
	ListenAddr    string
	DataDir       string
	Network       string
	NetworkSubnet string
	TelemtImage   string
	// PublicHost overrides the host used in generated tg:// links. Empty
	// means telemt's own external-IP detection supplies it.
	PublicHost   string
	PollInterval time.Duration
	// ReservedPorts may not be assigned to a proxy; Caddy owns them.
	ReservedPorts []int
}

func Load() (Config, error) {
	c := Config{
		ListenAddr:    env("PANEL_LISTEN", ":8080"),
		DataDir:       env("PANEL_DATA_DIR", "/data"),
		Network:       env("MTPANEL_NETWORK", "mtpanel_net"),
		NetworkSubnet: env("MTPANEL_SUBNET", "172.28.0.0/16"),
		TelemtImage:   env("TELEMT_IMAGE", "ghcr.io/telemt/telemt:latest"),
		PublicHost:    env("PANEL_PUBLIC_HOST", ""),
		ReservedPorts: []int{80, 8443},
	}

	d, err := time.ParseDuration(env("PANEL_POLL_INTERVAL", "5s"))
	if err != nil {
		return Config{}, fmt.Errorf("PANEL_POLL_INTERVAL: %w", err)
	}
	if d < time.Second {
		return Config{}, fmt.Errorf("PANEL_POLL_INTERVAL: %v is below the 1s minimum", d)
	}
	c.PollInterval = d

	return c, nil
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// ValidateHostDataDir sanity-checks a resolved host-side data directory (the
// value PANEL_HOST_DATA_DIR ended up as, whatever supplied it) and returns a
// human-readable warning when it looks implausible, or "" when it looks
// fine. It never returns an error — the value is advisory, not fatal, since
// a false positive must never stop the panel from booting — but the point is
// to make a broken bind-mount source loud instead of silent.
//
// The motivating failure (Finding 6 of the pre-merge review): compose's
// PANEL_HOST_DATA_DIR=${PWD}/data works for the documented interactive
// install but collapses to the literal string "/data" if $PWD is unset or
// empty (true under some systemd units and sudo configurations). "/data" is
// non-empty, so the older "PANEL_HOST_DATA_DIR is unset" check never fired,
// yet it is almost never a real host path — it happens to equal the panel's
// own in-container data dir, which is exactly the tell this checks for.
func ValidateHostDataDir(hostDataDir, containerDataDir string) string {
	if hostDataDir == "" {
		return "PANEL_HOST_DATA_DIR is unset; proxy config mounts will use " + containerDataDir + " — set PANEL_HOST_DATA_DIR to this repo's host-side ./data path (see .env.example) or the containers Docker starts will mount an empty, non-existent host directory"
	}
	if !filepath.IsAbs(hostDataDir) {
		return fmt.Sprintf("PANEL_HOST_DATA_DIR=%q is not an absolute path; Docker resolves bind-mount sources on the host and needs one, or proxy containers will fail to start", hostDataDir)
	}
	if hostDataDir == containerDataDir {
		return fmt.Sprintf("PANEL_HOST_DATA_DIR=%q is suspicious: it matches the panel's own in-container data directory, which is what compose's \"${PWD}/data\" default collapses to when $PWD is unset or empty (common under systemd units and some sudo configurations) — this is almost never a real host path; set PANEL_HOST_DATA_DIR explicitly in .env", hostDataDir)
	}
	return ""
}
