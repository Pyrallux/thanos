package orchestrator

// This file centralises state-machine transition helpers so that the logic
// is testable independent of Docker.
//
// Allowed transitions
//
//   unmanaged → dormant      (when labels are first detected)
//   dormant   → starting     (packet detected / manual start)
//   starting  → running      (container reaches running state)
//   starting  → dormant      (start failed)
//   running   → stopping     (idle timeout / manual stop)
//   stopping  → dormant     (clean exit)
//   *         → crashed      (unexpected exit, if crash_detection enabled)
//   crashed   → dormant      (manual restart attempt)
//
// canTransition returns true if from→to is a valid edge in the state graph.

var validTransitions = map[State]map[State]bool{
	StateUnmanaged: {StateDormant: true},
	StateDormant:    {StateStarting: true, StateCrashed: true},
	StateStarting:   {StateRunning: true, StateDormant: true, StateCrashed: true},
	StateRunning:     {StateStopping: true, StateCrashed: true, StateDormant: true},
	StateStopping:    {StateDormant: true, StateCrashed: true},
	StateCrashed:      {StateDormant: true, StateStarting: true},
}

// CanTransition reports whether transitioning from→to is valid.
func CanTransition(from, to State) bool {
	if tos, ok := validTransitions[from]; ok {
		return tos[to]
	}
	return false
}