package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kiineld/telemt-panel/internal/docker"
	"github.com/kiineld/telemt-panel/internal/proxy"
)

func TestBannerWhenDockerIsDown(t *testing.T) {
	h, auth, _, fake := newTestServerWithFake(t)
	c := authedSession(t, h, auth)
	fake.FailPing = errors.New("cannot connect to the Docker daemon")

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — a dead daemon must not break the page", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Docker") {
		t.Error("page should carry a banner naming Docker as unreachable")
	}
}

func TestProxyListStillRendersWhenDockerIsDown(t *testing.T) {
	h, auth, svc, fake := newTestServerWithFake(t)
	c := authedSession(t, h, auth)

	p, err := svc.Create(context.Background(), proxy.CreateRequest{
		Name: "survivor", Port: 14501, TLSDomain: "a.com",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	fake.FailPing = errors.New("daemon down")

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if !strings.Contains(rec.Body.String(), p.Name) {
		t.Error("the proxy list must still render from SQLite when Docker is unreachable")
	}
}

func TestOrphansListed(t *testing.T) {
	h, auth, _, fake := newTestServerWithFake(t)
	c := authedSession(t, h, auth)

	id, err := fake.Create(context.Background(), docker.ContainerSpec{
		Name:   "telemt-ghost",
		Labels: map[string]string{proxy.LabelManaged: "true", proxy.LabelProxyID: "ghost"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if !strings.Contains(rec.Body.String(), id) {
		t.Errorf("orphan container %s is not surfaced on the index page", id)
	}
}

func TestRemoveOrphan(t *testing.T) {
	h, auth, _, fake := newTestServerWithFake(t)
	c := authedSession(t, h, auth)

	id, _ := fake.Create(context.Background(), docker.ContainerSpec{
		Name:   "telemt-ghost",
		Labels: map[string]string{proxy.LabelManaged: "true", proxy.LabelProxyID: "ghost"},
	})

	req := httptest.NewRequest(http.MethodPost, "/orphans/"+id+"/delete", nil)
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	if fake.Count() != 0 {
		t.Errorf("fake.Count() = %d, want 0 — the orphan should be removed", fake.Count())
	}
}

func TestRemoveOrphanRefusesManagedProxy(t *testing.T) {
	h, auth, svc, fake := newTestServerWithFake(t)
	c := authedSession(t, h, auth)

	p, err := svc.Create(context.Background(), proxy.CreateRequest{
		Name: "real", Port: 14502, TLSDomain: "a.com",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/orphans/"+p.ContainerID+"/delete", nil)
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code == http.StatusSeeOther && fake.Count() == 0 {
		t.Fatal("a live proxy's container was removed through the orphan route")
	}
}
