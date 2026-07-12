// Package serverlogs manages per-server state-change logs stored in SQLite
// (the server_log table). Each state transition is written as a row with
// a timestamp, the old/new states, and a human-readable blurb.
package serverlogs

import (
	"database/sql"
	"log/slog"
	"time"
)

// LogEntry is a single state-change log row.
type LogEntry struct {
	ID            int64     `json:"id"`
	Timestamp     time.Time `json:"timestamp"`
	ContainerID   string    `json:"container_id"`
	ContainerName string    `json:"container_name"`
	OldState      string    `json:"old_state"`
	NewState      string    `json:"new_state"`
	Blurb         string    `json:"blurb"`
}

// AppendEntry writes a state-change log entry to the server_log table.
// Best-effort: errors are logged but never returned, so logging never
// blocks the orchestrator's state machine.
func AppendEntry(db *sql.DB, containerID, containerName, oldState, newState, blurb string) {
	_, err := db.Exec(
		`INSERT INTO server_log (container_id, container_name, old_state, new_state, blurb)
		 VALUES (?, ?, ?, ?, ?)`,
		containerID, containerName, oldState, newState, blurb)
	if err != nil {
		slog.Warn("serverlogs: failed to insert log entry", "container", containerName, "err", err)
	}
}

// ReadEntries returns up to maxEntries most recent log entries for a container.
// If maxEntries is 0, returns up to 200 entries (the historical default).
func ReadEntries(db *sql.DB, containerID string, maxEntries int) ([]LogEntry, error) {
	if maxEntries <= 0 {
		maxEntries = 200
	}

	rows, err := db.Query(
		`SELECT id, timestamp, container_id, container_name, old_state, new_state, blurb
		 FROM server_log
		 WHERE container_id = ?
		 ORDER BY timestamp DESC
		 LIMIT ?`,
		containerID, maxEntries)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []LogEntry
	for rows.Next() {
		var e LogEntry
		if err := rows.Scan(&e.ID, &e.Timestamp, &e.ContainerID, &e.ContainerName, &e.OldState, &e.NewState, &e.Blurb); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// DeleteEntries removes all log entries for a container. Called when a
// container is removed from Thanos management.
func DeleteEntries(db *sql.DB, containerID string) {
	_, err := db.Exec(`DELETE FROM server_log WHERE container_id = ?`, containerID)
	if err != nil {
		slog.Warn("serverlogs: failed to delete log entries", "container_id", containerID, "err", err)
	}
}