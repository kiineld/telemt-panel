package proxy

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kiineld/telemt-panel/internal/config"
	"github.com/kiineld/telemt-panel/internal/docker"
	"github.com/kiineld/telemt-panel/internal/store"
	"github.com/kiineld/telemt-panel/internal/telemt/client"
)

// stubClient is a TelemtClient whose Health result the test controls.
type stubClient struct {
	healthErr error
	users     []client.UserInfo
}

func (s *stubClient) Health(context.Context) error { return s.healthErr }
func (s *stubClient) Users(context.Context) ([]client.UserInfo, error) {
	return s.users, nil
}
func (s *stubClient) PatchUser(context.Context, string, client.PatchUser) (client.UserInfo, error) {
	return client.UserInfo{}, nil
}

func newService(t *testing.T, fake *docker.Fake, stub *stubClient) (*Service, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "panel.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	svc := New(Deps{
		Store:        st,
		Runtime:      fake,
		Cfg:          config.Config{DataDir: dir, Network: "mtpanel_net", NetworkSubnet: "172.28.0.0/16", TelemtImage: "img", PublicHost: "1.2.3.4", ReservedPorts: []int{80, 8443}},
		HostDataDir:  dir,
		NewClient:    func(store.Proxy, string) TelemtClient { return stub },
		Now:          time.Now,
		HealthBudget: 50 * time.Millisecond,
	})
	return svc, dir
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	p := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return p
}

func TestCreateHappyPath(t *testing.T) {
	fake := docker.NewFake()
	svc, dir := newService(t, fake, &stubClient{})
	port := freePort(t)

	p, err := svc.Create(context.Background(), CreateRequest{
		Name: "main", Port: port, TLSDomain: "petrovich.ru",
		AdTag: "ffeeddccbbaa99887766554433221100",
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	if p.State != store.StateRunning {
		t.Errorf("State = %q, want running", p.State)
	}
	if len(p.Secret) != 32 {
		t.Errorf("Secret = %q, want 32 hex chars", p.Secret)
	}
	if p.APIToken == "" {
		t.Error("APIToken should be generated")
	}
	if p.ContainerID == "" {
		t.Error("ContainerID should be recorded")
	}

	// Config file written where the container will bind-mount it.
	cfgPath := filepath.Join(dir, "proxies", p.ID, "config.toml")
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read rendered config: %v", err)
	}
	body := string(raw)
	if !strings.Contains(body, p.Secret) {
		t.Error("rendered config is missing the generated secret")
	}
	if !strings.Contains(body, "ffeeddccbbaa99887766554433221100") {
		t.Error("rendered config is missing the ad tag")
	}
	if !strings.Contains(body, `whitelist = ["172.28.0.0/16"]`) {
		t.Errorf("rendered config has the wrong API whitelist:\n%s", body)
	}

	// Container spec is correct.
	if len(fake.Created) != 1 {
		t.Fatalf("len(fake.Created) = %d, want 1", len(fake.Created))
	}
	spec := fake.Created[0]
	if spec.Port != port {
		t.Errorf("spec.Port = %d, want %d", spec.Port, port)
	}
	if spec.Labels["mtpanel.proxy_id"] != p.ID {
		t.Errorf("spec.Labels[mtpanel.proxy_id] = %q, want %q", spec.Labels["mtpanel.proxy_id"], p.ID)
	}
	if spec.Labels["mtpanel.managed"] != "true" {
		t.Error("container is missing the mtpanel.managed label")
	}
	if spec.ConfigHostDir != filepath.Join(dir, "proxies", p.ID) {
		t.Errorf("spec.ConfigHostDir = %q", spec.ConfigHostDir)
	}
	if spec.Name != "telemt-"+p.ID {
		t.Errorf("spec.Name = %q, want telemt-%s", spec.Name, p.ID)
	}

	if got := fake.Networks["mtpanel_net"]; got != "172.28.0.0/16" {
		t.Errorf("network subnet = %q", got)
	}
}

func TestCreateRejectsReservedPort(t *testing.T) {
	fake := docker.NewFake()
	svc, _ := newService(t, fake, &stubClient{})

	_, err := svc.Create(context.Background(), CreateRequest{
		Name: "x", Port: 8443, TLSDomain: "a.com",
	})
	if !errors.Is(err, ErrPortReserved) {
		t.Fatalf("Create() error = %v, want ErrPortReserved", err)
	}
	if fake.Count() != 0 {
		t.Errorf("fake.Count() = %d, want 0 — nothing should be created", fake.Count())
	}
}

func TestCreateRejectsDuplicatePort(t *testing.T) {
	fake := docker.NewFake()
	svc, _ := newService(t, fake, &stubClient{})
	port := freePort(t)
	ctx := context.Background()

	if _, err := svc.Create(ctx, CreateRequest{Name: "a", Port: port, TLSDomain: "a.com"}); err != nil {
		t.Fatalf("first Create() error = %v", err)
	}
	// The first proxy's container is a fake and binds nothing, so the host
	// bind check passes; the database must be what rejects this.
	_, err := svc.Create(ctx, CreateRequest{Name: "b", Port: port, TLSDomain: "b.com"})
	if !errors.Is(err, store.ErrPortTaken) {
		t.Fatalf("second Create() error = %v, want ErrPortTaken", err)
	}
	if fake.Count() != 1 {
		t.Errorf("fake.Count() = %d, want 1", fake.Count())
	}
}

func TestCreateRollsBackOnContainerCreateFailure(t *testing.T) {
	fake := docker.NewFake()
	fake.FailCreate = errors.New("no such image")
	svc, dir := newService(t, fake, &stubClient{})

	_, err := svc.Create(context.Background(), CreateRequest{
		Name: "x", Port: freePort(t), TLSDomain: "a.com",
	})
	if err == nil {
		t.Fatal("Create() error = nil, want failure")
	}

	proxies, _ := svc.deps.Store.ListProxies(context.Background())
	if len(proxies) != 0 {
		t.Errorf("store has %d proxies, want 0 after rollback", len(proxies))
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "proxies"))
	if len(entries) != 0 {
		t.Errorf("config dir has %d entries, want 0 after rollback", len(entries))
	}
}

