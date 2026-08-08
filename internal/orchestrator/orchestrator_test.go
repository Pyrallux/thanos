package orchestrator

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// newTestDB returns an in-memory SQLite database with the server_log table
// created — the only table setState touches via serverlogs.AppendEntry.
func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	_, err = db.Exec(`CREATE TABLE server_log (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		timestamp DATETIME DEFAULT CURRENT_TIMESTAMP,
		container_id TEXT NOT NULL,
		container_name TEXT NOT NULL,
		old_state TEXT,
		new_state TEXT,
		blurb TEXT
	)`)
	if err != nil {
		t.Fatalf("create table: %v", err)
	}
	return db
}

// TestSetStateRecordsLastOnline verifies that transitioning out of the
// running state stamps LastOnlineAt — the timestamp the UI/Discord display
// as "Last online". This is the core of the #8 fix: previously StartedAt
// (boot time) was shown instead of when the server actually went offline.
func TestSetStateRecordsLastOnline(t *testing.T) {
	o := &Orchestrator{
		db:          newTestDB(t),
		containers:  make(map[string]*ContainerInfo),
		stateTimers: make(map[string]*time.Timer),
	}
	id := "test-container"
	started := time.Now().Add(-10 * time.Minute)
	o.containers[id] = &ContainerInfo{
		ID:        id,
		Name:      "test",
		State:     StateRunning,
		StartedAt: started,
	}

	before := time.Now()
	o.setState(id, StateStopping)
	after := time.Now()

	ci := o.GetContainer(id)
	if ci == nil {
		t.Fatal("container not found after setState")
	}
	if ci.LastOnlineAt.Before(before) || ci.LastOnlineAt.After(after) {
		t.Errorf("LastOnlineAt not set on running->stopping transition: got %v, want [%v,%v]",
			ci.LastOnlineAt, before, after)
	}
	if !ci.StartedAt.Equal(started) {
		t.Errorf("StartedAt should be unchanged: got %v, want %v", ci.StartedAt, started)
	}
}

// TestSetStateNoLastOnlineWhenNotRunning verifies LastOnlineAt is NOT set
// when transitioning between non-running states (e.g. dormant->starting).
func TestSetStateNoLastOnlineWhenNotRunning(t *testing.T) {
	o := &Orchestrator{
		db:          newTestDB(t),
		containers:  make(map[string]*ContainerInfo),
		stateTimers: make(map[string]*time.Timer),
	}
	id := "test-container"
	o.containers[id] = &ContainerInfo{
		ID:    id,
		Name:  "test",
		State: StateDormant,
	}
	o.setState(id, StateStarting)
	ci := o.GetContainer(id)
	if !ci.LastOnlineAt.IsZero() {
		t.Errorf("LastOnlineAt should remain zero for dormant->starting, got %v", ci.LastOnlineAt)
	}
}

// TestSetStateNoOpOnSameState verifies that setState is a no-op when the
// state hasn't changed — no log entry, no watcher notification. This
// prevents redundant "starting -> starting" log spam when WakeContainer
// calls setState(Starting) after already setting the state inline.
func TestSetStateNoOpOnSameState(t *testing.T) {
	o := &Orchestrator{
		db:          newTestDB(t),
		containers:  make(map[string]*ContainerInfo),
		stateTimers: make(map[string]*time.Timer),
	}
	id := "test-container"
	o.containers[id] = &ContainerInfo{
		ID:    id,
		Name:  "test",
		State: StateStarting,
	}
	o.setState(id, StateStarting) // should be a no-op

	// Verify no log entry was written (table should be empty).
	var count int
	err := o.db.QueryRow(`SELECT COUNT(*) FROM server_log`).Scan(&count)
	if err != nil {
		t.Fatalf("query server_log: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 log entries for same-state no-op, got %d", count)
	}
}

