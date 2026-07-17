package sentinel

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"time"

	"github.com/google/gopacket"
	"github.com/google/gopacket/layers"
	"github.com/google/gopacket/pcap"
)

// startSniffer opens the live capture handle on the given interface, applies
// the BPF filter, and enters the packet-processing loop. It blocks until the
// context is cancelled or a fatal error occurs.
//
// On Windows, the friendly interface name (e.g. "Ethernet" or
// "vEthernet (Default Switch)") must be resolved to the Npcap device name
// (e.g. \Device\NPF_{GUID}). We do this by matching IP addresses between
// the Go net.Interfaces() list and pcap.FindAllDevs().
func (s *Sentinel) startSniffer(ctx context.Context, iface string) error {
	pcapName, err := resolvePcapDevice(iface)
	if err != nil {
		return fmt.Errorf("resolve interface %q: %w", iface, err)
	}

	slog.Info("opening pcap handle", "interface", iface, "pcap_device", pcapName)

	// 1. Open the live capture handle.
	handle, err := pcap.OpenLive(pcapName, 65536, true, pcap.BlockForever)
	if err != nil {
		return fmt.Errorf("pcap open %s: %w", pcapName, err)
	}
	defer handle.Close()

	// 2. Apply the initial BPF filter.
	s.applyFilter(handle)

	slog.Info("packet sniffer started", "interface", iface, "pcap_device", pcapName, "filter", s.CurrentFilter())

	// 3. Start a goroutine to reapply the filter when watched ports change.
	go s.filterUpdater(ctx, handle)

	// 4. Process packets.
	packetSource := gopacket.NewPacketSource(handle, handle.LinkType())
	for {
		select {
		case <-ctx.Done():
			slog.Info("packet sniffer stopping (context cancelled)")
			return nil
		case packet, ok := <-packetSource.Packets():
			if !ok {
				return fmt.Errorf("packet source closed")
			}
			s.handlePacket(packet)
		}
	}
}

// filterUpdater periodically checks if the watched ports have changed and
// recompiles the BPF filter if so.
func (s *Sentinel) filterUpdater(ctx context.Context, handle *pcap.Handle) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	lastFilter := s.CurrentFilter()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			current := s.CurrentFilter()
			if current != lastFilter {
				slog.Info("reapplying BPF filter", "old", lastFilter, "new", current)
				s.applyFilter(handle)
				lastFilter = current
			}
		}
	}
}

// applyFilter compiles and sets the BPF filter on the capture handle based on
// the current watched ports map.
func (s *Sentinel) applyFilter(handle *pcap.Handle) {
	filter := s.buildCurrentFilter()
	if err := handle.SetBPFFilter(filter); err != nil {
		slog.Error("failed to set BPF filter", "filter", filter, "err", err)
	}
}

// buildCurrentFilter builds the BPF filter string from the current watched
// ports (dormant) and running ports. The filter matches:
//   - TCP SYN to dormant ports (wake-on-connect)
//   - Any TCP/UDP to/from running ports (idle traffic monitoring)
func (s *Sentinel) buildCurrentFilter() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return buildCombinedBPFFilter(s.watchedPorts, s.runningPorts)
}

// CurrentFilter returns the current BPF filter string (thread-safe).
func (s *Sentinel) CurrentFilter() string {
	return s.buildCurrentFilter()
}

// handlePacket processes a single captured packet: extracts the destination
// port, looks up the container, and tells the orchestrator to wake it.
// Also resets the idle timer for running containers when traffic is detected.
func (s *Sentinel) handlePacket(pkt gopacket.Packet) {
	dstPort := extractDstPort(pkt)
	srcPort := extractSrcPort(pkt)
	if dstPort == 0 && srcPort == 0 {
		return
	}

	srcIP := extractSrcIP(pkt)
	proto := extractProtocol(pkt)

	// Check if source IP is blacklisted. Blacklisted traffic is still logged
	// (so the user can see who's hitting them) but does NOT trigger wake or
	// idle-reset — the packet is effectively dropped after logging.
	blocked := srcIP != "" && s.cfg.IsBlacklisted(srcIP)

	// Check if this is traffic to a dormant container (wake-on-connect).
	s.mu.RLock()
	cid := s.watchedPorts[dstPort]
	s.mu.RUnlock()

	if cid != "" {
		if blocked {
			slog.Info("wake-on-connect: blocked packet from blacklisted IP",
				"src_ip", srcIP, "dst_port", dstPort, "container_id", cid)
		} else {
			slog.Info("wake-on-connect: packet detected",
				"dst_port", dstPort, "src_ip", srcIP, "container_id", cid)
		}

		// Log the wake event (including blocked ones) for traffic audit.
		if s.tlog != nil && srcIP != "" {
			ci := s.orch.GetContainer(cid)
			name := cid
			if ci != nil {
				name = ci.DisplayName
			}
			s.tlog.LogWake(cid, name, srcIP, srcPort, dstPort, proto, blocked)
		}

		// Only wake the container if the source is NOT blacklisted.
		if !blocked {
			go s.orch.WakeContainer(context.Background(), cid, "wake_on_connect")
		}
		return
	}

	// Check if this is traffic on a running container's ports (idle reset).
	// Copy the map under lock so iteration is safe from concurrent writes.
	s.mu.RLock()
	runningPortsCopy := make(map[int]string, len(s.runningPorts))
	for k, v := range s.runningPorts {
		runningPortsCopy[k] = v
	}
	s.mu.RUnlock()

	for port, rcid := range runningPortsCopy {
		if port == dstPort || port == srcPort {
			// Log ongoing traffic (dedup'd in the logger), including blocked.
			if s.tlog != nil && srcIP != "" {
				ci := s.orch.GetContainer(rcid)
				name := rcid
				if ci != nil {
					name = ci.DisplayName
				}
				s.tlog.LogTraffic(rcid, name, srcIP, port, blocked)
			}

			// Only reset idle timer if the source is NOT blacklisted.
			if !blocked {
				s.orch.ResetIdleTimer(rcid)
			}
		}
	}
}

