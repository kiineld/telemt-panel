package proxy

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/kiineld/telemt-panel/internal/docker"
	"github.com/kiineld/telemt-panel/internal/store"
	"github.com/kiineld/telemt-panel/internal/telemt/client"
)

// Get returns a single proxy by ID.
func (s *Service) Get(ctx context.Context, id string) (store.Proxy, error) {
	return s.deps.Store.GetProxy(ctx, id)
}

// List returns every proxy known to the panel.
func (s *Service) List(ctx context.Context) ([]store.Proxy, error) {
	return s.deps.Store.ListProxies(ctx)
}

// ClientFor builds a control-API client for a running proxy, resolving its
// address on the panel's private network.
func (s *Service) ClientFor(ctx context.Context, p store.Proxy) (TelemtClient, error) {
	if p.ContainerID == "" {
		return nil, fmt.Errorf("proxy %s has no container", p.ID)
	}
	info, err := s.deps.Runtime.Inspect(ctx, p.ContainerID)
	if err != nil {
		return nil, err
	}
	if info.IPAddress == "" {
		return nil, fmt.Errorf("proxy %s has no address on %s", p.ID, s.deps.Cfg.Network)
	}
	return s.deps.NewClient(p, info.IPAddress), nil
}

// Delete removes the container, the config directory and the database row.
// A container that has already vanished (e.g. removed by hand with
// `docker rm`) is not an error — the DB row and config dir still go.
func (s *Service) Delete(ctx context.Context, id string) error {
	p, err := s.deps.Store.GetProxy(ctx, id)
	if err != nil {
		return err
	}

	if p.ContainerID != "" {
		err := s.deps.Runtime.Remove(ctx, p.ContainerID)
		if err != nil && !errors.Is(err, docker.ErrNoSuchContainer) {
			return err
		}
	}
	if err := os.RemoveAll(s.configDir(id)); err != nil {
		return fmt.Errorf("proxy: remove config dir: %w", err)
	}
	// The container and config dir are already gone at this point — an
	// irreversible action. If this commit were lost to caller cancellation,
	// the DB row would survive with a ContainerID pointing at nothing,
	// and Reconcile (State != StateCreating, no matching container) would
	// rebuild a container the operator explicitly asked to delete.
	return s.deps.Store.DeleteProxy(context.WithoutCancel(ctx), id)
}

// LimitsPatch expresses three states per field. A nil outer pointer leaves the
// value alone; a non-nil outer pointer to a nil inner pointer clears it; a
// non-nil outer pointer to a value sets it. AdTag is single-pointer: nil
// leaves it alone, a pointer to "" clears it, and any other value sets it.
type LimitsPatch struct {
	AdTag             *string
	DataQuotaBytes    **uint64
	ExpirationRFC3339 **string
	MaxTCPConns       **int
	MaxUniqueIPs      **int
}

// UpdateLimits applies changes that telemt can hot-reload, with no downtime.
// The config file is written first so the change survives a container
// restart even if the live PATCH fails; the live-apply outcome is then
// recorded in StateMessage rather than failing the call outright.
func (s *Service) UpdateLimits(ctx context.Context, id string, patch LimitsPatch) (store.Proxy, error) {
	p, err := s.deps.Store.GetProxy(ctx, id)
	if err != nil {
		return store.Proxy{}, err
	}

	if patch.AdTag != nil {
		p.AdTag = *patch.AdTag
	}
	if patch.DataQuotaBytes != nil {
		p.DataQuotaBytes = *patch.DataQuotaBytes
	}
	if patch.ExpirationRFC3339 != nil {
		p.ExpirationRFC3339 = *patch.ExpirationRFC3339
	}
	if patch.MaxTCPConns != nil {
		p.MaxTCPConns = *patch.MaxTCPConns
	}
	if patch.MaxUniqueIPs != nil {
		p.MaxUniqueIPs = *patch.MaxUniqueIPs
	}

	// Persist to config.toml first so the change survives a container restart,
	// then hot-apply it to the running process.
	if err := s.writeConfig(p); err != nil {
		return store.Proxy{}, err
	}

	p.StateMessage = ""
	if c, cerr := s.ClientFor(ctx, p); cerr == nil {
		if _, perr := c.PatchUser(ctx, Username, apiPatch(patch)); perr != nil {
			// The file is already correct, so the change lands on next restart.
			p.StateMessage = "limits saved; live apply failed: " + perr.Error()
		}
	} else {
		p.StateMessage = "limits saved; live apply failed: " + cerr.Error()
	}

	// writeConfig above already durably persisted the change to disk; this
	// commit must survive caller cancellation too, or the DB row's cached
	// limit values go stale relative to config.toml until the next
	// successful UpdateLimits call.
	if err := s.deps.Store.UpdateProxy(context.WithoutCancel(ctx), p); err != nil {
		return store.Proxy{}, err
	}
	return p, nil
}

