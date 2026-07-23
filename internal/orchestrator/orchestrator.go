package orchestrator

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"thanos/internal/config"
	"thanos/internal/docker"
	"thanos/internal/serverlogs"
)

// State represents a container's lifecycle state
type State string

const (
	StateUnmanaged State = "unmanaged"
	StateDormant   State = "dormant"
	StateStarting  State = "starting"
	StateRunning   State = "running"
	StateStopping  State = "stopping"
	StateCrashed   State = "crashed"
)

// ContainerInfo holds runtime metadata about a managed container.
type ContainerInfo struct {
	ID            string
	Name          string
	DisplayName   string
	State         State
	Ports         []int
	SnapTimeout   int
	Labels          docker.Labels
	StartedAt       time.Time // set when container transitions to running
	LastTrafficAt   time.Time // set by StartIdleTimer; reflects when the idle countdown (re)started
	LastOnlineAt    time.Time // set when container leaves running state — when it was last actually online
	StopReason      string    // set when transitioning to stopping ("manual_stop", "idle_timeout", etc.)
	LastExitCode    int       // set when container exits (used for crash notifications)
	LastWakeAttempt time.Time // set when WakeContainer is called — used for cooldown
}

// Orchestrator owns the container state machine, Docker event subscription,
// idle timers, and the watched-port map. It is the central hub that the
// sentinel, API, and Discord bot all interact with.
type Orchestrator struct {
	cfg  *config.Config
	dock *docker.Client
	db   *sql.DB

	mu          sync.RWMutex
	containers  map[string]*ContainerInfo // keyed by container ID
	stateTimers map[string]*time.Timer    // idle timers per container

	// wakeCooldowns tracks the last wake attempt per container to prevent
	// rapid retry loops when dock.Start() fails or the container exits
	// immediately. Rapid SYNs can trigger WakeContainer dozens of times in
	// a second, each one calling dock.Start() and creating a tight loop.
	wakeCooldowns map[string]time.Time

	// watchers are notified on container state changes.
	watchers []StateWatcher
}

// StateWatcher receives notifications whenever a container changes state.
type StateWatcher interface {
	OnStateChange(ci *ContainerInfo)
}

// New creates an Orchestrator connected to Docker and the config DB.
func New(cfg *config.Config) (*Orchestrator, error) {
	dc, err := docker.New()
	if err != nil {
		return nil, err
	}

	return &Orchestrator{
		cfg:           cfg,
		dock:          dc,
		db:            cfg.DB,
		containers:    make(map[string]*ContainerInfo),
		stateTimers:   make(map[string]*time.Timer),
		wakeCooldowns: make(map[string]time.Time),
	}, nil
}

// RegisterWatcher adds a StateWatcher (e.g., Discord bot) that will be
// called whenever a container transitions between states.
func (o *Orchestrator) RegisterWatcher(w StateWatcher) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.watchers = append(o.watchers, w)
}

// Dock returns the Docker client wrapper (used by API health check).
func (o *Orchestrator) Dock() *docker.Client {
	return o.dock
}

// Containers returns a snapshot of all managed containers, sorted by display
// name for a stable, consistent ordering. This prevents the UI and Discord
// embed from "fighting" for card order on every refresh.
func (o *Orchestrator) Containers() []*ContainerInfo {
	o.mu.RLock()
	defer o.mu.RUnlock()
	out := make([]*ContainerInfo, 0, len(o.containers))
	for _, c := range o.containers {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].DisplayName) < strings.ToLower(out[j].DisplayName)
	})
	return out
}

// GetContainer returns a single container by ID, or nil if not found.
func (o *Orchestrator) GetContainer(id string) *ContainerInfo {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.containers[id]
}

// FindByName looks up a container by its display name or container name.
func (o *Orchestrator) FindByName(name string) *ContainerInfo {
	o.mu.RLock()
	defer o.mu.RUnlock()
	for _, c := range o.containers {
		if strings.EqualFold(c.DisplayName, name) || strings.EqualFold(c.Name, name) {
			return c
		}
	}
	return nil
}

