package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"golang.org/x/crypto/bcrypt"

	"thanos/internal/config"
	"thanos/internal/docker"
	"thanos/internal/serverlogs"
	"thanos/internal/traffic"
	"thanos/internal/version"
	"thanos/web"
)

// handleHealth returns Thanos service health and uptime.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	// Check Docker connectivity.
	dockerOK := "ok"
	if err := s.orch.Dock().Ping(r.Context()); err != nil {
		dockerOK = "disconnected: " + err.Error()
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"version": version.Version,
		"docker":  dockerOK,
	})
}

// handleIcon serves the configured site icon file, or the embedded
// thanos-icon.jpg if no icon is configured. The icon path is stored in
// thanos_config under the key "icon_path".
func (s *Server) handleIcon(w http.ResponseWriter, r *http.Request) {
	// Check if a custom icon path is configured.
	iconPath, err := s.cfg.GetKV("icon_path")
	if err == nil && iconPath != "" {
		// Validate the path is not a traversal and the file exists.
		if abs, _ := filepath.Abs(iconPath); abs == iconPath || !strings.Contains(iconPath, "..") {
			if data, ferr := os.ReadFile(iconPath); ferr == nil {
				ext := strings.ToLower(filepath.Ext(iconPath))
				mime := "image/jpeg"
				if ext == ".png" {
					mime = "image/png"
				} else if ext == ".svg" {
					mime = "image/svg+xml"
				} else if ext == ".gif" {
					mime = "image/gif"
				} else if ext == ".webp" {
					mime = "image/webp"
				}
				w.Header().Set("Content-Type", mime)
				w.Header().Set("Cache-Control", "no-cache")
				http.ServeContent(w, r, filepath.Base(iconPath), time.Time{}, bytes.NewReader(data))
				return
			}
		}
	}
	// Serve the embedded default Thanos icon.
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-cache")
	data, err := web.WebFS.ReadFile("static/thanos-icon.jpg")
	if err != nil {
		// Fallback to SVG if the embedded file is missing.
		w.Header().Set("Content-Type", "image/svg+xml")
		_, _ = w.Write([]byte(defaultIconSVG))
		return
	}
	_, _ = w.Write(data)
}

const defaultIconSVG = `<svg xmlns="http://www.w3.org/2000/svg" width="64" height="64" viewBox="0 0 64 64">
  <circle cx="32" cy="32" r="30" fill="#451d70" stroke="#8a6fd1" stroke-width="2"/>
  <path d="M20 28 L24 36 L20 44" fill="none" stroke="#d4af37" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"/>
  <path d="M44 28 L40 36 L44 44" fill="none" stroke="#d4af37" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"/>
  <path d="M22 22 Q32 16 42 22" fill="none" stroke="#d4af37" stroke-width="2.5" stroke-linecap="round"/>
  <path d="M24 48 Q32 52 40 48" fill="none" stroke="#d4af37" stroke-width="2.5" stroke-linecap="round"/>
  <rect x="14" y="18" width="36" height="28" rx="6" fill="none" stroke="#8a6fd1" stroke-width="2" opacity="0.5"/>
</svg>`

// handleContainers returns all Thanos-managed containers with their state.
func (s *Server) handleContainers(w http.ResponseWriter, r *http.Request) {
	containers := s.orch.Containers()

	out := make([]map[string]any, 0, len(containers))
	for _, c := range containers {
		lastTraffic := ""
		if !c.LastTrafficAt.IsZero() {
			lastTraffic = c.LastTrafficAt.Format(time.RFC3339)
		}
		lastStarted := ""
		if !c.StartedAt.IsZero() {
			lastStarted = c.StartedAt.Format(time.RFC3339)
		}
		out = append(out, map[string]any{
			"id":            c.ID,
			"name":          c.Name,
			"display_name":  c.DisplayName,
			"state":         string(c.State),
			"ports":         c.Ports,
			"snap_timeout":   c.SnapTimeout,
			"last_started":   lastStarted,
			"last_traffic":   lastTraffic,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{"containers": out})
}

// handleContainerAction routes POST /api/containers/{id}/start|stop.
func (s *Server) handleContainerAction(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	// Expected: api/containers/{id}/{action}
	if len(parts) < 4 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid path"})
		return
	}

	id := parts[2]
	action := parts[3]

	// stats is a GET endpoint; start/stop require POST.
	if action != "stats" && r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}

	switch action {
	case "start":
		slog.Info("API: start requested", "container", id)
		if err := s.orch.ManualStart(r.Context(), id); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "start requested"})
	case "stop":
		slog.Info("API: stop requested", "container", id)
		if err := s.orch.ManualStop(r.Context(), id); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "stop requested"})
	case "stats":
		// One-shot stats query for the container.
		stats, err := s.getContainerStats(id)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, stats)
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown action: " + action})
	}
}

