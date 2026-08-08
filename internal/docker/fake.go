package docker

import (
	"context"
	"fmt"
	"sync"
)

// Fake is an in-memory Runtime for tests. Set the Fail* fields to make a
// specific call return an error and exercise rollback paths.
type Fake struct {
	mu         sync.Mutex
	seq        int
	containers map[string]*fakeContainer
	Networks   map[string]string

	FailPull   error
	FailCreate error
	FailStart  error
	FailRemove error

	// Created records every spec passed to Create, in order.
	Created []ContainerSpec
}

type fakeContainer struct {
	info ContainerInfo
	logs string
}

func NewFake() *Fake {
	return &Fake{containers: map[string]*fakeContainer{}, Networks: map[string]string{}}
}

func (f *Fake) EnsureNetwork(_ context.Context, name, subnet string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Networks[name] = subnet
	return nil
}

func (f *Fake) Pull(context.Context, string) error { return f.FailPull }

func (f *Fake) Create(_ context.Context, spec ContainerSpec) (string, error) {
	if f.FailCreate != nil {
		return "", f.FailCreate
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	id := fmt.Sprintf("ctr%d", f.seq)
	f.containers[id] = &fakeContainer{info: ContainerInfo{
		ID: id, Name: spec.Name, Labels: spec.Labels,
		IPAddress: fmt.Sprintf("172.28.0.%d", f.seq+1),
	}}
	f.Created = append(f.Created, spec)
	return id, nil
}

func (f *Fake) Start(_ context.Context, id string) error {
	if f.FailStart != nil {
		return f.FailStart
	}
	return f.mutate(id, func(c *fakeContainer) { c.info.Running = true })
}

func (f *Fake) Stop(_ context.Context, id string) error {
	return f.mutate(id, func(c *fakeContainer) { c.info.Running = false })
}

func (f *Fake) Remove(_ context.Context, id string) error {
	if f.FailRemove != nil {
		return f.FailRemove
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.containers[id]; !ok {
		return fmt.Errorf("%w: %s", ErrNoSuchContainer, id)
	}
	delete(f.containers, id)
	return nil
}

func (f *Fake) Inspect(_ context.Context, id string) (ContainerInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.containers[id]
	if !ok {
		return ContainerInfo{}, fmt.Errorf("%w: %s", ErrNoSuchContainer, id)
	}
	return c.info, nil
}

func (f *Fake) List(_ context.Context, labels map[string]string) ([]ContainerInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []ContainerInfo
	for _, c := range f.containers {
		match := true
		for k, v := range labels {
			if c.info.Labels[k] != v {
				match = false
				break
			}
		}
		if match {
			out = append(out, c.info)
		}
	}
	return out, nil
}

func (f *Fake) Logs(_ context.Context, id string, _ int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.containers[id]
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrNoSuchContainer, id)
	}
	return c.logs, nil
}

// Count reports how many containers currently exist. Tests use it to assert
// that rollback left nothing behind.
func (f *Fake) Count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.containers)
}

func (f *Fake) mutate(id string, fn func(*fakeContainer)) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.containers[id]
	if !ok {
		return fmt.Errorf("%w: %s", ErrNoSuchContainer, id)
	}
	fn(c)
	return nil
}
