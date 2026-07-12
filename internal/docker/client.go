// Package docker wraps the Docker SDK client so the rest of Thanos doesn't
// import the Docker SDK directly.
package docker

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
	dockerclient "github.com/docker/docker/client"
)

// Client is a thin wrapper around the official Docker SDK client.
type Client struct {
	CLI *dockerclient.Client
}

// New connects to the local Docker engine using default environment options.
func New() (*Client, error) {
	cli, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}
	return &Client{CLI: cli}, nil
}

// Ping verifies the Docker engine is reachable.
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.CLI.Ping(ctx)
	return err
}

// ListAll returns all containers (running and stopped).
func (c *Client) ListAll(ctx context.Context) ([]container.Summary, error) {
	return c.CLI.ContainerList(ctx, container.ListOptions{All: true})
}

// ListManaged returns all containers that have the thanos.enabled=true label.
func (c *Client) ListManaged(ctx context.Context) ([]container.Summary, error) {
	f := filters.NewArgs()
	f.Add("label", "thanos.enabled=true")
	return c.CLI.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: f,
	})
}

// Start starts a container by ID.
func (c *Client) Start(ctx context.Context, id string) error {
	return c.CLI.ContainerStart(ctx, id, container.StartOptions{})
}

// Stop stops a container by ID with a grace period (seconds).
func (c *Client) Stop(ctx context.Context, id string, timeoutSeconds int) error {
	timeout := timeoutSeconds
	return c.CLI.ContainerStop(ctx, id, container.StopOptions{
		Timeout: &timeout,
	})
}

// Inspect returns detailed info about a container by ID.
func (c *Client) Inspect(ctx context.Context, id string) (container.InspectResponse, error) {
	return c.CLI.ContainerInspect(ctx, id)
}

// Logs opens a streaming reader for container logs with follow=true.
// The caller must close the returned reader when done.
func (c *Client) Logs(ctx context.Context, id string) (io.ReadCloser, error) {
	return c.CLI.ContainerLogs(ctx, id, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     true,
		Tail:       "100",
		Timestamps: true,
	})
}

// Stats opens a streaming reader for container resource stats.
// The caller must close the returned reader when done.
func (c *Client) Stats(ctx context.Context, id string) (container.StatsResponseReader, error) {
	return c.CLI.ContainerStats(ctx, id, true)
}

// UpdateLabelsWithOptions recreates a container with updated labels.
// If deleteOriginal is true, the old container is removed before recreation
// (standard behavior). If false, the old container is kept (but the new one
// will have a different ID).
func (c *Client) UpdateLabelsWithOptions(ctx context.Context, id string, labels map[string]string, deleteOriginal bool) error {
	// 1. Inspect the current container to get its full config.
	inspect, err := c.CLI.ContainerInspect(ctx, id)
	if err != nil {
		return fmt.Errorf("inspect: %w", err)
	}

	// 2. Stop the container if it's running.
	if inspect.State != nil && inspect.State.Running {
		_ = c.CLI.ContainerStop(ctx, id, container.StopOptions{})
	}

	// 3. Merge existing labels with the new ones.
	existingLabels := map[string]string{}
	if inspect.Config != nil && inspect.Config.Labels != nil {
		for k, v := range inspect.Config.Labels {
			existingLabels[k] = v
		}
	}
	for k, v := range labels {
		if v == "" {
			delete(existingLabels, k) // empty value = remove label
		} else {
			existingLabels[k] = v
		}
	}

	// 4. Remove the old container if requested.
	if deleteOriginal {
		if err := c.CLI.ContainerRemove(ctx, id, container.RemoveOptions{Force: true}); err != nil {
			slog.Warn("failed to remove old container during label update", "id", id, "err", err)
		}
	}

	// 5. Recreate with the same config but new labels.
	name := inspect.Name
	if name != "" && name[0] == '/' {
		name = name[1:]
	}
	if !deleteOriginal {
		// If keeping the original, give the new one a different name.
		name = name + "-thanos"
	}

	createConfig := container.Config{}
	if inspect.Config != nil {
		createConfig = *inspect.Config
	}
	createConfig.Labels = existingLabels

	hostConfig := container.HostConfig{}
	if inspect.HostConfig != nil {
		hostConfig = *inspect.HostConfig
	}

	// Preserve the networking config from the original container.
	networkingConfig := &network.NetworkingConfig{}
	if inspect.NetworkSettings != nil && len(inspect.NetworkSettings.Networks) > 0 {
		networkingConfig.EndpointsConfig = make(map[string]*network.EndpointSettings, len(inspect.NetworkSettings.Networks))
		for netName, endpoint := range inspect.NetworkSettings.Networks {
			// Use the SDK's Copy method to avoid modifying the original.
			networkingConfig.EndpointsConfig[netName] = endpoint.Copy()
		}
	}

	_, err = c.CLI.ContainerCreate(ctx, &createConfig, &hostConfig, networkingConfig, nil, name)
	if err != nil {
		return fmt.Errorf("recreate container: %w", err)
	}

	return nil
}

// DeleteComposeFile removes the docker-compose.yml file for the given
// container from the /docker directory. Called when a container is removed
// from Thanos management.
func (c *Client) DeleteComposeFile(ctx context.Context, id string) error {
	inspect, err := c.CLI.ContainerInspect(ctx, id)
	if err != nil {
		// Container may already be removed — that's OK, just skip.
		return nil
	}

	name := inspect.Name
	if name != "" && name[0] == '/' {
		name = name[1:]
	}

	path := filepath.Join("docker", name+".yml")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove compose file: %w", err)
	}
	return nil
}

// ExtractHostPorts returns the unique host-side public ports for a container
// summary. Only ports that have a PublicPort > 0 are returned (i.e., ports
// actually mapped to the host). Duplicate ports (e.g., same port for TCP and
// UDP) are deduplicated.
func ExtractHostPorts(ports []container.Port) []int {
	seen := map[int]bool{}
	out := make([]int, 0, len(ports))
	for _, p := range ports {
		if p.PublicPort > 0 && !seen[int(p.PublicPort)] {
			seen[int(p.PublicPort)] = true
			out = append(out, int(p.PublicPort))
		}
	}
	return out
}

// ExtractHostPortsFromInspect extracts host-side public ports from a
// container's InspectResponse. This works for stopped containers too,
// since the port bindings are stored in HostConfig.
func ExtractHostPortsFromInspect(inspect container.InspectResponse) []int {
	if inspect.HostConfig == nil || inspect.HostConfig.PortBindings == nil {
		return []int{}
	}
	seen := map[int]bool{}
	out := []int{}
	for _, bindings := range inspect.HostConfig.PortBindings {
		for _, binding := range bindings {
			if binding.HostPort != "" {
				p, err := strconv.Atoi(binding.HostPort)
				if err == nil && !seen[p] {
					seen[p] = true
					out = append(out, p)
				}
			}
		}
	}
	return out
}