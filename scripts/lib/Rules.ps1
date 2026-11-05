# Shared detection rules: which files count as sensitive, based on NAME and PATH only.
# Used by audit.ps1 (the Go tool has its own copy in rules.go). Never opens files.
# Dot-source: . "$PSScriptRoot\lib\Rules.ps1"

# Folders never descended into (dependencies, build output, VCS, IDE).
$PruneDirs = @('node_modules','.git','bin','obj','dist','build','out','.venv','venv','env',
  '__pycache__','vendor','.idea','.vs','.vscode','.next','.nuxt','target','packages',
  '.gradle','coverage','.cache','.terraform','.angular','.svelte-kit','Pods','DerivedData')

# Folder names whose files are all considered secrets.
$SecretDirs = @('.secrets','secrets','.secret','secret','credentials','.credentials','certs','.certs','keys','.keys')

function Get-Category([string]$name, [string]$relPath, [bool]$inSecretDir) {
    $n = $name.ToLowerInvariant()
    $ext = [IO.Path]::GetExtension($n)

    # Example/template env files usually hold no secrets.
    if ($n -match '^\.env\.(example|sample|template|dist|defaults)$' -or $n -match '\.(example|sample|template)$') { return 'example' }
    # SSH private keys (public halves are skipped earlier).
    if ($n -match '^id_(rsa|dsa|ecdsa|ed25519)(_sk)?$' -or $ext -eq '.ppk') { return 'ssh-key' }
    # Whole-file secrets.
    if ($ext -in '.pem','.key','.p12','.pfx','.jks','.keystore','.der','.crt','.cer','.p8','.gpg','.asc','.kdbx','.mobileprovision') { return 'whole-file' }
    if ($n -match '(service[-_]?account|credentials?|client[-_]?secret|firebase[-_]?adminsdk|-key)\S*\.json$') { return 'whole-file' }
    if ($n -in 'google-services.json','googleservice-info.plist') { return 'whole-file' }
    # Key-value env files.
    if ($n -eq '.env' -or $n -like '.env.*' -or $ext -eq '.env' -or $n -eq '.envrc' -or $n -eq '.dev.vars') { return 'env-kv' }
    # Files inside a secrets folder: plain-text one-value files by default.
    if ($inSecretDir) {
        if ($ext -in '','.txt','.secret','.token','.pwd','.pass') { return 'secrets-folder-kv' }
        return 'secrets-folder-other'
    }
    # Templatable configs with embedded secrets.
    if ($n -match '^appsettings(\..+)?\.json$' -or $n -in 'local.settings.json','secrets.json','usersecrets.json',
        'config.yaml','config.yml','secrets.yaml','secrets.yml','secrets.toml','.npmrc','.pypirc','.netrc','.htpasswd',
        'wp-config.php','settings.local.json','application-local.properties','application-local.yml',
        'docker-compose.override.yml','terraform.tfvars','.dockercfg','config.json' ) { return 'templatable-config' }
    if ($ext -eq '.tfvars' -or $n -match '^application-.*\.(properties|ya?ml)$') { return 'templatable-config' }
    return $null
}

function Test-Candidate([string]$name, [bool]$inSecretDir) {
    $n = $name.ToLowerInvariant()
    if ($n.EndsWith('.pub')) { return $false }
    if ($inSecretDir) { return $true }
    return [bool](Get-Category $name '' $false)
}

function Get-ProjectActivity([IO.DirectoryInfo]$dir) {
    if (Test-Path (Join-Path $dir.FullName '.git')) {
        $ts = git -C $dir.FullName log -1 --format=%ct 2>$null
        if ($LASTEXITCODE -eq 0 -and $ts) { return [DateTimeOffset]::FromUnixTimeSeconds([long]$ts).LocalDateTime }
    }
    return $dir.LastWriteTime
}

function Find-Candidates([string]$projectPath) {
    $results = [System.Collections.Generic.List[object]]::new()
    $stack = [System.Collections.Generic.Stack[object]]::new()
    $stack.Push(@($projectPath, $false))
    while ($stack.Count -gt 0) {
        $item = $stack.Pop(); $dirPath = $item[0]; $inSecret = $item[1]
        try { $entries = [IO.Directory]::EnumerateFileSystemEntries($dirPath) } catch { continue }
        foreach ($e in $entries) {
            $name = [IO.Path]::GetFileName($e)
            $attr = [IO.File]::GetAttributes($e)
            if ($attr -band [IO.FileAttributes]::Directory) {
                if ($attr -band [IO.FileAttributes]::ReparsePoint) { continue }
                if ($PruneDirs -contains $name) { continue }
                $stack.Push(@($e, ($inSecret -or ($SecretDirs -contains $name.ToLowerInvariant()))))
            } elseif (Test-Candidate $name $inSecret) {
                $results.Add([pscustomobject]@{ Full = $e; InSecretDir = $inSecret })
            }
        }
    }
    return $results
}

function Get-GitStatusMap([string]$projectPath, [string[]]$relPaths) {
    $map = @{}
    if (-not (Test-Path (Join-Path $projectPath '.git'))) { foreach ($p in $relPaths) { $map[$p] = 'no-git' }; return $map }
    $unix = $relPaths | ForEach-Object { $_ -replace '\\','/' }
    $tracked = @{}
    $ls = git -C $projectPath ls-files --cached -- @($unix) 2>$null
    foreach ($t in @($ls)) { if ($t) { $tracked[$t] = $true } }
    $ignored = @{}
    # Paths as arguments: piping via --stdin from PowerShell adds CR and never matches.
    $ci = git -C $projectPath check-ignore --no-index -- @($unix) 2>$null
    foreach ($i in @($ci)) { if ($i) { $ignored[$i.Trim('"')] = $true } }
    for ($k = 0; $k -lt $relPaths.Count; $k++) {
        $u = $unix[$k]
        $map[$relPaths[$k]] = if ($tracked.ContainsKey($u)) { 'TRACKED' } elseif ($ignored.ContainsKey($u)) { 'ignored' } else { 'untracked' }
    }
    return $map
}

# Load "<project>\<relpath>" wildcard patterns from an ignore file (comments with #).
function Get-IgnorePatterns([string]$path) {
    if (-not $path -or -not (Test-Path -LiteralPath $path)) { return @() }
    return @(Get-Content -LiteralPath $path | ForEach-Object { $_.Trim() } | Where-Object { $_ -and -not $_.StartsWith('#') })
}

function Test-IgnoredPath([string]$project, [string]$relPath, [string[]]$patterns) {
    $key = "$project\$($relPath -replace '/','\')"
    foreach ($p in $patterns) { if ($key -like $p) { return $true } }
    return $false
}