// setState updates the in-memory state, logs the transition to the
// per-server log file, and notifies all watchers.
func (o *Orchestrator) setState(id string, s State) {
	o.mu.Lock()
	ci, ok := o.containers[id]
	if !ok {
		o.mu.Unlock()
		return
	}
	oldState := ci.State
	if oldState == s {
		o.mu.Unlock()
		return // no-op: no state change to report
	}
	ci.State = s
	// Record when the container was last actually online: the moment it
	// leaves the running state. This is the timestamp the UI and Discord
	// show as "Last online" — not StartedAt (which is when it booted).
	if oldState == StateRunning && s != StateRunning {
		ci.LastOnlineAt = time.Now()
	}
	o.mu.Unlock()

	// Write a timestamped entry to the server_log table.
	blurb := stateChangeBlurb(oldState, s, ci.StopReason)
	serverlogs.AppendEntry(o.db, ci.ID, ci.DisplayName, string(oldState), string(s), blurb)

	for _, w := range o.watchers {
		w.OnStateChange(ci)
	}
}

// stateChangeBlurb generates a human-readable description of why a state
// transition happened, based on the old and new states and the stop reason.
func stateChangeBlurb(oldState, newState State, stopReason string) string {
	switch newState {
	case StateStarting:
		return "server is starting up (wake or manual start)"
	case StateRunning:
		return "container reached running state"
	case StateStopping:
		if stopReason != "" {
			return fmt.Sprintf("stopping server (reason: %s)", stopReason)
		}
		return "stopping server (idle or manual stop)"
	case StateDormant:
		if oldState == StateCrashed {
			return "server reset to dormant after crash"
		}
		return "server stopped and is now dormant"
	case StateCrashed:
		return "server crashed unexpectedly"
	case StateUnmanaged:
		return "container removed from Thanos management"
	default:
		return string(oldState) + " -> " + string(newState)
	}
}

// logEvent writes an entry to the event_log table.
func (o *Orchestrator) logEvent(containerID, eventType, details string) {
	_, err := o.db.Exec(
		`INSERT INTO event_log (container_id, event_type, details) VALUES (?, ?, ?)`,
		containerID, eventType, details)
	if err != nil {
		slog.Warn("failed to log event", "err", err)
	}
}

// Run is the main orchestrator loop. It:
//  1. Pings Docker and fails fast if unreachable.
//  2. Discovers all thanos-managed containers and reconciles in-memory state.
//  3. Stops containers unless keep_running_on_boot is set.
//  4. Starts the Docker event subscription loop.
func (o *Orchestrator) Run(ctx context.Context) error {
	// Wait for Docker to be reachable.
	for {
		err := o.dock.Ping(ctx)
		if err == nil {
			break
		}
		slog.Warn("docker engine unreachable, retrying in 5s", "err", err)
		select {
		case <-time.After(5 * time.Second):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	slog.Info("connected to Docker engine")

	// Reconcile state with Docker (this also syncs compose files).
	if err := o.Reconcile(ctx); err != nil {
		slog.Error("reconciliation failed", "err", err)
	}

	// Start listening for Docker events (start/stop/die/destroy).
	go o.SubscribeEvents(ctx)

	// Periodically reconcile to clean up any stale containers that were
	// deleted outside the event stream (e.g. while Thanos was down).
	go o.periodicReconcile(ctx)

	slog.Info("orchestrator running")
	<-ctx.Done()
	return ctx.Err()
}

// periodicReconcile runs reconciliation on a 60-second interval to clean up
// any stale container records (e.g. containers deleted while Thanos was down
// or that missed the Docker event stream). Also prunes old traffic_log rows.
func (o *Orchestrator) periodicReconcile(ctx context.Context) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := o.ReconcileNoStop(ctx); err != nil {
				slog.Warn("periodic reconciliation failed", "err", err)
			}
			// Prune traffic_log entries older than 7 days. known_clients
			// persists indefinitely (it's the audit record).
			if _, err := o.db.Exec(
				`DELETE FROM traffic_log WHERE timestamp < datetime('now', '-7 days')`); err != nil {
				slog.Warn("failed to prune traffic_log", "err", err)
			}
		}
	}
}

// Reconcile queries Docker for all thanos-enabled containers, populates the
// in-memory map, and stops any running containers that don't have
// keep_running_on_boot=true (only on initial startup).
func (o *Orchestrator) Reconcile(ctx context.Context) error {
	return o.reconcile(ctx, true)
}

// ReconcileNoStop reconciles without stopping running containers. Used by
// periodic reconciliation and after label updates.
func (o *Orchestrator) ReconcileNoStop(ctx context.Context) error {
	return o.reconcile(ctx, false)
}

