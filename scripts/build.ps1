param(
    [string]$Output = "bin\thanos.exe",
    [switch]$Clean
)

. "$PSScriptRoot\_common.ps1"

Set-ThanosLocation
try {
    # Always remove old build artifacts before building.
    if (Test-Path $Output) {
        Remove-Item $Output -Force
    }
    # Also clean up stale executables from the project root.
    if (Test-Path "thanos.exe") {
        Remove-Item "thanos.exe" -Force
    }

    Ensure-Directory -Path (Split-Path $Output -Parent)
    $ldflags = Get-ThanosLdFlags
    $ver = Get-ThanosVersion
    Invoke-GoCommand -Arguments @("build", "-ldflags", $ldflags, "-o", $Output, ".\cmd\thanos")
    Write-Host "Built $Output (version: $ver)" -ForegroundColor Green
}
finally {
    Reset-ThanosLocation
}
