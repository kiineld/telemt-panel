package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kiineld/telemt-panel/internal/config"
	"github.com/kiineld/telemt-panel/internal/docker"
	"github.com/kiineld/telemt-panel/internal/poller"
	"github.com/kiineld/telemt-panel/internal/proxy"
	"github.com/kiineld/telemt-panel/internal/store"
	"github.com/kiineld/telemt-panel/internal/telemt/client"
)

type okClient struct{}

func (okClient) Health(context.Context) error { return nil }
func (okClient) Users(context.Context) ([]client.UserInfo, error) {
	return []client.UserInfo{{Username: "user", ActiveUniqueIPs: 2, CurrentConnections: 3}}, nil
}
func (okClient) PatchUser(context.Context, string, client.PatchUser) (client.UserInfo, error) {
	return client.UserInfo{}, nil
}

// newTestServerWithFake is the shared test-server constructor: it also
// returns the docker.Fake backing the service, so tests can flip its Fail*
// fields (e.g. FailPing) to exercise degraded-state behavior. newTestServer
// below is the common case that doesn't need the fake.
func newTestServerWithFake(t *testing.T) (http.Handler, *Auth, *proxy.Service, *docker.Fake) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "p.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	fake := docker.NewFake()
	cfg := config.Config{
		DataDir: dir, Network: "n", NetworkSubnet: "172.28.0.0/16",
		TelemtImage: "img", PublicHost: "1.2.3.4",
		ReservedPorts: []int{80, 8443}, PollInterval: time.Hour,
	}
	svc := proxy.New(proxy.Deps{
		Store: st, Runtime: fake, Cfg: cfg, HostDataDir: dir,
		NewClient:    func(store.Proxy, string) proxy.TelemtClient { return okClient{} },
		HealthBudget: 50 * time.Millisecond,
	})
	auth := NewAuth(st)

	h, err := NewServer(ServerDeps{
		Auth: auth, Proxy: svc, Poller: poller.New(svc, time.Hour), Cfg: cfg,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	return h, auth, svc, fake
}

func newTestServer(t *testing.T) (http.Handler, *Auth, *proxy.Service) {
	h, auth, svc, _ := newTestServerWithFake(t)
	return h, auth, svc
}

func loginCookie(t *testing.T, h http.Handler, auth *Auth) *http.Cookie {
	t.Helper()
	pw, err := auth.Bootstrap(context.Background())
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	form := url.Values{"username": {"admin"}, "password": {pw}}
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie {
			return c
		}
	}
	t.Fatalf("no session cookie in response (status %d)", rec.Code)
	return nil
}

func TestHealthzIsPublic(t *testing.T) {
	h, _, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestIndexRedirectsWhenLoggedOut(t *testing.T) {
	h, _, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/login" {
		t.Errorf("Location = %q, want /login", got)
	}
}

func TestLoginSetsHardenedCookie(t *testing.T) {
	h, auth, _ := newTestServer(t)
	c := loginCookie(t, h, auth)
	if !c.HttpOnly {
		t.Error("session cookie must be HttpOnly")
	}
	if c.SameSite != http.SameSiteLaxMode && c.SameSite != http.SameSiteStrictMode {
		t.Error("session cookie must set SameSite")
	}
	if c.Path != "/" {
		t.Errorf("cookie Path = %q, want /", c.Path)
	}
}

func TestFirstLoginForcesPasswordChange(t *testing.T) {
	h, auth, _ := newTestServer(t)
	c := loginCookie(t, h, auth)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/password" {
		t.Fatalf("status = %d, Location = %q; want 303 to /password", rec.Code, rec.Header().Get("Location"))
	}
}

func TestCreateProxyThroughForm(t *testing.T) {
	h, auth, svc := newTestServer(t)
	c := loginCookie(t, h, auth)

	// Clear the forced password change so the app routes normally.
	adm, _ := auth.Session(context.Background(), c.Value)
	if err := auth.ChangePassword(context.Background(), adm.ID, "a-long-password"); err != nil {
		t.Fatalf("ChangePassword: %v", err)
	}

	form := url.Values{
		"name": {"main"}, "port": {"14431"},
		"tls_domain": {"petrovich.ru"},
		"ad_tag":     {"ffeeddccbbaa99887766554433221100"},
	}
	req := httptest.NewRequest(http.MethodPost, "/proxies", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body:\n%s", rec.Code, rec.Body.String())
	}

	proxies, err := svc.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(proxies) != 1 {
		t.Fatalf("len(proxies) = %d, want 1", len(proxies))
	}
	if proxies[0].Port != 14431 || proxies[0].TLSDomain != "petrovich.ru" {
		t.Errorf("created proxy = %+v", proxies[0])
	}
}

func TestCreateProxyRejectsReservedPort(t *testing.T) {
	h, auth, _ := newTestServer(t)
	c := loginCookie(t, h, auth)
	adm, _ := auth.Session(context.Background(), c.Value)
	_ = auth.ChangePassword(context.Background(), adm.ID, "a-long-password")

	form := url.Values{"name": {"x"}, "port": {"8443"}, "tls_domain": {"a.com"}}
	req := httptest.NewRequest(http.MethodPost, "/proxies", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "reserved") {
		t.Errorf("body should explain the port is reserved:\n%s", rec.Body.String())
	}
}

func TestProxyRoutesRequireAuth(t *testing.T) {
	h, _, _ := newTestServer(t)
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/proxies"},
		{http.MethodGet, "/proxies/abc"},
		{http.MethodPost, "/proxies/abc/delete"},
		{http.MethodGet, "/events"},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
		if rec.Code != http.StatusSeeOther && rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s status = %d, want 303 or 401", tc.method, tc.path, rec.Code)
		}
	}
}

func TestStaticAssetsAreServed(t *testing.T) {
	h, _, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/static/vendor/htmx.min.js", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — did you vendor htmx in step 1?", rec.Code)
	}
}
