param(
    [switch]$BuildFirst,
    [string]$Binary = "bin\thanos.exe"
)

. "$PSScriptRoot\_common.ps1"

Set-ThanosLocation
try {
    if ($BuildFirst -or -not (Test-Path $Binary)) {
        Ensure-Directory -Path (Split-Path $Binary -Parent)
        $ldflags = Get-ThanosLdFlags
        Invoke-GoCommand -Arguments @("build", "-ldflags", $ldflags, "-o", $Binary, ".\cmd\thanos")
    }

    & $Binary
    if ($LASTEXITCODE -ne 0) {
        throw "Thanos exited with code $LASTEXITCODE"
    }
}
finally {
    Reset-ThanosLocation
}
