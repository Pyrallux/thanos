package config

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/netip"
	"strings"
)

// CommunityList describes a public blocklist that can be auto-fetched.
type CommunityList struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	SourceURL   string `json:"source_url"`  // direct URL to the list data
	InfoURL     string `json:"info_url"`    // human-readable about/source page
	Format      string `json:"format"`      // "text" or "aws_json"
}

// AvailableCommunityLists is the set of community lists offered in the UI.
var AvailableCommunityLists = []CommunityList{
	{
		ID:          "firehol_level1",
		Name:         "FireHOL Level 1",
		Description:  "Maximum protection with minimum false positives. Composed from fullbogons, spamhaus_drop, dshield, and feodo. Safe for all servers. Updated daily.",
		SourceURL:    "https://raw.githubusercontent.com/firehol/blocklist-ipsets/master/firehol_level1.netset",
		InfoURL:      "https://iplists.firehol.org/?ipset=firehol_level1",
		Format:       "text",
	},
	{
		ID:          "spamhaus_drop",
		Name:         "Spamhaus DROP",
		Description:  "Don't Route Or Peer — hijacked/bogon ranges that no legitimate residential traffic comes from. Small, stable, very safe.",
		SourceURL:    "https://www.spamhaus.org/drop/drop.txt",
		InfoURL:      "https://www.spamhaus.org/drop/",
		Format:       "text",
	},
	{
		ID:          "spamhaus_edrop",
		Name:         "Spamhaus EDROP",
		Description:  "Extended DROP — additional hijacked/spoofed ranges. Complements the main DROP list.",
		SourceURL:    "https://www.spamhaus.org/drop/edrop.txt",
		InfoURL:      "https://www.spamhaus.org/drop/",
		Format:       "text",
	},
	{
		ID:          "aws",
		Name:         "AWS IP Ranges",
		Description:  "Official Amazon Web Services IP ranges. Updated regularly by AWS.",
		SourceURL:    "https://ip-ranges.amazonaws.com/ip-ranges.json",
		InfoURL:      "https://docs.aws.amazon.com/vpc/latest/userguide/aws-ip-ranges.html",
		Format:       "aws_json",
	},
}

var communityListByID = func() map[string]CommunityList {
	m := make(map[string]CommunityList, len(AvailableCommunityLists))
	for _, l := range AvailableCommunityLists {
		m[l.ID] = l
	}
	return m
}()

// fetchCommunityList downloads and parses a community blocklist into prefixes.
func fetchCommunityList(list CommunityList) ([]netip.Prefix, error) {
	client := &httpClient
	resp, err := client.Get(list.SourceURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 50<<20)) // 50MB cap
	if err != nil {
		return nil, err
	}

	switch list.Format {
	case "text":
		return parseTextList(string(body)), nil
	case "aws_json":
		return parseAWSJSON(body), nil
	default:
		return parseTextList(string(body)), nil
	}
}

// parseTextList parses plain-text CIDR lists (one per line, # comments).
func parseTextList(raw string) []netip.Prefix {
	var list []netip.Prefix
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, ";") || strings.HasPrefix(line, "#") {
			continue
		}
		// Some lists have "CIDR ; description" — take the first field.
		if idx := strings.IndexAny(line, " ;\t"); idx > 0 {
			line = line[:idx]
		}
		if !strings.Contains(line, "/") {
			if addr, err := netip.ParseAddr(line); err == nil {
				bits := 32
				if addr.Is6() {
					bits = 128
				}
				line = fmtCIDR(addr, bits)
			}
		}
		if p, err := netip.ParsePrefix(line); err == nil {
			list = append(list, p)
		}
	}
	return list
}

// parseAWSJSON parses the AWS ip-ranges.json format.
func parseAWSJSON(body []byte) []netip.Prefix {
	var data struct {
		Prefixes       []struct{ IPPrefix string `json:"ip_prefix"` }       `json:"prefixes"`
		IPv6Prefixes   []struct{ IPv6Prefix string `json:"ipv6_prefix"` }  `json:"ipv6_prefixes"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		slog.Warn("community list: failed to parse AWS JSON", "err", err)
		return nil
	}
	var list []netip.Prefix
	for _, p := range data.Prefixes {
		if prefix, err := netip.ParsePrefix(p.IPPrefix); err == nil {
			list = append(list, prefix)
		}
	}
	for _, p := range data.IPv6Prefixes {
		if prefix, err := netip.ParsePrefix(p.IPv6Prefix); err == nil {
			list = append(list, prefix)
		}
	}
	return list
}

// RefreshCommunityLists fetches all enabled community lists and caches the
// merged prefixes in cfg.communityBlacklist. Called on startup and when
// settings are saved.
func (c *Config) RefreshCommunityLists() {
	c.mu.Lock()
	enabled := make([]string, 0, len(c.CommunityLists))
	for id, on := range c.CommunityLists {
		if on {
			enabled = append(enabled, id)
		}
	}
	c.mu.Unlock()

	if len(enabled) == 0 {
		c.mu.Lock()
		c.communityBlacklist = nil
		c.mu.Unlock()
		return
	}

	var merged []netip.Prefix
	for _, id := range enabled {
		list, ok := communityListByID[id]
		if !ok {
			slog.Warn("community list: unknown list ID", "id", id)
			continue
		}
		slog.Info("community list: fetching", "id", id, "url", list.SourceURL)
		prefixes, err := fetchCommunityList(list)
		if err != nil {
			slog.Warn("community list: fetch failed", "id", id, "err", err)
			continue
		}
		slog.Info("community list: loaded", "id", id, "entries", len(prefixes))
		merged = append(merged, prefixes...)
	}

	c.mu.Lock()
	c.communityBlacklist = merged
	c.mu.Unlock()
}