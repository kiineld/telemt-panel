//go:build docker

package proxy

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kiineld/telemt-panel/internal/config"
	"github.com/kiineld/telemt-panel/internal/docker"
	"github.com/kiineld/telemt-panel/internal/store"
)

// TestRealDockerLifecycle needs a Docker daemon and network access to
// ghcr.io. It is the only test in the project that exercises the real
// Docker runtime, the real telemt binary and the real rendered config.toml
// together.
//
// Run with: go test -tags docker ./internal/proxy/ -run TestRealDockerLifecycle -v -timeout 10m
func TestRealDockerLifecycle(t *testing.T) {
	rt, err := docker.NewDockerRuntime()
	if err != nil {
		t.Skipf("no docker daemon: %v", err)
	}

	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "panel.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	svc := New(Deps{
		Store: st, Runtime: rt, HostDataDir: dir,
		Cfg: config.Config{
			DataDir: dir, Network: "mtpanel_test_net", NetworkSubnet: "172.29.0.0/16",
			TelemtImage: "ghcr.io/telemt/telemt:latest", PublicHost: "127.0.0.1",
			ReservedPorts: []int{80, 8443},
		},
		// The first run on a cold host has to pull the telemt image, which
		// can take minutes; give the health wait a realistic budget rather
		// than the fast one unit tests use, and keep the overall test
		// timeout (set via -timeout on the go test invocation, see the run
		// command above) comfortably above it.
		HealthBudget: 90 * time.Second,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a free port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()

	p, err := svc.Create(ctx, CreateRequest{
		Name: "integration", Port: port, TLSDomain: "petrovich.ru",
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	t.Cleanup(func() { _ = svc.Delete(context.Background(), p.ID) })

	if p.State != store.StateRunning {
		logs, _ := svc.Logs(ctx, p.ID)
		t.Fatalf("State = %q (%s); container logs:\n%s", p.State, p.StateMessage, logs)
	}

	// The control API answers and reports our single user.
	c, err := svc.ClientFor(ctx, p)
	if err != nil {
		t.Fatalf("ClientFor() error = %v", err)
	}
	users, err := c.Users(ctx)
	if err != nil {
		t.Fatalf("Users() error = %v", err)
	}
	if len(users) != 1 || users[0].Username != Username {
		t.Fatalf("users = %+v, want exactly one named %q", users, Username)
	}

	// telemt's own link agrees with the one we compute locally. The two can
	// legitimately differ in the host portion (telemt may report its
	// autodetected external IP while the panel was told to use 127.0.0.1
	// via PublicHost above), so the comparison that matters is the secret
	// portion, not the whole string — a wrong secret is what breaks a real
	// user's phone, a differing host does not.
	panelLink := svc.Link(p)
	t.Logf("panel link:  %s", panelLink)
	if len(users[0].Links.TLS) == 0 {
		t.Error("telemt returned no TLS links")
	} else {
		telemtLink := users[0].Links.TLS[0]
		t.Logf("telemt link: %s", telemtLink)

		telemtSecret, err := secretParam(telemtLink)
		if err != nil {
			t.Errorf("parse telemt link: %v", err)
		}
		panelSecret, err := secretParam(panelLink)
		if err != nil {
			t.Errorf("parse panel link: %v", err)
		}
		if telemtSecret != "" && panelSecret != "" && telemtSecret != panelSecret {
			t.Errorf("secret mismatch: telemt=%s panel=%s", telemtSecret, panelSecret)
		}
	}

	// The proxy's port is actually listening on the host.
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 5*time.Second)
	if err != nil {
		t.Fatalf("dial proxy port %d: %v", port, err)
	}
	_ = conn.Close()

	// Delete removes the container, the DB row and the config directory.
	if err := svc.Delete(ctx, p.ID); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, err := svc.Get(ctx, p.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Get() after delete error = %v, want ErrNotFound", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "proxies", p.ID)); !os.IsNotExist(err) {
		t.Error("config directory survived deletion")
	}
}

// TestRealDockerLifecycleWithEmptyPublicHost is the case that would have
// caught Finding 1 of the final pre-merge review: the shipped default is an
// EMPTY PANEL_PUBLIC_HOST (docker-compose.yml's
// PANEL_PUBLIC_HOST=${PANEL_PUBLIC_HOST:-}), and on that path the panel must
// end up surfacing a link with a real host — never the literal placeholder
// "SERVER-IP" — by preferring telemt's own self-reported link once the
// container is healthy (ReconcileLink; see also the web package's linkFor).
// TestRealDockerLifecycle above deliberately does not exercise this: it sets
// PublicHost so it can assert the two links' secrets agree while excusing
// host divergence, which can never fail on a wrong/placeholder host.
//
// Run with: go test -tags docker ./internal/proxy/ -run TestRealDockerLifecycleWithEmptyPublicHost -v -timeout 10m
func TestRealDockerLifecycleWithEmptyPublicHost(t *testing.T) {
	rt, err := docker.NewDockerRuntime()
	if err != nil {
		t.Skipf("no docker daemon: %v", err)
	}

	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "panel.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	svc := New(Deps{
		Store: st, Runtime: rt, HostDataDir: dir,
		Cfg: config.Config{
			DataDir: dir, Network: "mtpanel_test_net2", NetworkSubnet: "172.30.0.0/16",
			TelemtImage: "ghcr.io/telemt/telemt:latest",
			// Deliberately unset — the shipped docker-compose.yml default.
			PublicHost:    "",
			ReservedPorts: []int{80, 8443},
		},
		HealthBudget: 90 * time.Second,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a free port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()

	p, err := svc.Create(ctx, CreateRequest{
		Name: "integration-empty-host", Port: port, TLSDomain: "petrovich.ru",
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	t.Cleanup(func() { _ = svc.Delete(context.Background(), p.ID) })

	if p.State != store.StateRunning {
		logs, _ := svc.Logs(ctx, p.ID)
		t.Fatalf("State = %q (%s); container logs:\n%s", p.State, p.StateMessage, logs)
	}

	c, err := svc.ClientFor(ctx, p)
	if err != nil {
		t.Fatalf("ClientFor() error = %v", err)
	}
	users, err := c.Users(ctx)
	if err != nil {
		t.Fatalf("Users() error = %v", err)
	}
	if len(users) != 1 {
		t.Fatalf("users = %+v, want exactly one", users)
	}
	if len(users[0].Links.TLS) == 0 {
		t.Fatal("telemt returned no TLS links; cannot reconcile against an empty PublicHost")
	}
	telemtLink := users[0].Links.TLS[0]
	t.Logf("telemt link: %s", telemtLink)

	// This is what the panel actually shows an operator: the locally
	// computed link (which, with PublicHost empty, is the SERVER-IP
	// placeholder) reconciled against telemt's own reported link.
	localLink := svc.Link(p)
	surfaced, fromTelemt := ReconcileLink(localLink, false, users[0].Links.TLS)
	t.Logf("local link:    %s", localLink)
	t.Logf("surfaced link: %s", surfaced)

	if !fromTelemt {
		t.Error("ReconcileLink() did not prefer telemt's self-reported link with PublicHost unset")
	}
	if strings.Contains(surfaced, "SERVER-IP") {
		t.Errorf("surfaced link = %q, must not contain the literal SERVER-IP placeholder", surfaced)
	}
	if surfaced != telemtLink {
		t.Errorf("surfaced link = %q, want it to agree with telemt's own reported link %q", surfaced, telemtLink)
	}

	if err := svc.Delete(ctx, p.ID); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
}

// secretParam extracts the "secret" query parameter's value from a tg:// or
// https://t.me/proxy link without requiring the two hosts to agree — only
// the ?server= part differs legitimately between telemt's self-reported link
// and the one the panel computes from PublicHost.
func secretParam(link string) (string, error) {
	i := strings.Index(link, "secret=")
	if i == -1 {
		return "", errors.New("no secret param")
	}
	rest := link[i+len("secret="):]
	if j := strings.IndexByte(rest, '&'); j != -1 {
		rest = rest[:j]
	}
	return rest, nil
}
