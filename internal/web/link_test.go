// link_test.go covers Finding 1 of the pre-merge review: the panel must
// prefer telemt's own self-reported tg:// link once one is available, and
// must never present the literal placeholder host "SERVER-IP" as if it were
// a usable, copyable link.
package web

import (
	"context"
	"net/http"
	"net/http/httptest"
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

// linksClient is a TelemtClient stub whose Users() reports whatever
// self-reported tg:// links a test configures, so tests can drive the
// poller snapshot that linkFor reconciles against.
type linksClient struct{ links []string }

func (linksClient) Health(context.Context) error { return nil }
func (c linksClient) Users(context.Context) ([]client.UserInfo, error) {
	return []client.UserInfo{{Username: "user", Links: client.UserLinks{TLS: c.links}}}, nil
}
func (linksClient) PatchUser(context.Context, string, client.PatchUser) (client.UserInfo, error) {
	return client.UserInfo{}, nil
}

// newLinkTestServer builds a test server with a configurable PublicHost and
// TelemtClient, so link-reconciliation tests can control both sides of
// linkFor's decision.
func newLinkTestServer(t *testing.T, publicHost string, cl proxy.TelemtClient) (http.Handler, *Auth, *proxy.Service, *poller.Poller) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "p.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	cfg := config.Config{
		DataDir: dir, Network: "n", NetworkSubnet: "172.28.0.0/16",
		TelemtImage: "img", PublicHost: publicHost,
		ReservedPorts: []int{80, 8443}, PollInterval: time.Hour,
	}
	svc := proxy.New(proxy.Deps{
		Store: st, Runtime: docker.NewFake(), Cfg: cfg, HostDataDir: dir,
		NewClient:    func(store.Proxy, string) proxy.TelemtClient { return cl },
		HealthBudget: 50 * time.Millisecond,
	})
	auth := NewAuth(st)
	pl := poller.New(svc, time.Hour)
	h, err := NewServer(ServerDeps{Auth: auth, Proxy: svc, Poller: pl, Cfg: cfg})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	return h, auth, svc, pl
}

