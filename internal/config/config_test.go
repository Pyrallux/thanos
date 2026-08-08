package config

import (
	"database/sql"
	"sync"
	"testing"

	_ "modernc.org/sqlite"
)

func TestIsBlacklisted_BlacklistMode(t *testing.T) {
	cfg := &Config{
		Blacklist: parseBlacklist("10.0.0.0/8\n192.168.1.5"),
	}
	cfg.mu = sync.RWMutex{}
	// IP in CIDR range.
	if !cfg.IsBlacklisted("10.1.2.3") {
		t.Error("10.1.2.3 should be blacklisted by 10.0.0.0/8")
	}
	// Bare IP match.
	if !cfg.IsBlacklisted("192.168.1.5") {
		t.Error("192.168.1.5 should be blacklisted")
	}
	// IP not in any blacklist entry.
	if cfg.IsBlacklisted("8.8.8.8") {
		t.Error("8.8.8.8 should not be blacklisted")
	}
}

func TestIsBlacklisted_WhitelistMode(t *testing.T) {
	cfg := &Config{
		Whitelist:        parseBlacklist("192.168.1.0/24\n10.0.0.0/8"),
		WhitelistEnabled: true,
	}
	cfg.mu = sync.RWMutex{}
	// IP in whitelist — allowed (not blacklisted).
	if cfg.IsBlacklisted("192.168.1.50") {
		t.Error("192.168.1.50 is in whitelist, should not be blacklisted")
	}
	if cfg.IsBlacklisted("10.5.5.5") {
		t.Error("10.5.5.5 is in whitelist, should not be blacklisted")
	}
	// IP not in whitelist — should be blacklisted.
	if !cfg.IsBlacklisted("8.8.8.8") {
		t.Error("8.8.8.8 is not in whitelist, should be blacklisted")
	}
}

func TestIsBlacklisted_CommunityList(t *testing.T) {
	cfg := &Config{
		Blacklist:          nil,
		communityBlacklist: parseBlacklist("20.0.0.0/8"),
	}
	cfg.mu = sync.RWMutex{}
	if !cfg.IsBlacklisted("20.1.2.3") {
		t.Error("20.1.2.3 should be blacklisted by community list")
	}
	if cfg.IsBlacklisted("8.8.8.8") {
		t.Error("8.8.8.8 should not be blacklisted")
	}
}

func TestIsBlacklisted_WhitelistOverridesBlacklist(t *testing.T) {
	cfg := &Config{
		Blacklist:        parseBlacklist("10.0.0.0/8"),
		Whitelist:        parseBlacklist("10.0.0.0/8"),
		WhitelistEnabled: true,
	}
	cfg.mu = sync.RWMutex{}
	// Even though 10.x.x.x is in the blacklist, whitelist mode takes
	// precedence and allows it.
	if cfg.IsBlacklisted("10.1.2.3") {
		t.Error("whitelist mode should override blacklist — 10.1.2.3 is in whitelist")
	}
}

func TestParseCommunityListConfig(t *testing.T) {
	m := parseCommunityListConfig("firehol_level1,spamhaus_drop, spamhaus_edrop")
	if !m["firehol_level1"] {
		t.Error("firehol_level1 should be enabled")
	}
	if !m["spamhaus_drop"] {
		t.Error("spamhaus_drop should be enabled")
	}
	if !m["spamhaus_edrop"] {
		t.Error("spamhaus_edrop should be enabled")
	}
	if len(m) != 3 {
		t.Errorf("expected 3 entries, got %d", len(m))
	}
}

func TestParseTextList(t *testing.T) {
	input := "# comment line\n10.0.0.0/8\n; semicolon comment\n192.168.1.5\n5.5.5.5 ; description\n"
	prefixes := parseTextList(input)
	if len(prefixes) != 3 {
		t.Fatalf("expected 3 prefixes, got %d", len(prefixes))
	}
	// Verify the bare IP got /32.
	for _, p := range prefixes {
		if p.Addr().String() == "192.168.1.5" && p.Bits() != 32 {
			t.Errorf("expected /32 for bare IP, got /%d", p.Bits())
		}
	}
}

// TestParseBlacklistEdgeCases verifies that parseBlacklist handles empty
// input, comments, bare IPs, invalid entries, and IPv6. A regression here
// would cause the blacklist/whitelist to silently ignore or reject entries,
// breaking IP filtering.
func TestParseBlacklistEdgeCases(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  int // expected number of prefixes
	}{
		{"empty", "", 0},
		{"only comments", "# all comments\n; nothing here\n", 0},
		{"bare ipv4 gets /32", "203.0.113.5", 1},
		{"bare ipv6 gets /128", "::1", 1},
		{"valid cidr", "10.0.0.0/8", 1},
		{"invalid entry skipped", "not-an-ip\n10.0.0.0/8", 1},
		{"mixed valid and invalid", "# header\n10.0.0.0/8\nbad\n192.168.1.0/24", 2},
		{"whitespace trimmed", "  10.0.0.0/8  \n", 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseBlacklist(tt.input)
			if len(got) != tt.want {
				t.Errorf("parseBlacklist(%q) = %d prefixes, want %d", tt.input, len(got), tt.want)
			}
		})
	}
}

