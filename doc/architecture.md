# Application architecture

This document explains how `backup-cleanup` is structured as a Go program, why each piece is designed the way it is, and what the program does at runtime.

## Table of contents

1. [Repository layout](#1-repository-layout)
2. [Go module and package model](#2-go-module-and-package-model)
3. [Key types](#3-key-types)
4. [Startup and configuration loading](#4-startup-and-configuration-loading)
5. [Runtime workflow](#5-runtime-workflow)
6. [Retention logic in detail](#6-retention-logic-in-detail)
7. [Helper functions reference](#7-helper-functions-reference)
8. [Logging and telemetry](#8-logging-and-telemetry)
9. [Version embedding](#9-version-embedding)
10. [Design decisions](#10-design-decisions)

---

## 1. Repository layout

```text
backup-cleanup/
├── .woodpecker.yml          # CI pipeline (see doc/ci-pipeline.md)
├── Makefile                 # Build and test convenience targets
├── go.mod                   # Module path: git.digixoil.se/digixoil/backup-cleanup
├── cmd/backup-cleanup/
│   ├── main.go              # All application code lives here
│   └── main_test.go         # All tests live here (same package)
├── configs/
│   ├── backup-cleanup.conf.example
│   └── backup-cleanup.json.example
└── doc/
    ├── architecture.md      ← you are here
    ├── testing-guide.md
    ├── ci-pipeline.md
    ├── version-control.md
    ├── compatibility-notes.md
    ├── csharp-review-guide.md
    └── original-backup-cleanup.sh
```

The application is deliberately kept in **one source file** (`main.go`).  This mirrors the original single Bash script and makes the code easy to read as a line-by-line replacement.

---

## 2. Go module and package model

### Module path

```
git.digixoil.se/digixoil/backup-cleanup
```

The module path is set in `go.mod`.  It matches the Gitea repository URL so that `go get` works without any extra configuration.

### Package

The entire program lives in `package main`.  Go requires exactly one `main` package per executable; having all code in one package means tests can access unexported (lowercase) functions directly without any special setup.

### No external dependencies

`go.mod` declares no external dependencies.  Every import comes from the Go standard library.  This means:
- No `go mod download` step is needed in CI.
- The binary is fully self-contained after compilation.

---

## 3. Key types

### `Config`

```go
type Config struct {
    ConfigFile                 string
    BackupPath                 string
    LogTag                     string
    PulseBackupHostID          string
    CleanupPulseSubject        string
    CleanupEnabled             int
    FullDailyRetentionDays     int
    FullWeeklyRetentionWeeks   int
    FullWeeklyDay              string
    FullMonthlyRetentionMonths int
    DiffRetentionDays          int
    LogRetentionDays           int
    ExcludePatterns            string
    DryRun                     int
    Debug                      int
}
```

`Config` is a plain data struct (no methods, no embedding).  It is populated once by `loadConfigAndParseArgs` and then treated as read-only for the rest of the run.

The field names deliberately mirror the original shell variable names (`BACKUP_PATH` → `BackupPath`, etc.) so that the mapping between the config file and the Go code is obvious.

**C# analogy:** like an options class bound from `appsettings.json` plus environment variables plus command-line arguments, in that layered priority order.

### `Totals`

```go
type Totals struct {
    DatabasesProcessed int
    FullDeleted        int
    DiffDeleted        int
    LogDeleted         int
    FilesDeleted       int
    FilesKept          int
}
```

A simple counter struct that accumulates deletion and keep statistics across the entire run.  It is embedded in `App` and mutated by each cleanup function.

**C# analogy:** a small mutable DTO.

### `App`

```go
type App struct {
    cfg                 Config
    totals              Totals
    cleanupPulseStarted bool
}
```

`App` is the central service object.  It owns the `Config` (read-only after construction) and the `Totals` (written during the run).  All methods that do real work are receiver methods on `*App`.

Putting state in a struct rather than package-level globals makes tests easy: each test constructs its own `App` with a purpose-built `Config` and a throwaway `Totals`, so tests cannot interfere with each other.

**C# analogy:** a service class where dependencies (config) are injected at construction time.

---

## 4. Startup and configuration loading

### Entry point

```go
func main() {
    cfg, exitNow, exitCode, err := loadConfigAndParseArgs(os.Args[1:])
    ...
    app := &App{cfg: cfg}
    if err := app.run(); err != nil { ... }
}
```

`main` is minimal: it delegates to `loadConfigAndParseArgs` (testable with any argument slice), then constructs `App` and calls `run`.

### `loadConfigAndParseArgs`

This function implements the full startup sequence in one place:

1. **Determine config file path** — use `CONFIG_FILE` env var if set, otherwise fall back to `/etc/backup-utils/backup-cleanup.conf`.
2. **Load the config file** — if the path ends in `.json`, call `parseJSONConfig`; otherwise source it with `sourceConfigWithBash`.
3. **Apply environment overrides** — read each `Config` field from the environment, applying defaults with `valueOrDefault` / `intValueOrDefault`.
4. **Parse CLI flags** — iterate over `os.Args[1:]` and apply `--backup-path`, `--dry-run`, `--debug`, `--version`, `-h`/`--help`.

The function signature returns `(Config, exitNow bool, exitCode int, err error)`.  The `exitNow` flag lets `--help` and `--version` exit cleanly without entering the cleanup workflow.

### Config file sourcing (`sourceConfigWithBash`)

The config file is a shell script fragment, not a Go-native format.  Parsing it in pure Go would require reimplementing shell variable expansion, which would risk diverging from the original behavior.

Instead, the program runs:

```bash
bash -c "set -a; source '$1'; env -0" -- /path/to/backup-cleanup.conf
```

`set -a` automatically exports every variable that is set, so they all appear in the `env` output.  The NUL-delimited output is then parsed into a `map[string]string` by `envSliceToMap`.

**Security note:** the config path is passed as `$1` (a positional argument to bash), never interpolated into the shell string itself.  This prevents path injection if the config path contains spaces or special characters.

---

## 5. Runtime workflow

```
main()
  └─ loadConfigAndParseArgs()      # Config, exit flags
  └─ App.run()
       ├─ Log startup banners
       ├─ Check CLEANUP_ENABLED
       ├─ emitPulse("job.scheduled.started")
       ├─ findAndProcessBackups()
       │    ├─ List subdirectories of BackupPath
       │    ├─ For each database dir:
       │    │    ├─ Skip if .cleanup_processed marker exists
       │    │    ├─ processDatabaseBackups()
       │    │    │    ├─ cleanupFullBackups()
       │    │    │    ├─ cleanupDiffBackups()
       │    │    │    └─ cleanupLogBackups()
       │    │    └─ touch .cleanup_processed
       │    └─ defer deleteCleanupProcessedMarkers()   # always runs
       ├─ Log summary line
       └─ emitPulse("job.scheduled.completed")
```

### `.cleanup_processed` marker files

During a run, each database directory that has been processed gets a `.cleanup_processed` marker file written to it.  On the next pass of the same run, directories with a marker are skipped.

This prevents double-processing if `findAndProcessBackups` is called multiple times (or if the backup directory tree has symlinks that would otherwise cause the same database to be visited twice).

Critically, `defer deleteCleanupProcessedMarkers(basePath)` is called at the start of `findAndProcessBackups`.  The `defer` guarantees that all markers are removed even if a later database fails with an error.  This was a bug in the original Bash script where a partial failure left markers on disk and caused databases to be permanently skipped.

---

## 6. Retention logic in detail

### FULL backups — GFS (Grandfather-Father-Son)

FULL backups use three overlapping retention tiers.  A file is **kept** if it qualifies for **any** one of the three tiers.  It is deleted only if it fails all three.

#### Tier 1 — Daily

Keep every FULL backup whose embedded date is within the last `FULL_DAILY_RETENTION_DAYS` days.

```
keep if: fileDate >= (now - FULL_DAILY_RETENTION_DAYS × 86400)
```

#### Tier 2 — Weekly

Divide the last `FULL_WEEKLY_RETENTION_WEEKS` weeks into buckets by `YYYY-WW` using GNU `date +%W` semantics (Monday-first, week `00` before the first Monday).  Within each week, keep only the backup whose date falls on `FULL_WEEKLY_DAY` (default: Sunday).

The weekly tier extends back from today by `FULL_WEEKLY_RETENTION_WEEKS × 7` days.

#### Tier 3 — Monthly

Divide the last `FULL_MONTHLY_RETENTION_MONTHS` months into buckets by `YYYYMM`.  Within each month, keep the **oldest** backup (the first one that arrived for that month).

The monthly window extends back by `FULL_MONTHLY_RETENTION_MONTHS × 30 × 86400` seconds (an approximation consistent with the original script).

#### Date extraction

The date for a FULL backup file is determined by `extractDateFromFilename`:

1. Try to match `([0-9]{8})_[0-9]{6}\.bak$` against the filename.  A file named `MyDB_20250131_230000.bak` yields `20250131`.
2. If the pattern does not match, fall back to the file's modification time (`mtime`).

#### Week numbering

`weekNumberMondayFirst` reimplements GNU `date +%W` semantics:
- The first Monday of the year is in week 1.
- Days before the first Monday of the year are in week 0.
- This differs from ISO 8601 week numbering (which Go's `time.ISOWeek()` implements).

### DIFF backups — age-based

```
delete if: now - file.mtime > DIFF_RETENTION_DAYS × 86400
```

The DIFF directory is `<backupPath>/<database>/DIFF/`.  Only `.bak` files are considered.

### LOG backups — age-based

```
delete if: now - file.mtime > LOG_RETENTION_DAYS × 86400
```

The LOG directory is `<backupPath>/<database>/LOG/`.  Both `.bak` and `.trn` extensions are considered.

### Exclusions

`isExcluded(path)` checks whether the file's full path contains any of the space-separated patterns in `EXCLUDE_PATTERNS`.  If any pattern matches, the file is skipped regardless of its age or tier.

---

## 7. Helper functions reference

| Function | Purpose |
|---|---|
| `loadConfigAndParseArgs(args)` | Load config (JSON or shell), apply defaults, parse flags; return `Config` |
| `sourceConfigWithBash(path)` | Source a shell config file via Bash; return env map |
| `envSliceToMap(env)` | Convert `KEY=value` strings to `map[string]string` |
| `valueOrDefault(s, def)` | Return `s` if non-empty, otherwise `def` |
| `intValueOrDefault(s, def, name)` | Parse `s` as int ≥ 0; return `def` if empty |
| `extractDateFromFilename(path)` | Return `YYYYMMDD` from filename or mtime |
| `parseYYYYMMDDLocal(s)` | Parse an 8-digit date string in local timezone |
| `weekNumberMondayFirst(t)` | GNU `date +%W` week number |
| `getWeekdayNumber(name)` | Weekday name → 0 (Sunday) … 6 (Saturday) |
| `fileExists(path)` | True if path is a regular file |
| `dirExists(path)` | True if path is a directory |
| `touch(path)` | Create or update a marker file |
| `deleteFile(path, reason)` | Delete a file; log if not found; respect dry-run |
| `isExcluded(path)` | True if path matches any exclusion pattern |
| `exitCodeOrDefault(code, fallback)` | Return non-zero exit code |
| `printHelp()` | Print usage text to stdout |

---

## 8. Logging and telemetry

### Syslog

All operational log messages go to syslog via Go's `log/syslog` package.  The tag is configured by `LOG_TAG` (default: `backup-cleanup`).  This matches the original script's `logger -t backup-cleanup` call.

Debug messages (enabled by `--debug`) also write to stdout so they are visible in the terminal or CI log.

### `send-pulse` telemetry

The program optionally calls the `send-pulse` external command to emit metrics:

- `job.scheduled.started` — emitted at the beginning of a run (sets `cleanupPulseStarted = true`)
- `job.cleanup.databases.processed` — number of database directories handled
- `job.cleanup.files.deleted` / `job.cleanup.files.kept` — global totals
- `job.cleanup.full.deleted` / `job.cleanup.diff.deleted` / `job.cleanup.log.deleted` — per-type totals
- `job.scheduled.completed` — emitted on clean completion
- `job.scheduled.failed` — emitted if `run()` returns an error
- `job.scheduled.skipped` — emitted if `CLEANUP_ENABLED != 1`

If `send-pulse` is not installed, `exec.LookPath` returns an error and the telemetry call is silently skipped.  This matches the original script's behavior.

---

## 9. Version embedding

The binary version, commit SHA, and build date are injected by the linker at compile time:

```makefile
LDFLAGS := -s -w \
    -X main.version=$(VERSION) \
    -X main.commit=$(COMMIT) \
    -X main.buildDate=$(DATE)
```

In `main.go`:

```go
var (
    version   = "dev"
    commit    = "unknown"
    buildDate = "unknown"
)
```

If the binary is built without the `-X` flags (e.g. `go run ./cmd/backup-cleanup`), the defaults apply.

The `--version` flag prints:

```
backup-cleanup v1.2.3 (commit abc1234, built 2026-05-16T10:00:00Z)
```

---

## 10. Design decisions

### Single file

Keeping all code in `main.go` was a deliberate choice.  The original program was a single Bash file; keeping the replacement as a single Go file makes the diff obvious to a reviewer.  If the program grows significantly, splitting into sub-packages would be appropriate.

### White-box tests (same package)

Tests are in `package main`, not `package main_test`.  This allows tests to call unexported functions directly (`extractDateFromFilename`, `weekNumberMondayFirst`, etc.) without adding an exported test surface to the production code.

### No `flag` package

The standard library's `flag` package changes exit codes and output format in ways that differ from the original script.  Argument parsing is implemented by hand to ensure exact compatibility.

### `int` fields for boolean-like config

`DryRun` and `Debug` are `int`, not `bool`, because the config file and environment use `0`/`1` values.  Using `int` avoids a parsing layer and keeps the comparison `cfg.DryRun == 1` identical to the original `[[ $DRY_RUN -eq 1 ]]` pattern.

### No goroutines

Cleanup is sequential and single-threaded, matching the original script.  Adding concurrency would complicate the `Totals` counters, the marker file logic, and test reproducibility without meaningful performance benefit for this workload.
