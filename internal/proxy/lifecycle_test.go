package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/kiineld/telemt-panel/internal/docker"
	"github.com/kiineld/telemt-panel/internal/store"
	"github.com/kiineld/telemt-panel/internal/telemt/client"
)

func mustCreate(t *testing.T, svc *Service, port int) store.Proxy {
	t.Helper()
	p, err := svc.Create(context.Background(), CreateRequest{
		Name: "p", Port: port, TLSDomain: "petrovich.ru",
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	return p
}

func TestDeleteRemovesEverything(t *testing.T) {
	fake := docker.NewFake()
	svc, dir := newService(t, fake, &stubClient{})
	p := mustCreate(t, svc, freePort(t))

	if err := svc.Delete(context.Background(), p.ID); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if fake.Count() != 0 {
		t.Errorf("fake.Count() = %d, want 0", fake.Count())
	}
	if _, err := svc.Get(context.Background(), p.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Get() after delete error = %v, want ErrNotFound", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "proxies", p.ID)); !os.IsNotExist(err) {
		t.Error("config dir should be removed")
	}
}

func TestDeleteSucceedsWhenContainerAlreadyGone(t *testing.T) {
	fake := docker.NewFake()
	svc, _ := newService(t, fake, &stubClient{})
	p := mustCreate(t, svc, freePort(t))

	// Simulate someone running `docker rm` by hand.
	if err := fake.Remove(context.Background(), p.ContainerID); err != nil {
		t.Fatalf("pre-remove: %v", err)
	}
	if err := svc.Delete(context.Background(), p.ID); err != nil {
		t.Fatalf("Delete() error = %v, want nil when the container is already gone", err)
	}
}

// patchRecorder captures what UpdateLimits sends to the control API.
type patchRecorder struct {
	stubClient
	got client.PatchUser
}

func (p *patchRecorder) PatchUser(_ context.Context, _ string, in client.PatchUser) (client.UserInfo, error) {
	p.got = in
	return client.UserInfo{Username: Username}, nil
}

func TestUpdateLimitsIsHot(t *testing.T) {
	fake := docker.NewFake()
	rec := &patchRecorder{}
	svc, _ := newService(t, fake, &stubClient{})
	svc.deps.NewClient = func(store.Proxy, string) TelemtClient { return rec }

	p := mustCreate(t, svc, freePort(t))
	before := fake.Count()

	quota := uint64(5000)
	quotaPtr := &quota
	updated, err := svc.UpdateLimits(context.Background(), p.ID, LimitsPatch{
		DataQuotaBytes: &quotaPtr,
	})
	if err != nil {
		t.Fatalf("UpdateLimits() error = %v", err)
	}
	if updated.DataQuotaBytes == nil || *updated.DataQuotaBytes != 5000 {
		t.Errorf("DataQuotaBytes = %v, want 5000", updated.DataQuotaBytes)
	}
	// client.PatchUser's fields are client.Opt[T], a struct with unexported
	// state — the only way to observe what would actually be sent over the
	// wire is to marshal it the same way the real client does.
	if !rec.got.DataQuotaBytes.IsSet() {
		t.Fatal("patch sent to telemt did not set DataQuotaBytes")
	}
	body, err := json.Marshal(rec.got)
	if err != nil {
		t.Fatalf("marshal sent patch: %v", err)
	}
	if !strings.Contains(string(body), `"data_quota_bytes":5000`) {
		t.Errorf("patch sent to telemt = %s, want data_quota_bytes=5000", body)
	}
	if fake.Count() != before {
		t.Error("UpdateLimits must not touch containers")
	}
}

func TestUpdateLimitsClearsWithNilPointer(t *testing.T) {
	fake := docker.NewFake()
	rec := &patchRecorder{}
	svc, _ := newService(t, fake, &stubClient{})
	svc.deps.NewClient = func(store.Proxy, string) TelemtClient { return rec }

	quota := uint64(100)
	p, err := svc.Create(context.Background(), CreateRequest{
		Name: "p", Port: freePort(t), TLSDomain: "a.com", DataQuotaBytes: &quota,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	var clear *uint64 // nil
	updated, err := svc.UpdateLimits(context.Background(), p.ID, LimitsPatch{DataQuotaBytes: &clear})
	if err != nil {
		t.Fatalf("UpdateLimits() error = %v", err)
	}
	if updated.DataQuotaBytes != nil {
		t.Errorf("DataQuotaBytes = %v, want nil after clearing", updated.DataQuotaBytes)
	}
	// The clear must actually reach telemt as an explicit JSON null, not be
	// silently dropped from the payload — that was the bug PatchUser's Opt
	// type exists to fix.
	if !rec.got.DataQuotaBytes.IsSet() {
		t.Fatal("patch sent to telemt did not set DataQuotaBytes at all")
	}
	body, err := json.Marshal(rec.got)
	if err != nil {
		t.Fatalf("marshal sent patch: %v", err)
	}
	if !strings.Contains(string(body), `"data_quota_bytes":null`) {
		t.Errorf("patch sent to telemt = %s, want data_quota_bytes=null", body)
	}
}

func TestRecreateChangesPortAndDomain(t *testing.T) {
	fake := docker.NewFake()
	svc, dir := newService(t, fake, &stubClient{})
	p := mustCreate(t, svc, freePort(t))
	oldContainer := p.ContainerID
	newPort := freePort(t)

	got, err := svc.Recreate(context.Background(), p.ID, newPort, "bsi.bund.de")
	if err != nil {
		t.Fatalf("Recreate() error = %v", err)
	}
	if got.Port != newPort {
		t.Errorf("Port = %d, want %d", got.Port, newPort)
	}
	if got.TLSDomain != "bsi.bund.de" {
		t.Errorf("TLSDomain = %q", got.TLSDomain)
	}
	if got.ContainerID == oldContainer {
		t.Error("ContainerID should change after recreate")
	}
	if got.Secret != p.Secret {
		t.Error("secret must survive a recreate, or every user's link breaks unnecessarily")
	}
	if fake.Count() != 1 {
		t.Errorf("fake.Count() = %d, want 1 — the old container must be removed", fake.Count())
	}

	raw, err := os.ReadFile(filepath.Join(dir, "proxies", p.ID, "config.toml"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(raw), "bsi.bund.de") {
		t.Error("rewritten config should carry the new domain")
	}
}

// If the caller's context is cancelled while Recreate's startContainer is
// polling for health — an HTTP client disconnecting during the up-to-30s
// wait, for example — the old container has already been destroyed and the
// new one already created/started, both irreversible. startContainer's
// terminal state commit must still land: a caller-cancelled ctx must not
// leave the row stuck at StateRecreating, with no path back to a settled
// state short of a manual fix or a later Reconcile pass. Modelled on
// TestCreateCommitsErrorStateWhenContextCancelledDuringHealthWait.
func TestRecreateCommitsErrorStateWhenContextCancelledDuringHealthWait(t *testing.T) {
	fake := docker.NewFake()
	svc, _ := newService(t, fake, &stubClient{})
	p := mustCreate(t, svc, freePort(t))
	newPort := freePort(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// cancelingHealthClient (defined in create_test.go) cancels the very
	// context Recreate was invoked with on its first Health call, then
	// reports unhealthy — exactly like an HTTP client that hangs up
	// mid-request while waitHealthy is still polling.
	svc.deps.NewClient = func(store.Proxy, string) TelemtClient {
		return &cancelingHealthClient{cancel: cancel}
	}

	got, err := svc.Recreate(ctx, p.ID, newPort, "bsi.bund.de")
	if err != nil {
		t.Fatalf("Recreate() error = %v, want nil even though ctx was cancelled during the health wait", err)
	}
	if got.State != store.StateError {
		t.Errorf("returned State = %q, want error", got.State)
	}

	// The discriminating assertion: re-read with a fresh, uncancelled
	// context so a stale in-memory value can't mask a lost commit.
	stored, err := svc.deps.Store.GetProxy(context.Background(), p.ID)
	if err != nil {
		t.Fatalf("GetProxy: %v", err)
	}
	if stored.State != store.StateError {
		t.Errorf("persisted State = %q, want error — StateRecreating here means startContainer's terminal UpdateProxy was lost to context cancellation, leaving the row stuck", stored.State)
	}
	if stored.Port != newPort || stored.TLSDomain != "bsi.bund.de" {
		t.Errorf("persisted Port/TLSDomain = %d/%q, want %d/%q — the port and domain change must have landed even though the health wait was cancelled",
			stored.Port, stored.TLSDomain, newPort, "bsi.bund.de")
	}
}

func TestRecreateRejectsReservedPort(t *testing.T) {
	fake := docker.NewFake()
	svc, _ := newService(t, fake, &stubClient{})
	p := mustCreate(t, svc, freePort(t))

	if _, err := svc.Recreate(context.Background(), p.ID, 80, "a.com"); !errors.Is(err, ErrPortReserved) {
		t.Fatalf("Recreate() error = %v, want ErrPortReserved", err)
	}
}

// TestRecreateSurfacesPortConflict covers Finding 5 on the Recreate path
// (startContainer, shared with Reconcile): a raw Docker port-conflict string
// from Start must reach the caller wrapped as ErrPortConflict, naming the
// port, not verbatim.
func TestRecreateSurfacesPortConflict(t *testing.T) {
	fake := docker.NewFake()
	svc, _ := newService(t, fake, &stubClient{})
	p := mustCreate(t, svc, freePort(t))
	newPort := freePort(t)

	fake.FailStart = errors.New(`Bind for 0.0.0.0:` + strconv.Itoa(newPort) + ` failed: port is already allocated`)

	_, err := svc.Recreate(context.Background(), p.ID, newPort, "bsi.bund.de")
	if !errors.Is(err, ErrPortConflict) {
		t.Fatalf("Recreate() error = %v, want it to wrap ErrPortConflict", err)
	}
	if !strings.Contains(err.Error(), strconv.Itoa(newPort)) {
		t.Errorf("Recreate() error = %v, want it to name the conflicting port %d", err, newPort)
	}

	got, gerr := svc.Get(context.Background(), p.ID)
	if gerr != nil {
		t.Fatalf("Get() error = %v", gerr)
	}
	if got.State != store.StateError {
		t.Errorf("State = %q, want error", got.State)
	}
	if !strings.Contains(got.StateMessage, strconv.Itoa(newPort)) {
		t.Errorf("StateMessage = %q, want it to name the conflicting port %d", got.StateMessage, newPort)
	}
}

func TestReconcileRebuildsMissingContainer(t *testing.T) {
	fake := docker.NewFake()
	svc, _ := newService(t, fake, &stubClient{})
	p := mustCreate(t, svc, freePort(t))

	if err := fake.Remove(context.Background(), p.ContainerID); err != nil {
		t.Fatalf("remove: %v", err)
	}

	rep, err := svc.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if len(rep.Restarted) != 1 || rep.Restarted[0] != p.ID {
		t.Errorf("Restarted = %v, want [%s]", rep.Restarted, p.ID)
	}
	if fake.Count() != 1 {
		t.Errorf("fake.Count() = %d, want 1", fake.Count())
	}
}

func TestReconcileFlagsOrphans(t *testing.T) {
	fake := docker.NewFake()
	svc, _ := newService(t, fake, &stubClient{})

	id, err := fake.Create(context.Background(), docker.ContainerSpec{
		Name:   "telemt-ghost",
		Labels: map[string]string{LabelManaged: "true", LabelProxyID: "ghost"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	rep, err := svc.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if len(rep.Orphans) != 1 || rep.Orphans[0] != id {
		t.Errorf("Orphans = %v, want [%s]", rep.Orphans, id)
	}
}

func TestReconcileCleansUpAbandonedCreate(t *testing.T) {
	fake := docker.NewFake()
	svc, _ := newService(t, fake, &stubClient{})

	// A row left behind by a panel that died mid-create: state creating,
	// no container.
	p := store.Proxy{
		ID: "half", Name: "half", Port: freePort(t), TLSDomain: "a.com",
		Secret: "00112233445566778899aabbccddeeff", APIToken: "t",
		State: store.StateCreating,
	}
	if err := svc.deps.Store.CreateProxy(context.Background(), p); err != nil {
		t.Fatalf("seed: %v", err)
	}

	rep, err := svc.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if len(rep.CleanedUp) != 1 || rep.CleanedUp[0] != "half" {
		t.Errorf("CleanedUp = %v, want [half]", rep.CleanedUp)
	}
	if _, err := svc.Get(context.Background(), "half"); !errors.Is(err, store.ErrNotFound) {
		t.Error("abandoned proxy row should be deleted")
	}
}