// reconcile is the internal implementation. When initial is false (periodic
// reconcile), running containers are NOT stopped — they may have been woken
// by packet detection and should keep running.
func (o *Orchestrator) reconcile(ctx context.Context, initial bool) error {
	managed, err := o.dock.ListManaged(ctx)
	if err != nil {
		return err
	}

	o.mu.Lock()
	// Save existing container data before clearing the map so we can
	// preserve StartedAt and LastTrafficAt across periodic reconciles.
	oldContainers := o.containers
	o.containers = make(map[string]*ContainerInfo, len(managed))

	seenPorts := map[int]string{} // for port-conflict detection
	for _, sc := range managed {
		labels := docker.ParseLabels(sc)
		if !labels.Enabled {
			continue
		}

		ports := docker.ExtractHostPorts(sc.Ports)
		// If no ports from summary (stopped container), inspect for bindings.
		if len(ports) == 0 {
			if inspect, err := o.dock.Inspect(ctx, sc.ID); err == nil {
				ports = docker.ExtractHostPortsFromInspect(inspect)
			}
		}

		// Inspect to get Docker's actual start time for running containers.
		var dockerStartedAt time.Time
		if inspect, err := o.dock.Inspect(ctx, sc.ID); err == nil {
			if inspect.State != nil && inspect.State.StartedAt != "" {
				if t, err := time.Parse(time.RFC3339Nano, inspect.State.StartedAt); err == nil {
					dockerStartedAt = t
				}
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

		// Preserve LastTrafficAt, StartedAt, and LastOnlineAt from the
		// existing in-memory record so periodic reconciles don't reset
		// the uptime, heartbeat, or "last online" timestamp.
		if old, ok := oldContainers[sc.ID]; ok {
			if !old.LastTrafficAt.IsZero() {
				ci.LastTrafficAt = old.LastTrafficAt
			}
			if !old.StartedAt.IsZero() {
				ci.StartedAt = old.StartedAt
			}
			if !old.LastOnlineAt.IsZero() {
				ci.LastOnlineAt = old.LastOnlineAt
			}
			// Preserve transient Thanos-managed states (Starting, Stopping)
			// across periodic reconciles. Docker may not report "running"
			// yet during a wake, and reconciling it back to Dormant kills
			// the in-progress start.
			if old.State == StateStarting || old.State == StateStopping {
				ci.State = old.State
			}
		}

		// Port conflict check: two different managed containers mapping to
		// the same host port. Only the first one wins.
		for _, p := range ports {
			if existing, ok := seenPorts[p]; ok && existing != sc.ID {
				slog.Warn("port conflict detected — only first container will be managed",
					"port", p, "first", existing, "second", sc.ID)
				ci.Labels.Enabled = false
			} else {
				seenPorts[p] = sc.ID
			}
		}

		if !ci.Labels.Enabled {
			ci.State = StateUnmanaged
			o.containers[sc.ID] = ci
			continue
		}

		// Determine current state from Docker's status string.
		// Thanos inherits the current container state — it does NOT stop
		// running containers on startup (unless keep_running_on_boot is
		// false AND this is the very first run, which is handled by the
		// setup wizard). This means if Thanos is restarted while a server
		// is running, it will simply manage the running server.
		isRunning := sc.State == "running"
		if isRunning {
			ci.State = StateRunning
			// Use Docker's actual start time instead of time.Now().
			if ci.StartedAt.IsZero() {
				if !dockerStartedAt.IsZero() {
					ci.StartedAt = dockerStartedAt
				} else {
					ci.StartedAt = time.Now()
				}
			}
			// LastTrafficAt is set by StartIdleTimer when the idle timer
			// is armed below; no need to set it here.
		} else if ci.State != StateStarting && ci.State != StateStopping {
			// Only fall back to Dormant if Thanos isn't already managing
			// a transient state (Starting/Stopping). Otherwise a periodic
			// reconcile during a wake would revert Starting → Dormant
			// and kill the in-progress container start.
			ci.State = StateDormant
		}

		o.containers[sc.ID] = ci
		slog.Info("managing container",
			"name", ci.DisplayName, "id", sc.ID, "state", ci.State,
			"ports", ci.Ports, "snap_timeout", ci.SnapTimeout)

		// If running, start idle timer — but only if one isn't already
		// armed. The periodic reconcile runs every 60s, and re-arming
		// on every pass would keep resetting the timer, preventing the
		// idle timeout from ever firing.
		if ci.State == StateRunning && ci.SnapTimeout > 0 {
			_, alreadyArmed := o.stateTimers[ci.ID]
			o.mu.Unlock()
			if !alreadyArmed {
				o.StartIdleTimer(ci.ID, ci.SnapTimeout)
			}
			o.mu.Lock()
		}
	}
	o.mu.Unlock()

	// Notify watchers of all managed container states so the sentinel
	// can initialize its watched-port map.
	o.mu.RLock()
	watchers := make([]StateWatcher, len(o.watchers))
	copy(watchers, o.watchers)
	o.mu.RUnlock()
	for _, ci := range o.Containers() {
		for _, w := range watchers {
			w.OnStateChange(ci)
		}
	}

	// Sync per-server docker-compose.yml files in /docker.
	managedIDs := make([]string, 0, len(o.containers))
	o.mu.RLock()
	for id := range o.containers {
		managedIDs = append(managedIDs, id)
	}
	o.mu.RUnlock()
	if err := o.dock.SyncComposeFiles(ctx, managedIDs); err != nil {
		slog.Warn("failed to sync compose files during reconcile", "err", err)
	}

	slog.Info("reconciliation complete", "managed", len(o.containers))
	return nil
}

// WakeContainer is called by the sentinel (on packet detection) or by the
// API/Discord bot (manual start). It starts the container if it's dormant.
// Debounce: only one start call per container until it reaches running state.
// A cooldown prevents rapid retry loops when dock.Start() fails or the
// container exits immediately — without it, a stream of SYNs can trigger
// dozens of WakeContainer calls per second, each calling dock.Start() and
// creating a tight starting→dormant→starting loop.
const wakeCooldown = 10 * time.Second

func (o *Orchestrator) WakeContainer(ctx context.Context, id string, reason string) error {
	o.mu.Lock()
	ci, ok := o.containers[id]
	if !ok {
		o.mu.Unlock()
		slog.Warn("wake requested for unknown container", "id", id)
		return nil
	}
	if ci.State != StateDormant && ci.State != StateCrashed {
		o.mu.Unlock()
		slog.Debug("ignoring wake — container not dormant", "id", id, "state", ci.State)
		return nil
	}
	if !CanTransition(ci.State, StateStarting) {
		o.mu.Unlock()
		return nil
	}
	// Cooldown: if the last wake attempt was recent, skip. This prevents
	// rapid SYN floods from creating a start/stop loop.
	if last, ok := o.wakeCooldowns[id]; ok && time.Since(last) < wakeCooldown {
		o.mu.Unlock()
		slog.Debug("wake cooldown active, skipping", "id", id, "elapsed", time.Since(last))
		return nil
	}
	o.wakeCooldowns[id] = time.Now()
	ci.LastWakeAttempt = time.Now()
	oldState := ci.State
	ci.State = StateStarting
	o.mu.Unlock()

	// Notify watchers so the Discord bot can post a wake notification.
	o.setState(id, StateStarting)

	o.logEvent(id, "wake", reason)
	slog.Info("waking container", "name", ci.DisplayName, "id", id, "reason", reason, "old_state", oldState)

	// Use background context for the Docker start call and the wait-for-running
	// poll — the HTTP request context will be cancelled when the API response
	// is sent, but we need the start to complete asynchronously.
	bgCtx := context.Background()
	if o.dock == nil {
		slog.Warn("dock client is nil, cannot start container", "id", id)
		o.setState(id, StateDormant)
		return fmt.Errorf("docker client not available")
	}
	if err := o.dock.Start(bgCtx, id); err != nil {
		slog.Error("failed to start container", "id", id, "err", err)
		o.setState(id, StateDormant)
		return err
	}

	// The Docker event subscription will detect the "start" event and
	// transition to Running. But also poll once as a fallback.
	go o.waitForRunning(bgCtx, id)
	return nil
}

// waitForRunning polls the container state after a start call and transitions
// to running once Docker confirms it.
func (o *Orchestrator) waitForRunning(ctx context.Context, id string) {
	for i := 0; i < 30; i++ {
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
		inspect, err := o.dock.Inspect(ctx, id)
		if err != nil {
			slog.Warn("inspect failed during wait-for-running", "id", id, "err", err)
			continue
		}
		if inspect.State != nil && inspect.State.Running {
			o.onContainerRunning(id)
			return
		}
		if inspect.State != nil && inspect.State.Status == "exited" {
			exitCode := inspect.State.ExitCode
			slog.Warn("container exited during start", "id", id, "exitCode", exitCode)
			if exitCode == 0 || exitCode == 143 {
				o.setState(id, StateDormant)
			} else {
				o.onContainerCrash(id, exitCode)
			}
			return
		}
	}
	slog.Warn("timeout waiting for container to reach running state", "id", id)
	o.setState(id, StateDormant)
}

// onContainerRunning is called when a container transitions to the running
// state. It arms the idle timer and notifies watchers.
func (o *Orchestrator) onContainerRunning(id string) {
	// Get Docker's actual start time from the inspect response.
	var dockerStartedAt time.Time
	if inspect, err := o.dock.Inspect(context.Background(), id); err == nil {
		if inspect.State != nil && inspect.State.StartedAt != "" {
			if t, err := time.Parse(time.RFC3339Nano, inspect.State.StartedAt); err == nil {
				dockerStartedAt = t
			}
		}
	}

	o.mu.Lock()
	if ci, ok := o.containers[id]; ok {
		if !dockerStartedAt.IsZero() {
			ci.StartedAt = dockerStartedAt
		} else {
			ci.StartedAt = time.Now()
		}
	}
	o.mu.Unlock()
	o.setState(id, StateRunning)
	// Guard against nil — container may have been removed between
	// releasing the lock and calling GetContainer.
	if ci := o.GetContainer(id); ci != nil {
		// StartIdleTimer sets LastTrafficAt so the UI shows the
		// start of the idle countdown.
		o.StartIdleTimer(id, ci.SnapTimeout)
	}
	o.logEvent(id, "manual_start", "container reached running state")
}

// onContainerCrash handles unexpected container exits.
func (o *Orchestrator) onContainerCrash(id string, exitCode int) {
	o.StopIdleTimer(id)
	o.mu.Lock()
	if ci, ok := o.containers[id]; ok {
		ci.LastExitCode = exitCode
	}
	o.mu.Unlock()
	o.setState(id, StateCrashed)
	o.logEvent(id, "crash", fmt.Sprintf("exit_code=%d", exitCode))

	slog.Error("container crashed", "id", id, "exitCode", exitCode)
}

// ManualStart starts a container by ID or name (from API/Discord).
func (o *Orchestrator) ManualStart(ctx context.Context, idOrName string) error {
	ci := o.GetContainer(idOrName)
	if ci == nil {
		ci = o.FindByName(idOrName)
	}
	if ci == nil {
		slog.Warn("manual start: container not found", "idOrName", idOrName)
		return nil
	}
	return o.WakeContainer(ctx, ci.ID, "manual_start")
}

// ManualStop stops a container by ID or name (from API/Discord).
func (o *Orchestrator) ManualStop(ctx context.Context, idOrName string) error {
	ci := o.GetContainer(idOrName)
	if ci == nil {
		ci = o.FindByName(idOrName)
	}
	if ci == nil {
		slog.Warn("manual stop: container not found", "idOrName", idOrName)
		return nil
	}
	return o.Snap(ctx, ci.ID, "manual_stop")
}

// Snap initiates an idle/manual shutdown for the given container.
func (o *Orchestrator) Snap(ctx context.Context, id string, reason string) error {
	o.mu.Lock()
	ci, ok := o.containers[id]
	if !ok {
		o.mu.Unlock()
		return nil
	}
	if ci.State != StateRunning {
		o.mu.Unlock()
		slog.Debug("ignoring snap — container not running", "id", id, "state", ci.State)
		return nil
	}
	// Set stop reason while still holding the lock to avoid TOCTOU.
	ci.StopReason = reason
	o.mu.Unlock()

	o.setState(id, StateStopping)
	o.StopIdleTimer(id)
	// Map the snap reason to the event_log event_type.
	eventType := reason
	if reason == "idle_timeout" {
		eventType = "idle_shutdown"
	}
	o.logEvent(id, eventType, reason)
	slog.Info("snapping container", "name", ci.DisplayName, "id", id, "reason", reason)

	// Use background context — the HTTP request context will be cancelled when
	// the API response is sent, but docker stop needs to complete asynchronously.
	if err := o.dock.Stop(context.Background(), id, 10); err != nil {
		slog.Error("failed to stop container", "id", id, "err", err)
		return err
	}
	return nil
}
