package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func sampleProxy(id string, port int) Proxy {
	quota := uint64(1000)
	return Proxy{
		ID: id, Name: "p" + id, Port: port,
		TLSDomain: "petrovich.ru", AdTag: "",
		Secret:   "00112233445566778899aabbccddeeff",
		APIToken: "tok", State: StateCreating,
		DataQuotaBytes: &quota,
	}
}

func TestCreateAndGetProxy(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	want := sampleProxy("a", 443)
	if err := s.CreateProxy(ctx, want); err != nil {
		t.Fatalf("CreateProxy() error = %v", err)
	}
	got, err := s.GetProxy(ctx, "a")
	if err != nil {
		t.Fatalf("GetProxy() error = %v", err)
	}
	if got.Port != 443 || got.TLSDomain != "petrovich.ru" || got.State != StateCreating {
		t.Errorf("GetProxy() = %+v", got)
	}
	if got.DataQuotaBytes == nil || *got.DataQuotaBytes != 1000 {
		t.Errorf("DataQuotaBytes = %v, want 1000", got.DataQuotaBytes)
	}
	if got.CreatedAt.IsZero() {
		t.Error("CreatedAt is zero; Open should stamp it")
	}
}

func TestGetProxyNotFound(t *testing.T) {
	s := newStore(t)
	_, err := s.GetProxy(context.Background(), "nope")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetProxy() error = %v, want ErrNotFound", err)
	}
}

func TestPortUniqueness(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	if err := s.CreateProxy(ctx, sampleProxy("a", 443)); err != nil {
		t.Fatalf("first CreateProxy() error = %v", err)
	}
	err := s.CreateProxy(ctx, sampleProxy("b", 443))
	if !errors.Is(err, ErrPortTaken) {
		t.Fatalf("second CreateProxy() error = %v, want ErrPortTaken", err)
	}
}

func TestPortFreedAfterDelete(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	if err := s.CreateProxy(ctx, sampleProxy("a", 443)); err != nil {
		t.Fatalf("CreateProxy() error = %v", err)
	}
	if err := s.DeleteProxy(ctx, "a"); err != nil {
		t.Fatalf("DeleteProxy() error = %v", err)
	}
	if err := s.CreateProxy(ctx, sampleProxy("b", 443)); err != nil {
		t.Fatalf("re-CreateProxy() on freed port error = %v", err)
	}
}

func TestUpdateProxy(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	p := sampleProxy("a", 443)
	if err := s.CreateProxy(ctx, p); err != nil {
		t.Fatalf("CreateProxy() error = %v", err)
	}
	p.State = StateRunning
	p.ContainerID = "deadbeef"
	p.StateMessage = ""
	p.DataQuotaBytes = nil
	if err := s.UpdateProxy(ctx, p); err != nil {
		t.Fatalf("UpdateProxy() error = %v", err)
	}
	got, err := s.GetProxy(ctx, "a")
	if err != nil {
		t.Fatalf("GetProxy() error = %v", err)
	}
	if got.State != StateRunning || got.ContainerID != "deadbeef" {
		t.Errorf("after update = %+v", got)
	}
	if got.DataQuotaBytes != nil {
		t.Errorf("DataQuotaBytes = %v, want nil after clearing", got.DataQuotaBytes)
	}
	if !got.UpdatedAt.After(got.CreatedAt) && !got.UpdatedAt.Equal(got.CreatedAt) {
		t.Error("UpdatedAt should be stamped on update")
	}
}

func TestListProxiesSortedByPort(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	for _, tc := range []struct {
		id   string
		port int
	}{{"c", 8443 + 1}, {"a", 443}, {"b", 1080}} {
		if err := s.CreateProxy(ctx, sampleProxy(tc.id, tc.port)); err != nil {
			t.Fatalf("CreateProxy(%s) error = %v", tc.id, err)
		}
	}
	got, err := s.ListProxies(ctx)
	if err != nil {
		t.Fatalf("ListProxies() error = %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	if got[0].Port != 443 || got[1].Port != 1080 || got[2].Port != 8444 {
		t.Errorf("ports = %d, %d, %d", got[0].Port, got[1].Port, got[2].Port)
	}
}

func TestAdminLifecycle(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	n, err := s.AdminCount(ctx)
	if err != nil {
		t.Fatalf("AdminCount() error = %v", err)
	}
	if n != 0 {
		t.Fatalf("AdminCount() = %d, want 0", n)
	}

	a, err := s.CreateAdmin(ctx, "admin", "hash1")
	if err != nil {
		t.Fatalf("CreateAdmin() error = %v", err)
	}
	if !a.MustChangePassword {
		t.Error("new admin should have MustChangePassword = true")
	}

	got, err := s.AdminByUsername(ctx, "admin")
	if err != nil {
		t.Fatalf("AdminByUsername() error = %v", err)
	}
	if got.PasswordHash != "hash1" {
		t.Errorf("PasswordHash = %q", got.PasswordHash)
	}

	if err := s.SetAdminPassword(ctx, a.ID, "hash2"); err != nil {
		t.Fatalf("SetAdminPassword() error = %v", err)
	}
	got, _ = s.AdminByUsername(ctx, "admin")
	if got.PasswordHash != "hash2" {
		t.Errorf("PasswordHash = %q, want hash2", got.PasswordHash)
	}
	if got.MustChangePassword {
		t.Error("MustChangePassword should clear once the password is set")
	}
}

func TestSessions(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	a, err := s.CreateAdmin(ctx, "admin", "h")
	if err != nil {
		t.Fatalf("CreateAdmin() error = %v", err)
	}
	if err := s.CreateSession(ctx, "th", a.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	got, err := s.SessionAdmin(ctx, "th")
	if err != nil {
		t.Fatalf("SessionAdmin() error = %v", err)
	}
	if got.Username != "admin" {
		t.Errorf("Username = %q", got.Username)
	}

	if err := s.DeleteSession(ctx, "th"); err != nil {
		t.Fatalf("DeleteSession() error = %v", err)
	}
	if _, err := s.SessionAdmin(ctx, "th"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SessionAdmin() after delete error = %v, want ErrNotFound", err)
	}
}

func TestExpiredSessionRejected(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	a, _ := s.CreateAdmin(ctx, "admin", "h")
	if err := s.CreateSession(ctx, "old", a.ID, time.Now().Add(-time.Minute)); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	if _, err := s.SessionAdmin(ctx, "old"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SessionAdmin() on expired session error = %v, want ErrNotFound", err)
	}
}