// TestWakeCooldownBlocksRapidRetries verifies that WakeContainer respects
// a cooldown after a failed wake attempt. Without this, a stream of SYNs
// can trigger dozens of dock.Start() calls per second, creating a tight
// starting→dormant→starting loop.
func TestWakeCooldownBlocksRapidRetries(t *testing.T) {
	o := &Orchestrator{
		db:            newTestDB(t),
		containers:    make(map[string]*ContainerInfo),
		stateTimers:   make(map[string]*time.Timer),
		wakeCooldowns: make(map[string]time.Time),
	}
	id := "test-container"
	o.containers[id] = &ContainerInfo{
		ID:    id,
		Name:  "test",
		State: StateDormant,
	}

	// First wake attempt — should set the cooldown and transition to Starting.
	// dock is nil, so WakeContainer will fail on dock.Start() and revert to
	// Dormant — but the cooldown timestamp should already be set.
	o.WakeContainer(context.Background(), id, "test")

	// After the failed start, WakeContainer reverts to Dormant.
	// The cooldown timestamp is what matters — check it was set.
	o.mu.RLock()
	lastWake, hasCooldown := o.wakeCooldowns[id]
	o.mu.RUnlock()
	if !hasCooldown {
		t.Fatal("first wake should set the cooldown timestamp")
	}
	if time.Since(lastWake) > 1*time.Second {
		t.Errorf("cooldown timestamp should be recent, got %v ago", time.Since(lastWake))
	}

	// Ensure state is back to Dormant (start failed because dock is nil).
	if ci := o.GetContainer(id); ci.State != StateDormant {
		t.Fatalf("state should be Dormant after failed start, got %s", ci.State)
	}

	// Second wake attempt immediately after — should be blocked by cooldown.
	// The state should NOT change to Starting (the guard returns before
	// touching the state).
	o.WakeContainer(context.Background(), id, "test")
	if ci := o.GetContainer(id); ci.State != StateDormant {
		t.Fatal("second wake during cooldown should NOT change state")
	}

	// Simulate cooldown expiry.
	o.mu.Lock()
	o.wakeCooldowns[id] = time.Now().Add(-wakeCooldown - 1*time.Second)
	o.mu.Unlock()

	// Third wake attempt after cooldown — should transition to Starting
	// (then fail on dock.Start() and revert, but the cooldown is reset).
	o.WakeContainer(context.Background(), id, "test")
	if ci := o.GetContainer(id); ci.State != StateDormant {
		t.Fatalf("wake after cooldown should attempt start (state after failure = Dormant), got %s", ci.State)
	}

	// Cooldown should be refreshed.
	o.mu.RLock()
	lastWake2 := o.wakeCooldowns[id]
	o.mu.RUnlock()
	if !lastWake2.After(lastWake) {
		t.Fatal("cooldown should be refreshed after wake attempt")
	}
}

// TestStateChangeBlurb verifies that the human-readable blurbs generated for
// each state transition are non-empty and distinguish crash-recovery from
// normal stops. These blurbs appear in the per-server state log shown in the
// web UI, so a regression would produce confusing or empty log entries.
func TestStateChangeBlurb(t *testing.T) {
	tests := []struct {
		name       string
		old, new   State
		stopReason string
		wantSubstr string
	}{
		{"starting", StateDormant, StateStarting, "", "starting up"},
		{"running", StateStarting, StateRunning, "", "running state"},
		{"stopping with reason", StateRunning, StateStopping, "idle_timeout", "reason: idle_timeout"},
		{"stopping without reason", StateRunning, StateStopping, "", "idle or manual stop"},
		{"dormant after crash", StateCrashed, StateDormant, "", "after crash"},
		{"dormant normal", StateRunning, StateDormant, "", "now dormant"},
		{"crashed", StateRunning, StateCrashed, "", "crashed unexpectedly"},
		{"unmanaged", StateDormant, StateUnmanaged, "", "removed from Thanos"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stateChangeBlurb(tt.old, tt.new, tt.stopReason)
			if got == "" {
				t.Fatal("blurb should not be empty")
			}
			if !strings.Contains(got, tt.wantSubstr) {
				t.Errorf("stateChangeBlurb(%s→%s, reason=%q) = %q, want substring %q",
					tt.old, tt.new, tt.stopReason, got, tt.wantSubstr)
			}
		})
	}
}

// TestFindByName verifies lookup by both display name and container name,
// case-insensitively. The Discord /start command passes a user-typed name,
// so case sensitivity or a missing name match would cause the command to
// fail to find a server that exists.
func TestFindByName(t *testing.T) {
	o := &Orchestrator{
		containers: make(map[string]*ContainerInfo),
	}
	o.containers["id-a"] = &ContainerInfo{ID: "id-a", Name: "mc-survival", DisplayName: "Minecraft Survival"}
	o.containers["id-b"] = &ContainerInfo{ID: "id-b", Name: "valheim", DisplayName: "Valheim"}

	tests := []struct {
		query string
		want  string // container ID, empty if not found
	}{
		{"Minecraft Survival", "id-a"}, // exact display name
		{"minecraft survival", "id-a"}, // case-insensitive display name
		{"mc-survival", "id-a"},        // container name
		{"MC-SURVIVAL", "id-a"},        // case-insensitive container name
		{"Valheim", "id-b"},            // exact display name
		{"valheim", "id-b"},            // container name
		{"nonexistent", ""},            // not found
		{"", ""},                       // empty query
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			ci := o.FindByName(tt.query)
			if tt.want == "" {
				if ci != nil {
					t.Errorf("FindByName(%q) = %s, want nil", tt.query, ci.ID)
				}
				return
			}
			if ci == nil {
				t.Fatalf("FindByName(%q) = nil, want %s", tt.query, tt.want)
			}
			if ci.ID != tt.want {
				t.Errorf("FindByName(%q) = %s, want %s", tt.query, ci.ID, tt.want)
			}
		})
	}
}

