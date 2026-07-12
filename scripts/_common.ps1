Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Get-ThanosRoot {
    return (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
}

function Set-ThanosLocation {
    Push-Location (Get-ThanosRoot)
}

function Reset-ThanosLocation {
    Pop-Location
}

function Ensure-Directory {
    param([Parameter(Mandatory = $true)][string]$Path)

    if (-not (Test-Path $Path)) {
        New-Item -ItemType Directory -Path $Path -Force | Out-Null
    }
}

function Invoke-GoCommand {
    param(
        [Parameter(Mandatory = $true)]
        [string[]]$Arguments
    )

    & go @Arguments
    if ($LASTEXITCODE -ne 0) {
        throw "go command failed with exit code $LASTEXITCODE"
    }
}

# Returns the Thanos version string derived from git tags.
# Uses `git describe --tags --always --dirty` so:
#   - On a tagged commit:  "v1.0.0"
#   - After a tag + commits: "v1.0.0-3-gabc1234"
#   - No tags yet:          short commit hash ("abc1234")
# Falls back to "dev" if git is unavailable or not a repo.
function Get-ThanosVersion {
    $v = git describe --tags --always --dirty 2>$null
    if ($LASTEXITCODE -ne 0 -or -not $v) {
        return "dev"
    }
    return $v.Trim()
}

# Builds the ldflags string that injects the version into the binary.
function Get-ThanosLdFlags {
    $v = Get-ThanosVersion
    return "-X thanos/internal/version.version=$v"
}
