<#
.SYNOPSIS
  Audit: list sensitive-looking files per project WITHOUT opening them.

.DESCRIPTION
  Scans project folders under -Root, finds files whose NAME/EXTENSION looks
  sensitive, and reports project, relative path, size, extension, a guessed
  category, and git status (ignored / tracked / untracked / no-git).
  Only file metadata and read-only git commands (check-ignore, ls-files) are used.
  File contents are never read.

.EXAMPLE
  pwsh scripts\audit.ps1 -Root ~/src -Last 20
  pwsh scripts\audit.ps1 -Root ~/src -Projects proj-a,proj-b
  pwsh scripts\audit.ps1                         # all projects next to this repo
  Output goes to out\ (gitignored), because it names your projects and secret files.
#>
[CmdletBinding()]
param(
    [string]$Root = (Split-Path -Parent (Split-Path -Parent $PSScriptRoot)),   # folder that contains this repo
    [int]$Last = 0,                       # 0 = all projects, else N most recently active
    [string[]]$Projects,                  # explicit project folder names (overrides -Last)
    [string[]]$Exclude = @('secret-sync'),  # project folder names, wildcards allowed
    [string]$IgnoreFile = (Join-Path $PSScriptRoot '..\out\audit-ignore.txt'), # your false positives: project\path wildcards (kept out of git)
    [string]$OutCsv = (Join-Path $PSScriptRoot '..\out\audit.csv')
)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

. "$PSScriptRoot\lib\Rules.ps1"
# ---- select projects ----
$all = Get-ChildItem -LiteralPath $Root -Directory -Force | Where-Object {
    $name = $_.Name
    -not $name.StartsWith('.') -and -not ($Exclude | Where-Object { $name -like $_ })
}
$ignorePatterns = @()
if (Test-Path -LiteralPath $IgnoreFile) {
    $ignorePatterns = @(Get-Content -LiteralPath $IgnoreFile | ForEach-Object { $_.Trim() } | Where-Object { $_ -and -not $_.StartsWith('#') })
}
$script:ignoredByList = 0
if ($Projects) {
    $selected = $all | Where-Object { $Projects -contains $_.Name }
} else {
    $withAct = $all | ForEach-Object { [pscustomobject]@{ Dir = $_; Activity = Get-ProjectActivity $_ } } | Sort-Object Activity -Descending
    if ($Last -gt 0) { $withAct = $withAct | Select-Object -First $Last }
    $selected = $withAct | ForEach-Object { $_.Dir }
    Write-Host "Selected $(@($withAct).Count) project(s) by last activity (git commit date, else folder date):"
    $withAct | ForEach-Object { Write-Host ("  {0:yyyy-MM-dd}  {1}" -f $_.Activity, $_.Dir.Name) }
}

# ---- scan ----
$rows = [System.Collections.Generic.List[object]]::new()
$script:trackedConfigs = 0
foreach ($proj in $selected) {
    $cands = @(Find-Candidates $proj.FullName)
    if ($cands.Count -eq 0) { continue }
    $rels = @($cands | ForEach-Object { [IO.Path]::GetRelativePath($proj.FullName, $_.Full) })
    $status = Get-GitStatusMap $proj.FullName $rels
    for ($k = 0; $k -lt $cands.Count; $k++) {
        $fi = [IO.FileInfo]::new($cands[$k].Full)
        $cat = Get-Category $fi.Name $rels[$k] $cands[$k].InSecretDir
        $st = $status[$rels[$k]]
        # Committed configs (appsettings.json, config.yaml...) are normal; only count them.
        if ($cat -eq 'templatable-config' -and $st -eq 'TRACKED') { $script:trackedConfigs++; continue }
        $key = "$($proj.Name)\$($rels[$k])"
        if ($ignorePatterns | Where-Object { $key -like $_ }) { $script:ignoredByList++; continue }
        $rows.Add([pscustomobject]@{
            Project   = $proj.Name
            Path      = $rels[$k]
            SizeBytes = $fi.Length
            Extension = $fi.Extension
            Category  = $cat
            GitStatus = $st
        })
    }
}

$outDir = Split-Path -Parent $OutCsv
if (-not (Test-Path $outDir)) { New-Item -ItemType Directory -Force -Path $outDir | Out-Null }
$rows | Export-Csv -LiteralPath $OutCsv -NoTypeInformation -Encoding utf8
$OutCsv = (Resolve-Path $OutCsv).Path

# ---- summary ----
Write-Host "`nWrote $($rows.Count) row(s) to $OutCsv"
Write-Host "Projects scanned: $(@($selected).Count); projects with findings: $(@($rows | Select-Object -ExpandProperty Project -Unique).Count)`n"
Write-Host 'Per category (files / projects):'
$rows | Group-Object Category | Sort-Object Name | ForEach-Object {
    '  {0,-22} {1,5} files  {2,4} projects' -f $_.Name, $_.Count, @($_.Group | Select-Object -ExpandProperty Project -Unique).Count
} | Write-Host
Write-Host "  ($script:ignoredByList file(s) skipped via $(Split-Path -Leaf $IgnoreFile))"
Write-Host "  (plus $script:trackedConfigs committed config file(s) such as appsettings.json, not listed; they may embed secrets, review separately)"
Write-Host "`nPer git status:"
$rows | Group-Object GitStatus | Sort-Object Name | ForEach-Object { '  {0,-10} {1,5}' -f $_.Name, $_.Count } | Write-Host

$tracked = @($rows | Where-Object { $_.GitStatus -eq 'TRACKED' -and $_.Category -ne 'example' })
if ($tracked.Count) {
    Write-Host "`n!! Sensitive-looking files TRACKED by git (possibly committed secrets):" -ForegroundColor Yellow
    $tracked | ForEach-Object { Write-Host "  $($_.Project)\$($_.Path)  [$($_.Category)]" }
}
$untracked = @($rows | Where-Object { $_.GitStatus -eq 'untracked' -and $_.Category -ne 'example' })
if ($untracked.Count) {
    Write-Host "`n!  Sensitive-looking files NOT ignored and not tracked (one 'git add .' away from a commit):" -ForegroundColor Yellow
    $untracked | ForEach-Object { Write-Host "  $($_.Project)\$($_.Path)  [$($_.Category)]" }
}
$whole = @($rows | Where-Object { $_.Category -in 'whole-file','ssh-key','secrets-folder-other' })
Write-Host "`nWhole-file group (decides whether the hybrid store is needed): $($whole.Count) file(s), $([math]::Round((($whole | Measure-Object SizeBytes -Sum).Sum)/1KB,1)) KB"
$whole | Sort-Object Project, Path | ForEach-Object { Write-Host ('  {0}\{1}  {2} bytes  [{3}, {4}]' -f $_.Project, $_.Path, $_.SizeBytes, $_.Category, $_.GitStatus) }