// extractSrcIP extracts the source IP address from a packet's network layer.
// Returns empty string if no IP layer is present.
func extractSrcIP(pkt gopacket.Packet) string {
	if netLayer := pkt.NetworkLayer(); netLayer != nil {
		return netLayer.NetworkFlow().Src().String()
	}
	return ""
}

// extractProtocol returns "tcp" or "udp" based on the transport layer.
func extractProtocol(pkt gopacket.Packet) string {
	if pkt.Layer(layers.LayerTypeTCP) != nil {
		return "tcp"
	}
	if pkt.Layer(layers.LayerTypeUDP) != nil {
		return "udp"
	}
	return ""
}

// extractDstPort extracts the destination port from a TCP or UDP packet.
// Returns 0 if the packet is not TCP/UDP or has no port info.
func extractDstPort(pkt gopacket.Packet) int {
	// Try TCP layer.
	if tcpLayer := pkt.Layer(layers.LayerTypeTCP); tcpLayer != nil {
		tcp, _ := tcpLayer.(*layers.TCP)
		return int(tcp.DstPort)
	}
	// Try UDP layer.
	if udpLayer := pkt.Layer(layers.LayerTypeUDP); udpLayer != nil {
		udp, _ := udpLayer.(*layers.UDP)
		return int(udp.DstPort)
	}
	return 0
}

// extractSrcPort extracts the source port from a TCP or UDP packet.
// Used for detecting outbound traffic from running containers (idle reset).
func extractSrcPort(pkt gopacket.Packet) int {
	if tcpLayer := pkt.Layer(layers.LayerTypeTCP); tcpLayer != nil {
		tcp, _ := tcpLayer.(*layers.TCP)
		return int(tcp.SrcPort)
	}
	if udpLayer := pkt.Layer(layers.LayerTypeUDP); udpLayer != nil {
		udp, _ := udpLayer.(*layers.UDP)
		return int(udp.SrcPort)
	}
	return 0
}

// resolvePcapDevice resolves a friendly network interface name (e.g.
// "Ethernet", "vEthernet (Default Switch)", "Loopback") to the pcap device
// name (e.g. "\Device\NPF_{GUID}") by matching IP addresses between
// net.Interfaces() and pcap.FindAllDevs().
//
// "Loopback" is a special case that maps to \Device\NPF_Loopback on Windows.
func resolvePcapDevice(ifaceName string) (string, error) {
	// Special case: Loopback
	if ifaceName == "Loopback" {
		pcapDevices, err := pcap.FindAllDevs()
		if err != nil {
			return "", fmt.Errorf("find pcap devices: %w", err)
		}
		for _, dev := range pcapDevices {
			if strings.Contains(strings.ToLower(dev.Name), "loopback") {
				slog.Info("resolved loopback pcap device", "pcap_device", dev.Name)
				return dev.Name, nil
			}
		}
		return "", fmt.Errorf("loopback pcap device not found")
	}

	// 1. Find the Go net.Interface by name.
	goIfaces, err := net.Interfaces()
	if err != nil {
		return "", fmt.Errorf("list net interfaces: %w", err)
	}

	var goIface *net.Interface
	for i := range goIfaces {
		if goIfaces[i].Name == ifaceName {
			goIface = &goIfaces[i]
			break
		}
	}
	if goIface == nil {
		return "", fmt.Errorf("interface %q not found in net.Interfaces()", ifaceName)
	}

	// 2. Get the set of IP addresses for this Go interface.
	goAddrs := map[string]bool{}
	goIfaceAddrs, err := goIface.Addrs()
	if err != nil {
		return "", fmt.Errorf("get addresses for %q: %w", ifaceName, err)
	}
	for _, addr := range goIfaceAddrs {
		// addr.String() returns "192.168.1.2/24" — extract just the IP.
		ipStr := addr.String()
		if idx := strings.Index(ipStr, "/"); idx >= 0 {
			ipStr = ipStr[:idx]
		}
		goAddrs[ipStr] = true
	}

	slog.Debug("resolving pcap device",
		"interface", ifaceName, "go_addrs", goAddrs)

	// 3. Find the pcap device that has matching IP addresses.
	pcapDevices, err := pcap.FindAllDevs()
	if err != nil {
		return "", fmt.Errorf("find pcap devices: %w", err)
	}

	var bestMatch string
	var bestScore int
	for _, dev := range pcapDevices {
		score := 0
		for _, addr := range dev.Addresses {
			ipStr := addr.IP.String()
			if goAddrs[ipStr] {
				score++
			}
		}
		if score > bestScore {
			bestScore = score
			bestMatch = dev.Name
		}
	}

	if bestMatch == "" {
		// Fall back: try matching by name directly (works on Linux where
		// pcap device names match interface names).
		for _, dev := range pcapDevices {
			if dev.Name == ifaceName {
				return dev.Name, nil
			}
		}
		return "", fmt.Errorf("no pcap device matches interface %q (addresses: %v)",
			ifaceName, goAddrs)
	}

	slog.Info("resolved pcap device",
		"interface", ifaceName, "pcap_device", bestMatch, "matched_addrs", bestScore)

	return bestMatch, nil
}