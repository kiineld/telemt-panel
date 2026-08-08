package poller

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/kiineld/telemt-panel/internal/proxy"
	"github.com/kiineld/telemt-panel/internal/store"
	"github.com/kiineld/telemt-panel/internal/telemt/client"
)

type fakeClient struct {
	mu    sync.Mutex
	users []client.UserInfo
	err   error
	calls int
}

func (f *fakeClient) Health(context.Context) error { return nil }
func (f *fakeClient) Users(context.Context) ([]client.UserInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.users, f.err
}
func (f *fakeClient) PatchUser(context.Context, string, client.PatchUser) (client.UserInfo, error) {
	return client.UserInfo{}, nil
}
func (f *fakeClient) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

type fakeSource struct {
	proxies   []store.Proxy
	clients   map[string]*fakeClient
	clientErr error
}

func (s *fakeSource) List(context.Context) ([]store.Proxy, error) { return s.proxies, nil }
func (s *fakeSource) ClientFor(_ context.Context, p store.Proxy) (proxy.TelemtClient, error) {
	if s.clientErr != nil {
		return nil, s.clientErr
	}
	return s.clients[p.ID], nil
}

func TestPollOnceCachesStats(t *testing.T) {
	fc := &fakeClient{users: []client.UserInfo{{
		Username: "user", ActiveUniqueIPs: 4,
		ActiveUniqueIPsList: []string{"1.1.1.1", "2.2.2.2", "3.3.3.3", "4.4.4.4"},
		CurrentConnections:  11, TotalOctets: 999,
		Links: client.UserLinks{TLS: []string{"tg://proxy?x=1"}},
	}}}
	src := &fakeSource{
		proxies: []store.Proxy{{ID: "a"}},
		clients: map[string]*fakeClient{"a": fc},
	}

	p := New(src, time.Second)
	p.PollOnce(context.Background())

	got, ok := p.Get("a")
	if !ok {
		t.Fatal("Get(a) not found")
	}
	if !got.OK {
		t.Errorf("OK = false, Err = %q", got.Err)
	}
	if got.UniqueIPs != 4 {
		t.Errorf("UniqueIPs = %d, want 4", got.UniqueIPs)
	}
	if len(got.IPs) != 4 {
		t.Errorf("len(IPs) = %d, want 4", len(got.IPs))
	}
	if got.Connections != 11 {
		t.Errorf("Connections = %d, want 11", got.Connections)
	}
	if got.TotalOctets != 999 {
		t.Errorf("TotalOctets = %d, want 999", got.TotalOctets)
	}
	if len(got.Links) != 1 || got.Links[0] != "tg://proxy?x=1" {
		t.Errorf("Links = %v", got.Links)
	}
	if got.At.IsZero() {
		t.Error("At should be stamped")
	}
}

func TestPollOnceRecordsFailure(t *testing.T) {
	src := &fakeSource{
		proxies: []store.Proxy{{ID: "a"}},
		clients: map[string]*fakeClient{"a": {err: errors.New("connection refused")}},
	}
	p := New(src, time.Second)
	p.PollOnce(context.Background())

	got, ok := p.Get("a")
	if !ok {
		t.Fatal("Get(a) not found")
	}
	if got.OK {
		t.Error("OK = true, want false")
	}
	if got.Err == "" {
		t.Error("Err should describe the failure")
	}
}

func TestPollOnceFlagsEmptyUserListAsOKWithWarning(t *testing.T) {
	// A reachable control API with no error but zero users must not look
	// like a healthy, idle proxy: OK stays true (so the failure/backoff
	// counter does not creep up and lock it out of retrying), but Err must
	// carry a non-fatal note so this state is distinguishable from a normal
	// healthy poll.
	src := &fakeSource{
		proxies: []store.Proxy{{ID: "a"}},
		clients: map[string]*fakeClient{"a": {}}, // no err, no users
	}
	p := New(src, time.Second)
	p.PollOnce(context.Background())

	got, ok := p.Get("a")
	if !ok {
		t.Fatal("Get(a) not found")
	}
	if !got.OK {
		t.Error("OK = false, want true — a reachable API is not a failure")
	}
	if got.Err == "" {
		t.Error("Err should be non-empty when telemt reports no users, so this is distinguishable from a healthy idle proxy")
	}
}

