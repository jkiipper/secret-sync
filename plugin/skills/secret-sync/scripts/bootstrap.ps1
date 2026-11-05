<#
.SYNOPSIS
  Install the secret-sync binary on Windows from a GitHub release, verified by SHA-256.

.DESCRIPTION
  Downloads secret-sync-windows-<arch>.exe and SHA256SUMS from the release, refuses to
  install if the checksum does not match, copies the binary to
  %LOCALAPPDATA%\Programs\secret-sync and adds that folder to the user PATH.
  Works in Windows PowerShell 5.1 and PowerShell 7.

.EXAMPLE
  powershell -ExecutionPolicy Bypass -File bootstrap.ps1
  powershell -ExecutionPolicy Bypass -File bootstrap.ps1 -Version v0.1.0
#>
param(
    [string]$Version = 'latest',
    [string]$InstallDir = (Join-Path $env:LOCALAPPDATA 'Programs\secret-sync'),
    [string]$Repo = 'jkiipper/secret-sync',
    [string]$SourceDir,          # testing/mirrors: take the files from this folder instead of GitHub
    [switch]$NoPath              # do not touch the user PATH
)
$ErrorActionPreference = 'Stop'
[Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12

$arch = switch ($env:PROCESSOR_ARCHITECTURE) { 'AMD64' { 'amd64' } 'ARM64' { 'arm64' } default { $env:PROCESSOR_ARCHITECTURE } }
$asset = "secret-sync-windows-$arch.exe"
$base = if ($Version -eq 'latest') { "https://github.com/$Repo/releases/latest/download" } else { "https://github.com/$Repo/releases/download/$Version" }

$tmp = Join-Path ([IO.Path]::GetTempPath()) ('secret-sync-' + [guid]::NewGuid().ToString('n'))
New-Item -ItemType Directory -Path $tmp | Out-Null
try {
    foreach ($f in $asset, 'SHA256SUMS') {
        $dest = Join-Path $tmp $f
        if ($SourceDir) {
            $src = Join-Path $SourceDir $f
            if (-not (Test-Path -LiteralPath $src)) { throw "$f not found in $SourceDir" }
            Copy-Item -LiteralPath $src -Destination $dest
        } else {
            try { Invoke-WebRequest -Uri "$base/$f" -OutFile $dest -UseBasicParsing }
            catch { throw "Download failed for $base/$f (no prebuilt binary for windows/$arch? build from source: https://github.com/$Repo)" }
        }
    }

    $expected = $null
    foreach ($line in Get-Content -LiteralPath (Join-Path $tmp 'SHA256SUMS')) {
        $parts = $line.Trim() -split '\s+', 2
        if ($parts.Count -eq 2 -and $parts[1].TrimStart('*') -eq $asset) { $expected = $parts[0] }
    }
    if (-not $expected) { throw "SHA256SUMS has no entry for $asset" }
    $actual = (Get-FileHash -Algorithm SHA256 -LiteralPath (Join-Path $tmp $asset)).Hash
    if ($actual -ne $expected) { throw "Checksum mismatch for $asset; NOT installing. expected $expected, got $actual" }
    Write-Host "Checksum OK ($asset)"

    New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
    $exe = Join-Path $InstallDir 'secret-sync.exe'
    Copy-Item -LiteralPath (Join-Path $tmp $asset) -Destination $exe -Force
    Write-Host "Installed $exe"
    & $exe version

    if (-not $NoPath) {
        $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
        $parts = @($userPath -split ';' | Where-Object { $_ })
        if ($parts -notcontains $InstallDir) {
            [Environment]::SetEnvironmentVariable('Path', (($parts + $InstallDir) -join ';'), 'User')
            Write-Host "Added $InstallDir to your user PATH (new terminals will see it)."
        }
    }
} finally {
    Remove-Item -LiteralPath $tmp -Recurse -Force -ErrorAction SilentlyContinue
}
