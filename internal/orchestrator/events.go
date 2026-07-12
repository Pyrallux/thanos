package orchestrator

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/api/types/filters"

	"thanos/internal/docker"
	"thanos/internal/serverlogs"
)

// SubscribeEvents listens to the Docker engine event stream for container
// start/stop/die events. This is how Thanos detects:
//   - A container reached "running" after being started → transition to running
//   - A container was stopped (by Thanos or externally) → transition to dormant
//   - A container died unexpectedly → transition to crashed
func (o *Orchestrator) SubscribeEvents(ctx context.Context) {
	f := filters.NewArgs()
	f.Add("type", "container")
	f.Add("event", "start")
	f.Add("event", "stop")
	f.Add("event", "die")
	f.Add("event", "destroy")

	for {
		// Re-subscribe loop — if the event stream errors (Docker restart, etc.),
		// we reconnect after a delay.
		err := o.consumeEvents(ctx, f)
		if ctx.Err() != nil {
			return
		}
		slog.Warn("docker event stream closed, reconnecting in 5s", "err", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}

// consumeEvents subscribes to the Docker event stream and processes messages
// until the stream closes or the context is cancelled.
func (o *Orchestrator) consumeEvents(ctx context.Context, f filters.Args) error {
	msgCh, errCh := o.dock.CLI.Events(ctx, events.ListOptions{Filters: f})

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-errCh:
			if err != nil {
				return err
			}
		case msg := <-msgCh:
			o.handleEvent(ctx, msg)
		}
	}
}

// handleEvent processes a single Docker event message.
func (o *Orchestrator) handleEvent(ctx context.Context, msg events.Message) {
	// Only care about container events.
	if msg.Type != events.ContainerEventType {
		return
	}

	ci := o.GetContainer(msg.Actor.ID)
	if ci == nil {
		// This container is not managed by Thanos. Check if it's a newly
		// created managed container we haven't seen yet.
		if msg.Action == events.ActionStart || msg.Action == events.ActionDie {
			go o.tryDiscoverContainer(ctx, msg.Actor.ID)
		}
		return
	}

	switch msg.Action {
	case events.ActionStart:
		// Container was started (by Thanos or externally).
		o.mu.Lock()
		wasRunning := ci.State == StateRunning
		shouldNotify := ci.State == StateStarting || ci.State == StateDormant || ci.State == StateCrashed
		if shouldNotify && ci.StartedAt.IsZero() {
			ci.StartedAt = time.Now()
		}
		o.mu.Unlock()
		if shouldNotify {
			o.setState(msg.Actor.ID, StateRunning)
		}
		// Only (re)start the idle timer if the container wasn't already
		// running — avoids resetting the timer on spurious start events.
		if !wasRunning {
			o.StartIdleTimer(ci.ID, ci.SnapTimeout)
		}
		o.logEvent(ci.ID, "manual_start", "docker start event")
		slog.Info("container started", "name", ci.DisplayName, "id", ci.ID, "was_running", wasRunning)

		// Sync compose file in case the container was recreated with new config.
		go func() {
			if err := o.dock.WriteComposeFile(context.Background(), ci.ID); err != nil {
				slog.Debug("failed to sync compose file on start event", "id", ci.ID, "err", err)
			}
		}()

	case events.ActionStop:
		// Container was stopped (by Thanos or externally). Transition to
		// stopping state — the die event will confirm the exit code and
		// determine if it was a clean stop or a crash.
		o.StopIdleTimer(ci.ID)
		// Read state under lock to avoid race with concurrent setState.
		o.mu.RLock()
		wasRunning := ci.State == StateRunning
		o.mu.RUnlock()
		if wasRunning {
			o.setState(msg.Actor.ID, StateStopping)
		}
		slog.Info("container stop event", "name", ci.DisplayName, "id", ci.ID)

	case events.ActionDie:
		// Container exited. Check exit code to determine if it was a crash.
		exitCode := 0
		if v, ok := msg.Actor.Attributes["exitCode"]; ok {
			exitCode, _ = strconv.Atoi(v)
		}
		o.handleDie(ci.ID, exitCode)

	case events.ActionDestroy:
		// Container was removed (docker rm). Clean up the in-memory record,
		// delete its compose file from the /docker directory, and delete
		// its server_log entries from the database.
		o.StopIdleTimer(ci.ID)

		// Delete the compose file using the known container name.
		composePath := filepath.Join("docker", ci.Name+".yml")
		if err := os.Remove(composePath); err != nil && !os.IsNotExist(err) {
			slog.Warn("failed to delete compose file on destroy", "container", ci.DisplayName, "err", err)
		}

		// Delete the per-server state-change log entries.
		serverlogs.DeleteEntries(o.db, ci.ID)

		// Traffic data (traffic_log, known_clients) is cleaned up by the
		// sentinel's StateWatcher when it receives the StateUnmanaged event
		// from setState below.

		o.mu.Lock()
		delete(o.containers, ci.ID)
		o.mu.Unlock()
		slog.Info("container destroyed, removing from managed set", "name", ci.DisplayName, "id", ci.ID)

		// Notify watchers so the sentinel removes its ports.
		ci.State = StateUnmanaged
		o.mu.RLock()
		watchers := make([]StateWatcher, len(o.watchers))
		copy(watchers, o.watchers)
		o.mu.RUnlock()
		for _, w := range watchers {
			w.OnStateChange(ci)
		}
	}
}

