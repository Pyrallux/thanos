package orchestrator

import (
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
