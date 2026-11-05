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
  ignore.txt                          <project>\<path> wildcards never synced
  <project>/
    .env.age                          encrypted copy of <project>/.env
    .env.age.meta.json                which machine pushed it, and when
    .secrets/api_key.age
```

Each machine keeps a small local state file (outside the store) with hashes of what it last
synced. That is how `status` tells *changed here* from *changed on the other machine* and
detects **conflicts** (changed on both).

## Commands

```
secret-sync init   -machine NAME -projects-root DIR -store-root DIR -identity FILE
secret-sync status [PROJECT | PROJECT/PATH ...] [-discover]
secret-sync push   PROJECT | PROJECT/PATH ...    [-dry-run] [-force]
secret-sync pull   [PROJECT | PROJECT/PATH ...]  [-dry-run] [-force]
secret-sync ignore PROJECT/PATH-WILDCARD ...
```

| Status | Meaning | Next step |
|---|---|---|
| `new`, `local-changed`, `missing-in-store` | This machine has something the store lacks | `push` |
| `remote-new`, `remote-changed`, `missing-locally` | The store has something this machine lacks | `pull` |
| `in-sync` / `in-sync*` | Identical (`*`: not linked yet, any push/pull links it) | — |
| `conflict` | Changed on both sides | Decide, then `push -force` or `pull -force` |

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

1. Generate one age key and use it on every machine (`age-keygen -o ~/.config/secret-sync/age-key.txt`,
   or any age X25519 key). Back it up in your password manager.
2. Choose a store folder inside your sync service, and mark it *available offline*.
3. Build or download `secret-sync`, then run `init` on each machine.

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