// handleDie processes a container death event. Exit code 0 or 143 (SIGTERM)
// is treated as a clean stop. Exit code 137 (SIGKILL) is treated as clean
// ONLY if the container was in the stopping state (Thanos initiated the stop
// and the grace period expired, causing Docker to escalate to SIGKILL).
// Any other exit code while running = crash.
func (o *Orchestrator) handleDie(id string, exitCode int) {
	o.StopIdleTimer(id)

	o.mu.Lock()
	ci, ok := o.containers[id]
	if !ok {
		o.mu.Unlock()
		return
	}

	// Store the exit code for crash notifications.
	ci.LastExitCode = exitCode

	isCleanStop := exitCode == 0 || exitCode == 143 // 143 = SIGTERM (docker stop)
	wasStopping := ci.State == StateStopping
	wasDormant := ci.State == StateDormant

	// If already dormant, the die event is a duplicate — ignore.
	if wasDormant {
		o.mu.Unlock()
		slog.Debug("die event for already-dormant container, ignoring", "id", id, "exitCode", exitCode)
		return
	}

	if isCleanStop || wasStopping {
		// Clean exit (exit 0 or SIGTERM) OR Thanos was stopping the container
		// (docker stop may result in 137 if the grace period expires and Docker
		// escalates to SIGKILL — that's still a Thanos-initiated stop, not a crash).
		stopReason := ci.StopReason
		o.mu.Unlock()
		eventType := "idle_shutdown"
		if stopReason == "manual_stop" {
			eventType = "manual_stop"
		}
		o.logEvent(id, eventType, "clean exit code="+strconv.Itoa(exitCode))
		slog.Info("container exited cleanly", "name", ci.DisplayName, "id", id, "exitCode", exitCode)
		o.setState(id, StateDormant)
	} else {
		// Unexpected exit while running = crash.
		o.mu.Unlock()
		o.logEvent(id, "crash", "exit_code="+strconv.Itoa(exitCode))
		slog.Error("container crashed", "name", ci.DisplayName, "id", id, "exitCode", exitCode)
		o.setState(id, StateCrashed)
	}
}

// tryDiscoverContainer checks if a container we don't know about is
// thanos-managed. If so, it adds it to the managed set.
func (o *Orchestrator) tryDiscoverContainer(ctx context.Context, id string) {
	summary, err := o.dock.ListManaged(ctx)
	if err != nil {
		return
	}
	for _, sc := range summary {
		if sc.ID == id {
			labels := docker.ParseLabels(sc)
			if !labels.Enabled {
				return
			}
			ports := docker.ExtractHostPorts(sc.Ports)
			// If no ports found from summary (container is stopped), inspect
			// to get port bindings from the container config.
			if len(ports) == 0 {
				if inspect, err := o.dock.Inspect(ctx, id); err == nil {
					ports = docker.ExtractHostPortsFromInspect(inspect)
				}
			}
			ci := &ContainerInfo{
				ID:          sc.ID,
				Name:        strings.TrimPrefix(strings.Join(sc.Names, ","), "/"),
				DisplayName: labels.DisplayName,
				State:       StateDormant,
				Ports:       ports,
				SnapTimeout: labels.SnapTimeout,
				Labels:      labels,
			}
			if ci.DisplayName == "" {
				ci.DisplayName = ci.Name
			}
			o.mu.Lock()
			if _, exists := o.containers[id]; !exists {
				o.containers[id] = ci
				slog.Info("discovered new managed container", "name", ci.DisplayName, "id", id, "ports", ports)
				// Notify watchers so the sentinel adds the ports.
				watchers := make([]StateWatcher, len(o.watchers))
				copy(watchers, o.watchers)
				o.mu.Unlock()
				for _, w := range watchers {
					w.OnStateChange(ci)
				}
			} else {
				o.mu.Unlock()
			}
			return
		}
	}
}