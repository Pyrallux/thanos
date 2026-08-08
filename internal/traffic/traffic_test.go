package traffic

import (
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// newTestDB creates an in-memory SQLite database with the traffic_log and
// known_clients tables, matching the production schema.
func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	_, err = db.Exec(`
		CREATE TABLE traffic_log (
			id             INTEGER PRIMARY KEY AUTOINCREMENT,
			timestamp      DATETIME DEFAULT CURRENT_TIMESTAMP,
			container_id   TEXT NOT NULL,
			container_name TEXT NOT NULL,
			src_ip         TEXT NOT NULL,
			src_port       INTEGER,
			dst_port       INTEGER NOT NULL,
			protocol       TEXT NOT NULL,
			event_type     TEXT NOT NULL DEFAULT 'wake_on_connect',
			blocked        INTEGER NOT NULL DEFAULT 0
		);
		CREATE TABLE known_clients (
			container_id   TEXT NOT NULL,
			src_ip         TEXT NOT NULL,
			container_name TEXT NOT NULL,
			first_seen     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			last_seen      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			pkt_count      INTEGER NOT NULL DEFAULT 1,
			last_port      INTEGER,
			blocked        INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (container_id, src_ip)
		);
	`)
	if err != nil {
		t.Fatalf("create tables: %v", err)
	}
	return db
}

// TestLogWakeAndRecentWakes verifies that a wake event is persisted to
// traffic_log and retrievable via RecentWakes. This is the audit trail
// shown in the web UI's traffic modal — a regression would make the
// traffic log silently empty.
func TestLogWakeAndRecentWakes(t *testing.T) {
	db := newTestDB(t)
	l := New(db)

	l.LogWake("id-a", "Minecraft", "203.0.113.5", 50000, 25565, "tcp", false)
	l.LogWake("id-a", "Minecraft", "203.0.113.5", 50001, 25565, "tcp", false)
	l.LogWake("id-b", "Valheim", "198.51.100.1", 50000, 2456, "udp", true)

	// All wakes, most recent first.
	entries, err := RecentWakes(db, "", 10)
	if err != nil {
		t.Fatalf("RecentWakes: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 wake entries, got %d", len(entries))
	}

	// Filter by container.
	entriesA, err := RecentWakes(db, "id-a", 10)
	if err != nil {
		t.Fatalf("RecentWakes(id-a): %v", err)
	}
	if len(entriesA) != 2 {
		t.Fatalf("expected 2 entries for id-a, got %d", len(entriesA))
	}

	// Verify blocked flag is preserved.
	entriesB, err := RecentWakes(db, "id-b", 10)
	if err != nil {
		t.Fatalf("RecentWakes(id-b): %v", err)
	}
	if len(entriesB) != 1 {
		t.Fatalf("expected 1 entry for id-b, got %d", len(entriesB))
	}
	if !entriesB[0].Blocked {
		t.Error("entry for id-b should have Blocked=true")
	}
}

// TestLogTrafficDedup verifies that LogTraffic only writes to the database
// once per (container, IP) pair within the dedup window. Without dedup,
// a high-packet-rate UDP game server would flood SQLite with thousands of
// identical rows per second.
func TestLogTrafficDedup(t *testing.T) {
	db := newTestDB(t)
	l := New(db)
	l.dedupWindow = 200 * time.Millisecond // short window for testing

	// First call for (id-a, 10.0.0.1) — should write.
	l.LogTraffic("id-a", "Minecraft", "10.0.0.1", 25565, false)
	// Second call immediately — should be deduped (skip DB write).
	l.LogTraffic("id-a", "Minecraft", "10.0.0.1", 25565, false)
	// Different IP — should write.
	l.LogTraffic("id-a", "Minecraft", "10.0.0.2", 25565, false)

	clients, err := KnownClients(db, "id-a", 100)
	if err != nil {
		t.Fatalf("KnownClients: %v", err)
	}
	if len(clients) != 2 {
		t.Fatalf("expected 2 unique clients after dedup, got %d", len(clients))
	}

	// Wait for dedup window to expire, then call again — should write.
	time.Sleep(250 * time.Millisecond)
	l.LogTraffic("id-a", "Minecraft", "10.0.0.1", 25565, false)

	clients, err = KnownClients(db, "id-a", 100)
	if err != nil {
		t.Fatalf("KnownClients after window: %v", err)
	}
	if len(clients) != 2 {
		t.Fatalf("expected 2 clients (same IP re-upserted), got %d", len(clients))
	}
	// The re-upserted client should have pkt_count=2.
	for _, c := range clients {
		if c.SrcIP == "10.0.0.1" {
			if c.PktCount != 2 {
				t.Errorf("pkt_count for 10.0.0.1 = %d, want 2 (one before window, one after)", c.PktCount)
			}
		}
	}
}