// apiPatch is the single source of the hot-apply payload sent to telemt's
// control API. LimitsPatch's double pointers map directly onto client.Opt's
// tri-state: an unset (nil outer) field is left off apiPatch entirely, while
// a non-nil outer pointer is forwarded via client.From, which itself turns a
// nil inner pointer into an explicit JSON null (clearing the override).
func apiPatch(patch LimitsPatch) client.PatchUser {
	var api client.PatchUser
	if patch.AdTag != nil {
		if *patch.AdTag == "" {
			api.UserAdTag = client.Null[string]()
		} else {
			api.UserAdTag = client.Value(*patch.AdTag)
		}
	}
	if patch.DataQuotaBytes != nil {
		api.DataQuotaBytes = client.From(*patch.DataQuotaBytes)
	}
	if patch.ExpirationRFC3339 != nil {
		api.ExpirationRFC3339 = client.From(*patch.ExpirationRFC3339)
	}
	if patch.MaxTCPConns != nil {
		api.MaxTCPConns = client.From(*patch.MaxTCPConns)
	}
	if patch.MaxUniqueIPs != nil {
		api.MaxUniqueIPs = client.From(*patch.MaxUniqueIPs)
	}
	return api
}

// Recreate applies a port or fake-domain change, which telemt cannot
// hot-reload. The secret is preserved so links only break for the reason the
// operator chose (a new domain), never gratuitously. The new port is
// validated before the old container is destroyed, so a rejected port
// leaves a working proxy running; the check is skipped when the port is
// unchanged.
func (s *Service) Recreate(ctx context.Context, id string, port int, tlsDomain string) (store.Proxy, error) {
	p, err := s.deps.Store.GetProxy(ctx, id)
	if err != nil {
		return store.Proxy{}, err
	}
	if port != p.Port {
		if err := CheckPort(port, s.deps.Cfg.ReservedPorts); err != nil {
			return store.Proxy{}, err
		}
	}
	if tlsDomain == "" {
		return store.Proxy{}, errors.New("proxy: fake domain is required")
	}

	old := p.ContainerID
	p.State = store.StateRecreating
	// Nothing destructive has happened yet — the old container is still
	// running untouched. If this commit is lost to cancellation, Recreate
	// simply returns an error with the row exactly as it was; a caller who
	// hung up gains nothing from a commit that outlives them here, and there
	// is no partial, irreversible state to protect. Left on the live ctx
	// deliberately.
	if err := s.deps.Store.UpdateProxy(ctx, p); err != nil {
		return store.Proxy{}, err
	}

	if old != "" {
		if err := s.deps.Runtime.Remove(ctx, old); err != nil && !errors.Is(err, docker.ErrNoSuchContainer) {
			return store.Proxy{}, err
		}
	}

	p.Port, p.TLSDomain, p.ContainerID = port, tlsDomain, ""
	// The old container was just destroyed above — irreversible. If this
	// commit were lost to cancellation, the DB row would keep reporting the
	// OLD port/domain/container while the real container is gone; Reconcile
	// would then rebuild on the stale OLD port, silently reverting the
	// change the operator asked for. Must survive cancellation.
	if err := s.deps.Store.UpdateProxy(context.WithoutCancel(ctx), p); err != nil {
		return store.Proxy{}, err
	}
	if err := s.writeConfig(p); err != nil {
		return store.Proxy{}, err
	}
	return s.startContainer(ctx, p)
}

