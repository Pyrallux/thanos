package serverlogs

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

// newTestDB creates an in-memory SQLite database with the server_log table.
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

// TestAppendAndReadEntries verifies that a state-change entry written by
// AppendEntry is retrievable via ReadEntries with the correct field values.
// This is the per-server state log shown in the web UI — a regression would
// make the log appear empty or show garbled data.
func TestAppendAndReadEntries(t *testing.T) {
	db := newTestDB(t)

	AppendEntry(db, "id-a", "Minecraft", "dormant", "starting", "server is starting up")
	AppendEntry(db, "id-b", "Valheim", "dormant", "starting", "server is starting up")

	// Read entries for id-a — should get 1.
	entries, err := ReadEntries(db, "id-a", 100)
	if err != nil {
		t.Fatalf("ReadEntries: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry for id-a, got %d", len(entries))
	}
	// Verify field values.
	e := entries[0]
	if e.ContainerID != "id-a" {
		t.Errorf("ContainerID = %q, want %q", e.ContainerID, "id-a")
	}
	if e.ContainerName != "Minecraft" {
		t.Errorf("ContainerName = %q, want %q", e.ContainerName, "Minecraft")
	}
	if e.OldState != "dormant" {
		t.Errorf("OldState = %q, want %q", e.OldState, "dormant")
	}
	if e.NewState != "starting" {
		t.Errorf("NewState = %q, want %q", e.NewState, "starting")
	}
	if e.Blurb != "server is starting up" {
		t.Errorf("Blurb = %q, want %q", e.Blurb, "server is starting up")
	}
}

// TestReadEntriesDefaultLimit verifies that maxEntries <= 0 defaults to 200.
func TestReadEntriesDefaultLimit(t *testing.T) {
	db := newTestDB(t)
	for i := 0; i < 250; i++ {
		AppendEntry(db, "id-a", "Minecraft", "dormant", "starting", "wake")
	}
	entries, err := ReadEntries(db, "id-a", 0)
	if err != nil {
		t.Fatalf("ReadEntries: %v", err)
	}
	if len(entries) != 200 {
		t.Errorf("expected 200 entries with default limit, got %d", len(entries))
	}
}

// TestReadEntriesForUnknownContainer verifies that reading entries for a
// container ID that has no log entries returns an empty slice (not an error).
func TestReadEntriesForUnknownContainer(t *testing.T) {
	db := newTestDB(t)
	AppendEntry(db, "id-a", "Minecraft", "dormant", "starting", "wake")

	entries, err := ReadEntries(db, "nonexistent", 100)
	if err != nil {
		t.Fatalf("ReadEntries: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 entries for unknown container, got %d", len(entries))
	}
}

// TestDeleteEntries verifies that DeleteEntries removes all log entries for
// a container without affecting other containers. This is called when a
// container is destroyed — leaked rows would accumulate forever.
func TestDeleteEntries(t *testing.T) {
	db := newTestDB(t)
	AppendEntry(db, "id-a", "Minecraft", "dormant", "starting", "wake")
	AppendEntry(db, "id-a", "Minecraft", "starting", "running", "running")
	AppendEntry(db, "id-b", "Valheim", "dormant", "starting", "wake")

	DeleteEntries(db, "id-a")

	entriesA, err := ReadEntries(db, "id-a", 100)
	if err != nil {
		t.Fatalf("ReadEntries(id-a): %v", err)
	}
	if len(entriesA) != 0 {
		t.Errorf("expected 0 entries after delete, got %d", len(entriesA))
	}

	entriesB, err := ReadEntries(db, "id-b", 100)
	if err != nil {
		t.Fatalf("ReadEntries(id-b): %v", err)
	}
	if len(entriesB) != 1 {
		t.Errorf("expected 1 entry for id-b (unaffected), got %d", len(entriesB))
	}
}
