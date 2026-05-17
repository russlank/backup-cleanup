# backup-cleanup

[![Latest Release](https://img.shields.io/github/v/release/russlank/backup-cleanup?display_name=tag&sort=semver)](https://github.com/russlank/backup-cleanup/releases/latest)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![Buy Me A Coffee](https://img.shields.io/badge/Buy%20Me%20A%20Coffee-%23FFDD00.svg?&style=flat&logo=buy-me-a-coffee&logoColor=black)](https://buymeacoffee.com/russlank)


A Go replacement for the [`backup-cleanup`](doc/original-backup-cleanup.sh) Bash script that removes old SQL Server backup files according to Grandfather-Father-Son (GFS) retention rules.

`backup-cleanup` is designed to be used alongside [Ola Hallengren's SQL Server Maintenance Solution](https://ola.hallengren.com/) — a widely-used set of SQL Server Agent jobs and stored procedures that produce `FULL`, `DIFF`, and `LOG` backup files.  `backup-cleanup` enforces a GFS retention policy on those files and removes outdated copies from the backup storage host.

The goal is operational compatibility: the binary keeps the same command name, CLI flags, environment/config variable names, retention decisions, log messages, dry-run behavior, and optional `send-pulse` telemetry behavior as the original Bash script.

## Quick navigation

| Topic | File |
|---|---|
| Configuration reference | [this file, §Configuration](#configuration) |
| JSON configuration format | [this file, §JSON configuration](#json-configuration) |
| Go application architecture | [doc/architecture.md](doc/architecture.md) |
| Testing guide (how to add tests) | [doc/testing-guide.md](doc/testing-guide.md) |
| CI pipelines (Woodpecker + GitHub Actions) | [doc/ci-pipeline.md](doc/ci-pipeline.md) |
| Version control and release workflow | [doc/version-control.md](doc/version-control.md) |
| Compatibility with original Bash script | [doc/compatibility-notes.md](doc/compatibility-notes.md) |
| Original Bash script (reference) | [doc/original-backup-cleanup.sh](doc/original-backup-cleanup.sh) |

## Project layout

```text
backup-cleanup/
├── .github/workflows/ci.yml      # GitHub Actions pipeline
├── .woodpecker.yml              # Woodpecker CI pipeline
├── Makefile                     # Build, test, and dist targets
├── go.mod                       # Go module definition
├── cmd/backup-cleanup/
│   ├── main.go                  # Entire command implementation (single package)
│   └── main_test.go             # All unit and integration tests (same package)
├── configs/
│   ├── backup-cleanup.conf.example
│   └── backup-cleanup.json.example
├── dist/                        # Compiled binaries and SHA256SUMS (git-ignored)
├── doc/
│   ├── architecture.md          # Go architecture and code walkthrough
│   ├── testing-guide.md         # How tests are organised and how to add more
│   ├── ci-pipeline.md           # Woodpecker CI pipeline details
│   ├── version-control.md       # Git workflow and release process
│   ├── compatibility-notes.md   # Differences from the original Bash script
│   ├── csharp-review-guide.md   # Notes for C# reviewers
│   └── original-backup-cleanup.sh
└── scripts/
    └── build-dist.sh            # Standalone cross-compile script
```

## Build prerequisites

Building from source requires Go 1.23 or newer.  The resulting binary has **no runtime dependency** on Go or any shared library.

```bash
go version   # must be 1.23+
```

## Build

```bash
make dist
```

This produces:

```text
dist/backup-cleanup-linux-amd64
dist/backup-cleanup-linux-arm64
dist/backup-cleanup-windows-amd64.exe
dist/SHA256SUMS
```

The binaries embed the version, commit SHA, and build date — visible via `--version`.

## Install

```bash
# x86-64 server
sudo install -m 0755 dist/backup-cleanup-linux-amd64 /usr/local/bin/backup-cleanup

# ARM64 server
sudo install -m 0755 dist/backup-cleanup-linux-arm64 /usr/local/bin/backup-cleanup

# Windows (PowerShell — copy to any directory in %PATH%)
Copy-Item dist\backup-cleanup-windows-amd64.exe C:\Tools\backup-cleanup.exe
```

## Test

```bash
make test
# or
go test -count=1 ./...

# verbose (shows each test case name)
go test -v -count=1 ./...
```

See [doc/testing-guide.md](doc/testing-guide.md) for the full description of how the test suite is structured and how to add new tests.

## Usage

```text
backup-cleanup [options]

OPTIONS:
  --backup-path <path>     Path to backup directory (default: /mnt/backup01/remote)
  --dry-run                Show what would be deleted without actually deleting
  --debug                  Enable debug output
  --version                Print version and exit
  -h, --help               Show this help
```

Always run with `--dry-run` before enabling real deletion in a new environment:

```bash
backup-cleanup --dry-run --debug
```

## Configuration

The default configuration file is:

```text
/etc/backup-utils/backup-cleanup.conf
```

The file must be a shell-compatible list of `KEY=value` assignments (it is sourced by Bash at startup).  Variable references and comments are supported:

```bash
PULSE_BACKUP_HOST_ID="pulse.monitor.local"
CLEANUP_PULSE_SUBJECT="backup/${PULSE_BACKUP_HOST_ID}/cleanup"
```

See [`configs/backup-cleanup.conf.example`](configs/backup-cleanup.conf.example) for a complete annotated template.

### Configuration reference

| Variable | Default | Description |
|---|---|---|
| `BACKUP_PATH` | `/mnt/backup01/remote` | Root directory containing per-database backup folders |
| `LOG_TAG` | `backup-cleanup` | Syslog tag |
| `PULSE_BACKUP_HOST_ID` | `pulse.monitor.local` | Host identifier used in the default pulse subject |
| `CLEANUP_PULSE_SUBJECT` | `backup/{host}/cleanup` | Metric subject passed to `send-pulse` |
| `CLEANUP_ENABLED` | `1` | Set to `0` to skip cleanup without disabling the scheduled job |
| `FULL_DAILY_RETENTION_DAYS` | `7` | Keep every FULL backup this many days old or newer |
| `FULL_WEEKLY_RETENTION_WEEKS` | `4` | Keep one FULL backup per GNU `%W` week bucket (on `FULL_WEEKLY_DAY`) within this window |
| `FULL_WEEKLY_DAY` | `Sunday` | Weekday name for the weekly FULL backup anchor |
| `FULL_MONTHLY_RETENTION_MONTHS` | `12` | Keep the oldest FULL backup per calendar month within this window |
| `DIFF_RETENTION_DAYS` | `14` | Delete DIFF backups older than this many days (by file mtime) |
| `LOG_RETENTION_DAYS` | `7` | Delete LOG backups older than this many days (by file mtime) |
| `EXCLUDE_PATTERNS` | _(empty)_ | Space-separated path substrings; matching files are never deleted |

All integer values must be non-negative.  Setting a value to `0` disables that retention tier.

## JSON configuration

As an alternative to the shell-compatible `.conf` format, `backup-cleanup` also accepts a JSON configuration file.  This format is required on Windows (where `bash` is not available to source `.conf` files) and is also convenient when config management tooling generates JSON.

Point the binary at a JSON file by setting the `CONFIG_FILE` environment variable:

```bash
CONFIG_FILE=/etc/backup-utils/backup-cleanup.json backup-cleanup
```

On Windows:

```powershell
$env:CONFIG_FILE = 'C:\ProgramData\backup-utils\backup-cleanup.json'
.\backup-cleanup.exe --dry-run
```

The JSON keys are identical to the shell variable names.  Both string and integer JSON values are accepted:

```json
{
  "BACKUP_PATH": "D:\\Backups\\remote",
  "CLEANUP_ENABLED": 1,
  "FULL_DAILY_RETENTION_DAYS": 7,
  "FULL_WEEKLY_RETENTION_WEEKS": 4,
  "FULL_WEEKLY_DAY": "Sunday",
  "FULL_MONTHLY_RETENTION_MONTHS": 12,
  "DIFF_RETENTION_DAYS": 14,
  "LOG_RETENTION_DAYS": 7
}
```

> **Note:** Variable expansion (`${VAR}`) is not supported in JSON files.  Use literal values for all settings.

See [`configs/backup-cleanup.json.example`](configs/backup-cleanup.json.example) for a complete annotated template.

## CI and releases

Releases are published automatically by the Woodpecker CI pipeline when a semver tag is pushed:

```bash
git tag v1.2.3
git push origin v1.2.3
```

Woodpecker lints, vets, tests, cross-compiles, and uploads three binaries (`linux-amd64`, `linux-arm64`, `windows-amd64.exe`) plus `SHA256SUMS` to Gitea releases.

For GitHub-hosted mirrors, `.github/workflows/ci.yml` provides the equivalent flow (lint, vet, test, build dist, release on tag). See [doc/ci-pipeline.md](doc/ci-pipeline.md) for full details.

## Compatibility notes

See [doc/compatibility-notes.md](doc/compatibility-notes.md).

Key improvements over the original Bash script:

- Invalid or negative integer config values fail early with a clear error.
- Stale `.cleanup_processed` markers are always cleaned up, even after a partial failure.
- `time.Now()` is captured once per cleanup function, not per file.
- A summary log line is emitted at the end of every run.

## License

MIT — see [LICENSE](LICENSE).

Copyright (c) 2026 Russlan Kafri (<https://github.com/russlank>).

## Documentation style note

This codebase targets both Go developers and C# reviewers.  `main.go` therefore uses standard Go documentation comments combined with XML-style `<summary>` / `<remarks>` blocks inside comments to make the intent clear to a C# reader.  See [doc/csharp-review-guide.md](doc/csharp-review-guide.md).
