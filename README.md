# Thanos

> Scale-to-Zero for Docker game servers. Passive packet sniffing wakes dormant containers on connection, idle timers snap them back down.

## Overview

Thanos monitors your Docker game servers and keeps them dormant (stopped) until a player actually connects. When someone tries to join, the packet sniffer detects the incoming connection and starts the container in real time. After a configurable idle period with no traffic, Thanos stops the container again — saving CPU, memory, and power.

Thanos is a layer on top of standard Docker. It never proxies or intercepts network traffic — it only observes and orchestrates. If Thanos crashes or is uninstalled, all containers remain natively operable via standard Docker.

## Current Features

- **Wake-on-connect** — BPF packet sniffer detects inbound TCP SYN / UDP to your server ports and starts the container automatically
- **Idle snap** — Stops containers after a configurable inactivity timeout (e.g. 15 minutes with no traffic)
- **Web dashboard** — Browser-based UI for starting/stopping servers, viewing Docker logs, state logs, and traffic history; editing per-server settings; accessible over your LAN
- **Discord integration** — Persistent status embed with per-server state emojis, slash commands (`/start`, `/stop`, `/status`, `/config`), and event notifications (wake, idle shutdown, crash)
- **Crash detection** — Unexpected container exits are flagged as crashed (not silently restarted) to protect save data
- **Traffic logging** — Every wake-on-connect event is logged with source IP, port, and protocol; known client IPs are tracked per server
- **Per-server state logs** — Every state transition (dormant→starting→running→stopping→dormant, crash, etc.) is recorded in SQLite with timestamp and reason
- **Docker-compose sync** — Generates and maintains a `docker-compose.yml` file per managed server in `/docker`, synced on startup and on every CRUD operation
- **Windows service support** — Runs as a native Windows service (auto-start on boot, crash recovery), via NSSM, or as a console app
- **CPU & memory monitoring** — Live resource usage displayed on each server card with last-known-good caching to prevent flicker

## Project Structure

```
thanos/
├── cmd/thanos/                  # Entry point + Windows service wrapper
├── internal/
│   ├── api/                     # REST API + WebSocket log/stats streaming + Basic Auth
│   ├── config/                  # SQLite config + DB migrations + first-run setup wizard
│   ├── discord/                 # Discord bot (status embed, slash commands, event notifications)
│   ├── docker/                  # Docker SDK wrapper + label parsing + compose file sync
│   ├── orchestrator/            # Docker lifecycle + state machine + idle timers + event subscription
│   ├── sentinel/                # Packet sniffer + BPF filter + port watcher + traffic logging
│   ├── serverlogs/              # Per-server state-change log (SQLite server_log table)
│   ├── traffic/                 # Wake-on-connect traffic logging + known clients (SQLite)
│   └── version/                 # Build version (injected via ldflags from git tags)
├── web/static/                  # Embedded Web UI (HTML/CSS/JS) — no build step needed
├── docker/                      # Auto-generated per-server docker-compose.yml files (gitignored)
├── scripts/                     # Windows + Linux install/deploy/build scripts
└── go.mod
```

## Getting Started

### Prerequisites

1. **Go 1.25+** — [Install Go](https://go.dev/doc/install)
2. **Docker** — [Install Docker Engine](https://docs.docker.com/get-docker/)
3. **Packet capture library** (required for wake-on-connect):
   - **Windows:** Install [Npcap](https://npcap.com/) (select "WinPcap API-compatible Mode" during install). Without Npcap, Thanos runs in manual-start-only mode (web UI and Docker orchestration work, but wake-on-connect is disabled).
   - **Linux:** Install libpcap dev headers:
     ```bash
     sudo apt install libpcap-dev   # Debian/Ubuntu
     sudo dnf install libpcap-devel  # Fedora/RHEL
     ```

### Build

```bash
# Linux (requires CGO for libpcap)
CGO_ENABLED=1 go build -o thanos ./cmd/thanos

# Windows
go build -o thanos.exe ./cmd/thanos

# Or use the build script (Windows, auto-injects git version)
.\scripts\build.ps1
```

> **Versioning:** The build scripts automatically derive the version from `git describe --tags` and inject it into the binary. If you build manually with `go build`, the binary reports `dev` as its version. To set a version, tag the repo first: `git tag v1.0.0`, then build. The version shows in the startup log, the `/api/health` endpoint, and the web UI footer.

### Run

```bash
# Run directly (builds if needed)
.\scripts\run.ps1

# Or build + run manually
go build -o bin\thanos.exe .\cmd\thanos
.\bin\thanos.exe
```

On first run, open [http://localhost:4040/setup](http://localhost:4040/setup) to:

- Set web UI username/password
- Optionally configure Discord bot token/channel
- Select which network interface to sniff (use **Loopback** for local testing, your LAN adapter for production)

All configuration is stored in `thanos.db` (SQLite) in the working directory. State-change logs and traffic data are also stored there.

### Label Your Containers

Add Thanos labels to any Docker container you want managed:

```yaml
# docker-compose.yml
services:
  minecraft:
    image: itzg/minecraft-server
    ports:
      - "25565:25565"
    labels:
      - "thanos.enabled=true"
      - "thanos.snap_timeout=0.25" # hours (0.25 = 15 minutes)
      - "thanos.display_name=Minecraft Survival"
    volumes:
      - ./mc-data:/data
```

Or use the **Manage Containers** button in the web UI to add/remove labels on existing containers without editing files.

### Available Docker Labels

| Label                         | Default | Description                                           |
| ----------------------------- | ------- | ----------------------------------------------------- |
| `thanos.enabled`              | `false` | Enables Thanos management                             |
| `thanos.snap_timeout`         | `0.25`  | Hours of inactivity before shutdown (0 = never)       |
| `thanos.display_name`         | name    | Friendly name for Discord/Web UI                      |
| `thanos.notify_discord`       | `true`  | Send Discord notifications                            |
| `thanos.crash_detection`      | `true`  | Monitor for unexpected exits                          |
| `thanos.keep_running_on_boot` | `false` | Keep container running on Thanos startup (don't snap) |

> The `thanos.snap_timeout` label is specified in **hours** (decimals supported, e.g. `0.25` = 15 minutes, `2` = 2 hours).

## Installation

### Windows

Run in an **elevated PowerShell** (Run as Administrator):

```powershell
# Option A: NSSM (recommended — handles crash recovery, log rotation)
.\scripts\install-windows.ps1

# Option B: Windows native sc.exe (no extra software needed)
.\scripts\install-windows.ps1 -Method sc
```

Both methods install to `C:\Program Files\Thanos\`, set the service to auto-start on boot, and open TCP port 4040 in Windows Firewall for LAN access.

After installation:

```powershell
net start Thanos
```

| Action    | Command                                            |
| --------- | -------------------------------------------------- |
| Start     | `net start Thanos`                                 |
| Stop      | `net stop Thanos`                                  |
| Status    | `sc.exe query Thanos`                              |
| Redeploy  | `.\scripts\deploy.ps1` (stops, rebuilds, restarts) |
| Uninstall | `net stop Thanos; sc.exe delete Thanos`            |

### Linux

```bash
sudo ./scripts/install-linux.sh
```

### Network Access

The web UI binds to `0.0.0.0:4040` (all interfaces) by default, making it accessible from other devices on your LAN at `http://<machine-IP>:4040`. The install script opens the firewall automatically. If your network is set to **Public** in Windows, either switch it to **Private** or manually add a firewall rule:

```powershell
New-NetFirewallRule -DisplayName "Thanos Web UI" -Direction Inbound -Action Allow -Protocol TCP -LocalPort 4040 -Profile Public
```
