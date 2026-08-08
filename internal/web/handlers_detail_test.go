package web

import (
	"context"
	"encoding/json"
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

func authedSession(t *testing.T, h http.Handler, auth *Auth) *http.Cookie {
	t.Helper()
	c := loginCookie(t, h, auth)
	adm, _ := auth.Session(context.Background(), c.Value)
	if err := auth.ChangePassword(context.Background(), adm.ID, "a-long-password"); err != nil {
		t.Fatalf("ChangePassword: %v", err)
	}
	return c
}

func TestQRDataURI(t *testing.T) {
	uri, err := qrDataURI("tg://proxy?server=1.2.3.4&port=443&secret=eeff")
	if err != nil {
		t.Fatalf("qrDataURI() error = %v", err)
	}
	if !strings.HasPrefix(uri, "data:image/png;base64,") {
		t.Errorf("uri = %q, want a PNG data URI", uri[:min(40, len(uri))])
	}
	if len(uri) < 200 {
		t.Errorf("data URI is suspiciously short (%d chars)", len(uri))
	}
}

func TestProxyDetailShowsLinkAndIPs(t *testing.T) {
	h, auth, svc := newTestServer(t)
	c := authedSession(t, h, auth)

	p, err := svc.Create(context.Background(), proxy.CreateRequest{
		Name: "main", Port: 14432, TLSDomain: "petrovich.ru",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/proxies/"+p.ID, nil)
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "secret=ee"+p.Secret) {
		t.Error("detail page does not show the fake-TLS link")
	}
	if !strings.Contains(body, "data:image/png;base64,") {
		t.Error("detail page does not show a QR code")
	}
	if !strings.Contains(body, "petrovich.ru") {
		t.Error("detail page does not show the fake domain")
	}
}

func TestProxyDetailUnknownID(t *testing.T) {
	h, auth, _ := newTestServer(t)
	c := authedSession(t, h, auth)

	req := httptest.NewRequest(http.MethodGet, "/proxies/nope", nil)
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestPostLimitsUpdatesQuota(t *testing.T) {
	h, auth, svc := newTestServer(t)
	c := authedSession(t, h, auth)
	p, err := svc.Create(context.Background(), proxy.CreateRequest{
		Name: "m", Port: 14433, TLSDomain: "a.com",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	form := url.Values{"quota_gb": {"5"}, "max_conns": {"100"}}
	req := httptest.NewRequest(http.MethodPost, "/proxies/"+p.ID+"/limits", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body:\n%s", rec.Code, rec.Body.String())
	}

	got, err := svc.Get(context.Background(), p.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.DataQuotaBytes == nil || *got.DataQuotaBytes != 5*1024*1024*1024 {
		t.Errorf("DataQuotaBytes = %v, want 5 GiB", got.DataQuotaBytes)
	}
	if got.MaxTCPConns == nil || *got.MaxTCPConns != 100 {
		t.Errorf("MaxTCPConns = %v, want 100", got.MaxTCPConns)
	}
}

func TestPostLimitsClearsEmptyFields(t *testing.T) {
	h, auth, svc := newTestServer(t)
	c := authedSession(t, h, auth)
	quota := uint64(1 << 30)
	p, err := svc.Create(context.Background(), proxy.CreateRequest{
		Name: "m", Port: 14434, TLSDomain: "a.com", DataQuotaBytes: &quota,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	form := url.Values{"quota_gb": {""}}
	req := httptest.NewRequest(http.MethodPost, "/proxies/"+p.ID+"/limits", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(c)
	h.ServeHTTP(httptest.NewRecorder(), req)

	got, _ := svc.Get(context.Background(), p.ID)
	if got.DataQuotaBytes != nil {
		t.Errorf("DataQuotaBytes = %v, want nil when the field is submitted empty", got.DataQuotaBytes)
	}
}

// limitsPatchRecorder is a TelemtClient stub that records the PatchUser
// payload UpdateLimits actually sends, so tests can inspect the wire format
// rather than just the store-side result.
type limitsPatchRecorder struct{ got client.PatchUser }

func (*limitsPatchRecorder) Health(context.Context) error { return nil }
func (*limitsPatchRecorder) Users(context.Context) ([]client.UserInfo, error) {
	return []client.UserInfo{{Username: "user"}}, nil
}
func (r *limitsPatchRecorder) PatchUser(_ context.Context, _ string, p client.PatchUser) (client.UserInfo, error) {
	r.got = p
	return client.UserInfo{}, nil
}

// TestPostLimitsClearSendsExplicitJSONNull verifies the empty-field-clears
// behavior all the way to the wire, through the real HTTP handler: telemt's
// control API must see an explicit JSON null for data_quota_bytes, not an
// omitted key (which means "leave unchanged" under JSON Merge Patch
// semantics) and not a literal 0 (a real, very restrictive quota, not "no
// quota").
func TestPostLimitsClearSendsExplicitJSONNull(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "p.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	cfg := config.Config{
		DataDir: dir, Network: "n", NetworkSubnet: "172.28.0.0/16",
		TelemtImage: "img", PublicHost: "1.2.3.4",
		ReservedPorts: []int{80, 8443}, PollInterval: time.Hour,
	}
	rec := &limitsPatchRecorder{}
	svc := proxy.New(proxy.Deps{
		Store: st, Runtime: docker.NewFake(), Cfg: cfg, HostDataDir: dir,
		NewClient:    func(store.Proxy, string) proxy.TelemtClient { return rec },
		HealthBudget: 50 * time.Millisecond,
	})
	auth := NewAuth(st)
	h, err := NewServer(ServerDeps{Auth: auth, Proxy: svc, Poller: poller.New(svc, time.Hour), Cfg: cfg})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	c := authedSession(t, h, auth)

	quota := uint64(1 << 30)
	p, err := svc.Create(context.Background(), proxy.CreateRequest{
		Name: "m", Port: 14440, TLSDomain: "a.com", DataQuotaBytes: &quota,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	form := url.Values{"quota_gb": {""}}
	req := httptest.NewRequest(http.MethodPost, "/proxies/"+p.ID+"/limits", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(c)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body:\n%s", rr.Code, rr.Body.String())
	}

	if !rec.got.DataQuotaBytes.IsSet() {
		t.Fatal("patch sent to telemt did not set data_quota_bytes at all — an omitted key means \"leave unchanged\" under JSON Merge Patch, not \"clear\"")
	}
	body, err := json.Marshal(rec.got)
	if err != nil {
		t.Fatalf("marshal sent patch: %v", err)
	}
	if !strings.Contains(string(body), `"data_quota_bytes":null`) {
		t.Errorf("patch sent to telemt = %s, want data_quota_bytes=null (explicit JSON null), not an omitted key or a literal 0", body)
	}
}

func TestPostRecreateChangesDomain(t *testing.T) {
	h, auth, svc := newTestServer(t)
	c := authedSession(t, h, auth)
	p, err := svc.Create(context.Background(), proxy.CreateRequest{
		Name: "m", Port: 14435, TLSDomain: "a.com",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	form := url.Values{"port": {"14436"}, "tls_domain": {"bsi.bund.de"}}
	req := httptest.NewRequest(http.MethodPost, "/proxies/"+p.ID+"/recreate", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body:\n%s", rec.Code, rec.Body.String())
	}
	got, _ := svc.Get(context.Background(), p.ID)
	if got.Port != 14436 || got.TLSDomain != "bsi.bund.de" {
		t.Errorf("after recreate = port %d, domain %q", got.Port, got.TLSDomain)
	}
}

func TestDeleteProxyThroughForm(t *testing.T) {
	h, auth, svc := newTestServer(t)
	c := authedSession(t, h, auth)
	p, err := svc.Create(context.Background(), proxy.CreateRequest{
		Name: "m", Port: 14437, TLSDomain: "a.com",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/proxies/"+p.ID+"/delete", nil)
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	list, _ := svc.List(context.Background())
	if len(list) != 0 {
		t.Errorf("len(proxies) = %d, want 0", len(list))
	}
}

// TestProxyDetailThreeStateStats covers the three states poller.Snapshot
// can be in, per Task 11's pattern: unreachable (!OK), reachable-but-warning
// (OK && Err != ""), and normal (OK && Err == "").
func TestProxyDetailThreeStateStats(t *testing.T) {
	h, auth, svc := newTestServer(t)
	c := authedSession(t, h, auth)

	p, err := svc.Create(context.Background(), proxy.CreateRequest{
		Name: "m", Port: 14439, TLSDomain: "a.com",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/proxies/"+p.ID, nil)
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	// No poller snapshot has been recorded yet, so Stats is the zero value
	// (OK == false): the page must say stats are unavailable, not render
	// zeroed counters as if they were real.
	if !strings.Contains(rec.Body.String(), "unavailable") {
		t.Error("detail page should report stats unavailable before any poll has run")
	}
}
