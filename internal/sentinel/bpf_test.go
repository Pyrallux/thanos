package sentinel

import (
	"strings"
	"testing"
)

func TestBuildCombinedBPFFilter(t *testing.T) {
	all := func(ids ...string) map[int]watchedPort {
		m := map[int]watchedPort{}
		for i, id := range ids {
			m[25565+i] = watchedPort{containerID: id, tcp: true, udp: true}
		}
		return m
	}

	tests := []struct {
		name        string
		dormant     map[int]watchedPort
		running     map[int]watchedPort
		wantTCP     bool
		wantUDP     bool
		wantNoMatch bool
	}{
		{
			name:        "no ports yields no-match filter",
			dormant:     map[int]watchedPort{},
			running:     map[int]watchedPort{},
			wantNoMatch: true,
		},
		{
			name:    "dormant port watched for both protocols",
			dormant: all("a"),
			wantTCP: true,
			wantUDP: true,
		},
		{
			name:    "running port watched for both protocols",
			running: all("b"),
			wantTCP: true,
			wantUDP: true,
		},
		{
			name: "tcp-only dormant port",
			dormant: map[int]watchedPort{
				25565: {containerID: "a", tcp: true, udp: false},
			},
			wantTCP: true,
			wantUDP: false,
		},
		{
			name: "udp-only dormant port",
			dormant: map[int]watchedPort{
				25565: {containerID: "a", tcp: false, udp: true},
			},
			wantTCP: false,
			wantUDP: true,
		},
		{
			name: "neither protocol yields no-match filter",
			dormant: map[int]watchedPort{
				25565: {containerID: "a", tcp: false, udp: false},
			},
			wantNoMatch: true,
		},
		{
			name: "mixed dormant and running with different protocols",
			dormant: map[int]watchedPort{
				25565: {containerID: "a", tcp: true, udp: false},
			},
			running: map[int]watchedPort{
				19132: {containerID: "b", tcp: false, udp: true},
			},
			wantTCP: true,
			wantUDP: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := buildCombinedBPFFilter(tt.dormant, tt.running)
			if tt.wantNoMatch {
				if f != "udp port 0" {
					t.Errorf("expected no-match filter, got %q", f)
				}
				return
			}
			if tt.wantTCP && !strings.Contains(f, "tcp[tcpflags]") {
				t.Errorf("expected TCP clause in filter, got %q", f)
			}
			if !tt.wantTCP && strings.Contains(f, "tcp[tcpflags]") {
				t.Errorf("did not expect TCP clause in filter, got %q", f)
			}
			if tt.wantUDP && !strings.Contains(f, "(udp and") {
				t.Errorf("expected UDP clause in filter, got %q", f)
			}
			if !tt.wantUDP && strings.Contains(f, "(udp and") {
				t.Errorf("did not expect UDP clause in filter, got %q", f)
			}
		})
	}
}
