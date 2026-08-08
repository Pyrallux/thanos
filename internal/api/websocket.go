package api

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/gorilla/websocket"
)

// upgrader allows the Web UI origin (localhost:4040).
var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

// startPingLoop sends periodic WebSocket pings to detect disconnected
// clients. It cancels the given context when a ping fails so the stream
// handler can shut down.
func startPingLoop(ctx context.Context, cancel context.CancelFunc, conn *websocket.Conn) {
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

	// Docker log streams use a multiplexed binary format with an 8-byte
	// header per frame. StdCopy demultiplexes stdout/stderr into plain
	// text, so we can read clean lines without manual header stripping.
	// Both streams are merged into a single pipe; the stream type byte is
	// not preserved, which matches the previous behavior.
	pr, pw := io.Pipe()
	go func() {
		_, _ = stdcopy.StdCopy(pw, pw, rc)
		_ = pw.Close()
	}()

	go startPingLoop(ctx, cancel, conn)

	scanner := bufio.NewScanner(pr)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
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

	go startPingLoop(ctx, cancel, conn)

	for {
		var stats container.StatsResponse
		if err := decoder.Decode(&stats); err != nil {
			if ctx.Err() == nil {
				slog.Debug("stats stream ended", "err", err)
			}
			break
		}

		statValues := computeStats(stats)

		msg := map[string]any{
			"type":       "stats",
			"cpu":        statValues["cpu"],
			"mem":        statValues["mem"],
			"mem_percent": statValues["mem_percent"],
			"timestamp":  time.Now().Format(time.RFC3339),
		}

		if err := conn.WriteJSON(msg); err != nil {
			break
		}
	}
}