// TestKnownClientsOrdering verifies that KnownClients returns results
// ordered by last_seen descending (most recent first). The web UI displays
// them in this order so the most active clients appear at the top.
func TestKnownClientsOrdering(t *testing.T) {
	db := newTestDB(t)

	// Insert with distinct timestamps so ordering is deterministic.
	// SQLite's CURRENT_TIMESTAMP has 1-second resolution, so we set
	// last_seen explicitly with spaced-out values.
	clients := []struct {
		ip       string
		lastSeen string
	}{
		{"10.0.0.1", "2026-01-01 00:00:01"},
		{"10.0.0.2", "2026-01-01 00:00:02"},
		{"10.0.0.3", "2026-01-01 00:00:03"},
	}
	for _, c := range clients {
		_, err := db.Exec(
			`INSERT INTO known_clients (container_id, src_ip, container_name, last_port, first_seen, last_seen, pkt_count, blocked)
			 VALUES ('id-a', ?, 'Minecraft', 25565, ?, ?, 1, 0)`,
			c.ip, c.lastSeen, c.lastSeen)
		if err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	got, err := KnownClients(db, "id-a", 100)
	if err != nil {
		t.Fatalf("KnownClients: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 clients, got %d", len(got))
	}
	// Most recent (highest last_seen) first.
	if got[0].SrcIP != "10.0.0.3" {
		t.Errorf("first client = %s, want 10.0.0.3 (most recent)", got[0].SrcIP)
	}
	if got[2].SrcIP != "10.0.0.1" {
		t.Errorf("last client = %s, want 10.0.0.1 (oldest)", got[2].SrcIP)
	}
}

// TestDeleteContainer verifies that DeleteContainer removes all traffic_log
// and known_clients rows for a container. This is called when a container
// is destroyed — if it leaks data, the DB grows indefinitely with orphaned
// rows for containers that no longer exist.
func TestDeleteContainer(t *testing.T) {
	db := newTestDB(t)
	l := New(db)

	l.LogWake("id-a", "Minecraft", "10.0.0.1", 50000, 25565, "tcp", false)
	l.LogWake("id-b", "Valheim", "10.0.0.2", 50000, 2456, "udp", false)

	l.DeleteContainer("id-a")

	// id-a should have no entries.
	wakes, err := RecentWakes(db, "id-a", 100)
	if err != nil {
		t.Fatalf("RecentWakes(id-a): %v", err)
	}
	if len(wakes) != 0 {
		t.Errorf("expected 0 wakes for deleted container, got %d", len(wakes))
	}
	clients, err := KnownClients(db, "id-a", 100)
	if err != nil {
		t.Fatalf("KnownClients(id-a): %v", err)
	}
	if len(clients) != 0 {
		t.Errorf("expected 0 clients for deleted container, got %d", len(clients))
	}

	// id-b should be unaffected.
	wakesB, err := RecentWakes(db, "id-b", 100)
	if err != nil {
		t.Fatalf("RecentWakes(id-b): %v", err)
	}
	if len(wakesB) != 1 {
		t.Errorf("expected 1 wake for id-b (unaffected), got %d", len(wakesB))
	}
}

// TestRecentWakesDefaultLimit verifies that a limit <= 0 defaults to 50.
func TestRecentWakesDefaultLimit(t *testing.T) {
	db := newTestDB(t)
	l := New(db)

	for i := 0; i < 60; i++ {
		l.LogWake("id-a", "Minecraft", "10.0.0.1", 50000+i, 25565, "tcp", false)
	}

	entries, err := RecentWakes(db, "", 0) // 0 → default 50
	if err != nil {
		t.Fatalf("RecentWakes: %v", err)
	}
	if len(entries) != 50 {
		t.Errorf("expected 50 entries with default limit, got %d", len(entries))
	}
}
