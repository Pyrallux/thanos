. "$PSScriptRoot\_common.ps1"
Set-ThanosLocation
try {
    foreach ($path in @("bin", "thanos.exe", "thanos.db", "thanos.db-wal", "thanos.db-shm")) {
        if (Test-Path $path) {
            Remove-Item $path -Recurse -Force
        }
    }

    Write-Host "Cleaned build artifacts and local database files." -ForegroundColor Green
}
finally {
    Reset-ThanosLocation
}
