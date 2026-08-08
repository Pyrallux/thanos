package sentinel

import (
	"strings"
	"testing"
)

// TestBuildCombinedBPFFilter verifies the BPF filter string construction
// that controls which packets the packet sniffer captures. A regression
// here would either miss wake-on-connect traffic (players can't join) or
// capture too much (performance degradation under high packet rates).
func TestBuildCombinedBPFFilter(t *testing.T) {
	// No ports watched — should return a never-match expression.
	got := buildCombinedBPFFilter(nil, nil)
	if got != "udp port 0" {
		t.Errorf("buildCombinedBPFFilter(nil, nil) = %q, want %q", got, "udp port 0")
	}

	// Only dormant ports — should include TCP SYN and UDP clauses.
	dormant := map[int]string{25565: "id-a"}
	got = buildCombinedBPFFilter(dormant, nil)
	if !strings.Contains(got, "tcp-syn") {
		t.Error("dormant-only filter should contain TCP SYN clause")
	}
	if !strings.Contains(got, "udp") {
		t.Error("dormant-only filter should contain UDP clause")
	}
	if !strings.Contains(got, "dst port 25565") {
		t.Error("dormant-only filter should contain port 25565")
	}

	// Only running ports — same structure.
	running := map[int]string{25565: "id-a"}
	got = buildCombinedBPFFilter(nil, running)
	if !strings.Contains(got, "tcp-syn") {
		t.Error("running-only filter should contain TCP SYN clause")
	}
	if !strings.Contains(got, "dst port 25565") {
		t.Error("running-only filter should contain port 25565")
	}

	// Both dormant and running — should contain both port sets.
	dormant2 := map[int]string{25565: "id-a"}
	running2 := map[int]string{19132: "id-b"}
	got = buildCombinedBPFFilter(dormant2, running2)
	if !strings.Contains(got, "dst port 25565") {
		t.Error("combined filter should contain dormant port 25565")
	}
	if !strings.Contains(got, "dst port 19132") {
		t.Error("combined filter should contain running port 19132")
	}
}

// TestPortList verifies that portList returns sorted "dst port N" entries
// for the given port map. The sorting ensures the generated BPF filter
// string is deterministic — without it, the filterUpdater would see a
// "changed" filter on every call even when the port set is the same,
// causing unnecessary BPF recompilation.
func TestPortList(t *testing.T) {
	ports := map[int]string{
		19132: "id-b",
		25565: "id-a",
		8123:  "id-c",
	}
	got := portList(ports)
	if len(got) != 3 {
		t.Fatalf("portList = %v, want 3 entries", got)
	}
	// Should be sorted by port number.
	want := []string{"dst port 8123", "dst port 19132", "dst port 25565"}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("portList[%d] = %q, want %q", i, got[i], w)
		}
	}
}

// TestPortListEmpty verifies that an empty port map produces an empty list.
func TestPortListEmpty(t *testing.T) {
	got := portList(map[int]string{})
	if len(got) != 0 {
		t.Errorf("portList(empty) = %v, want empty", got)
	}
}
