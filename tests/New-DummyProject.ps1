<#
.SYNOPSIS
  Creates a throwaway project with FAKE secrets for testing the migration scripts.
  All values are random or obviously fake. Safe to print.
#>
param([string]$Path = (Join-Path $PSScriptRoot 'dummy\demo-project'))
$ErrorActionPreference = 'Stop'

if (Test-Path $Path) { Remove-Item -Recurse -Force $Path }
New-Item -ItemType Directory -Force -Path (Join-Path $Path '.secrets') | Out-Null
function Rand([int]$n = 24) { -join ((48..57) + (65..90) + (97..122) | Get-Random -Count $n | ForEach-Object { [char]$_ }) }

# .env exercising parser edge cases
@"
# Fake env file for testing. Nothing here is real.
APP_NAME=demo-project
export API_KEY=fake_$(Rand)
DATABASE_URL="postgres://fakeuser:fake_$(Rand 12)@localhost:5432/demo?sslmode=disable"
SINGLE_QUOTED='fake value with spaces and a # hash'
DOUBLE_QUOTED="fake value with \"escaped\" quotes"
UNQUOTED_WITH_HASH=fake_$(Rand 8)#notacomment
INLINE_COMMENT=fake_$(Rand 8) # this is a comment
EQUALS_IN_VALUE=fake=a=b=c
EMPTY_VALUE=
  SPACED_KEY = fake_$(Rand 8)
lower_case_key=fake_$(Rand 8)
DUPLICATE_IN_LOCAL=from-dot-env

MULTILINE="line1
line2"
"@ | Set-Content -LiteralPath (Join-Path $Path '.env') -Encoding utf8NoBOM

# local.env overrides one key and adds one
@"
DUPLICATE_IN_LOCAL=from-local-env
LOCAL_ONLY=fake_$(Rand)
"@ | Set-Content -LiteralPath (Join-Path $Path 'local.env') -Encoding utf8NoBOM

# .secrets folder: one value per file (with and without trailing newline)
Set-Content -LiteralPath (Join-Path $Path '.secrets\resend_api_key') -Value "re_fake_$(Rand)" -NoNewline -Encoding utf8NoBOM
Set-Content -LiteralPath (Join-Path $Path '.secrets\pfx_password') -Value "fake$(Rand 4)" -Encoding utf8NoBOM

# Throwaway RSA key (generated now, never used anywhere)
$rsa = [System.Security.Cryptography.RSA]::Create(2048)
Set-Content -LiteralPath (Join-Path $Path '.secrets\fake_key.pem') -Value $rsa.ExportRSAPrivateKeyPem() -Encoding ascii

# Random binary standing in for a .pfx (~9.5 KB)
$bytes = [byte[]]::new(9600); [System.Security.Cryptography.RandomNumberGenerator]::Fill($bytes)
[IO.File]::WriteAllBytes((Join-Path $Path '.secrets\fake_cert.pfx'), $bytes)

# .gitignore as a real project would have
".env`nlocal.env`n.env.pass`n.secrets/`n" | Set-Content -LiteralPath (Join-Path $Path '.gitignore') -Encoding utf8NoBOM
git -C $Path init -q 2>$null   # throwaway repo so .gitignore checks are exercised

Write-Host "Dummy project created at $Path"
Get-ChildItem -Recurse -Force $Path | Select-Object @{n='Path';e={$_.FullName.Substring($Path.Length+1)}}, Length | Format-Table -AutoSize | Out-String | Write-Host
