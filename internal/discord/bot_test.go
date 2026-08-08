package discord

import (
	"testing"

	"thanos/internal/orchestrator"
)

func TestPresenceStatus(t *testing.T) {
	ci := func(name string, state orchestrator.State) *orchestrator.ContainerInfo {
		return &orchestrator.ContainerInfo{DisplayName: name, State: state}
	}

	tests := []struct {
		name       string
		containers []*orchestrator.ContainerInfo
		want       string
	}{
		{
			name:       "no containers",
			containers: nil,
			want:       "All 0 servers stopped",
		},
		{
			name:       "all stopped",
			containers: []*orchestrator.ContainerInfo{ci("MC", orchestrator.StateDormant), ci("ARK", orchestrator.StateDormant)},
			want:       "All 2 servers stopped",
		},
		{
			name:       "one running",
			containers: []*orchestrator.ContainerInfo{ci("MC", orchestrator.StateRunning), ci("ARK", orchestrator.StateDormant)},
			want:       "MC online",
		},
		{
			name:       "two running",
			containers: []*orchestrator.ContainerInfo{ci("MC", orchestrator.StateRunning), ci("ARK", orchestrator.StateRunning)},
			want:       "MC, ARK online",
		},
		{
			name:       "three running",
			containers: []*orchestrator.ContainerInfo{ci("MC", orchestrator.StateRunning), ci("ARK", orchestrator.StateRunning), ci("CS2", orchestrator.StateRunning)},
			want:       "MC, ARK, CS2 online",
		},
		{
			name: "many running collapses to count",
			containers: []*orchestrator.ContainerInfo{
				ci("A", orchestrator.StateRunning), ci("B", orchestrator.StateRunning),
				ci("C", orchestrator.StateRunning), ci("D", orchestrator.StateRunning),
			},
			want: "4 servers online",
		},
		{
			name:       "starting containers do not count as running",
			containers: []*orchestrator.ContainerInfo{ci("MC", orchestrator.StateStarting), ci("ARK", orchestrator.StateDormant)},
			want:       "All 2 servers stopped",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := presenceStatus(tt.containers); got != tt.want {
				t.Errorf("presenceStatus() = %q, want %q", got, tt.want)
			}
		})
	}
}
