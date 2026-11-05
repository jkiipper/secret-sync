<#
.SYNOPSIS
  End-to-end test of secret-sync with FAKE data, simulating two machines (A and B)
  on one PC: separate project roots, state dirs and configs, one shared test store.
  -Exe:          the binary to test (default bin\secret-sync.exe).
  -StoreRoot:    a DEDICATED, empty test folder (or set SECRET_SYNC_TEST_STORE). Never your real store:
                 the test resets it. It is marked with .secret-sync-test-store on first use.
  -IdentityFile: an age key (default ~/.config/secret-sync/age-key.txt). Only fake data is encrypted.
#>
param(
    [string]$Exe = (Join-Path $PSScriptRoot '..\bin\secret-sync.exe'),
    [string]$StoreRoot = $env:SECRET_SYNC_TEST_STORE,
    [string]$IdentityFile = ([IO.Path]::Combine($HOME, '.config', 'secret-sync', 'age-key.txt'))
)
$ErrorActionPreference = 'Stop'
if (-not $StoreRoot) { throw 'Set -StoreRoot or SECRET_SYNC_TEST_STORE to a dedicated, empty test folder.' }
New-Item -ItemType Directory -Force $StoreRoot | Out-Null
$marker = Join-Path $StoreRoot '.secret-sync-test-store'
if (-not (Test-Path $marker)) {
    if (Get-ChildItem -Force $StoreRoot) { throw "$StoreRoot is not empty and not a test store. Use a dedicated empty folder." }
    Set-Content $marker 'Test store for tests/Test-SecretSync.ps1. Safe to delete.'
}
# The store itself lives in a subfolder that every run deletes and recreates, so each run
# starts from a truly fresh store (exercising init -new-store) whatever earlier runs left.
$testRoot = $StoreRoot
$StoreRoot = Join-Path $testRoot 'store'
$ss = (Resolve-Path $Exe).Path
$work = Join-Path $PSScriptRoot 'work'
$aRoot = Join-Path $PSScriptRoot 'dummy'
$bRoot = Join-Path $work 'b-projects'
$proj = 'demo-project'
$script:AllOutput = [System.Collections.Generic.List[string]]::new()
$script:Fails = 0

function SS([string]$machine, [string[]]$a) {
    $cfg = Join-Path $work "$machine-config.json"
    $out = if ($ss -like '*.ps1') { & pwsh -NoProfile -File $ss @a -Config $cfg 2>&1 } else { & $ss @a -Config $cfg 2>&1 }
    $out = @($out | ForEach-Object { "$_" })
    $code = $LASTEXITCODE
    $script:AllOutput.AddRange([string[]]@($out))
    [pscustomobject]@{ Code = $code; Out = ($out -join "`n") }
}
function Check([string]$name, [bool]$ok, [string]$detail = '') {
    if ($ok) { Write-Host "  PASS  $name" -ForegroundColor Green } else { $script:Fails++; Write-Host "  FAIL  $name $detail" -ForegroundColor Red }
}
function Count-Status($r, [string]$status) { ([regex]::Matches($r.Out, "(?m)^\S+\s+\S.*?\s$([regex]::Escape($status))(\s|$)")).Count }
function Same([string]$rel) {
    $a = Join-Path $aRoot "$proj\$rel"; $b = Join-Path $bRoot "$proj\$rel"
    (Test-Path $a) -and (Test-Path $b) -and ((Get-FileHash $a).Hash -eq (Get-FileHash $b).Hash)
}

# ---------------------------------------------------------------- reset
Write-Host "== reset"
if (Test-Path $work) { Remove-Item -Recurse -Force $work }
if (Test-Path $StoreRoot) { Remove-Item -Recurse -Force $StoreRoot }
& pwsh -NoProfile -File (Join-Path $PSScriptRoot 'New-DummyProject.ps1') | Out-Null
New-Item -ItemType Directory -Force (Join-Path $bRoot $proj) | Out-Null
Copy-Item (Join-Path $aRoot "$proj\.gitignore") (Join-Path $bRoot "$proj\.gitignore")   # simulates a fresh clone
git -C (Join-Path $bRoot $proj) init -q 2>$null

