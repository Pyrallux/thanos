// Package config manages Thanos global configuration stored in SQLite.
package config

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"strconv"
	"strings"

	_ "modernc.org/sqlite"

	"golang.org/x/crypto/bcrypt"
)

// Config holds all global Thanos settings loaded from thanos.db.
type Config struct {
	DB *sql.DB

	// Thanos daemon config
	NetworkInterface string
	LogLevel         string
	APIPort          int

	// Discord config
	DiscordBotToken     string
	DiscordGuildID      string
	DiscordChannelID    string // status channel for the persistent embed
	DiscordLogChannelID string // log channel for snap/wake events
	DiscordStatusMsgID  string

	// Web UI auth
	WebUsername     string
	WebPasswordHash string

	// IP blacklist (newline-separated CIDR patterns, e.g. "23.111.14.183/32")
	Blacklist     []netip.Prefix
	blacklistRaw  string
}

// Defaults applied when a key is missing from the DB on first load.
var defaults = map[string]string{
	"network_interface": "",
	"log_level":         "info",
	"api_port":           "4040",
}

// NetworkInterfaceInfo describes an OS network interface for the setup and
// settings screens.
type NetworkInterfaceInfo struct {
	Name  string   `json:"name"`
	Addrs []string `json:"addrs"`
}

// ListNetworkInterfaces returns the available interfaces with Loopback first.
func ListNetworkInterfaces() ([]NetworkInterfaceInfo, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}

	out := []NetworkInterfaceInfo{{Name: "Loopback", Addrs: []string{"127.0.0.1/8", "::1/128"}}}
	for _, ifc := range ifaces {
		addrs := []string{}
		a, err := ifc.Addrs()
		if err == nil {
			for _, addr := range a {
				addrs = append(addrs, addr.String())
			}
		}
		out = append(out, NetworkInterfaceInfo{Name: ifc.Name, Addrs: addrs})
	}
	return out, nil
}

// Load opens (or creates) thanos.db, runs migrations, and returns a Config.
// If the DB is freshly created (no web_ui_config row), it blocks while the
// first-run setup wizard is served on http://localhost:4040/setup.
func Load(ctx context.Context) (*Config, error) {
	db, err := sql.Open("sqlite", "thanos.db")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1) // SQLite single-writer; use WAL in migrations.

	if err := migrate(db); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}

	needsSetup, err := isFirstRun(db)
	if err != nil {
		return nil, err
	}

	if needsSetup {
		slog.Info("first run detected; launching setup wizard")
		cfg := &Config{DB: db}
		if err := runSetupWizard(ctx, db, cfg); err != nil {
			return nil, fmt.Errorf("setup wizard: %w", err)
		}
		return cfg, nil
	}

	return loadConfig(db)
}

