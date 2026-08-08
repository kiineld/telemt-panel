// Package docker wraps container lifecycle behind a Runtime interface so the
// proxy service can be unit-tested without a Docker daemon.
package docker

import (
	"context"
	"errors"
)

var ErrNoSuchContainer = errors.New("docker: no such container")

// ContainerSpec is the subset of container configuration the panel needs.
// telemt's Control API port (9091) is deliberately not published to the host:
// it is reachable only over the panel's private network.
type ContainerSpec struct {
	Name          string
	Image         string
	Labels        map[string]string
	ConfigHostDir string
	Port          int
	Network       string
}

type ContainerInfo struct {
	ID        string
	Name      string
	Running   bool
	Labels    map[string]string
	IPAddress string
}

type Runtime interface {
	EnsureNetwork(ctx context.Context, name, subnet string) error
	Pull(ctx context.Context, image string) error
	Create(ctx context.Context, spec ContainerSpec) (string, error)
	Start(ctx context.Context, id string) error
	Stop(ctx context.Context, id string) error
	Remove(ctx context.Context, id string) error
	Inspect(ctx context.Context, id string) (ContainerInfo, error)
	List(ctx context.Context, labels map[string]string) ([]ContainerInfo, error)
	Logs(ctx context.Context, id string, tail int) (string, error)
}
