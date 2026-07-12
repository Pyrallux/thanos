package docker

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/go-connections/nat"
)

// composeDir is the directory where per-server docker-compose.yml files
// are stored. It is relative to the Thanos working directory.
const composeDir = "docker"

// WriteComposeFile generates a docker-compose.yml file for the given
// container by inspecting it and extracting its configuration. The file
// is written to {composeDir}/{containerName}.yml.
//
// If the container cannot be inspected, the error is returned but does
// not prevent other operations. Existing compose files are overwritten.
func (c *Client) WriteComposeFile(ctx context.Context, id string) error {
	inspect, err := c.CLI.ContainerInspect(ctx, id)
	if err != nil {
		return fmt.Errorf("inspect container for compose file: %w", err)
	}

	name := inspect.Name
	if name != "" && name[0] == '/' {
		name = name[1:]
	}

	content, err := generateComposeYAML(inspect)
	if err != nil {
		return fmt.Errorf("generate compose yaml: %w", err)
	}

	if err := os.MkdirAll(composeDir, 0o755); err != nil {
		return fmt.Errorf("create compose dir: %w", err)
	}

	path := filepath.Join(composeDir, name+".yml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write compose file: %w", err)
	}

	slog.Info("compose file written", "container", name, "path", path)
	return nil
}

// SyncComposeFiles ensures that every managed container has an up-to-date
// docker-compose.yml file in the /docker directory, and removes compose
// files for containers that are no longer managed.
//
// managedNames is the set of container names (without leading "/") that
// are currently managed by Thanos.
func (c *Client) SyncComposeFiles(ctx context.Context, managedIDs []string) error {
	if err := os.MkdirAll(composeDir, 0o755); err != nil {
		return fmt.Errorf("create compose dir: %w", err)
	}

	// Track which compose files should exist after sync.
	expectedFiles := make(map[string]bool)

	for _, id := range managedIDs {
		if err := c.WriteComposeFile(ctx, id); err != nil {
			slog.Warn("failed to write compose file during sync", "container_id", id, "err", err)
			continue
		}
		// Record the expected filename.
		inspect, err := c.CLI.ContainerInspect(ctx, id)
		if err != nil {
			continue
		}
		name := strings.TrimPrefix(inspect.Name, "/")
		expectedFiles[name+".yml"] = true
	}

	// Remove stale compose files (files in /docker that don't correspond
	// to any managed container).
	entries, err := os.ReadDir(composeDir)
	if err != nil {
		slog.Warn("failed to read compose dir during sync", "err", err)
		return nil
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if !strings.HasSuffix(entry.Name(), ".yml") {
			continue
		}
		if !expectedFiles[entry.Name()] {
			path := filepath.Join(composeDir, entry.Name())
			if err := os.Remove(path); err != nil {
				slog.Warn("failed to remove stale compose file", "file", entry.Name(), "err", err)
			} else {
				slog.Info("removed stale compose file", "file", entry.Name())
			}
		}
	}

	return nil
}

