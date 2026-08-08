package orchestrator

import "testing"

// TestCanTransition_ValidEdges verifies every documented edge in the state
// machine is accepted. If a valid edge is accidentally removed, a container
// could get stuck in a state with no way out (e.g. crashed → starting removed
// would prevent wake-on-connect from recovering a crashed server).
func TestCanTransition_ValidEdges(t *testing.T) {
	valid := []struct {
		from, to State
	}{
		{StateUnmanaged, StateDormant},
		{StateDormant, StateStarting},
		{StateDormant, StateCrashed},
		{StateStarting, StateRunning},
		{StateStarting, StateDormant},
		{StateStarting, StateCrashed},
		{StateRunning, StateStopping},
		{StateRunning, StateCrashed},
		{StateRunning, StateDormant},
		{StateStopping, StateDormant},
		{StateStopping, StateCrashed},
		{StateCrashed, StateDormant},
		{StateCrashed, StateStarting},
	}
	for _, e := range valid {
		if !CanTransition(e.from, e.to) {
			t.Errorf("CanTransition(%s→%s) = false, want true", e.from, e.to)
		}
	}
}

// TestCanTransition_InvalidEdges verifies that undocumented / nonsensical
// transitions are rejected. If an invalid edge is accidentally allowed (e.g.
// dormant→running bypassing starting), the state machine loses its guarantees
// and watchers (Discord, sentinel) would receive out-of-order notifications.
func TestCanTransition_InvalidEdges(t *testing.T) {
	invalid := []struct {
		from, to State
	}{
		{StateDormant, StateRunning},   // must go through starting
		{StateDormant, StateStopping},  // can't stop what isn't running
		{StateDormant, StateUnmanaged}, // unmanaged is a terminal cleanup state
		{StateRunning, StateStarting},  // can't re-start a running container
		{StateRunning, StateUnmanaged},
		{StateCrashed, StateRunning},  // must go through starting
		{StateCrashed, StateStopping}, // crashed containers are already stopped
		{StateCrashed, StateUnmanaged},
		{StateStarting, StateStarting}, // self-transition not allowed
		{StateRunning, StateRunning},
		{StateStopping, StateStarting}, // must reach dormant first
		{StateStopping, StateRunning},
		{StateUnmanaged, StateRunning},
		{StateUnmanaged, StateCrashed},
	}
	for _, e := range invalid {
		if CanTransition(e.from, e.to) {
			t.Errorf("CanTransition(%s→%s) = true, want false", e.from, e.to)
		}
	}
}