func TestPollOnceHandlesUnreachableClient(t *testing.T) {
	src := &fakeSource{
		proxies:   []store.Proxy{{ID: "a"}},
		clientErr: errors.New("no such container"),
	}
	p := New(src, time.Second)
	p.PollOnce(context.Background())

	got, _ := p.Get("a")
	if got.OK {
		t.Error("OK = true, want false when no client can be built")
	}
}

func TestBackoffAfterRepeatedFailures(t *testing.T) {
	fc := &fakeClient{err: errors.New("down")}
	src := &fakeSource{
		proxies: []store.Proxy{{ID: "a"}},
		clients: map[string]*fakeClient{"a": fc},
	}
	p := New(src, time.Second)

	// Sweeps 1-3 all attempt; from sweep 4 on, only every 6th sweep attempts.
	for i := 0; i < 3; i++ {
		p.PollOnce(context.Background())
	}
	if fc.callCount() != 3 {
		t.Fatalf("calls after 3 sweeps = %d, want 3", fc.callCount())
	}

	for i := 0; i < 5; i++ {
		p.PollOnce(context.Background())
	}
	if got := fc.callCount(); got != 3 {
		t.Errorf("calls after 5 more sweeps = %d, want 3 — backoff should skip them", got)
	}

	p.PollOnce(context.Background())
	if got := fc.callCount(); got != 4 {
		t.Errorf("calls on the 6th backoff sweep = %d, want 4", got)
	}
}

func TestBackoffResetsOnSuccess(t *testing.T) {
	fc := &fakeClient{err: errors.New("down")}
	src := &fakeSource{
		proxies: []store.Proxy{{ID: "a"}},
		clients: map[string]*fakeClient{"a": fc},
	}
	p := New(src, time.Second)
	for i := 0; i < 3; i++ {
		p.PollOnce(context.Background())
	}

	fc.mu.Lock()
	fc.err = nil
	fc.mu.Unlock()

	// Advance to the sweep where backoff lets it retry.
	for i := 0; i < 6; i++ {
		p.PollOnce(context.Background())
	}
	if got, _ := p.Get("a"); !got.OK {
		t.Fatal("proxy should recover after a successful poll")
	}

	before := fc.callCount()
	p.PollOnce(context.Background())
	if fc.callCount() != before+1 {
		t.Error("polling should return to every sweep after a success")
	}
}

func TestSubscribeNotifiesOnSweep(t *testing.T) {
	src := &fakeSource{
		proxies: []store.Proxy{{ID: "a"}},
		clients: map[string]*fakeClient{"a": {}},
	}
	p := New(src, time.Second)

	ch, cancel := p.Subscribe()
	defer cancel()

	p.PollOnce(context.Background())
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("subscriber was not notified within 1s")
	}
}

func TestSubscribeCancelStopsNotifications(t *testing.T) {
	src := &fakeSource{proxies: []store.Proxy{{ID: "a"}}, clients: map[string]*fakeClient{"a": {}}}
	p := New(src, time.Second)

	ch, cancel := p.Subscribe()
	cancel()
	p.PollOnce(context.Background())

	select {
	case _, open := <-ch:
		if open {
			t.Error("cancelled subscriber received a notification")
		}
	default:
	}
}

func TestAllReturnsACopy(t *testing.T) {
	src := &fakeSource{proxies: []store.Proxy{{ID: "a"}}, clients: map[string]*fakeClient{"a": {}}}
	p := New(src, time.Second)
	p.PollOnce(context.Background())

	m := p.All()
	delete(m, "a")
	if _, ok := p.Get("a"); !ok {
		t.Error("mutating the map from All() affected the poller's state")
	}
}

func TestRunStopsOnContextCancel(t *testing.T) {
	src := &fakeSource{proxies: []store.Proxy{{ID: "a"}}, clients: map[string]*fakeClient{"a": {}}}
	p := New(src, 10*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { p.Run(ctx); close(done) }()

	time.Sleep(30 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not return after context cancellation")
	}
}