# Noise that must never be offered: a public CA bundle, Claude Code's own settings,
# and a secret-looking config already committed to git (hidden unless -show-tracked).
$aProj = Join-Path $aRoot $proj
New-Item -ItemType Directory -Force (Join-Path $aProj 'lib\certifi'), (Join-Path $aProj '.claude') | Out-Null
Set-Content (Join-Path $aProj 'lib\certifi\cacert.pem') 'public CA bundle (fake)'
Set-Content (Join-Path $aProj '.claude\settings.local.json') '{}'
Set-Content (Join-Path $aProj 'appsettings.json') '{ "Logging": {} }'
git -C $aProj add appsettings.json
git -C $aProj -c user.name=test -c user.email=test@example.invalid commit -q -m 'tracked config' 2>$null

$secretFiles = @('.env', 'local.env', '.secrets\resend_api_key', '.secrets\pfx_password', '.secrets\fake_key.pem', '.secrets\fake_cert.pfx')
# Fake values that must never appear in any output.
$fakeValues = @(Select-String -Path (Join-Path $aRoot "$proj\.env"), (Join-Path $aRoot "$proj\local.env") -Pattern 'fake_[A-Za-z0-9]{6,}' -AllMatches |
        ForEach-Object { $_.Matches.Value }) + @(Get-Content -Raw (Join-Path $aRoot "$proj\.secrets\resend_api_key"))

# ---------------------------------------------------------------- init
Write-Host "== init"
$r = SS 'a' @('init', '-Machine', 'machine-A', '-ProjectsRoot', $aRoot, '-StoreRoot', $StoreRoot, '-IdentityFile', $IdentityFile, '-StateDir', (Join-Path $work 'state-a'), '-new-store')
Check 'init A' ($r.Code -eq 0) $r.Out
$r = SS 'b' @('init', '-Machine', 'machine-B', '-ProjectsRoot', $bRoot, '-StoreRoot', $StoreRoot, '-IdentityFile', $IdentityFile, '-StateDir', (Join-Path $work 'state-b'))
Check 'init B (same key accepted)' ($r.Code -eq 0) $r.Out

# ---------------------------------------------------------------- opt-in manifest
Write-Host "== no manifest yet: candidates only"
$r = SS 'a' @('status', $proj)
Check 'A status: 6 candidates, nothing managed' ((Count-Status $r 'candidate') -eq 6 -and (Count-Status $r 'new') -eq 0) $r.Out
Check 'reports the missing manifest' ($r.Out -match 'No manifest yet') $r.Out
Check 'public cacert.pem not offered' ($r.Out -notmatch 'cacert') $r.Out
Check '.claude settings not offered' ($r.Out -notmatch '\.claude') $r.Out
Check 'git-tracked appsettings.json hidden and counted' ($r.Out -notmatch 'appsettings' -and $r.Out -match '1 candidate file\(s\) already tracked') $r.Out
$r = SS 'a' @('status', $proj, '-show-tracked')
Check '-show-tracked lists it' ($r.Out -match 'appsettings\.json.*NOT GITIGNORED \(TRACKED\)') $r.Out
$r = SS 'a' @('push', $proj)
Check 'push without manifest uploads nothing' ($r.Out -notmatch 'pushed' -and -not (Test-Path (Join-Path $StoreRoot $proj))) $r.Out
$r = SS 'a' @('include', "$proj/.env", "$proj/local.env", "$proj/.secrets/*")
Check 'include creates the manifest' ($r.Code -eq 0 -and (Test-Path (Join-Path $StoreRoot "$proj\secret-sync.json"))) $r.Out
$r = SS 'a' @('include', "$proj/.env")
Check 'include is idempotent' ($r.Out -match 'already listed') $r.Out
$r = SS 'a' @('include', 'no-such-project/.env')
Check 'include rejects an unknown project' ($r.Code -eq 1) $r.Out

# ---------------------------------------------------------------- first push
Write-Host "== first push from A"
$r = SS 'a' @('status', $proj)
Check 'A status: 6 new files' ((Count-Status $r 'new') -eq 6) $r.Out
$r = SS 'a' @('push', $proj, '-DryRun')
Check 'dry-run writes nothing' (@(Get-ChildItem (Join-Path $StoreRoot $proj) -Recurse -Filter *.age).Count -eq 0) $r.Out
$r = SS 'a' @('push', $proj)
Check 'A push: 6 pushed' (([regex]::Matches($r.Out, '(?m)\spushed\s*$')).Count -eq 6) $r.Out
Check 'summary counts what was done' ($r.Out.Contains('before: new=6 | done: pushed=6')) $r.Out
Check 'store holds 6 .age files' (@(Get-ChildItem (Join-Path $StoreRoot $proj) -Recurse -Filter *.age).Count -eq 6)
$r = SS 'a' @('push', $proj)
Check 'second push is a no-op (6 in-sync)' ((Count-Status $r 'in-sync') -eq 6 -and $r.Out -notmatch 'pushed') $r.Out