// generateComposeYAML builds a docker-compose.yml string from a container
// inspect response. It includes image, ports, volumes, environment,
// restart policy, and labels.
func generateComposeYAML(inspect container.InspectResponse) (string, error) {
	name := strings.TrimPrefix(inspect.Name, "/")

	var sb strings.Builder
	sb.WriteString("services:\n")
	sb.WriteString(fmt.Sprintf("  %s:\n", sanitizeYAMLKey(name)))

	// Image
	image := ""
	if inspect.Config != nil && inspect.Config.Image != "" {
		image = inspect.Config.Image
	}
	sb.WriteString(fmt.Sprintf("    image: %s\n", image))

	// Restart policy
	if inspect.HostConfig != nil {
		rp := inspect.HostConfig.RestartPolicy
		if rp.Name != "" && rp.Name != "no" {
			sb.WriteString(fmt.Sprintf("    restart: %s\n", rp.Name))
		}
	}

	// Ports
	if inspect.HostConfig != nil && inspect.HostConfig.PortBindings != nil {
		ports := formatPortBindings(inspect.HostConfig.PortBindings)
		if len(ports) > 0 {
			sb.WriteString("    ports:\n")
			for _, p := range ports {
				sb.WriteString(fmt.Sprintf("      - %s\n", p))
			}
		}
	}

	// Volumes (binds)
	if inspect.HostConfig != nil && len(inspect.HostConfig.Binds) > 0 {
		sb.WriteString("    volumes:\n")
		for _, bind := range inspect.HostConfig.Binds {
			sb.WriteString(fmt.Sprintf("      - %s\n", yamlQuote(bind)))
		}
	}

	// Environment
	if inspect.Config != nil && len(inspect.Config.Env) > 0 {
		sb.WriteString("    environment:\n")
		// Sort for stable output.
		envs := make([]string, len(inspect.Config.Env))
		copy(envs, inspect.Config.Env)
		sort.Strings(envs)
		for _, env := range envs {
			sb.WriteString(fmt.Sprintf("      - %s\n", yamlQuote(env)))
		}
	}

	// Labels
	if inspect.Config != nil && len(inspect.Config.Labels) > 0 {
		// Only include Thanos labels to keep the compose file focused.
		thanosLabels := map[string]string{}
		for k, v := range inspect.Config.Labels {
			if strings.HasPrefix(k, "thanos.") {
				thanosLabels[k] = v
			}
		}
		if len(thanosLabels) > 0 {
			sb.WriteString("    labels:\n")
			keys := make([]string, 0, len(thanosLabels))
			for k := range thanosLabels {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				sb.WriteString(fmt.Sprintf("      %s: %s\n", sanitizeYAMLKey(k), yamlQuote(thanosLabels[k])))
			}
		}
	}

	return sb.String(), nil
}

// formatPortBindings converts Docker PortBindings map into a sorted slice
// of "HOST:CONTAINER/protocol" strings for docker-compose format.
func formatPortBindings(bindings nat.PortMap) []string {
	// Collect all port mappings.
	type portMapping struct {
		host      string
		container string
		protocol  string
	}
	var mappings []portMapping

	for containerPort, binds := range bindings {
		// containerPort is like "25565/tcp" or "25565/udp"
		cp := string(containerPort)
		protocol := "tcp"
		if idx := strings.LastIndex(cp, "/"); idx >= 0 {
			protocol = cp[idx+1:]
			cp = cp[:idx]
		}
		for _, b := range binds {
			hp := b.HostPort
			if hp == "" {
				hp = cp
			}
			mappings = append(mappings, portMapping{
				host:      hp,
				container: cp,
				protocol:  protocol,
			})
		}
	}

	// Sort for stable output.
	sort.Slice(mappings, func(i, j int) bool {
		if mappings[i].container != mappings[j].container {
			return mappings[i].container < mappings[j].container
		}
		return mappings[i].protocol < mappings[j].protocol
	})

	out := make([]string, 0, len(mappings))
	for _, m := range mappings {
		out = append(out, fmt.Sprintf(`"%s:%s/%s"`, m.host, m.container, m.protocol))
	}
	return out
}

// sanitizeYAMLKey replaces dots in label keys with nothing special — YAML
// keys with dots need quoting. For simplicity, we quote keys containing dots.
func sanitizeYAMLKey(key string) string {
	if strings.ContainsAny(key, ".:") {
		return fmt.Sprintf("%q", key)
	}
	return key
}

// yamlQuote wraps a string in double quotes if it contains special YAML
// characters. Simple values are left unquoted for readability.
func yamlQuote(s string) string {
	if s == "" {
		return `""`
	}
	// Quote if the string contains characters that would break YAML.
	if strings.ContainsAny(s, ":#{}[]&*!|>'\"@`") || strings.Contains(s, "\n") {
		// %q handles all escaping correctly, including double quotes.
		return fmt.Sprintf("%q", s)
	}
	return s
}