// isFirstRun returns true when the web_ui_config table has no real
// credentials (empty username means the setup wizard hasn't run yet).
func isFirstRun(db *sql.DB) (bool, error) {
	var username sql.NullString
	err := db.QueryRow(`SELECT username FROM web_ui_config WHERE id = 1`).Scan(&username)
	if err == sql.ErrNoRows {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return !username.Valid || username.String == "", nil
}

// loadConfig reads all persisted settings into a Config struct.
func loadConfig(db *sql.DB) (*Config, error) {
	cfg := &Config{DB: db}

	// thanos_config is key/value — read each row individually.
	rows, err := db.Query(`SELECT key, value FROM thanos_config`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	kv := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		kv[k] = v
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// apply defaults then overrides
	for k, v := range defaults {
		if _, ok := kv[k]; !ok {
			kv[k] = v
		}
	}

	cfg.NetworkInterface = kv["network_interface"]
	cfg.LogLevel = kv["log_level"]
	cfg.APIPort, _ = atoiSafe(kv["api_port"], 4040)
	cfg.DiscordLogChannelID = kv["discord_log_channel_id"]
	cfg.Blacklist = parseBlacklist(kv["blacklist"])
	cfg.blacklistRaw = kv["blacklist"]

	// Discord — columns may be NULL on first run before setup wizard runs.
	var dBotToken, dGuildID, dChannelID, dStatusMsgID sql.NullString
	err = db.QueryRow(`SELECT bot_token, guild_id, channel_id, status_message_id FROM discord_config`).
		Scan(&dBotToken, &dGuildID, &dChannelID, &dStatusMsgID)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	cfg.DiscordBotToken = dBotToken.String
	cfg.DiscordGuildID = dGuildID.String
	cfg.DiscordChannelID = dChannelID.String
	cfg.DiscordStatusMsgID = dStatusMsgID.String

	// Web UI auth
	var wUser, wHash sql.NullString
	err = db.QueryRow(`SELECT username, password_hash FROM web_ui_config`).
		Scan(&wUser, &wHash)
	if err != nil {
		return nil, err
	}
	cfg.WebUsername = wUser.String
	cfg.WebPasswordHash = wHash.String

	return cfg, nil
}

// migrate creates the schema if it doesn't exist and enables WAL mode.
func migrate(db *sql.DB) error {
	_, err := db.Exec(`PRAGMA journal_mode=WAL;`)
	if err != nil {
		return err
	}

	schema := `
CREATE TABLE IF NOT EXISTS thanos_config (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS discord_config (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    bot_token TEXT,
    guild_id TEXT,
    channel_id TEXT,
    status_message_id TEXT
);

CREATE TABLE IF NOT EXISTS web_ui_config (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    username TEXT NOT NULL,
    password_hash TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS container_state (
    container_id TEXT PRIMARY KEY,
    container_name TEXT NOT NULL,
    display_name TEXT,
    state TEXT NOT NULL DEFAULT 'unmanaged',
    last_state_change DATETIME,
    snap_timeout INTEGER DEFAULT 900,
    ports_json TEXT,
    crash_count INTEGER DEFAULT 0,
    last_crash_exit_code INTEGER,
    last_wake_time DATETIME,
    last_stop_time DATETIME
);

CREATE TABLE IF NOT EXISTS event_log (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    container_id TEXT,
    event_type TEXT NOT NULL,
    timestamp DATETIME DEFAULT CURRENT_TIMESTAMP,
    details TEXT
);

-- Per-server state-change log (replaces the server-logs/ text files).
CREATE TABLE IF NOT EXISTS server_log (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    timestamp      DATETIME DEFAULT CURRENT_TIMESTAMP,
    container_id   TEXT NOT NULL,
    container_name TEXT NOT NULL,
    old_state       TEXT,
    new_state       TEXT,
    blurb          TEXT
);
CREATE INDEX IF NOT EXISTS idx_server_log_container_time
    ON server_log (container_id, timestamp DESC);

-- Append-only event log for wake-on-connect packets (dormant port hits).
-- blocked=1 means the source IP matched the blacklist and was logged but not acted on.
CREATE TABLE IF NOT EXISTS traffic_log (
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
CREATE INDEX IF NOT EXISTS idx_traffic_log_container_time
    ON traffic_log (container_id, timestamp DESC);

-- Deduplicated persistent record of every unique (container, IP) pair.
-- blocked=1 means the IP is on the blacklist; traffic is logged but wake/idle-reset is skipped.
CREATE TABLE IF NOT EXISTS known_clients (
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

INSERT OR IGNORE INTO discord_config (id) VALUES (1);
INSERT OR IGNORE INTO web_ui_config (id, username, password_hash) VALUES (1, '', '');
`

	_, err = db.Exec(schema)
	if err != nil {
		return err
	}

	// Migrations: add columns that may not exist in older databases.
	// CREATE TABLE IF NOT EXISTS already includes these columns for fresh DBs,
	// but existing DBs need ALTER TABLE. Errors are ignored if the column exists.
	migrations := []string{
		`ALTER TABLE traffic_log ADD COLUMN blocked INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE known_clients ADD COLUMN blocked INTEGER NOT NULL DEFAULT 0`,
	}
	for _, m := range migrations {
		db.Exec(m)
	}

	return nil
}

// SaveKV persists a single key/value pair into thanos_config (upsert).
func (c *Config) SaveKV(key, value string) error {
	_, err := c.DB.Exec(
		`INSERT INTO thanos_config (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value=excluded.value`,
		key, value)
	return err
}

// GetKV reads a single key from thanos_config. Returns empty string if not found.
func (c *Config) GetKV(key string) (string, error) {
	var val string
	err := c.DB.QueryRow(`SELECT value FROM thanos_config WHERE key = ?`, key).Scan(&val)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return val, err
}

// SaveBlacklist persists the blacklist (raw text) and updates the in-memory list.
func (c *Config) SaveBlacklist(raw string) error {
	if err := c.SaveKV("blacklist", raw); err != nil {
		return err
	}
	c.Blacklist = parseBlacklist(raw)
	c.blacklistRaw = raw
	return nil
}

// BlacklistString returns the raw blacklist text for the settings UI.
func (c *Config) BlacklistString() string {
	return c.blacklistRaw
}

// parseBlacklist parses newline-separated CIDR entries into a list of netip prefixes.
// Invalid entries are silently skipped.
func parseBlacklist(raw string) []netip.Prefix {
	if raw == "" {
		return nil
	}
	var list []netip.Prefix
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Accept bare IPs — /32 for IPv4, /128 for IPv6.
		if !strings.Contains(line, "/") {
			if addr, err := netip.ParseAddr(line); err == nil {
				bits := 32
				if addr.Is6() {
					bits = 128
				}
				line = fmt.Sprintf("%s/%d", addr.String(), bits)
			}
		}
		if p, err := netip.ParsePrefix(line); err == nil {
			list = append(list, p)
		} else {
			slog.Warn("blacklist: skipping invalid CIDR", "entry", line, "err", err)
		}
	}
	return list
}

// IsBlacklisted checks whether an IP address matches any blacklist entry.
func (c *Config) IsBlacklisted(ip string) bool {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return false
	}
	for _, p := range c.Blacklist {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// SaveDiscord persists the discord_config singleton row.
func (c *Config) SaveDiscord() error {
	_, err := c.DB.Exec(
		`INSERT INTO discord_config (id, bot_token, guild_id, channel_id, status_message_id)
		 VALUES (1, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		   bot_token=excluded.bot_token,
		   guild_id=excluded.guild_id,
		   channel_id=excluded.channel_id,
		   status_message_id=excluded.status_message_id`,
		c.DiscordBotToken, c.DiscordGuildID, c.DiscordChannelID, c.DiscordStatusMsgID)
	return err
}

// SaveWebAuth persists the web_ui_config singleton row.
func (c *Config) SaveWebAuth() error {
	_, err := c.DB.Exec(
		`INSERT INTO web_ui_config (id, username, password_hash)
		 VALUES (1, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		   username=excluded.username,
		   password_hash=excluded.password_hash`,
		c.WebUsername, c.WebPasswordHash)
	return err
}

func atoiSafe(s string, fallback int) (int, error) {
	if s == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return fallback, nil
	}
	return n, nil
}

// CheckPassword compares a plaintext password against a stored bcrypt hash.
// Returns true if they match.
func CheckPassword(hash, plaintext string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plaintext)) == nil
}