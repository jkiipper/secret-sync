# secret-sync

Keep the **gitignored secret files** of your projects (`.env`, `.env.local`, `.secrets/*`,
certificates, service-account keys...) in sync between your machines, **without putting them
in your repositories**.

Files are encrypted with [age](https://age-encryption.org) and stored in a folder that any
file-sync service keeps in sync (Google Drive, Dropbox, OneDrive, Syncthing...). The sync
service only ever sees ciphertext. Each project gets its own folder, mirroring paths relative
to the project root, so a fresh clone on another machine gets exactly its own files back.

It is designed to be driven by an AI coding agent (a Claude Code skill), but works fine by hand.

## How it works

```
<store>/                              (e.g. a Google Drive folder, available offline)
  recipients.txt                      age public key(s)
  <project>/
    secret-sync.json                  manifest: which files of this project are synced
    .env.age                          encrypted copy of <project>/.env
    .env.age.meta.json                which machine pushed it, and when
    .secrets/api_key.age
```

**Projects opt in.** A project is synced only once it has a manifest, created by the first
`include`. The manifest lists patterns relative to the project root:

```json
{ "include": [".env", ".secrets/*"], "exclude": [".secrets/old_cert_2019.pfx"] }
```

Sensitive-looking files that neither list covers are reported as `candidate`, so whoever works
on the project (you, or an AI agent) decides: `include` or `exclude`. Nothing is ever synced
just because its name looks secret. Patterns use `/`, are case-insensitive, and `*` also matches
across folders; `.secrets/*` therefore covers files added there later.

Each machine keeps a small local state file (outside the store) with hashes of what it last
synced. That is how `status` tells *changed here* from *changed on the other machine* and
detects **conflicts** (changed on both).

## Commands

```
secret-sync doctor  [-json]           check this machine; prints each fix and who should run it
secret-sync keygen  [-identity FILE]  create a new age key (first machine only)
secret-sync init    -machine NAME -projects-root DIR -store-root DIR -identity FILE
secret-sync status  [PROJECT | PROJECT/PATH ...] [-discover] [-show-tracked]
secret-sync include PROJECT/PATTERN ...
secret-sync exclude PROJECT/PATTERN ...
secret-sync push    PROJECT | PROJECT/PATH ...    [-dry-run] [-force]
secret-sync pull    [PROJECT | PROJECT/PATH ...]  [-dry-run] [-force]
```

`status` covers the projects in the store; `-discover` adds every project folder under the
projects root, to find ones without a manifest yet.

| Status | Meaning | Next step |
|---|---|---|
| `candidate` | Looks sensitive, not in the manifest | `include` or `exclude` |
| `new`, `local-changed`, `missing-in-store` | This machine has something the store lacks | `push` |
| `remote-new`, `remote-changed`, `missing-locally` | The store has something this machine lacks | `pull` |
| `in-sync` / `in-sync*` | Identical (`*`: not linked yet, any push/pull links it) | — |
| `conflict` | Changed on both sides | Decide, then `push -force` or `pull -force` |

Built-in rules that are facts, not decisions: candidates already **tracked by git** are hidden
(git syncs them; `-show-tracked` lists them), public CA bundles such as `cacert.pem` are never
candidates, and dependency, build and IDE folders (`node_modules`, `.git`, `.claude`...) are
skipped.

Exit codes: `0` ok, `1` error, `2` conflicts or refused/skipped files.

## Safety model

- **Never prints file contents.** Data only flows file → age → file. Change detection uses hashes
  kept on the local machine; plaintext hashes are never written to the shared store (a hash of a
  short password can be brute-forced).
- **Never deletes**, and never overwrites a side that changed since the last sync unless you pass
  `-force`. A forced pull backs up the local file first.
- **Refuses to push a file that git does not ignore**, so a secret that is one `git add .` away
  from a commit gets flagged instead of synced.
- Pulled files are written atomically (temp file + rename), with owner-only permissions on Unix.

What it does **not** protect against:

- Secrets are **plaintext on disk** on every machine, as they were before. This tool gives you
  sync and backup, not runtime protection.
- Whoever holds the **age key** can decrypt everything. Keep a backup of it in a password
  manager, and never store it in the synced folder.
- Losing the key means losing the store. There is no recovery.

## Setup

Run `secret-sync doctor` and follow it. It works before anything is configured: it finds the
Google Drive folder (Windows, macOS; on Linux use an rclone mount or pass `-store-root`), checks
git, the key, the store and the config, and prints the exact command for each missing piece.

In short:

1. **First machine:** `secret-sync keygen` creates the key (`~/.config/secret-sync/age-key.txt`).
   Back it up in your password manager right away.
   **Other machines:** restore that same key file from your backup.
2. Mark the store folder (default `<Google Drive>/dev-secrets`) as *available offline*.
3. `secret-sync init ...` with the values `doctor` suggests.
4. `secret-sync pull` in each project.

### Build from source

Requires Go 1.25+.

```
go build -o bin/secret-sync .
```

age is built in via [filippo.io/age](https://pkg.go.dev/filippo.io/age); the `age` command is not
required. The files it writes are standard age files, so `age -d -i key.txt file.age` can always
recover them without this tool.

## Tests

`tests/Test-SecretSync.ps1` (PowerShell 7) runs an end-to-end scenario with **fake data**,
simulating two machines on one computer: push, pull, conflicts, the gitignore guard, recovery,
and a check that no fake value ever appears in the output.

```
$env:SECRET_SYNC_TEST_STORE = "<an empty folder dedicated to tests>"
pwsh tests/Test-SecretSync.ps1
```

The test resets its store, so never point it at a real one; it refuses non-empty folders that it
did not create.

## Extras

`scripts/audit.ps1` lists sensitive-looking files across many projects, by name only, with their
git status (ignored / tracked / untracked). Its output goes to `out/`, which is gitignored.
