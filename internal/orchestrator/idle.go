package orchestrator

import (
	"context"
	"log/slog"
	"time"
)

// StartIdleTimer arms (or re-arms) the idle timer for a running container.
// When the timer fires without being reset by traffic, the orchestrator
// initiates an idle shutdown (snap).
func (o *Orchestrator) StartIdleTimer(id string, timeout int) {
	o.mu.Lock()
	defer o.mu.Unlock()

	// Record the moment the countdown begins so the UI's "Last Traffic"
	// timestamp always reflects the actual idle timer. Set even when
	// timeout=0 so the UI shows traffic for never-snap containers.
	if ci, ok := o.containers[id]; ok {
		ci.LastTrafficAt = time.Now()
	}

	if timeout <= 0 {
		return // 0 = never auto-shutdown
	}

	// Cancel existing timer if any.
	if t, ok := o.stateTimers[id]; ok {
		t.Stop()
	}

	o.stateTimers[id] = time.AfterFunc(time.Duration(timeout)*time.Second, func() {
		slog.Info("idle timeout reached, initiating snap", "container", id, "timeout", timeout)
		_ = o.Snap(context.Background(), id, "idle_timeout")
	})
}

// ResetIdleTimer resets the idle timer back to its full duration. Called by
// the sentinel whenever traffic is detected on a running container's ports.
// StartIdleTimer updates LastTrafficAt so the UI stays in sync with the timer.
func (o *Orchestrator) ResetIdleTimer(id string) {
	ci := o.GetContainer(id)
	if ci == nil {
		return
	}
	o.StartIdleTimer(id, ci.SnapTimeout)
}

// StopIdleTimer cancels the idle timer (called when leaving running state).
func (o *Orchestrator) StopIdleTimer(id string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if t, ok := o.stateTimers[id]; ok {
		t.Stop()
		delete(o.stateTimers, id)
	}
}