// Package poller keeps a cache of live per-proxy statistics.
//
// A single loop polls every proxy, so browser count does not affect load on
// telemt. Failing proxies back off so a dead container does not dominate the
// sweep.
package poller

import (
	"context"
	"sync"
	"time"

	"github.com/kiineld/telemt-panel/internal/proxy"
	"github.com/kiineld/telemt-panel/internal/store"
)

// failuresBeforeBackoff is how many consecutive errors a proxy may have before
// the poller starts skipping sweeps for it.
const failuresBeforeBackoff = 3

// backoffEvery means a backed-off proxy is retried on one sweep in six.
const backoffEvery = 6

type Snapshot struct {
	ProxyID     string
	OK          bool
	Err         string
	UniqueIPs   int
	IPs         []string
	Connections uint64
	TotalOctets uint64
	Links       []string
	At          time.Time
}

// Source is the slice of proxy.Service the poller needs.
type Source interface {
	List(ctx context.Context) ([]store.Proxy, error)
	ClientFor(ctx context.Context, p store.Proxy) (proxy.TelemtClient, error)
}

type Poller struct {
	src      Source
	interval time.Duration

	mu        sync.RWMutex
	snapshots map[string]Snapshot
	failures  map[string]int
	// skips counts consecutive sweeps a backed-off proxy has been skipped
	// since its last real attempt. It resets to 0 on every attempt (success
	// or failure) and is compared against backoffEvery-1, so a proxy in
	// backoff is skipped for 5 sweeps and attempted on the 6th.
	skips map[string]int

	subMu sync.Mutex
	subs  map[chan struct{}]struct{}
}

func New(src Source, interval time.Duration) *Poller {
	return &Poller{
		src:       src,
		interval:  interval,
		snapshots: map[string]Snapshot{},
		failures:  map[string]int{},
		skips:     map[string]int{},
		subs:      map[chan struct{}]struct{}{},
	}
}

// Run polls until ctx is cancelled.
func (p *Poller) Run(ctx context.Context) {
	t := time.NewTicker(p.interval)
	defer t.Stop()

	p.PollOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.PollOnce(ctx)
		}
	}
}

// PollOnce performs one sweep across all proxies.
func (p *Poller) PollOnce(ctx context.Context) {
	proxies, err := p.src.List(ctx)
	if err != nil {
		return
	}

	// Decide, per proxy, whether this sweep skips it because it is in
	// backoff. A proxy enters backoff once it has failuresBeforeBackoff
	// consecutive failures; it is then skipped for backoffEvery-1 sweeps and
	// attempted again on the next one, at which point skips resets.
	p.mu.Lock()
	skip := make(map[string]bool, len(proxies))
	for _, pr := range proxies {
		if p.failures[pr.ID] >= failuresBeforeBackoff && p.skips[pr.ID] < backoffEvery-1 {
			p.skips[pr.ID]++
			skip[pr.ID] = true
		}
	}
	p.mu.Unlock()

	var wg sync.WaitGroup
	results := make(chan Snapshot, len(proxies))

	for _, pr := range proxies {
		if skip[pr.ID] {
			continue
		}
		wg.Add(1)
		go func(pr store.Proxy) {
			defer wg.Done()
			results <- p.pollProxy(ctx, pr)
		}(pr)
	}
	wg.Wait()
	close(results)

	p.mu.Lock()
	for snap := range results {
		p.snapshots[snap.ProxyID] = snap
		// A real attempt just happened, so restart the skip count.
		p.skips[snap.ProxyID] = 0
		if snap.OK {
			delete(p.failures, snap.ProxyID)
		} else {
			p.failures[snap.ProxyID]++
		}
	}
	// Drop cache entries for proxies that no longer exist.
	live := make(map[string]bool, len(proxies))
	for _, pr := range proxies {
		live[pr.ID] = true
	}
	for id := range p.snapshots {
		if !live[id] {
			delete(p.snapshots, id)
			delete(p.failures, id)
			delete(p.skips, id)
		}
	}
	p.mu.Unlock()

	p.notify()
}

func (p *Poller) pollProxy(ctx context.Context, pr store.Proxy) Snapshot {
	snap := Snapshot{ProxyID: pr.ID, At: time.Now()}

	c, err := p.src.ClientFor(ctx, pr)
	if err != nil {
		snap.Err = err.Error()
		return snap
	}

	users, err := c.Users(ctx)
	if err != nil {
		snap.Err = err.Error()
		return snap
	}

	// A reachable control API that reports zero users is still a successful
	// poll (OK stays true, so the failure/backoff counter does not creep up
	// and lock the proxy out of retrying) — but it is never a transient
	// "still starting up" state: the proxy's one user is written into
	// config.toml before the container starts, and waitHealthy only calls
	// Health(), never Users(). So an empty list here means something is
	// actually wrong — config drift, a telemt bug, or the user table being
	// lost out-of-band (e.g. telemt restarted and forgot its state while its
	// API stayed up). Surface that in Err so it reads as "reachable, but
	// something is wrong" rather than being indistinguishable from a
	// healthy, idle proxy.
	snap.OK = true
	if len(users) == 0 {
		snap.Err = "telemt reported no users for this proxy (expected exactly one) — check for config drift or lost state"
		return snap
	}
	u := users[0]
	snap.UniqueIPs = u.ActiveUniqueIPs
	snap.IPs = u.ActiveUniqueIPsList
	snap.Connections = u.CurrentConnections
	snap.TotalOctets = u.TotalOctets
	snap.Links = u.Links.TLS
	return snap
}

func (p *Poller) Get(proxyID string) (Snapshot, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	s, ok := p.snapshots[proxyID]
	return s, ok
}

// All returns a copy, so callers can range over it without holding the lock
// and without racing the poll loop's writes.
func (p *Poller) All() map[string]Snapshot {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make(map[string]Snapshot, len(p.snapshots))
	for k, v := range p.snapshots {
		out[k] = v
	}
	return out
}

// Subscribe returns a channel that receives a token after every sweep, plus a
// function to unsubscribe. The channel is buffered and sends are dropped when
// full, so a slow reader never blocks the poll loop. The returned cancel func
// is idempotent.
func (p *Poller) Subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)

	p.subMu.Lock()
	p.subs[ch] = struct{}{}
	p.subMu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			p.subMu.Lock()
			delete(p.subs, ch)
			p.subMu.Unlock()
			close(ch)
		})
	}
	return ch, cancel
}

func (p *Poller) notify() {
	p.subMu.Lock()
	defer p.subMu.Unlock()
	for ch := range p.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}
