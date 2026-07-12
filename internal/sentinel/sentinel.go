// Package sentinel implements the passive network packet sniffer that detects
// inbound connection attempts to dormant containers (wake-on-connect) and
// monitors traffic for idle shutdown.
package sentinel

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"thanos/internal/config"
	"thanos/internal/orchestrator"
	"thanos/internal/traffic"
)

// Sentinel wraps the packet sniffer and the port-to-container watcher.
type Sentinel struct {
	cfg  *config.Config
	orch *orchestrator.Orchestrator
	tlog *traffic.Logger

	mu            sync.RWMutex
	watchedPorts  map[int]string // dstPort → containerID (for dormant containers)
	runningPorts  map[int]string // port → containerID (for running containers, idle reset)
	captureCancel context.CancelFunc
}

// New creates a Sentinel. It does not open the capture handle until Run is
// called, so construction never fails even if Npcap/libpcap is missing.
func New(cfg *config.Config, orch *orchestrator.Orchestrator, tlog *traffic.Logger) (*Sentinel, error) {
	return &Sentinel{
		cfg:          cfg,
		orch:         orch,
		tlog:         tlog,
		watchedPorts: make(map[int]string),
		runningPorts: make(map[int]string),
	}, nil
}

// Run starts the packet sniffer. If the network interface cannot be opened,
// it logs an error and falls back to manual-start-only mode (the Web UI and
// API still work for manual start/stop).
//
// Note: On Windows, locally-originated TCP connections to the machine's own
// LAN IP do not traverse the physical network adapter, so the packet sniffer
// cannot detect them. Wake-on-connect works for traffic from external machines
// (which is the intended use case for game servers — players connect remotely).
func (s *Sentinel) Run(ctx context.Context) {
	for {
		iface := s.networkInterface()
		if iface == "" {
			slog.Warn("no network interface configured; packet sniffing disabled")
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
				continue
			}
		}

		slog.Info("network sentinel starting", "interface", iface)
		captureCtx, cancel := context.WithCancel(ctx)
		s.setCaptureCancel(cancel)
		err := s.startSniffer(captureCtx, iface)
		cancel()
		s.clearCaptureCancel()
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			slog.Error("packet sniffer failed to start", "err", err)
			slog.Info("Thanos will operate in manual-start-only mode")
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
		}
	}
}

// SetNetworkInterface updates the active sniffing interface and restarts the
// packet capture loop if it is currently running.
func (s *Sentinel) SetNetworkInterface(iface string) {
	s.mu.Lock()
	s.cfg.NetworkInterface = iface
	cancel := s.captureCancel
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}
}

func (s *Sentinel) networkInterface() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg.NetworkInterface
}

func (s *Sentinel) setCaptureCancel(cancel context.CancelFunc) {
	s.mu.Lock()
	s.captureCancel = cancel
	s.mu.Unlock()
}

func (s *Sentinel) clearCaptureCancel() {
	s.mu.Lock()
	s.captureCancel = nil
	s.mu.Unlock()
}



// OnStateChange implements orchestrator.StateWatcher. When a container
// transitions to dormant, we add its ports to the watched set. When it
// transitions to running, we remove its ports from the watched set and add
// them to the running set (for idle traffic monitoring).
func (s *Sentinel) OnStateChange(ci *orchestrator.ContainerInfo) {
	switch ci.State {
	case orchestrator.StateDormant:
		// Add this container's ports to the watched set (for wake-on-connect).
		// Remove from running set.
		s.mu.Lock()
		for _, p := range ci.Ports {
			s.watchedPorts[p] = ci.ID
			delete(s.runningPorts, p)
			slog.Info("watching port for dormant container", "port", p, "container", ci.DisplayName)
		}
		s.mu.Unlock()
	case orchestrator.StateRunning:
		// Remove from watched set, add to running set (for idle monitoring).
		s.mu.Lock()
		for _, p := range ci.Ports {
			delete(s.watchedPorts, p)
			s.runningPorts[p] = ci.ID
		}
		s.mu.Unlock()
	case orchestrator.StateStarting, orchestrator.StateStopping:
		// During transitions, don't watch (neither wake nor idle).
		s.mu.Lock()
		for _, p := range ci.Ports {
			delete(s.watchedPorts, p)
			delete(s.runningPorts, p)
		}
		s.mu.Unlock()
	case orchestrator.StateCrashed:
		// Crashed containers are stopped, so watch their ports for wake.
		s.mu.Lock()
		for _, p := range ci.Ports {
			s.watchedPorts[p] = ci.ID
			delete(s.runningPorts, p)
		}
		s.mu.Unlock()
	case orchestrator.StateUnmanaged:
		// Container was removed from Thanos — clean up all its port entries
		// and delete its traffic data.
		s.mu.Lock()
		for _, p := range ci.Ports {
			delete(s.watchedPorts, p)
			delete(s.runningPorts, p)
		}
		s.mu.Unlock()
		if s.tlog != nil {
			s.tlog.DeleteContainer(ci.ID)
		}
	}
}