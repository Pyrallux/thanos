package config

import (
	"sync"
	"testing"
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
		Whitelist:         parseBlacklist("192.168.1.0/24\n10.0.0.0/8"),
		WhitelistEnabled:  true,
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
		Blacklist:           nil,
		communityBlacklist:  parseBlacklist("20.0.0.0/8"),
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
		Blacklist:          parseBlacklist("10.0.0.0/8"),
		Whitelist:          parseBlacklist("10.0.0.0/8"),
		WhitelistEnabled:   true,
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