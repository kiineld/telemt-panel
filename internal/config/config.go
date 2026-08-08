// Package config loads panel settings from the environment.
package config

import (
	"fmt"
	"os"
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
