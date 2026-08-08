// Package proxy is the panel's service layer. It is the only place that
// coordinates the store, the container runtime and telemt's control API.
package proxy

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"

	"github.com/kiineld/telemt-panel/internal/config"
	"github.com/kiineld/telemt-panel/internal/docker"
	"github.com/kiineld/telemt-panel/internal/store"
	"github.com/kiineld/telemt-panel/internal/telemt/client"
	"github.com/kiineld/telemt-panel/internal/telemt/link"
	"github.com/kiineld/telemt-panel/internal/telemt/tconfig"
)

// Username is the telemt user name inside every proxy container. One proxy
// holds exactly one user, so this never varies.
const Username = "user"

// HealthBudget is how long Create waits for a new container's control API to
// answer before giving up and marking the proxy as errored. Deps.HealthBudget
// overrides this per Service, mainly so tests can run fast.
const HealthBudget = 30 * time.Second

const (
	LabelManaged = "mtpanel.managed"
	LabelProxyID = "mtpanel.proxy_id"
)

// TelemtClient is the subset of the control API the service uses.
type TelemtClient interface {
	Health(ctx context.Context) error
	Users(ctx context.Context) ([]client.UserInfo, error)
	PatchUser(ctx context.Context, username string, p client.PatchUser) (client.UserInfo, error)
}

type Deps struct {
	Store   *store.Store
	Runtime docker.Runtime
	Cfg     config.Config
	// HostDataDir is the data directory as the *host* sees it, which is what
	// bind mounts must use. Inside the panel container Cfg.DataDir is /data,
	// but Docker resolves mount sources on the host.
	HostDataDir string
	// NewClient builds a control-API client for a proxy. Overridden in tests.
	NewClient func(p store.Proxy, ip string) TelemtClient
	// Now is injected for deterministic tests.
	Now func() time.Time
	// HealthBudget overrides the HealthBudget constant. Defaulted in New
	// when zero; tests set it low so waitHealthy's polling loop is fast.
	HealthBudget time.Duration
}

type Service struct{ deps Deps }

func New(d Deps) *Service {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.NewClient == nil {
		d.NewClient = func(p store.Proxy, ip string) TelemtClient {
			return client.New("http://"+ip+":9091", p.APIToken)
		}
	}
	if d.HostDataDir == "" {
		d.HostDataDir = d.Cfg.DataDir
	}
	if d.HealthBudget == 0 {
		d.HealthBudget = HealthBudget
	}
	return &Service{deps: d}
}

type CreateRequest struct {
	Name              string
	Port              int
	TLSDomain         string
	AdTag             string
	DataQuotaBytes    *uint64
	ExpirationRFC3339 *string
	MaxTCPConns       *int
	MaxUniqueIPs      *int
}

// Create builds a proxy through a saga: every completed step registers a
// compensating action, and any failure before the container starts unwinds
// them all. Once the container is running it is kept even if it never becomes
// healthy, so its logs remain readable.
func (s *Service) Create(ctx context.Context, req CreateRequest) (store.Proxy, error) {
	if err := CheckPort(req.Port, s.deps.Cfg.ReservedPorts); err != nil {
		return store.Proxy{}, err
	}
	if req.TLSDomain == "" {
		return store.Proxy{}, errors.New("proxy: fake domain is required")
	}
	if req.Name == "" {
		req.Name = fmt.Sprintf("proxy-%d", req.Port)
	}

	secret, err := randomHex(16)
	if err != nil {
		return store.Proxy{}, err
	}
	token, err := randomHex(32)
	if err != nil {
		return store.Proxy{}, err
	}

	p := store.Proxy{
		ID: uuid.NewString(), Name: req.Name, Port: req.Port,
		TLSDomain: req.TLSDomain, AdTag: req.AdTag,
		Secret: secret, APIToken: token, State: store.StateCreating,
		DataQuotaBytes: req.DataQuotaBytes, ExpirationRFC3339: req.ExpirationRFC3339,
		MaxTCPConns: req.MaxTCPConns, MaxUniqueIPs: req.MaxUniqueIPs,
	}

	var undo []func()
	rollback := func() {
		for i := len(undo) - 1; i >= 0; i-- {
			undo[i]()
		}
	}

	// 1. Claim the port in the database. This is the authoritative check.
	if err := s.deps.Store.CreateProxy(ctx, p); err != nil {
		return store.Proxy{}, err
	}
	undo = append(undo, func() {
		_ = s.deps.Store.DeleteProxy(context.WithoutCancel(ctx), p.ID)
	})

	// 2. Write config.toml into the directory the container will mount.
	if err := s.writeConfig(p); err != nil {
		rollback()
		return store.Proxy{}, err
	}
	undo = append(undo, func() { _ = os.RemoveAll(s.configDir(p.ID)) })

	// 3. Make sure the private network and the image exist.
	if err := s.deps.Runtime.EnsureNetwork(ctx, s.deps.Cfg.Network, s.deps.Cfg.NetworkSubnet); err != nil {
		rollback()
		return store.Proxy{}, err
	}
	if err := s.deps.Runtime.Pull(ctx, s.deps.Cfg.TelemtImage); err != nil {
		rollback()
		return store.Proxy{}, err
	}

	// 4. Create the container.
	id, err := s.deps.Runtime.Create(ctx, docker.ContainerSpec{
		Name:  "telemt-" + p.ID,
		Image: s.deps.Cfg.TelemtImage,
		Labels: map[string]string{
			LabelManaged: "true",
			LabelProxyID: p.ID,
		},
		ConfigHostDir: s.hostConfigDir(p.ID),
		Port:          p.Port,
		Network:       s.deps.Cfg.Network,
	})
	if err != nil {
		rollback()
		return store.Proxy{}, err
	}
	undo = append(undo, func() {
		_ = s.deps.Runtime.Remove(context.WithoutCancel(ctx), id)
	})
	p.ContainerID = id

	// 5. Start it.
	if err := s.deps.Runtime.Start(ctx, id); err != nil {
		rollback()
		return store.Proxy{}, wrapPortConflict(p.Port, err)
	}

	// Past this point the container stays even on failure.
	if err := s.waitHealthy(ctx, p); err != nil {
		p.State = store.StateError
		p.StateMessage = err.Error()
	} else {
		p.State = store.StateRunning
		p.StateMessage = ""
	}
	// This commit must survive caller cancellation: waitHealthy can return
	// ctx.Err() if the caller disconnects mid-poll, but the container is
	// already kept past this point, and Create has promised a nil error for
	// that case. Committing against the live ctx here would both break that
	// promise and — worse — leave the row stuck at StateCreating, which
	// Reconcile treats as an abandoned create and deletes, orphaning a
	// perfectly live container.
	if err := s.deps.Store.UpdateProxy(context.WithoutCancel(ctx), p); err != nil {
		return store.Proxy{}, err
	}
	return p, nil
}