# ---------------------------------------------------------------- pull on B
Write-Host "== pull on B"
$r = SS 'b' @('status')
Check 'B status: 6 remote-new' ((Count-Status $r 'remote-new') -eq 6) $r.Out
Check 'B is told this is the FIRST SYNC of the project' ($r.Out -match 'FIRST SYNC of demo-project on this machine: 0 identical, 0 differ, 6 only in the store, 0 only here') $r.Out
$r = SS 'b' @('pull', $proj)
Check 'B pull exit 0' ($r.Code -eq 0) $r.Out
$r = SS 'b' @('status', $proj)
Check 'no FIRST SYNC notice once synced' ($r.Out -notmatch 'FIRST SYNC') $r.Out
foreach ($f in $secretFiles) { Check "identical after pull: $f" (Same $f) }

# ---------------------------------------------------------------- change on A
Write-Host "== change on A, propagate to B"
Add-Content (Join-Path $aRoot "$proj\.env") 'ADDED_LATER=fake_ADDEDLATER123'
$fakeValues += 'fake_ADDEDLATER123'
$r = SS 'a' @('status', $proj)
Check 'A: .env local-changed' ((Count-Status $r 'local-changed') -eq 1) $r.Out
$r = SS 'a' @('push', $proj); Check 'A push changed .env' ($r.Out -match 'pushed') $r.Out
$r = SS 'b' @('status', $proj)
Check 'B: .env remote-changed' ((Count-Status $r 'remote-changed') -eq 1) $r.Out
$r = SS 'b' @('pull', "$proj\.env"); Check 'B pull single file' ($r.Code -eq 0) $r.Out
Check 'identical after change: .env' (Same '.env')

# ---------------------------------------------------------------- conflict
Write-Host "== conflict: both machines edit local.env"
Add-Content (Join-Path $aRoot "$proj\local.env") 'A_EDIT=fake_AEDIT9876'
Add-Content (Join-Path $bRoot "$proj\local.env") 'B_EDIT=fake_BEDIT5432'
$fakeValues += 'fake_AEDIT9876', 'fake_BEDIT5432'
$r = SS 'a' @('push', $proj); Check 'A pushes first' ($r.Out -match 'pushed') $r.Out
$r = SS 'b' @('status', $proj)
Check 'B sees conflict (exit 2)' ((Count-Status $r 'conflict') -eq 1 -and $r.Code -eq 2) $r.Out
$r = SS 'b' @('push', $proj)
Check 'B push refuses to overwrite' ($r.Out -match 'SKIPPED' -and $r.Code -eq 2) $r.Out
$r = SS 'b' @('pull', $proj)
Check 'B pull refuses to overwrite' ($r.Out -match 'SKIPPED' -and $r.Code -eq 2) $r.Out
$r = SS 'b' @('pull', "$proj\local.env", '-Force')
Check 'B forced pull backs up local copy' ($r.Out -match 'backed up') $r.Out
Check '...and says where' ($r.Out -match 'backed up to .+backup.demo-project.local\.env\.\d{8}-\d{6}\)') $r.Out
Check 'a resolved conflict is reported as done' ($r.Out.Contains('before: conflict=1 | done: pulled=1')) $r.Out
Check 'backup file exists' (@(Get-ChildItem (Join-Path $work 'state-b\backup') -Recurse -File).Count -eq 1)
Check 'identical after resolve: local.env' (Same 'local.env')

# ---------------------------------------------------------------- guards
Write-Host "== guards"
Set-Content (Join-Path $aRoot "$proj\.env.extra") 'EXTRA=fake_EXTRA55555'
$fakeValues += 'fake_EXTRA55555'
$r = SS 'a' @('status', $proj)
Check 'new sensitive file shows up as candidate' ($r.Out -match '\.env\.extra\s+candidate') $r.Out
$r = SS 'a' @('push', $proj)
Check 'push leaves candidates alone' ($r.Out -notmatch 'pushed' -and $r.Code -eq 0) $r.Out
$r = SS 'a' @('include', "$proj/.env.extra"); $r = SS 'a' @('push', $proj)
Check 'push refuses an included file that is not gitignored' ($r.Out -match 'REFUSED' -and $r.Code -eq 2) $r.Out
Check '...and did not upload it' (-not (Test-Path (Join-Path $StoreRoot "$proj\.env.extra.age")))
$r = SS 'a' @('exclude', "$proj/.env.extra"); Check 'exclude command' ($r.Code -eq 0) $r.Out
$r = SS 'a' @('status', $proj)
Check 'excluded file hidden from status' ($r.Out -notmatch '\.env\.extra' -and $r.Code -eq 0) $r.Out