// startContainer creates and starts a container for an existing proxy row and
// records the outcome. Shared by Recreate and Reconcile.
//
// All three Store.UpdateProxy calls below use context.WithoutCancel(ctx).
// Recreate's caller has already destroyed the old container by the time this
// runs (irreversible), so every outcome recorded here — success or error —
// must survive the caller's context being cancelled (e.g. an HTTP client
// disconnecting during the up-to-30s health wait), exactly like Create's
// terminal commit (see commit c1748fe). Losing any of these to cancellation
// leaves the row stuck at StateRecreating with no path back except a manual
// fix or a later Reconcile pass. The Runtime.Create/Start/waitHealthy calls
// themselves stay on the live ctx so a cancelled caller still aborts those
// promptly — only the bookkeeping of the outcome is protected.
func (s *Service) startContainer(ctx context.Context, p store.Proxy) (store.Proxy, error) {
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
		p.State, p.StateMessage = store.StateError, err.Error()
		_ = s.deps.Store.UpdateProxy(context.WithoutCancel(ctx), p)
		return store.Proxy{}, err
	}
	p.ContainerID = id

	if err := s.deps.Runtime.Start(ctx, id); err != nil {
		p.State, p.StateMessage = store.StateError, err.Error()
		_ = s.deps.Store.UpdateProxy(context.WithoutCancel(ctx), p)
		return store.Proxy{}, err
	}

	if err := s.waitHealthy(ctx, p); err != nil {
		p.State, p.StateMessage = store.StateError, err.Error()
	} else {
		p.State, p.StateMessage = store.StateRunning, ""
	}
	// This is the call the Important finding named directly: waitHealthy can
	// return ctx.Err() when the caller disconnects mid-poll, and this commit
	// must land regardless — the container (or its absence) is already a
	// fact by this point.
	if err := s.deps.Store.UpdateProxy(context.WithoutCancel(ctx), p); err != nil {
		return store.Proxy{}, err
	}
	return p, nil
}

// Logs returns the tail of a proxy's container logs.
func (s *Service) Logs(ctx context.Context, id string) (string, error) {
	p, err := s.deps.Store.GetProxy(ctx, id)
	if err != nil {
		return "", err
	}
	if p.ContainerID == "" {
		return "", fmt.Errorf("proxy %s has no container", id)
	}
	return s.deps.Runtime.Logs(ctx, p.ContainerID, 200)
}

// ReconcileReport summarizes what Reconcile found and fixed.
type ReconcileReport struct {
	Orphans   []string // container IDs with no matching proxy row
	Restarted []string // proxy IDs whose container was missing and was rebuilt
	CleanedUp []string // proxy IDs abandoned mid-create and removed
}

// Reconcile makes the world match the database after a panel or host
// restart. Three cases are handled per proxy row: a container present but
// with a stale ContainerID gets the row updated; a row stuck in
// StateCreating with no container means the panel died mid-create, so the
// row and its config dir are removed; any other row with no container is
// rebuilt. Panel-labelled containers with no matching row are reported as
// orphans, never deleted automatically.
func (s *Service) Reconcile(ctx context.Context) (ReconcileReport, error) {
	var rep ReconcileReport

	proxies, err := s.deps.Store.ListProxies(ctx)
	if err != nil {
		return rep, err
	}
	containers, err := s.deps.Runtime.List(ctx, map[string]string{LabelManaged: "true"})
	if err != nil {
		return rep, err
	}

	byProxyID := make(map[string]docker.ContainerInfo, len(containers))
	for _, c := range containers {
		byProxyID[c.Labels[LabelProxyID]] = c
	}
	known := make(map[string]bool, len(proxies))
	for _, p := range proxies {
		known[p.ID] = true
	}

	for _, c := range containers {
		if !known[c.Labels[LabelProxyID]] {
			rep.Orphans = append(rep.Orphans, c.ID)
		}
	}

	for _, p := range proxies {
		c, ok := byProxyID[p.ID]
		if ok {
			// Keep the recorded container id honest after a daemon restart.
			// Left on the live ctx deliberately: this is a best-effort,
			// idempotent sync (its own error is already discarded) with no
			// destructive action attached — the real container is fine
			// either way, and a lost update here is corrected by the very
			// same comparison on the next Reconcile pass.
			if p.ContainerID != c.ID {
				p.ContainerID = c.ID
				_ = s.deps.Store.UpdateProxy(ctx, p)
			}
			continue
		}

		if p.State == store.StateCreating {
			// The panel died mid-create; nothing was ever running.
			_ = os.RemoveAll(s.configDir(p.ID))
			// The config dir is already gone above — irreversible. If this
			// commit were lost to cancellation, the row would survive at
			// StateCreating with no config left to ever create a container
			// from, stuck until a manual fix. Must survive cancellation,
			// same reasoning as Delete's terminal DeleteProxy.
			if err := s.deps.Store.DeleteProxy(context.WithoutCancel(ctx), p.ID); err == nil {
				rep.CleanedUp = append(rep.CleanedUp, p.ID)
			}
			continue
		}

		if _, err := s.startContainer(ctx, p); err == nil {
			rep.Restarted = append(rep.Restarted, p.ID)
		}
	}

	return rep, nil
}