// TestParseAWSJSON verifies that the AWS ip-ranges.json format is parsed
// correctly, extracting both IPv4 and IPv6 prefixes. This powers the
// community blocklist feature for AWS IP ranges.
func TestParseAWSJSON(t *testing.T) {
	body := []byte(`{
		"prefixes": [
			{"ip_prefix": "3.2.34.0/26"},
			{"ip_prefix": "15.230.56.0/22"},
			{"ip_prefix": "invalid-prefix"}
		],
		"ipv6_prefixes": [
			{"ipv6_prefix": "2600:1f18:4000::/36"}
		]
	}`)
	prefixes := parseAWSJSON(body)
	// 2 valid IPv4 + 1 valid IPv6 = 3. The invalid one is skipped.
	if len(prefixes) != 3 {
		t.Fatalf("parseAWSJSON = %d prefixes, want 3", len(prefixes))
	}
}

// TestParseAWSJSONInvalid verifies that invalid JSON returns nil without
// panicking.
func TestParseAWSJSONInvalid(t *testing.T) {
	prefixes := parseAWSJSON([]byte("not json"))
	if prefixes != nil {
		t.Errorf("parseAWSJSON(invalid) = %v, want nil", prefixes)
	}
}

// TestSaveKVAndGetKV verifies the key-value config persistence round-trip.
// This is how the settings screen saves all configuration — a regression
// would silently lose settings on restart.
func TestSaveKVAndGetKV(t *testing.T) {
	db := newConfigTestDB(t)
	cfg := &Config{DB: db}

	// Key doesn't exist → empty string, no error.
	val, err := cfg.GetKV("nonexistent")
	if err != nil {
		t.Fatalf("GetKV(nonexistent): %v", err)
	}
	if val != "" {
		t.Errorf("GetKV(nonexistent) = %q, want empty", val)
	}

	// Save and read back.
	if err := cfg.SaveKV("api_port", "4040"); err != nil {
		t.Fatalf("SaveKV: %v", err)
	}
	val, err = cfg.GetKV("api_port")
	if err != nil {
		t.Fatalf("GetKV: %v", err)
	}
	if val != "4040" {
		t.Errorf("GetKV(api_port) = %q, want %q", val, "4040")
	}

	// Upsert — update existing key.
	if err := cfg.SaveKV("api_port", "8080"); err != nil {
		t.Fatalf("SaveKV upsert: %v", err)
	}
	val, err = cfg.GetKV("api_port")
	if err != nil {
		t.Fatalf("GetKV after upsert: %v", err)
	}
	if val != "8080" {
		t.Errorf("GetKV(api_port) after upsert = %q, want %q", val, "8080")
	}
}

// TestSaveBlacklistRoundTrip verifies that SaveBlacklist persists the raw
// text to the DB and updates the in-memory list, and that BlacklistString
// returns the raw text. This is the save/load path for the settings screen.
func TestSaveBlacklistRoundTrip(t *testing.T) {
	db := newConfigTestDB(t)
	cfg := &Config{DB: db}
	cfg.mu = sync.RWMutex{}

	raw := "10.0.0.0/8\n192.168.1.5"
	if err := cfg.SaveBlacklist(raw); err != nil {
		t.Fatalf("SaveBlacklist: %v", err)
	}

	// In-memory list updated.
	if len(cfg.Blacklist) != 2 {
		t.Errorf("Blacklist = %d entries, want 2", len(cfg.Blacklist))
	}
	// Raw string preserved for UI.
	if cfg.BlacklistString() != raw {
		t.Errorf("BlacklistString() = %q, want %q", cfg.BlacklistString(), raw)
	}
	// Persisted to DB.
	val, err := cfg.GetKV("blacklist")
	if err != nil {
		t.Fatalf("GetKV(blacklist): %v", err)
	}
	if val != raw {
		t.Errorf("GetKV(blacklist) = %q, want %q", val, raw)
	}
}

// newConfigTestDB creates an in-memory SQLite database with the thanos_config
// table, matching the production schema.
func newConfigTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	_, err = db.Exec(`CREATE TABLE thanos_config (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL
	)`)
	if err != nil {
		t.Fatalf("create table: %v", err)
	}
	return db
}
