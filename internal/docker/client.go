package docker

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
)

type dockerRuntime struct{ cli *client.Client }

// var _ Runtime turns a signature drift between dockerRuntime and Runtime
// into a build error instead of a runtime surprise.
var _ Runtime = (*dockerRuntime)(nil)

func NewDockerRuntime() (Runtime, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("docker: connect: %w", err)
	}
	return &dockerRuntime{cli: cli}, nil
}

func (d *dockerRuntime) EnsureNetwork(ctx context.Context, name, subnet string) error {
	nets, err := d.cli.NetworkList(ctx, network.ListOptions{
		Filters: filters.NewArgs(filters.Arg("name", name)),
	})
	if err != nil {
		return fmt.Errorf("docker: list networks: %w", err)
	}
	for _, n := range nets {
		if n.Name == name {
			return nil
		}
	}
	_, err = d.cli.NetworkCreate(ctx, name, network.CreateOptions{
		Driver: "bridge",
		IPAM:   &network.IPAM{Config: []network.IPAMConfig{{Subnet: subnet}}},
	})
	if err != nil {
		return fmt.Errorf("docker: create network %s: %w", name, err)
	}
	return nil
}

func (d *dockerRuntime) Pull(ctx context.Context, ref string) error {
	rc, err := d.cli.ImagePull(ctx, ref, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("docker: pull %s: %w", ref, err)
	}
	defer rc.Close()
	// Draining the stream is what makes the pull synchronous: the daemon
	// streams pull progress and the image is not fully fetched until the
	// stream is exhausted. Returning early would leave it half-fetched.
	if _, err := io.Copy(io.Discard, rc); err != nil {
		return fmt.Errorf("docker: pull %s: %w", ref, err)
	}
	return nil
}

func (d *dockerRuntime) Create(ctx context.Context, spec ContainerSpec) (string, error) {
	port := nat.Port(strconv.Itoa(spec.Port) + "/tcp")

	resp, err := d.cli.ContainerCreate(ctx,
		&container.Config{
			Image:        spec.Image,
			Cmd:          []string{"/etc/telemt/config.toml"},
			WorkingDir:   "/run/telemt",
			Labels:       spec.Labels,
			Env:          []string{"RUST_LOG=info"},
			ExposedPorts: nat.PortSet{port: struct{}{}},
		},
		&container.HostConfig{
			RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyUnlessStopped},
			// Only the proxy's MTProto port is published; telemt's Control API
			// port 9091 must never appear here, host-side or exposed.
			PortBindings: nat.PortMap{port: []nat.PortBinding{{HostIP: "0.0.0.0", HostPort: strconv.Itoa(spec.Port)}}},
			Mounts: []mount.Mount{{
				// ConfigHostDir is mounted as a directory, not a single file:
				// telemt rewrites config.toml via temp+rename, which requires
				// the mount point itself to tolerate the rename underneath it.
				Type:   mount.TypeBind,
				Source: spec.ConfigHostDir,
				Target: "/etc/telemt",
			}},
			Tmpfs:          map[string]string{"/run/telemt": "rw,mode=1777,size=4m"},
			ReadonlyRootfs: true,
			CapDrop:        []string{"ALL"},
			CapAdd:         []string{"NET_BIND_SERVICE"},
			SecurityOpt:    []string{"no-new-privileges:true"},
		},
		&network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{spec.Network: {}},
		},
		nil, spec.Name)
	if err != nil {
		return "", fmt.Errorf("docker: create %s: %w", spec.Name, err)
	}
	return resp.ID, nil
}

func (d *dockerRuntime) Start(ctx context.Context, id string) error {
	if err := d.cli.ContainerStart(ctx, id, container.StartOptions{}); err != nil {
		return fmt.Errorf("docker: start %s: %w", id, err)
	}
	return nil
}

func (d *dockerRuntime) Stop(ctx context.Context, id string) error {
	if err := d.cli.ContainerStop(ctx, id, container.StopOptions{}); err != nil {
		return fmt.Errorf("docker: stop %s: %w", id, err)
	}
	return nil
}

func (d *dockerRuntime) Remove(ctx context.Context, id string) error {
	err := d.cli.ContainerRemove(ctx, id, container.RemoveOptions{Force: true})
	if client.IsErrNotFound(err) {
		return fmt.Errorf("%w: %s", ErrNoSuchContainer, id)
	}
	if err != nil {
		return fmt.Errorf("docker: remove %s: %w", id, err)
	}
	return nil
}

func (d *dockerRuntime) Inspect(ctx context.Context, id string) (ContainerInfo, error) {
	c, err := d.cli.ContainerInspect(ctx, id)
	if client.IsErrNotFound(err) {
		return ContainerInfo{}, fmt.Errorf("%w: %s", ErrNoSuchContainer, id)
	}
	if err != nil {
		return ContainerInfo{}, fmt.Errorf("docker: inspect %s: %w", id, err)
	}

	info := ContainerInfo{
		ID:      c.ID,
		Name:    strings.TrimPrefix(c.Name, "/"),
		Running: c.State != nil && c.State.Running,
		Labels:  c.Config.Labels,
	}
	if c.NetworkSettings != nil {
		for _, ep := range c.NetworkSettings.Networks {
			if ep.IPAddress != "" {
				info.IPAddress = ep.IPAddress
				break
			}
		}
	}
	return info, nil
}

func (d *dockerRuntime) List(ctx context.Context, labels map[string]string) ([]ContainerInfo, error) {
	args := filters.NewArgs()
	for k, v := range labels {
		args.Add("label", k+"="+v)
	}
	list, err := d.cli.ContainerList(ctx, container.ListOptions{All: true, Filters: args})
	if err != nil {
		return nil, fmt.Errorf("docker: list containers: %w", err)
	}

	out := make([]ContainerInfo, 0, len(list))
	for _, c := range list {
		// ContainerList's summary omits the network IP reliably, so inspect
		// each match individually to fill it in.
		info, err := d.Inspect(ctx, c.ID)
		if err != nil {
			continue
		}
		out = append(out, info)
	}
	return out, nil
}

func (d *dockerRuntime) Logs(ctx context.Context, id string, tail int) (string, error) {
	rc, err := d.cli.ContainerLogs(ctx, id, container.LogsOptions{
		ShowStdout: true, ShowStderr: true, Tail: strconv.Itoa(tail),
	})
	if client.IsErrNotFound(err) {
		return "", fmt.Errorf("%w: %s", ErrNoSuchContainer, id)
	}
	if err != nil {
		return "", fmt.Errorf("docker: logs %s: %w", id, err)
	}
	defer rc.Close()

	var b strings.Builder
	if _, err := io.Copy(&b, rc); err != nil {
		return "", fmt.Errorf("docker: read logs %s: %w", id, err)
	}
	return b.String(), nil
}