func TestCreateRollsBackOnStartFailure(t *testing.T) {
	fake := docker.NewFake()
	fake.FailStart = errors.New("port already allocated")
	svc, dir := newService(t, fake, &stubClient{})

	_, err := svc.Create(context.Background(), CreateRequest{
		Name: "x", Port: freePort(t), TLSDomain: "a.com",
	})
	if err == nil {
		t.Fatal("Create() error = nil, want failure")
	}
	if fake.Count() != 0 {
		t.Errorf("fake.Count() = %d, want 0 — the container should be removed", fake.Count())
	}
	proxies, _ := svc.deps.Store.ListProxies(context.Background())
	if len(proxies) != 0 {
		t.Errorf("store has %d proxies, want 0", len(proxies))
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "proxies"))
	if len(entries) != 0 {
		t.Errorf("config dir has %d entries, want 0", len(entries))
	}
}

// A container that starts but never becomes healthy is kept, not rolled back,
// so the operator can read its logs.
func TestCreateKeepsContainerOnHealthTimeout(t *testing.T) {
	fake := docker.NewFake()
	svc, _ := newService(t, fake, &stubClient{healthErr: errors.New("connection refused")})

	p, err := svc.Create(context.Background(), CreateRequest{
		Name: "x", Port: freePort(t), TLSDomain: "a.com",
	})
	if err != nil {
		t.Fatalf("Create() should not return an error for an unhealthy container, got %v", err)
	}
	if p.State != store.StateError {
		t.Errorf("State = %q, want error", p.State)
	}
	if p.StateMessage == "" {
		t.Error("StateMessage should explain the health failure")
	}
	if fake.Count() != 1 {
		t.Errorf("fake.Count() = %d, want 1 — the container must be kept for log inspection", fake.Count())
	}
}

func TestLink(t *testing.T) {
	fake := docker.NewFake()
	svc, _ := newService(t, fake, &stubClient{})
	p := store.Proxy{
		Port: 443, TLSDomain: "petrovich.ru",
		Secret: "00112233445566778899aabbccddeeff",
	}
	want := "tg://proxy?server=1.2.3.4&port=443&secret=ee00112233445566778899aabbccddeeff706574726f766963682e7275"
	if got := svc.Link(p); got != want {
		t.Errorf("Link() =\n  %q\nwant\n  %q", got, want)
	}
}
