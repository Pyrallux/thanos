package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"thanos/internal/api"
	"thanos/internal/config"
	"thanos/internal/discord"
	"thanos/internal/orchestrator"
	"thanos/internal/sentinel"
	"thanos/internal/traffic"
	"thanos/internal/version"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	// isWindowsService returns true when the process was launched by the
	// Windows Service Control Manager. In that case we run as a service
	// (registering with the SCM so it doesn't time out with error 2186).
	// On non-Windows or when run from a console, we fall through to the
	// normal interactive mode.
	if isWindowsService() {
		if err := runService(); err != nil {
			slog.Error("service error", "err", err)
			os.Exit(1)
		}
		return
	}

	// Interactive mode — Ctrl+C / SIGTERM to shut down.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	run(ctx)

	// Wait for shutdown signal.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	shutdown(cancel)
}

// server holds the API server instance so the shutdown path can reach it.
var server *api.Server

// run starts all Thanos components (config, orchestrator, sentinel, Discord,
// API server) in the given context. It returns once all goroutines are
// launched. The caller is responsible for cancellation and shutdown.
func run(ctx context.Context) {
	// Load (or create) configuration from SQLite. On first run this launches
	// the setup wizard and returns the initial config once the user completes it.
	cfg, err := config.Load(ctx)
	if err != nil {
		slog.Error("failed to load configuration", "err", err)
		os.Exit(1)
	}

	// Wire up the orchestrator (Docker lifecycle + state machine + idle timers).
	orch, err := orchestrator.New(cfg)
	if err != nil {
		slog.Error("failed to create orchestrator", "err", err)
		os.Exit(1)
	}
	go orch.Run(ctx)

	// Traffic logger — records wake-on-connect events and known clients.
	tlog := traffic.New(cfg.DB)

	// Network sentinel — passive packet sniffer for wake-on-connect.
	var sniffer *sentinel.Sentinel
	sniffer, err = sentinel.New(cfg, orch, tlog)
	if err != nil {
		slog.Error("failed to create network sentinel", "err", err)
		slog.Info("falling back to manual-start-only mode; web UI and Docker orchestration continue to work")
	} else {
		// Register the sentinel as a state watcher so it gets notified when
		// containers transition between dormant/running (to update the BPF filter).
		orch.RegisterWatcher(sniffer)
		go sniffer.Run(ctx)
	}

	// Discord bot — optional. If token is empty the bot is disabled.
	if cfg.DiscordBotToken != "" {
		bot, err := discord.New(cfg, orch)
		if err != nil {
			slog.Error("discord bot init failed, continuing without Discord", "err", err)
		} else {
			go bot.Run(ctx)
		}
	}

	// Web UI + REST/WebSocket API server.
	server = api.NewServer(cfg, orch, sniffer)
	go func() {
		if err := server.ListenAndServe(); err != nil {
			slog.Error("api server stopped", "err", err)
		}
	}()

	slog.Info("Thanos is running", "version", version.Version, "port", cfg.APIPort)
}

// shutdown gracefully stops the HTTP server and cancels the context.
func shutdown(cancel context.CancelFunc) {
	slog.Info("shutting down Thanos...")

	if server != nil {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := server.Shutdown(shutdownCtx); err != nil {
			slog.Warn("HTTP server shutdown error", "err", err)
		}
		shutdownCancel()
	}

	cancel()
}