// TestContainersSortedByDisplayName verifies that Containers() returns
// containers sorted alphabetically by display name (case-insensitive). The
// UI and Discord embed both rely on stable ordering — without it, cards
// and embed fields would reshuffle on every refresh.
func TestContainersSortedByDisplayName(t *testing.T) {
	o := &Orchestrator{
		containers: make(map[string]*ContainerInfo),
	}
	o.containers["id-3"] = &ContainerInfo{ID: "id-3", DisplayName: "Zebra Server"}
	o.containers["id-1"] = &ContainerInfo{ID: "id-1", DisplayName: "alpha"}
	o.containers["id-2"] = &ContainerInfo{ID: "id-2", DisplayName: "Beta"}

	got := o.Containers()
	if len(got) != 3 {
		t.Fatalf("expected 3 containers, got %d", len(got))
	}
	// Case-insensitive sort: alpha, Beta, Zebra Server
	want := []string{"id-1", "id-2", "id-3"}
	for i, w := range want {
		if got[i].ID != w {
			t.Errorf("Containers()[%d].ID = %s (%s), want %s", i, got[i].ID, got[i].DisplayName, w)
		}
	}
}

// TestSnapIgnoresNonRunningContainer verifies that Snap() is a no-op when the
// container is not in the running state. Without this guard, an idle timer
// firing after a manual stop (race) would call dock.Stop on an already-stopped
// container, potentially causing a Docker API error.
func TestSnapIgnoresNonRunningContainer(t *testing.T) {
	o := &Orchestrator{
		db:          newTestDB(t),
		containers:  make(map[string]*ContainerInfo),
		stateTimers: make(map[string]*time.Timer),
	}
	id := "test-container"
	o.containers[id] = &ContainerInfo{
		ID:    id,
		Name:  "test",
		State: StateDormant, // not running
	}

	err := o.Snap(context.Background(), id, "idle_timeout")
	if err != nil {
		t.Errorf("Snap on non-running container should return nil, got %v", err)
	}
	if ci := o.GetContainer(id); ci.State != StateDormant {
		t.Errorf("state should remain Dormant, got %s", ci.State)
	}
}

// TestManualStartNotFound verifies that ManualStart returns nil (not an error)
// when the container doesn't exist. The API and Discord handlers treat nil
// error as success, so returning an error for a missing container would cause
// a misleading 500 response instead of a clean no-op.
func TestManualStartNotFound(t *testing.T) {
	o := &Orchestrator{
		db:          newTestDB(t),
		containers:  make(map[string]*ContainerInfo),
		stateTimers: make(map[string]*time.Timer),
	}
	err := o.ManualStart(context.Background(), "nonexistent")
	if err != nil {
		t.Errorf("ManualStart on missing container should return nil, got %v", err)
	}
}

// TestManualStopNotFound verifies the same nil-error contract for ManualStop.
func TestManualStopNotFound(t *testing.T) {
	o := &Orchestrator{
		db:          newTestDB(t),
		containers:  make(map[string]*ContainerInfo),
		stateTimers: make(map[string]*time.Timer),
	}
	err := o.ManualStop(context.Background(), "nonexistent")
	if err != nil {
		t.Errorf("ManualStop on missing container should return nil, got %v", err)
	}
}

// TestSetStateNotifiesWatchers verifies that a state change calls
// OnStateChange on registered watchers with the updated container info.
// The Discord bot and sentinel both depend on these notifications — if
// setState stops notifying, wake/snap/crash events would be silently
// dropped.
func TestSetStateNotifiesWatchers(t *testing.T) {
	o := &Orchestrator{
		db:          newTestDB(t),
		containers:  make(map[string]*ContainerInfo),
		stateTimers: make(map[string]*time.Timer),
	}
	id := "test-container"
	o.containers[id] = &ContainerInfo{ID: id, Name: "test", State: StateDormant}

	w := &fakeWatcher{}
	o.RegisterWatcher(w)

	o.setState(id, StateStarting)

	if w.got == nil {
		t.Fatal("watcher was not notified")
	}
	if w.got.ID != id {
		t.Errorf("watcher got ID %s, want %s", w.got.ID, id)
	}
	if w.got.State != StateStarting {
		t.Errorf("watcher got state %s, want %s", w.got.State, StateStarting)
	}
	if w.callCount != 1 {
		t.Errorf("watcher called %d times, want 1", w.callCount)
	}
}

// fakeWatcher is a test StateWatcher that records the last notification.
type fakeWatcher struct {
	got       *ContainerInfo
	callCount int
}

func (f *fakeWatcher) OnStateChange(ci *ContainerInfo) {
	f.got = ci
	f.callCount++
}
