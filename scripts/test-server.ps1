param(
    [switch]$Force
)

. "$PSScriptRoot\_common.ps1"

Set-ThanosLocation
try {
    # Remove existing test container if present.
    $existing = docker ps -a --filter "name=thanos-test" --format "{{.Names}}" 2>$null
    if ($existing -eq "thanos-test") {
        if ($Force) {
            Write-Host "Removing existing thanos-test container..." -ForegroundColor Yellow
            docker rm -f thanos-test | Out-Null
        } else {
            Write-Host "Container 'thanos-test' already exists. Use -Force to recreate." -ForegroundColor Yellow
            return
        }
    }

    Write-Host "Creating test container..." -ForegroundColor Cyan
    docker run -d --name thanos-test `
        --label thanos.enabled=true `
        --label thanos.snap_timeout=0.5 `
        --label thanos.display_name="Test Server" `
        -p 25565:25565 `
        alpine sleep 9999 | Out-Null

    Write-Host "Test container created: thanos-test (port 25565, snap_timeout=0.5h)" -ForegroundColor Green
    Write-Host "Test with: Test-NetConnection 127.0.0.1 -Port 25565" -ForegroundColor Gray
}
finally {
    Reset-ThanosLocation
}