// TestLinkPrefersTelemtsSelfReportedLink covers FIX (a) for the zero-config
// path: with PANEL_PUBLIC_HOST unset, once the poller has a snapshot with a
// non-empty telemt link, both the list row and the detail page (and
// therefore the QR, which is derived from the same link) must show that
// link instead of the SERVER-IP placeholder the panel would otherwise
// compute locally.
func TestLinkPrefersTelemtsSelfReportedLink(t *testing.T) {
	telemtLink := "tg://proxy?server=203.0.113.9&port=443&secret=eeabc"
	// html/template HTML-escapes "&" to "&amp;" wherever the link is
	// rendered (data-link attribute, <pre> body), so assertions on the
	// rendered page must match the escaped form.
	telemtLinkEscaped := "tg://proxy?server=203.0.113.9&amp;port=443&amp;secret=eeabc"
	h, auth, svc, pl := newLinkTestServer(t, "", linksClient{links: []string{telemtLink}})
	c := authedSession(t, h, auth)

	p, err := svc.Create(context.Background(), proxy.CreateRequest{
		Name: "m", Port: 14450, TLSDomain: "a.com",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	pl.PollOnce(context.Background())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	body := rec.Body.String()
	if !strings.Contains(body, telemtLinkEscaped) {
		t.Errorf("list page does not show telemt's self-reported link:\n%s", body)
	}
	if strings.Contains(body, "secret=ee"+p.Secret) {
		t.Error("list page still shows the locally computed link instead of telemt's")
	}

	req = httptest.NewRequest(http.MethodGet, "/proxies/"+p.ID, nil)
	req.AddCookie(c)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	body = rec.Body.String()
	if !strings.Contains(body, telemtLinkEscaped) {
		t.Errorf("detail page does not show telemt's self-reported link:\n%s", body)
	}
	if strings.Contains(body, "secret=ee"+p.Secret) {
		t.Error("detail page still shows the locally computed link instead of telemt's")
	}
}

// TestLinkKeepsExplicitPublicHostOverTelemt is the regression test for the
// bug the second review round caught: linkFor (via proxy.ReconcileLink) used
// to prefer telemt's self-reported link unconditionally, which silently
// overrode an operator's explicitly set PANEL_PUBLIC_HOST — set precisely
// because telemt's own external-IP detection guesses wrong for their setup
// (behind NAT, behind a load balancer). With PublicHost set, the panel's own
// link must win on both the list row and the detail page, even once a
// differing telemt link shows up in a poller snapshot.
func TestLinkKeepsExplicitPublicHostOverTelemt(t *testing.T) {
	telemtLink := "tg://proxy?server=10.0.0.5&port=443&secret=eeabc" // telemt's own (wrong) guess
	h, auth, svc, pl := newLinkTestServer(t, "203.0.113.9", linksClient{links: []string{telemtLink}})
	c := authedSession(t, h, auth)

	p, err := svc.Create(context.Background(), proxy.CreateRequest{
		Name: "m", Port: 14452, TLSDomain: "a.com",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	pl.PollOnce(context.Background())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	body := rec.Body.String()
	if strings.Contains(body, "10.0.0.5") {
		t.Errorf("list page shows telemt's link even though PublicHost was set:\n%s", body)
	}
	if !strings.Contains(body, "secret=ee"+p.Secret) {
		t.Errorf("list page does not show the operator's own PublicHost-derived link:\n%s", body)
	}

	req = httptest.NewRequest(http.MethodGet, "/proxies/"+p.ID, nil)
	req.AddCookie(c)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	body = rec.Body.String()
	if strings.Contains(body, "10.0.0.5") {
		t.Errorf("detail page shows telemt's link even though PublicHost was set:\n%s", body)
	}
	if !strings.Contains(body, "secret=ee"+p.Secret) {
		t.Errorf("detail page does not show the operator's own PublicHost-derived link:\n%s", body)
	}
}

// TestAddrLineMatchesResolvedLinkHost covers the cosmetic follow-up the
// second review round flagged: with PublicHost unset, the informational
// "host:port" line must show the same host as the link/QR next to it (the
// host telemt itself reported), not a separately computed SERVER-IP
// placeholder that can disagree with what the link actually says.
func TestAddrLineMatchesResolvedLinkHost(t *testing.T) {
	h, auth, svc, pl := newLinkTestServer(t, "", linksClient{
		links: []string{"tg://proxy?server=203.0.113.9&port=443&secret=eeabc"},
	})
	c := authedSession(t, h, auth)

	p, err := svc.Create(context.Background(), proxy.CreateRequest{
		Name: "m", Port: 14453, TLSDomain: "a.com",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	pl.PollOnce(context.Background())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	body := rec.Body.String()
	if !strings.Contains(body, `class="addr">203.0.113.9:14453<`) {
		t.Errorf("list row's addr line should show telemt's reported host, not SERVER-IP:\n%s", body)
	}
	if strings.Contains(body, "SERVER-IP") {
		t.Errorf("list row still shows the SERVER-IP placeholder even though a real link is displayed:\n%s", body)
	}

	req = httptest.NewRequest(http.MethodGet, "/proxies/"+p.ID, nil)
	req.AddCookie(c)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	body = rec.Body.String()
	if !strings.Contains(body, `class="addr">203.0.113.9:14453 ·`) {
		t.Errorf("detail page's addr line should show telemt's reported host, not SERVER-IP:\n%s", body)
	}
	if strings.Contains(body, "SERVER-IP") {
		t.Errorf("detail page still shows the SERVER-IP placeholder even though a real link is displayed:\n%s", body)
	}
}

// TestLinkWarnsWhenNoPublicHostAndNoTelemtLink covers FIX (b): the exact
// shipped-default scenario from Finding 1 (empty PANEL_PUBLIC_HOST, and — in
// the window before the first successful poll — no telemt link yet either)
// must not render a copyable "server=SERVER-IP" link or its QR code. It must
// show a clear warning instead.
func TestLinkWarnsWhenNoPublicHostAndNoTelemtLink(t *testing.T) {
	h, auth, svc, _ := newLinkTestServer(t, "", linksClient{})
	c := authedSession(t, h, auth)

	p, err := svc.Create(context.Background(), proxy.CreateRequest{
		Name: "m", Port: 14451, TLSDomain: "a.com",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Deliberately not polling: no telemt snapshot exists yet, exactly like
	// the window between proxy creation and its first successful poll.

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	body := rec.Body.String()
	if strings.Contains(body, "server=SERVER-IP") {
		t.Error("list page presents a broken SERVER-IP link as if it were usable")
	}
	if !strings.Contains(body, "no link yet") {
		t.Errorf("list page should warn that no link is available yet:\n%s", body)
	}

	req = httptest.NewRequest(http.MethodGet, "/proxies/"+p.ID, nil)
	req.AddCookie(c)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	body = rec.Body.String()
	if strings.Contains(body, "server=SERVER-IP") {
		t.Error("detail page presents a broken SERVER-IP link as if it were usable")
	}
	if strings.Contains(body, "data:image/png;base64,") {
		t.Error("detail page renders a QR code for a link that does not exist")
	}
	if !strings.Contains(body, "No usable link yet") {
		t.Errorf("detail page should warn that no link is available yet:\n%s", body)
	}
}
