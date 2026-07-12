// Package traffic implements traffic logging for Thanos. It records
// wake-on-connect events to the traffic_log table and maintains a
// deduplicated known_clients table of every unique (container, IP) pair
// that has accessed a server.
package traffic

import (
	"database/sql"
	"log/slog"
	"sync"
	"time"
)

// Logger writes traffic events to SQLite. It is safe for concurrent use.
type Logger struct {
	db *sql.DB

	// In-memory dedup for running-container traffic: containerID → srcIP →
	// lastSeen. If an IP was seen in the last dedupWindow, skip the DB write.
	// This prevents thousands of packets per second from flooding SQLite.
	mu        sync.Mutex
	recent    map[string]map[string]time.Time
	dedupWindow time.Duration
}

// New creates a Logger backed by the given database.
func New(db *sql.DB) *Logger {
	return &Logger{
		db:         db,
		recent:     make(map[string]map[string]time.Time),
		dedupWindow: 60 * time.Second,
	}
}

// LogWake records a wake-on-connect event — a packet that triggered a
// container start. These are low-volume (one per connection attempt), so
// every one is logged to traffic_log and upserted into known_clients.
func (l *Logger) LogWake(containerID, containerName, srcIP string, srcPort, dstPort int, protocol string) {
	// Insert into traffic_log (append-only event record).
	_, err := l.db.Exec(
		`INSERT INTO traffic_log (container_id, container_name, src_ip, src_port, dst_port, protocol, event_type)
		 VALUES (?, ?, ?, ?, ?, ?, 'wake_on_connect')`,
		containerID, containerName, srcIP, srcPort, dstPort, protocol)
	if err != nil {
		slog.Warn("traffic: failed to insert traffic_log entry", "container", containerName, "err", err)
	}

	l.upsertClient(containerID, containerName, srcIP, dstPort)
}

// LogTraffic records ongoing traffic to a running container. Uses in-memory
// dedup so each unique (container, IP) pair hits the DB at most once per
// minute. This prevents high packet rates from flooding SQLite.
func (l *Logger) LogTraffic(containerID, containerName, srcIP string, dstPort int) {
	// Check the in-memory dedup map.
	l.mu.Lock()
	if _, ok := l.recent[containerID]; !ok {
		l.recent[containerID] = make(map[string]time.Time)
	}
	if last, ok := l.recent[containerID][srcIP]; ok {
		if time.Since(last) < l.dedupWindow {
			l.mu.Unlock()
			return // Skip — this IP was seen recently.
		}
	}
	l.recent[containerID][srcIP] = time.Now()
	l.mu.Unlock()

	l.upsertClient(containerID, containerName, srcIP, dstPort)
}

// upsertClient inserts or updates the known_clients row for a (container, IP)
// pair. On conflict, it updates last_seen, pkt_count, and last_port.
func (l *Logger) upsertClient(containerID, containerName, srcIP string, dstPort int) {
	_, err := l.db.Exec(
		`INSERT INTO known_clients (container_id, src_ip, container_name, last_port, last_seen, pkt_count)
		 VALUES (?, ?, ?, ?, CURRENT_TIMESTAMP, 1)
		 ON CONFLICT(container_id, src_ip) DO UPDATE SET
		   last_port = excluded.last_port,
		   last_seen = excluded.last_seen,
		   pkt_count = known_clients.pkt_count + 1`,
		containerID, srcIP, containerName, dstPort)
	if err != nil {
		slog.Warn("traffic: failed to upsert known_clients", "container", containerName, "ip", srcIP, "err", err)
	}
}

// DeleteContainer removes all traffic data for a container that was
// destroyed or removed from Thanos management.
func (l *Logger) DeleteContainer(containerID string) {
	l.mu.Lock()
	delete(l.recent, containerID)
	l.mu.Unlock()

	_, err := l.db.Exec(`DELETE FROM traffic_log WHERE container_id = ?`, containerID)
	if err != nil {
		slog.Warn("traffic: failed to delete traffic_log entries", "container_id", containerID, "err", err)
	}
	_, err = l.db.Exec(`DELETE FROM known_clients WHERE container_id = ?`, containerID)
	if err != nil {
		slog.Warn("traffic: failed to delete known_clients entries", "container_id", containerID, "err", err)
	}
}

// WakeEntry is a single traffic_log row.
type WakeEntry struct {
	ID            int64     `json:"id"`
	Timestamp     time.Time `json:"timestamp"`
	ContainerID   string    `json:"container_id"`
	ContainerName string    `json:"container_name"`
	SrcIP         string    `json:"src_ip"`
	SrcPort       int       `json:"src_port"`
	DstPort       int       `json:"dst_port"`
	Protocol      string    `json:"protocol"`
	EventType     string    `json:"event_type"`
}

// ClientEntry is a single known_clients row.
type ClientEntry struct {
	ContainerID   string    `json:"container_id"`
	ContainerName string    `json:"container_name"`
	SrcIP         string    `json:"src_ip"`
	FirstSeen     time.Time `json:"first_seen"`
	LastSeen      time.Time `json:"last_seen"`
	PktCount      int       `json:"pkt_count"`
	LastPort      int       `json:"last_port"`
}

// RecentWakes returns the most recent wake events across all containers
// (system-wide view). If containerID is non-empty, filters to that container.
func RecentWakes(db *sql.DB, containerID string, limit int) ([]WakeEntry, error) {
	if limit <= 0 {
		limit = 50
	}
	q := `SELECT id, timestamp, container_id, container_name, src_ip, src_port, dst_port, protocol, event_type
	      FROM traffic_log`
	var args []any
	if containerID != "" {
		q += ` WHERE container_id = ?`
		args = append(args, containerID)
	}
	q += ` ORDER BY timestamp DESC LIMIT ?`
	args = append(args, limit)

	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []WakeEntry
	for rows.Next() {
		var e WakeEntry
		if err := rows.Scan(&e.ID, &e.Timestamp, &e.ContainerID, &e.ContainerName, &e.SrcIP, &e.SrcPort, &e.DstPort, &e.Protocol, &e.EventType); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// KnownClients returns the known clients for a container, ordered by
// last_seen descending (most recent first).
func KnownClients(db *sql.DB, containerID string, limit int) ([]ClientEntry, error) {
	if limit <= 0 {
		limit = 100
	}
	q := `SELECT container_id, container_name, src_ip, first_seen, last_seen, pkt_count, last_port
	      FROM known_clients`
	var args []any
	if containerID != "" {
		q += ` WHERE container_id = ?`
		args = append(args, containerID)
	}
	q += ` ORDER BY last_seen DESC LIMIT ?`
	args = append(args, limit)

	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []ClientEntry
	for rows.Next() {
		var e ClientEntry
		if err := rows.Scan(&e.ContainerID, &e.ContainerName, &e.SrcIP, &e.FirstSeen, &e.LastSeen, &e.PktCount, &e.LastPort); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}