// handleAllContainers returns ALL Docker containers (not just Thanos-managed
// ones). Used by the "Add to Thanos" UI to list candidates.
func (s *Server) handleAllContainers(w http.ResponseWriter, r *http.Request) {
	all, err := s.orch.Dock().ListAll(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	out := make([]map[string]any, 0, len(all))
	for _, c := range all {
		labels := docker.ParseLabels(c)
		out = append(out, map[string]any{
			"id":          c.ID,
			"name":        safeContainerName(c.Names),
			"image":        c.Image,
			"state":        string(c.State),
			"thanos_enabled": labels.Enabled,
			"snap_timeout":   labels.SnapTimeout,
			"display_name":   labels.DisplayName,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{"containers": out})
}

// handleLabels handles POST /api/labels to add/update/remove Thanos labels
// on a container. The body is:
//
//	{"container_id": "abc", "labels": {"thanos.enabled": "true", "thanos.snap_timeout": "300"}}
//
// Setting a label to "" (empty string) removes it.
func (s *Server) handleLabels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}

	var req struct {
		ContainerID    string            `json:"container_id"`
		Labels         map[string]string `json:"labels"`
		DeleteOriginal bool              `json:"delete_original"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	if req.ContainerID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "container_id is required"})
		return
	}

	slog.Info("API: updating labels", "container", req.ContainerID, "labels", req.Labels, "delete_original", req.DeleteOriginal)

	// If the container is being removed from Thanos (thanos.enabled set to
	// empty), delete its docker-compose file from the /docker directory.
	if v, ok := req.Labels["thanos.enabled"]; ok && v == "" {
		if err := s.orch.Dock().DeleteComposeFile(context.Background(), req.ContainerID); err != nil {
			slog.Warn("failed to delete compose file", "container", req.ContainerID, "err", err)
		}
	}

	// Use background context — label update involves recreating the container
	// which may take longer than the HTTP request lifetime.
	if err := s.orch.Dock().UpdateLabelsWithOptions(context.Background(), req.ContainerID, req.Labels, req.DeleteOriginal); err != nil {
		slog.Error("failed to update labels", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	// Trigger reconciliation so Thanos picks up the new labels.
	// Use background context — r.Context() is cancelled when the HTTP
	// response is sent, but reconciliation needs to complete asynchronously.
	// Use ReconcileNoStop so we don't kill running containers.
	go s.orch.ReconcileNoStop(context.Background())

	writeJSON(w, http.StatusOK, map[string]string{"status": "labels updated"})
}

// safeContainerName returns the first container name without the leading
// "/", or an empty string if the Names slice is empty.
func safeContainerName(names []string) string {
	if len(names) == 0 {
		return ""
	}
	return strings.TrimPrefix(names[0], "/")
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// computeStats calculates CPU and memory percentages from a Docker stats
// response. Used by both one-shot and streaming stats handlers.
func computeStats(stats container.StatsResponse) map[string]any {
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

	memUsage := float64(stats.MemoryStats.Usage) / (1024 * 1024)
	memLimit := float64(stats.MemoryStats.Limit) / (1024 * 1024)

	memPercent := 0.0
	if memLimit > 0 {
		memPercent = (memUsage / memLimit) * 100
	}

	return map[string]any{
		"cpu":         fmt.Sprintf("%.1f%%", cpuPercent),
		"mem":         fmt.Sprintf("%.0fMB / %.0fMB", memUsage, memLimit),
		"mem_percent": fmt.Sprintf("%.1f%%", memPercent),
	}
}

// getContainerStats does a one-shot Docker stats query for a container
// and returns CPU and memory usage as a map.
//
// Docker's stats API reports CPU as a delta between two samples. With
// stream=false the first response often has empty PreCPUStats, yielding
// 0.0% CPU. We use stream=true and read two samples, computing the delta
// from the second one (which has PreCPUStats populated from the first).
func (s *Server) getContainerStats(id string) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	rc, err := s.orch.Dock().Stats(ctx, id)
	if err != nil {
		return nil, err
	}
	defer rc.Body.Close()

	decoder := json.NewDecoder(rc.Body)

	// Read first sample (used as the "previous" baseline).
	var first container.StatsResponse
	if err := decoder.Decode(&first); err != nil {
		return nil, fmt.Errorf("decode first stats sample: %w", err)
	}

	// If the first sample already has valid PreCPUStats, use it directly.
	var stats container.StatsResponse
	if first.PreCPUStats.CPUUsage.TotalUsage > 0 {
		stats = first
	} else {
		// Read the second sample — its PreCPUStats will be the first sample.
		if err := decoder.Decode(&stats); err != nil {
			// Fall back to the first sample if the second read fails
			// (e.g. timeout — memory is still valid, just CPU will be 0).
			stats = first
		}
	}

	return computeStats(stats), nil
}

// handleServerLogs returns the per-server state-change log entries for a
// container. The container ID is used to look up the container's display
// name, which is used as the log filename. Returns up to 200 most recent
// log entries.
func (s *Server) handleServerLogs(w http.ResponseWriter, r *http.Request) {
	containerID := strings.TrimPrefix(r.URL.Path, "/api/server-logs/")
	if containerID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "container ID required"})
		return
	}

	ci := s.orch.GetContainer(containerID)
	if ci == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "container not found"})
		return
	}

	entries, err := serverlogs.ReadEntries(s.cfg.DB, containerID, 200)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"container": ci.DisplayName,
		"logs":      entries,
	})
}

// handleTraffic returns recent wake-on-connect events from the traffic_log
// table. Optional query params:
//   - container=<id>  — filter to a specific container
//   - limit=<n>        — max results (default 50)
func (s *Server) handleTraffic(w http.ResponseWriter, r *http.Request) {
	containerID := r.URL.Query().Get("container")
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	entries, err := traffic.RecentWakes(s.cfg.DB, containerID, limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
}

// handleClients returns known client IPs from the known_clients table.
// Optional query params:
//   - container=<id>  — filter to a specific container
//   - limit=<n>        — max results (default 100)
func (s *Server) handleClients(w http.ResponseWriter, r *http.Request) {
	containerID := r.URL.Query().Get("container")
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	entries, err := traffic.KnownClients(s.cfg.DB, containerID, limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
}

func (s *Server) handleInterfaces(w http.ResponseWriter, r *http.Request) {
	list, err := config.ListNetworkInterfaces()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"interfaces": list})
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		interfaces, err := config.ListNetworkInterfaces()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"username":           s.cfg.WebUsername,
			"network_interface":  s.cfg.NetworkInterface,
			"interfaces":         interfaces,
			"discord_guild_id":    s.cfg.DiscordGuildID,
			"discord_channel_id":  s.cfg.DiscordChannelID,
			"discord_log_channel_id": s.cfg.DiscordLogChannelID,
			"blacklist":          s.cfg.BlacklistString(),
		})
		return
	case http.MethodPost:
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET or POST only"})
		return
	}

	var req struct {
		Username           string `json:"username"`
		Password           string `json:"password"`
		ConfirmPassword    string `json:"confirm_password"`
		NetworkInterface   string `json:"network_interface"`
		DiscordGuildID      string `json:"discord_guild_id"`
		DiscordChannelID    string `json:"discord_channel_id"`
		DiscordLogChannelID string `json:"discord_log_channel_id"`
		Blacklist          string `json:"blacklist"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}

	req.Username = strings.TrimSpace(req.Username)
	req.NetworkInterface = strings.TrimSpace(req.NetworkInterface)
	if req.Username == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "username is required"})
		return
	}
	if req.Username != s.cfg.WebUsername && req.Password == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "enter a new password when changing the username"})
		return
	}
	if req.NetworkInterface == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "network interface is required"})
		return
	}
	if req.Password != "" && req.Password != req.ConfirmPassword {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "passwords do not match"})
		return
	}

	s.cfg.WebUsername = req.Username
	if req.Password != "" {
		hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to hash password"})
			return
		}
		s.cfg.WebPasswordHash = string(hash)
	}
	s.cfg.NetworkInterface = req.NetworkInterface

	// Update Discord config if provided.
	var warnings []string
	if req.DiscordGuildID != s.cfg.DiscordGuildID ||
		req.DiscordChannelID != s.cfg.DiscordChannelID {
		s.cfg.DiscordGuildID = req.DiscordGuildID
		s.cfg.DiscordChannelID = req.DiscordChannelID
		if err := s.cfg.SaveDiscord(); err != nil {
			slog.Warn("failed to save discord config", "err", err)
			warnings = append(warnings, fmt.Sprintf("discord config: %v", err))
		}
	}
	if req.DiscordLogChannelID != s.cfg.DiscordLogChannelID {
		s.cfg.DiscordLogChannelID = req.DiscordLogChannelID
		if err := s.cfg.SaveKV("discord_log_channel_id", req.DiscordLogChannelID); err != nil {
			slog.Warn("failed to save discord log channel", "err", err)
			warnings = append(warnings, fmt.Sprintf("discord log channel: %v", err))
		}
	}

	// Save blacklist (CIDR patterns, one per line).
	if err := s.cfg.SaveBlacklist(req.Blacklist); err != nil {
		slog.Warn("failed to save blacklist", "err", err)
		warnings = append(warnings, fmt.Sprintf("blacklist: %v", err))
	}

	if err := s.cfg.SaveWebAuth(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("save web auth: %v", err)})
		return
	}
	if err := s.cfg.SaveKV("network_interface", req.NetworkInterface); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("save interface: %v", err)})
		return
	}
	if s.ifaceUpdater != nil {
		s.ifaceUpdater.SetNetworkInterface(req.NetworkInterface)
	}

	if len(warnings) > 0 {
		writeJSON(w, http.StatusOK, map[string]any{"status": "settings updated with warnings", "warnings": warnings})
	} else {
		writeJSON(w, http.StatusOK, map[string]string{"status": "settings updated"})
	}
}