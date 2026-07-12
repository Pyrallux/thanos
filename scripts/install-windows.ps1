# install-windows.ps1
# Installs Thanos as a Windows service.
#
# Two methods are supported:
#   - NSSM (recommended, handles crashes/restarts gracefully)
#   - sc.exe  (Windows built-in service manager, no extra software needed)
#
# Run in an elevated PowerShell: .\scripts\install-windows.ps1
# To force sc.exe:    .\scripts\install-windows.ps1 -Method sc

param(
    [string]$InstallDir = "C:\Program Files\Thanos",
    [string]$ServiceName = "Thanos",
    [ValidateSet("nssm","sc")]
    [string]$Method = "nssm"
)

$ErrorActionPreference = "Stop"

Write-Host "=== Thanos Windows Service Installer ===" -ForegroundColor Cyan

# 1. Check if Go binary exists
$exe = Join-Path $PSScriptRoot "..\thanos.exe"
if (-not (Test-Path $exe)) {
    Write-Host "thanos.exe not found. Building from source..." -ForegroundColor Yellow
    Push-Location (Join-Path $PSScriptRoot "..")
    . "$PSScriptRoot\_common.ps1"
    $ldflags = Get-ThanosLdFlags
    go build -ldflags $ldflags -o thanos.exe .\cmd\thanos
    Pop-Location
}

# 2. Create install directory
if (-not (Test-Path $InstallDir)) {
    New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
}

# 3. Copy binary
Copy-Item $exe $InstallDir -Force
Write-Host "Copied thanos.exe to $InstallDir"

# Copy thanos.db if it exists (so config is preserved across reinstalls).
$dbPath = Join-Path $PSScriptRoot "..\thanos.db"
if (Test-Path $dbPath) {
    Copy-Item $dbPath $InstallDir -Force
    Write-Host "Copied thanos.db to $InstallDir"
}

# 4. Check for Npcap
$npcapInstalled = Get-Service -Name "npcap" -ErrorAction SilentlyContinue
if (-not $npcapInstalled) {
    Write-Host "WARNING: Npcap service not found. Packet sniffing requires Npcap." -ForegroundColor Yellow
    Write-Host "  Download from: https://npcap.com/" -ForegroundColor Yellow
    Write-Host "  Thanos will run in manual-start-only mode without Npcap." -ForegroundColor Yellow
}

# 5. Install service using chosen method
if ($Method -eq "sc") {
    # --- Windows native sc.exe (no extra software needed) ---
    Write-Host "Installing service '$ServiceName' using sc.exe..." -ForegroundColor Cyan

    # Remove existing service if present
    $existing = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
    if ($existing) {
        Write-Host "Stopping and removing existing service..." -ForegroundColor Yellow
        Stop-Service $ServiceName -Force -ErrorAction SilentlyContinue
        sc.exe delete $ServiceName | Out-Null
        Start-Sleep -Seconds 2
    }

    sc.exe create $ServiceName binPath= (Join-Path $InstallDir "thanos.exe") start= auto | Out-Null
    sc.exe description $ServiceName "Thanos - Scale-to-Zero for Docker game servers" | Out-Null

    # Auto-restart on crash: restart after 10s, reset failure counter after 1 day.
    sc.exe failure $ServiceName reset= 86400 actions= restart/10000 | Out-Null

    Write-Host ""
    Write-Host "Service installed successfully (sc.exe)!" -ForegroundColor Green

} else {
    # --- NSSM (recommended: handles crashes, logs, graceful restarts) ---
    $nssm = Get-Command nssm -ErrorAction SilentlyContinue
    if (-not $nssm) {
        Write-Host "NSSM not found. Falling back to sc.exe (Windows native)." -ForegroundColor Yellow
        Write-Host "To use NSSM instead: choco install nssm  (or https://nssm.cc/download)" -ForegroundColor Yellow
        Write-Host ""
        & $PSCommandPath -Method sc -InstallDir $InstallDir -ServiceName $ServiceName
        return
    }

    Write-Host "Installing service '$ServiceName' using NSSM..." -ForegroundColor Cyan
    nssm install $ServiceName (Join-Path $InstallDir "thanos.exe")
    nssm set $ServiceName AppDirectory $InstallDir
    nssm set $ServiceName Start SERVICE_AUTO_START
    nssm set $ServiceName Description "Thanos - Scale-to-Zero for Docker game servers"
    nssm set $ServiceName AppStdout (Join-Path $InstallDir "thanos.log")
    nssm set $ServiceName AppStderr (Join-Path $InstallDir "thanos.log")
    nssm set $ServiceName AppRotateFiles 1
    nssm set $ServiceName AppRotateBytes 10485760

    Write-Host ""
    Write-Host "Service installed successfully (NSSM)!" -ForegroundColor Green
}

# 6. Open Windows Firewall for LAN access to the Thanos web UI.
$port = 4040
$fwRule = Get-NetFirewallRule -DisplayName "Thanos Web UI" -ErrorAction SilentlyContinue
if (-not $fwRule) {
    New-NetFirewallRule -DisplayName "Thanos Web UI" `
        -Direction Inbound -Action Allow -Protocol TCP -LocalPort $port `
        -Profile Private,Domain `
        -Description "Allow LAN access to Thanos admin panel" | Out-Null
    Write-Host "Opened firewall port $port (TCP inbound) for LAN access." -ForegroundColor Green
} else {
    Write-Host "Firewall rule 'Thanos Web UI' already exists." -ForegroundColor DarkGray
}

Write-Host ""
Write-Host "Next steps:" -ForegroundColor Cyan
Write-Host "  1. Start the service:  net start $ServiceName"
Write-Host "  2. On first run, open http://localhost:4040/setup to configure"
Write-Host "  3. From other machines on your LAN, open http://<this-pc-ip>:4040"
Write-Host ""
Write-Host "To uninstall:" -ForegroundColor Gray
Write-Host "  net stop $ServiceName; sc.exe delete $ServiceName"
Write-Host "  Remove-NetFirewallRule -DisplayName 'Thanos Web UI'"