Set-Content (Join-Path $aRoot "$proj\service-account.json") '{"private_key":"fake_SAKEY77777"}'
$fakeValues += 'fake_SAKEY77777'
$r = SS 'a' @('push', "$proj/service-account.json")
Check 'explicit push of a candidate is skipped with a hint' ($r.Out -match 'not in the manifest' -and $r.Code -eq 2) $r.Out

Write-Host "== wildcard include covers new files"
Set-Content (Join-Path $aRoot "$proj\.secrets\new_token") 'fake_NEWTOKEN4444' -NoNewline
$fakeValues += 'fake_NEWTOKEN4444'
$r = SS 'a' @('status', $proj)
Check '.secrets/new_token is managed (new)' ($r.Out -match 'new_token\s+new') $r.Out
$r = SS 'a' @('push', $proj); Check 'pushed' ($r.Out -match 'pushed') $r.Out
$r = SS 'b' @('pull', $proj); Check 'B receives it' (Same '.secrets\new_token') $r.Out

# ---------------------------------------------------------------- recovery cases
Write-Host "== recovery"
Remove-Item (Join-Path $bRoot "$proj\.secrets\pfx_password")
$r = SS 'b' @('status', $proj); Check 'missing-locally detected' ((Count-Status $r 'missing-locally') -eq 1) $r.Out
$r = SS 'b' @('pull', $proj); Check 'pull restores missing file' (Same '.secrets\pfx_password') $r.Out
Remove-Item (Join-Path $work 'state-b\demo-project.json')
$r = SS 'b' @('status', $proj); Check 'lost state: identical files show in-sync*' ((Count-Status $r 'in-sync*') -eq 7) $r.Out
Check '...reported as a first sync with 7 identical' ($r.Out -match 'FIRST SYNC of demo-project on this machine: 7 identical, 0 differ') $r.Out
$r = SS 'b' @('pull', $proj); Check 'pull re-links without rewriting' ($r.Out -match 'linked' -and $r.Out -notmatch 'pulled') $r.Out

# ---------------------------------------------------------------- doctor / keygen
Write-Host "== doctor and keygen (fresh machine)"
$fresh = Join-Path $work 'fresh'
$freshCfg = Join-Path $fresh 'config.json'; $freshKey = Join-Path $fresh 'age-key.txt'; $freshStore = Join-Path $fresh 'store\dev-secrets'
function SSRaw([string[]]$a) {
    $out = @(& $ss @a 2>&1 | ForEach-Object { "$_" }); $code = $LASTEXITCODE
    $script:AllOutput.AddRange([string[]]$out)
    [pscustomobject]@{ Code = $code; Out = ($out -join "`n") }
}
$r = SSRaw @('doctor', '-config', $freshCfg, '-identity', $freshKey, '-store-root', $freshStore)
Check 'fresh machine: not ready (exit 2)' ($r.Code -eq 2) $r.Out
Check 'suggests keygen for a brand-new store' ($r.Out -match 'secret-sync keygen') $r.Out
Check 'a new store is the user''s decision (init -new-store, fix for you)' ($r.Out -match 'fix \(you\):\s+first machine only: secret-sync init .*-new-store') $r.Out
$r = SSRaw @('keygen', '-identity', $freshKey, '-store-root', $freshStore, '-config', $freshCfg)
Check 'keygen creates a key and prints the public key' ($r.Code -eq 0 -and (Test-Path $freshKey) -and $r.Out -match 'Public key: age1') $r.Out
$r = SSRaw @('keygen', '-identity', $freshKey, '-store-root', $freshStore, '-config', $freshCfg)
Check 'keygen refuses to overwrite' ($r.Code -eq 1 -and $r.Out -match 'refusing') $r.Out

