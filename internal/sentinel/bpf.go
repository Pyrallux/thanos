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
// Per-port protocol filtering is honored: a port whose watch spec has
// tcp=false is excluded from the TCP clauses, and udp=false from the UDP
// clauses. If no ports are watched, returns a no-match expression.
func buildCombinedBPFFilter(dormantPorts, runningPorts map[int]watchedPort) string {
	if len(dormantPorts) == 0 && len(runningPorts) == 0 {
		return "udp port 0" // never matches
	}

	var parts []string

	// TCP SYN to dormant ports (wake-on-connect).
	if tcpPorts := tcpPortList(dormantPorts); len(tcpPorts) > 0 {
		parts = append(parts, "(tcp[tcpflags] & tcp-syn != 0 and ("+
			strings.Join(tcpPorts, " or ")+"))")
	}
	// UDP to dormant ports.
	if udpPorts := udpPortList(dormantPorts); len(udpPorts) > 0 {
		parts = append(parts, "(udp and ("+strings.Join(udpPorts, " or ")+"))")
	}

	// TCP SYN and UDP to running ports (new connections only, not keepalives).
	if tcpPorts := tcpPortList(runningPorts); len(tcpPorts) > 0 {
		parts = append(parts, "(tcp[tcpflags] & tcp-syn != 0 and ("+
			strings.Join(tcpPorts, " or ")+"))")
	}
	if udpPorts := udpPortList(runningPorts); len(udpPorts) > 0 {
		parts = append(parts, "(udp and ("+strings.Join(udpPorts, " or ")+"))")
	}

	if len(parts) == 0 {
		return "udp port 0" // never matches
	}
	return strings.Join(parts, " or ")
}

// tcpPortList returns a sorted slice of "dst port N" strings for the ports
// whose watch spec has TCP enabled.
func tcpPortList(ports map[int]watchedPort) []string {
	return portList(ports, func(wp watchedPort) bool { return wp.tcp })
}

// udpPortList returns a sorted slice of "dst port N" strings for the ports
// whose watch spec has UDP enabled.
func udpPortList(ports map[int]watchedPort) []string {
	return portList(ports, func(wp watchedPort) bool { return wp.udp })
}

// portList returns a sorted slice of "dst port N" strings for the given
// port map, filtered by the predicate.
func portList(ports map[int]watchedPort, include func(watchedPort) bool) []string {
	sorted := make([]int, 0, len(ports))
	for p, wp := range ports {
		if include(wp) {
			sorted = append(sorted, p)
		}
	}
	sort.Ints(sorted)

	var out []string
	for _, p := range sorted {
		out = append(out, fmt.Sprintf("dst port %d", p))
	}
	return out
}

