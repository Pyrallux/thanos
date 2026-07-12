package sentinel

import (
	"fmt"
	"sort"
	"strings"
)

// buildCombinedBPFFilter constructs a BPF filter that matches:
//   - TCP SYN packets destined for dormant container ports (wake-on-connect)
//   - UDP packets destined for dormant container ports (wake-on-connect)
//   - TCP SYN packets destined for running container ports (new connections only)
//   - UDP packets destined for running container ports (idle reset on real traffic)
//
// Only new connection traffic (TCP SYN or any UDP) resets the idle timer.
// TCP keepalive ACKs and ongoing session packets are ignored so the
// heartbeat timestamp only updates when a player actually connects.
//
// If no ports are watched, returns a no-match expression.
func buildCombinedBPFFilter(dormantPorts, runningPorts map[int]string) string {
	if len(dormantPorts) == 0 && len(runningPorts) == 0 {
		return "udp port 0" // never matches
	}

	var parts []string

	// TCP SYN to dormant ports (wake-on-connect).
	if len(dormantPorts) > 0 {
		dormantPortList := portList(dormantPorts)
		parts = append(parts, "(tcp[tcpflags] & tcp-syn != 0 and ("+
			strings.Join(dormantPortList, " or ")+"))")
		// Also match UDP to dormant ports.
		parts = append(parts, "(udp and ("+strings.Join(dormantPortList, " or ")+"))")
	}

	// TCP SYN and UDP to running ports (new connections only, not keepalives).
	if len(runningPorts) > 0 {
		runningPortList := portList(runningPorts)
		parts = append(parts, "(tcp[tcpflags] & tcp-syn != 0 and ("+
			strings.Join(runningPortList, " or ")+"))")
		parts = append(parts, "(udp and ("+strings.Join(runningPortList, " or ")+"))")
	}

	return strings.Join(parts, " or ")
}

// portList returns a sorted slice of "dst port N" or "src port N" strings
// for the given port map, covering both directions.
func portList(ports map[int]string) []string {
	sorted := make([]int, 0, len(ports))
	for p := range ports {
		sorted = append(sorted, p)
	}
	sort.Ints(sorted)

	var out []string
	for _, p := range sorted {
		out = append(out, fmt.Sprintf("dst port %d", p))
	}
	return out
}

