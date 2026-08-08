package docker

import (
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/go-connections/nat"
)

// TestExtractHostPorts verifies that the port extraction from a container
// summary correctly deduplicates and only returns ports with a public
// binding. A regression here would cause the sentinel to watch the wrong
// ports (or none), breaking wake-on-connect.
func TestExtractHostPorts(t *testing.T) {
	tests := []struct {
		name  string
		ports []container.Port
		want  []int
	}{
		{
			name:  "empty",
			ports: nil,
			want:  []int{},
		},
		{
			name: "single tcp",
			ports: []container.Port{
				{PrivatePort: 25565, PublicPort: 25565, Type: "tcp"},
			},
			want: []int{25565},
		},
		{
			name: "tcp and udp same port deduped",
			ports: []container.Port{
				{PrivatePort: 25565, PublicPort: 25565, Type: "tcp"},
				{PrivatePort: 25565, PublicPort: 25565, Type: "udp"},
			},
			want: []int{25565},
		},
		{
			name: "multiple distinct ports sorted",
			ports: []container.Port{
				{PrivatePort: 25565, PublicPort: 25565, Type: "tcp"},
				{PrivatePort: 19132, PublicPort: 19132, Type: "udp"},
				{PrivatePort: 8123, PublicPort: 8123, Type: "tcp"},
			},
			want: []int{25565, 19132, 8123},
		},
		{
			name: "port with no public binding skipped",
			ports: []container.Port{
				{PrivatePort: 25565, PublicPort: 0, Type: "tcp"},
			},
			want: []int{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractHostPorts(tt.ports)
			if len(got) != len(tt.want) {
				t.Fatalf("ExtractHostPorts = %v, want %v", got, tt.want)
			}
			gotSet := make(map[int]bool, len(got))
			for _, p := range got {
				gotSet[p] = true
			}
			for _, p := range tt.want {
				if !gotSet[p] {
					t.Errorf("ExtractHostPorts = %v, missing port %d", got, p)
				}
			}
		})
	}
}

// TestExtractHostPortsFromInspect verifies that port extraction from an
// inspect response works for stopped containers (where summary ports are
// empty). This is the fallback path used during reconcile when a container
// is dormant — if it breaks, dormant containers would have no watched ports.
func TestExtractHostPortsFromInspect(t *testing.T) {
	inspect := container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{
			HostConfig: &container.HostConfig{
				PortBindings: nat.PortMap{
					"25565/tcp": []nat.PortBinding{{HostPort: "25565"}},
					"19132/udp": []nat.PortBinding{{HostPort: "19132"}},
				},
			},
		},
	}
	got := ExtractHostPortsFromInspect(inspect)
	if len(got) != 2 {
		t.Fatalf("ExtractHostPortsFromInspect = %v, want 2 ports", got)
	}
	wantSet := map[int]bool{25565: true, 19132: true}
	for _, p := range got {
		if !wantSet[p] {
			t.Errorf("unexpected port %d in %v", p, got)
		}
	}
}

// TestExtractHostPortsFromInspectNil verifies that nil HostConfig or
// PortBindings don't panic. This is the state of a freshly created
// container with no port mappings.
func TestExtractHostPortsFromInspectNil(t *testing.T) {
	got := ExtractHostPortsFromInspect(container.InspectResponse{})
	if len(got) != 0 {
		t.Errorf("ExtractHostPortsFromInspect(empty) = %v, want empty", got)
	}
}

// TestParseLabelsNilMap verifies that ParseLabels handles a container with
// no labels without panicking, returning the defaults.
func TestParseLabelsNilMap(t *testing.T) {
	got := ParseLabels(container.Summary{Labels: nil})
	if got.Enabled {
		t.Error("Enabled should default to false")
	}
	if got.SnapTimeout != int(DefaultSnapTimeoutHours*3600) {
		t.Errorf("SnapTimeout = %d, want default %d", got.SnapTimeout, int(DefaultSnapTimeoutHours*3600))
	}
}
