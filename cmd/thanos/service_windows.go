//go:build windows

package main

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows/svc"
)

// isWindowsService returns true when the process was started by the Windows
// Service Control Manager. When run from a console (even as admin), it
// returns false and the binary behaves as a normal CLI app.
func isWindowsService() bool {
	is, err := svc.IsWindowsService()
	return err == nil && is
}

// thanosService implements svc.Handler.
type thanosService struct{}

func (*thanosService) Execute(args []string, r <-chan svc.ChangeRequest, changes chan<- svc.Status) (ssec bool, errno uint32) {
	// When running as a service, the working directory defaults to
	// C:\Windows\System32. Change to the directory of the executable so
	// thanos.db and docker/ are relative to the install dir.
	if exe, err := os.Executable(); err == nil {
		if dir := filepath.Dir(exe); dir != "" {
			if err := os.Chdir(dir); err != nil {
				slog.Warn("failed to change working directory", "dir", dir, "err", err)
			} else {
				slog.Info("service working directory set", "dir", dir)
			}
		}
	}

	// Tell the SCM we're starting up.
	changes <- svc.Status{State: svc.StartPending}

	ctx, cancel := context.WithCancel(context.Background())

	// Launch all Thanos components in a goroutine so the SCM gets the
	// "Running" signal quickly (within its 30s timeout). The actual
	// startup (Docker reconnect, config load, etc.) happens asynchronously.
	go run(ctx)

	// Give components a moment to initialize, then signal running.
	time.Sleep(2 * time.Second)

	// Signal the SCM that we're running and accept stop/shutdown commands.
	changes <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}

	// Block until the SCM sends a stop or shutdown request.
loop:
	for {
		select {
		case c := <-r:
			switch c.Cmd {
			case svc.Interrogate:
				changes <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				slog.Info("service stop requested")
				break loop
			default:
				slog.Debug("unhandled service command", "cmd", c.Cmd)
			}
		case <-ctx.Done():
			break loop
		}
	}

	// Tell the SCM we're stopping.
	changes <- svc.Status{State: svc.StopPending}

	// Gracefully shut down.
	shutdown(cancel)

	// Give goroutines a moment to clean up.
	time.Sleep(2 * time.Second)

	changes <- svc.Status{State: svc.Stopped}
	return false, 0
}

// runService registers the process as a Windows service with the SCM.
func runService() error {
	return svc.Run("Thanos", &thanosService{})
}