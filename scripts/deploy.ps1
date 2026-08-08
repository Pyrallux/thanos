# deploy.ps1 â€” Stop service, rebuild, redeploy, restart.
# MUST be run in an elevated PowerShell (Run as Administrator).
#
# Usage:
#   .\scripts\deploy.ps1              # uses default paths
#   .\scripts\deploy.ps1 -InstallDir "D:\Thanos"  # custom install dir

param(
    [string]$InstallDir = "C:\Program Files\Thanos",
    [string]$ServiceName = "Thanos"
)

$root = Resolve-Path (Join-Path $PSScriptRoot "..")

. "$PSScriptRoot\_common.ps1"

# --- Admin check ---
$isAdmin = ([Security.Principal.WindowsPrincipal] [Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
if (-not $isAdmin) {
    Write-Host "ERROR: This script must be run as Administrator." -ForegroundColor Red
    Write-Host "Right-click PowerShell -> Run as Administrator, then run this script again." -ForegroundColor Yellow
    exit 1
}

Write-Host "=== Thanos Deploy ===" -ForegroundColor Cyan

# 1. Stop the service (if it exists and is running).
$svc = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
if ($svc) {
    if ($svc.Status -eq "Running") {
        Write-Host "Stopping service '$ServiceName'..." -ForegroundColor Yellow
        Stop-Service $ServiceName -Force -ErrorAction Stop
        Start-Sleep -Seconds 3
    }
    Write-Host "Service stopped." -ForegroundColor Green
} else {
    Write-Host "Service '$ServiceName' not found â€” skipping stop." -ForegroundColor DarkGray
}

# Also kill any stray thanos process holding the port.
Get-Process thanos* -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
Start-Sleep -Seconds 1

# 2. Build the binary.
Write-Host "Building..." -ForegroundColor Yellow
Push-Location $root
$ldflags = Get-ThanosLdFlags
$ver = Get-ThanosVersion
& go build -ldflags $ldflags -o "bin\thanos.exe" .\cmd\thanos
if ($LASTEXITCODE -ne 0) {
    Pop-Location
    Write-Host "ERROR: Build failed (exit $LASTEXITCODE)" -ForegroundColor Red
    exit 1
}
Pop-Location
Write-Host "Built bin\thanos.exe (version: $ver)" -ForegroundColor Green

# 3. Copy the new binary to the install directory.
$exe = Join-Path $root "bin\thanos.exe"
if (-not (Test-Path $InstallDir)) {
    New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
}
Write-Host "Copying to $InstallDir..." -ForegroundColor Yellow
Copy-Item $exe $InstallDir -Force -ErrorAction Stop
Write-Host "Deployed thanos.exe to $InstallDir" -ForegroundColor Green

# Copy thanos.db from the project root ONLY if the install directory
# doesn't already have one. The production DB (in $InstallDir) is the
# source of truth — it has the live config, credentials, and blacklist.
# Overwriting it with the project-root DB would wipe user settings.
$srcDb = Join-Path $root "thanos.db"
$dstDb = Join-Path $InstallDir "thanos.db"
if ((Test-Path $srcDb) -and -not (Test-Path $dstDb)) {
    Write-Host "Copying thanos.db (first deploy)..." -ForegroundColor Yellow
    Copy-Item $srcDb $dstDb -Force -ErrorAction Stop
    foreach ($ext in @("-wal", "-shm")) {
        $sidecar = Join-Path $root "thanos.db$ext"
        if (Test-Path $sidecar) {
            Copy-Item $sidecar (Join-Path $InstallDir "thanos.db$ext") -Force -ErrorAction SilentlyContinue
        }
    }
    Write-Host "Initialized thanos.db" -ForegroundColor Green
} elseif (Test-Path $dstDb) {
    Write-Host "Preserving existing thanos.db in $InstallDir" -ForegroundColor DarkGray
} else {
    Write-Host "No thanos.db found -- will be created on first run." -ForegroundColor DarkGray
}

# 4. Start the service (if it was installed).
if ($svc) {
    Write-Host "Starting service '$ServiceName'..." -ForegroundColor Yellow
    # Use net start instead of Start-Service: it's more reliable for
    # service-aware binaries that need a moment to register with the SCM.
    $startResult = & net start $ServiceName 2>&1
    Start-Sleep -Seconds 4
    $svc = Get-Service $ServiceName
    Write-Host "Service status: $($svc.Status)" -ForegroundColor Green
    if ($svc.Status -ne "Running") {
        Write-Host "WARNING: Service is not running!" -ForegroundColor Red
        Write-Host "net start output: $startResult" -ForegroundColor DarkGray
        Write-Host "Check Event Viewer -> Windows Logs -> System for details." -ForegroundColor Yellow
    }
} else {
    Write-Host "Service not installed - run .\scripts\install-windows.ps1 to install." -ForegroundColor DarkGray
}

Write-Host "=== Deploy complete ===" -ForegroundColor Cyan