// Link returns the tg:// fake-TLS link for a proxy, computed locally so it
// can be shown before the container is healthy. It always returns something
// — falling back to the literal placeholder host "SERVER-IP" when
// Cfg.PublicHost is unset — because it does not know whether a better,
// telemt-reported value exists; see ReconcileLink, which callers must use
// before presenting a link to an operator.
func (s *Service) Link(p store.Proxy) string {
	host := s.deps.Cfg.PublicHost
	if host == "" {
		host = "SERVER-IP"
	}
	l, err := link.FakeTLS(host, p.Port, p.Secret, p.TLSDomain)
	if err != nil {
		return ""
	}
	return l
}

// ReconcileLink decides which tg:// link to present to an operator, given the
// panel's own locally computed link and telemt's self-reported ones (its
// control API's links.tls[], once the proxy is healthy and the poller has
// picked up a snapshot).
//
// Per the design spec's "Link generation" section: the panel computes the
// link locally so something appears immediately, then once the container is
// healthy reconciles against telemt's own value and prefers it — telemt
// resolves the host itself via its own external-IP detection, which is what
// makes the zero-config (no PANEL_PUBLIC_HOST) install path work at all.
//
// fromTelemt reports whether telemtLinks supplied the result, which callers
// use to decide whether an empty/placeholder local link is still acceptable
// to show (see the web package's linkFor).
func ReconcileLink(local string, telemtLinks []string) (l string, fromTelemt bool) {
	if len(telemtLinks) > 0 && telemtLinks[0] != "" {
		return telemtLinks[0], true
	}
	return local, false
}

func (s *Service) waitHealthy(ctx context.Context, p store.Proxy) error {
	deadline := s.deps.Now().Add(s.deps.HealthBudget)
	var lastErr error

	for s.deps.Now().Before(deadline) {
		info, err := s.deps.Runtime.Inspect(ctx, p.ContainerID)
		if err == nil && info.IPAddress != "" {
			if lastErr = s.deps.NewClient(p, info.IPAddress).Health(ctx); lastErr == nil {
				return nil
			}
		} else if err != nil {
			lastErr = err
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return fmt.Errorf("proxy: control API did not become healthy within %s: %v", s.deps.HealthBudget, lastErr)
}

func (s *Service) writeConfig(p store.Proxy) error {
	dir := s.configDir(p.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("proxy: create config dir: %w", err)
	}

	body, err := tconfig.Render(tconfig.Spec{
		Username: Username, Secret: p.Secret, Port: p.Port,
		TLSDomain: p.TLSDomain, AdTag: p.AdTag,
		APIToken: p.APIToken, APIWhitelist: []string{s.deps.Cfg.NetworkSubnet},
		PublicHost:     s.deps.Cfg.PublicHost,
		DataQuotaBytes: p.DataQuotaBytes, ExpirationRFC3339: p.ExpirationRFC3339,
		MaxTCPConns: p.MaxTCPConns, MaxUniqueIPs: p.MaxUniqueIPs,
	})
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(dir, "config.toml"), body)
}

// writeAtomic writes via a temp file and rename in the same directory, so a
// reader (telemt) never observes a partial config.
func writeAtomic(path, body string) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.toml")
	if err != nil {
		return fmt.Errorf("proxy: temp config: %w", err)
	}
	defer os.Remove(tmp.Name())

	if _, err := tmp.WriteString(body); err != nil {
		tmp.Close()
		return fmt.Errorf("proxy: write config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("proxy: close config: %w", err)
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return fmt.Errorf("proxy: chmod config: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("proxy: rename config: %w", err)
	}
	return nil
}

// configDir is the path as this process sees it.
func (s *Service) configDir(id string) string {
	return filepath.Join(s.deps.Cfg.DataDir, "proxies", id)
}

// hostConfigDir is the same directory as the Docker daemon sees it, which is
// what a bind mount source must be.
func (s *Service) hostConfigDir(id string) string {
	return filepath.Join(s.deps.HostDataDir, "proxies", id)
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("proxy: generate random: %w", err)
	}
	return hex.EncodeToString(b), nil
}