Write-Host "== store guard: never create a second store by accident"
$guardKey = Join-Path $work 'guard-key.txt'
$r = SSRaw @('keygen', '-identity', $guardKey, '-store-root', $StoreRoot)
Check 'keygen refuses when a store already exists' ($r.Code -eq 1 -and $r.Out -match 'already exists' -and -not (Test-Path $guardKey)) $r.Out
$r = SSRaw @('keygen', '-identity', $guardKey, '-store-root', $StoreRoot, '-force')
Check '...unless -force' ($r.Code -eq 0 -and (Test-Path $guardKey)) $r.Out
$noStore = Join-Path $work 'not-synced-yet\dev-secrets'
$r = SSRaw @('init', '-config', (Join-Path $work 'guard-config.json'), '-machine', 'm', '-projects-root', $aRoot, '-store-root', $noStore, '-identity', $IdentityFile)
Check 'init without -new-store refuses to create a store' ($r.Code -eq 1 -and $r.Out -match '-new-store' -and -not (Test-Path $noStore)) $r.Out
Check '...and writes no config' (-not (Test-Path (Join-Path $work 'guard-config.json'))) $r.Out
$halfSynced = Join-Path $work 'half-synced\dev-secrets'
New-Item -ItemType Directory -Force (Join-Path $halfSynced 'some-project') | Out-Null
$r = SSRaw @('init', '-config', (Join-Path $work 'guard-config.json'), '-machine', 'm', '-projects-root', $aRoot, '-store-root', $halfSynced, '-identity', $IdentityFile, '-new-store')
Check 'init -new-store refuses a non-empty folder without recipients.txt' ($r.Code -eq 1 -and $r.Out -match 'not empty' -and -not (Test-Path (Join-Path $halfSynced 'recipients.txt'))) $r.Out
$r = SSRaw @('init', '-config', (Join-Path $work 'guard-config.json'), '-machine', 'm', '-projects-root', $aRoot, '-store-root', $noStore, '-identity', $IdentityFile, '-new-store')
Check 'init -new-store creates a store in an empty place' ($r.Code -eq 0 -and (Test-Path (Join-Path $noStore 'recipients.txt'))) $r.Out
$r = SSRaw @('doctor', '-config', $freshCfg, '-identity', $freshKey, '-store-root', $StoreRoot)
Check 'doctor rejects a key that does not match the store' ($r.Code -eq 2 -and $r.Out -match "not the store's key") $r.Out
$r = SSRaw @('doctor', '-config', (Join-Path $work 'a-config.json'))
Check 'configured machine: ready' ($r.Code -eq 0 -and $r.Out -match 'Ready\.') $r.Out
Check 'offline access is a manual item until confirmed' ($r.Out -match '\[MANUAL\]\s+offline access' -and $r.Out -match 'doctor -offline-confirmed') $r.Out
$r = SSRaw @('doctor', '-config', (Join-Path $work 'a-config.json'), '-offline-confirmed')
Check 'doctor -offline-confirmed records it' ($r.Code -eq 0 -and $r.Out -match 'Recorded:' -and $r.Out -match '\[OK\s*\]\s+offline access\s+confirmed by you on \d{4}-\d{2}-\d{2}') $r.Out
$r = SS 'a' @('init', '-Machine', 'machine-A', '-ProjectsRoot', $aRoot, '-StoreRoot', $StoreRoot, '-IdentityFile', $IdentityFile, '-StateDir', (Join-Path $work 'state-a'))
$r = SSRaw @('doctor', '-config', (Join-Path $work 'a-config.json'))
Check '...remembered, also after re-running init on the same store' ($r.Out -match '\[OK\s*\]\s+offline access' -and $r.Out -notmatch '\[MANUAL\]') $r.Out
$r = SSRaw @('doctor', '-config', $freshCfg, '-offline-confirmed')
Check '-offline-confirmed needs a configured machine' ($r.Code -eq 1 -and $r.Out -match 'run init first') $r.Out
$r = SSRaw @('doctor', '-config', (Join-Path $work 'a-config.json'), '-json')
$j = $null; try { $j = $r.Out | ConvertFrom-Json } catch {}
Check 'doctor -json is valid JSON with ready=true' ($j -and $j.ready -eq $true -and $j.checks.Count -ge 4) $r.Out
$r = SSRaw @('version'); Check 'version command' ($r.Out -match '^secret-sync ') $r.Out

# ---------------------------------------------------------------- leak check
Write-Host "== leak check"
$joined = $script:AllOutput -join "`n"
$leaks = @($fakeValues | Where-Object { $_ -and $joined.Contains($_.Trim()) })
Check "no fake value in any output ($($fakeValues.Count) values checked)" ($leaks.Count -eq 0) "leaked: $($leaks.Count)"
Check 'no PEM content in output' ($joined -notmatch 'BEGIN RSA PRIVATE KEY')
Check 'no age private key in output' ($joined -notmatch 'AGE-SECRET-KEY-')

Write-Host ''
if ($script:Fails) { Write-Host "$script:Fails check(s) FAILED" -ForegroundColor Red; exit 1 } else { Write-Host 'ALL CHECKS PASSED' -ForegroundColor Green }
