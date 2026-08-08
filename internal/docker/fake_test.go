package docker

import (
	"context"
	"errors"
	"testing"
)

func TestFakeImplementsRuntime(t *testing.T) {
	var _ Runtime = NewFake()
}

func TestFakeLifecycle(t *testing.T) {
	f, ctx := NewFake(), context.Background()
	id, err := f.Create(ctx, ContainerSpec{Name: "a", Labels: map[string]string{"mtpanel.managed": "true"}})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := f.Start(ctx, id); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	info, err := f.Inspect(ctx, id)
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if !info.Running {
		t.Error("container should be running after Start")
	}
	if info.IPAddress == "" {
		t.Error("fake should assign an IP address")
	}

	got, err := f.List(ctx, map[string]string{"mtpanel.managed": "true"})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("List() len = %d, want 1", len(got))
	}

	if err := f.Remove(ctx, id); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	if f.Count() != 0 {
		t.Errorf("Count() = %d, want 0", f.Count())
	}
	if _, err := f.Inspect(ctx, id); !errors.Is(err, ErrNoSuchContainer) {
		t.Fatalf("Inspect() after remove error = %v, want ErrNoSuchContainer", err)
	}
}

func TestFakeLabelFilterExcludes(t *testing.T) {
	f, ctx := NewFake(), context.Background()
	if _, err := f.Create(ctx, ContainerSpec{Name: "a", Labels: map[string]string{"other": "1"}}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	got, err := f.List(ctx, map[string]string{"mtpanel.managed": "true"})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(got) != 0 {
		t.Errorf("List() len = %d, want 0", len(got))
	}
}

func TestFakeInjectedFailures(t *testing.T) {
	f, ctx := NewFake(), context.Background()
	f.FailCreate = errors.New("boom")
	if _, err := f.Create(ctx, ContainerSpec{}); err == nil {
		t.Fatal("Create() error = nil, want injected failure")
	}
}
