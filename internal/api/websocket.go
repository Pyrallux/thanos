package api

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/gorilla/websocket"
)

// upgrader allows the Web UI origin (localhost:4040).
var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

// handleLogStream upgrades to a WebSocket and streams Docker logs for the
// container specified by the ?id= query parameter.
func (s *Server) handleLogStream(w http.ResponseWriter, r *http.Request) {
	containerID := r.URL.Query().Get("id")
	if containerID == "" {
		http.Error(w, "missing id parameter", http.StatusBadRequest)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("websocket upgrade failed (logs)", "err", err)
		return
	}
	defer conn.Close()

	slog.Info("log stream connected", "remote", r.RemoteAddr, "container", containerID)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	rc, err := s.orch.Dock().Logs(ctx, containerID)
	if err != nil {
		slog.Error("failed to open log stream", "err", err)
		_ = conn.WriteJSON(map[string]string{"error": "failed to open log stream: " + err.Error()})
		return
	}
	defer rc.Close()

	// Docker log streams use a multiplexed format with an 8-byte header
	// per frame. We use bufio to read lines for simplicity — the Docker
	// SDK returns raw bytes that may include the header prefix.
	scanner := bufio.NewScanner(rc)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	// Ping goroutine to detect disconnected clients.
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
					cancel()
					return
				}
			}
		}
	}()

	for scanner.Scan() {
		line := scanner.Text()
		// Docker multiplexed streams have an 8-byte header prefix.
		// Strip non-printable header bytes if present.
		line = stripDockerHeader(line)

		if line == "" {
			continue
		}

		if err := conn.WriteJSON(map[string]string{
			"type": "log",
			"data": line,
		}); err != nil {
			break
		}
	}

	if err := scanner.Err(); err != nil {
		slog.Debug("log stream scanner ended", "err", err)
	}
}

// stripDockerHeader removes the 8-byte multiplexed stream header that Docker
// prepends to each log line when using the hijacked connection format.
// The header is: 1 byte stream type (0x01=stdout, 0x02=stderr) + 3 bytes
// padding (0x00) + 4 bytes big-endian payload length.
func stripDockerHeader(line string) string {
	if len(line) < 8 {
		return strings.TrimSpace(line)
	}
	// Check for known Docker stream type bytes in the first position.
	streamType := line[0]
	if streamType == 0x01 || streamType == 0x02 {
		// Bytes 1-3 must be padding (0x00) for a valid Docker header.
		if line[1] == 0x00 && line[2] == 0x00 && line[3] == 0x00 {
			return strings.TrimSpace(line[8:])
		}
	}
	return strings.TrimSpace(line)
}

// handleStatsStream upgrades to a WebSocket and streams Docker resource stats
// for the container specified by the ?id= query parameter.
func (s *Server) handleStatsStream(w http.ResponseWriter, r *http.Request) {
	containerID := r.URL.Query().Get("id")
	if containerID == "" {
		http.Error(w, "missing id parameter", http.StatusBadRequest)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("websocket upgrade failed (stats)", "err", err)
		return
	}
	defer conn.Close()

	slog.Info("stats stream connected", "remote", r.RemoteAddr, "container", containerID)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	rc, err := s.orch.Dock().Stats(ctx, containerID)
	if err != nil {
		slog.Error("failed to open stats stream", "err", err)
		_ = conn.WriteJSON(map[string]string{"error": "failed to open stats stream: " + err.Error()})
		return
	}
	defer rc.Body.Close()

	decoder := json.NewDecoder(rc.Body)

	// Ping goroutine to detect disconnected clients.
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
					cancel()
					return
				}
			}
		}
	}()

	for {
		var stats container.StatsResponse
		if err := decoder.Decode(&stats); err != nil {
			if ctx.Err() == nil {
				slog.Debug("stats stream ended", "err", err)
			}
			break
		}

		// Calculate CPU percentage.
		cpuPercent := 0.0
		if stats.CPUStats.CPUUsage.TotalUsage > 0 && stats.PreCPUStats.CPUUsage.TotalUsage > 0 {
			cpuDelta := float64(stats.CPUStats.CPUUsage.TotalUsage - stats.PreCPUStats.CPUUsage.TotalUsage)
			systemDelta := float64(stats.CPUStats.SystemUsage - stats.PreCPUStats.SystemUsage)
			if systemDelta > 0 && cpuDelta > 0 {
				onlineCPUs := stats.CPUStats.OnlineCPUs
				if onlineCPUs == 0 {
					onlineCPUs = 1
				}
				cpuPercent = (cpuDelta / systemDelta) * float64(onlineCPUs) * 100
			}
		}

		// Calculate memory usage in MB.
		memUsage := float64(stats.MemoryStats.Usage) / (1024 * 1024)
		memLimit := float64(stats.MemoryStats.Limit) / (1024 * 1024)

		memPercent := 0.0
		if memLimit > 0 {
			memPercent = (memUsage / memLimit) * 100
		}

		msg := map[string]any{
			"type":       "stats",
			"cpu":        fmt.Sprintf("%.1f%%", cpuPercent),
			"mem":        fmt.Sprintf("%.0fMB / %.0fMB", memUsage, memLimit),
			"mem_percent": fmt.Sprintf("%.1f%%", memPercent),
			"timestamp":  time.Now().Format(time.RFC3339),
		}

		if err := conn.WriteJSON(msg); err != nil {
			break
		}
	}
}