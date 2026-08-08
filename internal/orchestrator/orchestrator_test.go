package orchestrator

import (
	"context"
	"database/sql"
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
