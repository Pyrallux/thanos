// Package api implements the Thanos HTTP REST API, WebSocket log/stats
// streaming, and serves the embedded Web UI.
package api

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"

	"thanos/internal/config"
	"thanos/internal/orchestrator"
	"thanos/web"
)

// NetworkInterfaceUpdater allows the API to restart packet sniffing after the
// active adapter is changed from the settings screen.
type NetworkInterfaceUpdater interface {
	SetNetworkInterface(string)
}

// Server wraps the HTTP server and its dependencies.
type Server struct {
	cfg          *config.Config
	orch         *orchestrator.Orchestrator
	ifaceUpdater NetworkInterfaceUpdater
	srv          *http.Server
}

// NewServer creates a new API server with all routes registered.
func NewServer(cfg *config.Config, orch *orchestrator.Orchestrator, ifaceUpdater NetworkInterfaceUpdater) *Server {
	s := &Server{cfg: cfg, orch: orch, ifaceUpdater: ifaceUpdater}

	mux := http.NewServeMux()

	// REST API — protected by Basic Auth (except /setup during first run).
	apiMux := http.NewServeMux()
	apiMux.HandleFunc("/api/health", s.handleHealth)
	apiMux.HandleFunc("/api/containers", s.auth(s.handleContainers))
	apiMux.HandleFunc("/api/containers/", s.auth(s.handleContainerAction))
	apiMux.HandleFunc("/api/all-containers", s.auth(s.handleAllContainers))
	apiMux.HandleFunc("/api/labels", s.auth(s.handleLabels))
	apiMux.HandleFunc("/api/interfaces", s.auth(s.handleInterfaces))
	apiMux.HandleFunc("/api/settings", s.auth(s.handleSettings))
	apiMux.HandleFunc("/api/server-logs/", s.auth(s.handleServerLogs))
	apiMux.HandleFunc("/api/traffic", s.auth(s.handleTraffic))
	apiMux.HandleFunc("/api/clients", s.auth(s.handleClients))

	// WebSocket endpoints — auth via query param since WebSocket
	// connections can't send HTTP headers.
	apiMux.HandleFunc("/api/ws/logs", s.authQuery(s.handleLogStream))
	apiMux.HandleFunc("/api/ws/stats", s.authQuery(s.handleStatsStream))

	mux.Handle("/api/", apiMux)

	// Icon endpoint — serves a user-configured icon file if set, otherwise
	// a default Thanos SVG. The path is configurable via the "icon_path"
	// key in thanos_config (SQLite).
	mux.HandleFunc("/icon", s.handleIcon)

	// Web UI — embedded static files (strip the "static/" prefix so files
	// are served at root: /index.html, /style.css, /app.js).
	staticFS, err := fs.Sub(web.WebFS, "static")
	if err != nil {
		slog.Error("failed to access embedded static files", "err", err)
	} else {
		mux.Handle("/", http.FileServer(http.FS(staticFS)))
	}

	s.srv = &http.Server{
		Addr:    fmt.Sprintf(":%d", cfg.APIPort),
		Handler: mux,
	}

	return s
}

// ListenAndServe starts the HTTP server.
func (s *Server) ListenAndServe() error {
	slog.Info("API server listening", "addr", s.srv.Addr)
	return s.srv.ListenAndServe()
}

// Shutdown gracefully shuts down the HTTP server, allowing in-flight
// requests to complete before returning.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.srv.Shutdown(ctx)
}