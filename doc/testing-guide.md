# Testing guide

This document explains how the test suite is organised, what each group of tests covers, how to run tests, and — most importantly — how to add new tests when the code changes.

## Table of contents

1. [How to run the tests](#1-how-to-run-the-tests)
2. [Test file location and package choice](#2-test-file-location-and-package-choice)
3. [Test helpers](#3-test-helpers)
4. [Test groups and what they cover](#4-test-groups-and-what-they-cover)
5. [How to add a new unit test](#5-how-to-add-a-new-unit-test)
6. [How to add a new integration test](#6-how-to-add-a-new-integration-test)
7. [Working with time in tests](#7-working-with-time-in-tests)
8. [Test naming convention](#8-test-naming-convention)
9. [What is not tested and why](#9-what-is-not-tested-and-why)

---

## 1. How to run the tests

```bash
# Run all tests once (recommended)
go test -count=1 ./...

# Verbose: show each individual test name and PASS/FAIL
go test -v -count=1 ./...

# Run a single test by name (partial match, case-sensitive)
go test -v -count=1 -run TestCleanupFullBackups ./cmd/backup-cleanup/

# Run all tests whose names contain "Weekly"
go test -v -count=1 -run Weekly ./cmd/backup-cleanup/

# Using make (runs go test ./...)
make test
```

`-count=1` disables the Go test cache so every run re-executes the tests.  This is important for tests that create files on disk, because a cached "PASS" result might hide a regression.

---

## 2. Test file location and package choice

| File | Package | Reason |
|---|---|---|
| `cmd/backup-cleanup/main.go` | `package main` | Application code |
| `cmd/backup-cleanup/main_test.go` | `package main` | Tests (same package) |

Tests are in the **same package** as the code (`package main`, not `package main_test`).  This is called "white-box testing" in Go.  It allows tests to call unexported (lowercase) functions directly:

```go
// Works because both files are in package main:
got := weekNumberMondayFirst(someDate)
got := extractDateFromFilename("/path/to/file.bak")
got := getWeekdayNumber("Sunday")
```

If the tests were in `package main_test` (the Go "black-box" style), only exported symbols starting with a capital letter would be accessible — which would either force everything to be exported, or require a large exported test facade.

---

## 3. Test helpers

All helpers are defined at the top of `main_test.go`, before the first test function.

### `truncToDay(t time.Time) time.Time`

Returns midnight of the given time in the local timezone.  Used to create dates that are comparable to the dates embedded in FULL backup filenames (which also have midnight as their time component after `parseYYYYMMDDLocal`).

### `daysAgo(n int) time.Time`

Returns midnight of `n` calendar days before today.

```go
file := makeFullBakFile(t, dir, daysAgo(3))   // file dated 3 days ago
file := makeFullBakFile(t, dir, daysAgo(400)) // file dated ~13 months ago
```

### `makeFullBakFile(t, dir, date) string`

Creates an empty FULL `.bak` file in `dir` with the date embedded in the filename:

```
testdb_YYYYMMDD_000000.bak
```

Returns the absolute path.

```go
path := makeFullBakFile(t, dir, daysAgo(2))
// Creates: /tmp/.../testdb_20260514_000000.bak
```

### `makeFileWithMtime(t, path, mtime) string`

Creates an empty file at `path` and sets its modification time to `mtime`.  Used for DIFF and LOG backup tests where retention is based on `mtime`, not the filename.

```go
old := makeFileWithMtime(t, filepath.Join(diffDir, "old.bak"), daysAgo(30))
new := makeFileWithMtime(t, filepath.Join(diffDir, "new.bak"), daysAgo(1))
```

### `newTestApp(cfg Config) *App`

Constructs an `App` with the given `Config` and zeroed `Totals`.  This is the standard way to set up the subject-under-test for all `App` method tests.

```go
app := newTestApp(Config{
    FullDailyRetentionDays: 7,
    FullWeeklyRetentionWeeks: 4,
    FullWeeklyDay: "Sunday",
    FullMonthlyRetentionMonths: 12,
})
```

### `defaultFullCfg() Config`

Returns a `Config` pre-filled with the default GFS retention values.  Use this when you want a standard FULL backup configuration without listing every field.

### `assertExists(t, path, label)`

Fails the test if `path` does not exist on disk.

```go
assertExists(t, weeklyFile, "weekly anchor backup")
```

### `assertNotExists(t, path, label)`

Fails the test if `path` still exists on disk (i.e., was not deleted when it should have been).

```go
assertNotExists(t, oldFile, "old FULL backup outside all retention windows")
```

---

## 4. Test groups and what they cover

The tests are divided into sections by a comment header like:

```go
// ---------------------------------------------------------------------------
// cleanupFullBackups
// ---------------------------------------------------------------------------
```

### `getWeekdayNumber`

Verifies that weekday names (`"Sunday"`, `"sun"`, `"Monday"`, etc.) map to the correct numbers following the GNU `date +%w` convention (0 = Sunday, 6 = Saturday).  An unknown name must return 0 (Sunday), matching the Bash `case` fallback.

### `weekNumberMondayFirst`

Verifies that `weekNumberMondayFirst` reproduces GNU `date +%W` output for known dates:

- `2024-01-01` (Monday) → week 1
- `2025-01-01` (Wednesday before first Monday) → week 0
- Year-boundary cases (Jan 1 on Sunday, Jan 1 on Monday)

### `parseYYYYMMDDLocal`

Verifies date parsing for valid input and rejects strings that are too short, too long, or have out-of-range month/day values.

### `extractDateFromFilename`

Verifies the two-step date extraction strategy:
1. Regex match extracts `YYYYMMDD` from a properly-named `.bak` file.
2. Fallback to file `mtime` for files whose names don't match the pattern.
3. Empty string returned for a missing file with a non-matching name.

### `valueOrDefault` / `intValueOrDefault`

Verifies the config helper functions:
- Empty string uses the supplied default.
- Non-empty string is returned as-is (or parsed as int).
- Non-numeric strings are rejected.
- Negative integers are rejected.
- Zero is accepted (disables the retention tier).

### `parseJSONConfig`

Verifies that JSON config parsing:
- Accepts strings, numbers, and booleans.
- Converts numeric values into decimal strings for downstream integer parsing.
- Ignores nested/complex values.
- Returns a clear error for invalid JSON.

### `envSliceToMap`

Verifies that a `KEY=value` slice is correctly converted to a map:
- Values may contain additional `=` signs (only the first is the separator).
- Entries without `=` are silently skipped.
- Empty values (`KEY=`) produce an empty-string map entry.

### `isExcluded`

Tests the exclusion pattern matching:
- No patterns → never excluded.
- Single pattern → excluded when path contains it.
- Multiple space-separated patterns → excluded when **any** matches.

### `fileExists` / `dirExists` / `touch`

Low-level filesystem helpers:
- `fileExists` returns false for directories.
- `dirExists` returns false for regular files.
- `touch` creates a new file and updates an existing file's mtime.

### `deleteFile`

- Normal deletion removes the file.
- `ErrNotExist` logs a warning but does not return an error.
- Dry-run mode does not actually delete.

### `loadConfigAndParseArgs`

Coverage includes:
- Default values applied when no config and no env vars.
- `BACKUP_PATH` env var overrides the default.
- `--backup-path` CLI flag overrides everything.
- `--dry-run` sets `DryRun = 1`.
- `--debug` sets `Debug = 1`.
- `--version` sets `exitNow = true`, `exitCode = 0`.
- `-h` / `--help` sets `exitNow = true`, `exitCode = 0`.
- Unknown flag sets `exitNow = true`, `exitCode = 1`.
- Missing value for `--backup-path` returns an error.
- Invalid and negative integer config values fail early.
- `DRY_RUN=1` in env/config does not enable dry-run unless `--dry-run` is passed.
- JSON config files are loaded and can be overridden by CLI flags.
- Shell config sourcing (`.conf`) is exercised via `loadConfigAndParseArgs`; the test skips when `bash` is unavailable.

### `cleanupFullBackups`

Ten tests that create real files on disk in a `t.TempDir()` and verify which files survive after `cleanupFullBackups` runs:

| Test | Scenario |
|---|---|
| `_KeepsDailyFiles` | Files within daily window are kept |
| `_DeletesOldDailyFiles` | Files outside daily window are deleted |
| `_KeepsWeeklyAnchor` | File on `FULL_WEEKLY_DAY` within weekly window is kept |
| `_DeletesNonAnchorWeekly` | File on a non-anchor weekday within weekly window is deleted |
| `_KeepsMonthlyOldest` | Oldest file per month within monthly window is kept |
| `_DeletesNewerMonthly` | Newer file in the same month is deleted |
| `_ExcludedFilesNotDeleted` | Files matching `EXCLUDE_PATTERNS` survive |
| `_DryRunKeepsAll` | Dry-run mode deletes nothing |
| `_ZeroDailyRetention` | `FULL_DAILY_RETENTION_DAYS=0` disables the daily tier |
| `_MultipleFilesInWindow` | Multiple files in the daily window are all kept |

### `cleanupDiffBackups` / `cleanupLogBackups`

Four and three tests, respectively, verifying mtime-based age deletion:
- Old files (beyond retention) are deleted.
- New files (within retention) are kept.
- Dry-run mode keeps all files.
- `.trn` extension (LOG backups) is correctly handled.

### `findAndProcessBackups`

Five tests that create a multi-database directory tree:
- Only directories are processed (files at the root level are skipped).
- A database that has already been processed (`.cleanup_processed` marker exists) is skipped.
- Markers are removed at the end of the run.
- `DatabasesProcessed` counter is incremented correctly.
- Non-existent `BackupPath` returns an error.

### `App.run` integration

Four end-to-end tests that build a complete backup tree on disk:
- `CLEANUP_ENABLED=0` skips all processing.
- A full tree with old and new files produces correct delete and keep counts.
- Dry-run produces correct would-be-delete counts without touching files.
- A completely missing `BackupPath` returns an error.

---

## 5. How to add a new unit test

A unit test tests a single function in isolation.  Here is a worked example: adding a test for a new helper called `sanitizeLogTag`.

### Step 1 — Locate the right section

Find the section comment for the function you are testing, or add a new one:

```go
// ---------------------------------------------------------------------------
// sanitizeLogTag
// ---------------------------------------------------------------------------
```

### Step 2 — Write the test function

```go
func TestSanitizeLogTag_ReplacesSpaces(t *testing.T) {
    got := sanitizeLogTag("my tag")
    if got != "my-tag" {
        t.Fatalf("got %q, want %q", got, "my-tag")
    }
}

func TestSanitizeLogTag_EmptyReturnsDefault(t *testing.T) {
    got := sanitizeLogTag("")
    if got != "backup-cleanup" {
        t.Fatalf("got %q, want %q", got, "backup-cleanup")
    }
}
```

### Step 3 — Run the new test

```bash
go test -v -count=1 -run TestSanitizeLogTag ./cmd/backup-cleanup/
```

### Checklist for a good unit test

- [ ] One test per logical case (happy path, empty input, error input, boundary value).
- [ ] Test name describes the scenario: `TestFunctionName_Scenario`.
- [ ] Use `t.Fatalf` (stops the test immediately) for preconditions, and `t.Errorf` (continues) for independent assertions.
- [ ] No global state is modified.
- [ ] No real filesystem access unless the function under test requires it.

---

## 6. How to add a new integration test

An integration test creates real files on disk and calls an `App` method that reads and deletes them.  The pattern is consistent across all integration tests in the file.

Here is a template for a new `cleanupDiffBackups` scenario:

```go
func TestCleanupDiffBackups_ExcludedFileIsKept(t *testing.T) {
    // 1. Create a temp directory that acts as the database DIFF folder.
    dir := t.TempDir()

    // 2. Create files with controlled mtimes using the helper.
    old := makeFileWithMtime(t,
        filepath.Join(dir, "excluded_old.bak"),
        daysAgo(30), // older than the retention window
    )

    // 3. Build an App with a Config that matches the test scenario.
    app := newTestApp(Config{
        DiffRetentionDays: 14,
        ExcludePatterns:   "excluded_",
    })

    // 4. Call the method under test.
    if err := app.cleanupDiffBackups(dir); err != nil {
        t.Fatalf("unexpected error: %v", err)
    }

    // 5. Assert expected file system state.
    assertExists(t, old, "excluded file should survive despite age")
}
```

### Key points

- Always use `t.TempDir()` — Go automatically deletes it when the test finishes.
- Set `mtime` explicitly with `makeFileWithMtime`.  Never rely on `time.Now()` directly in a test; the test would become fragile as retention windows approach the file age.
- Keep the `Config` minimal: only set the fields relevant to the scenario.
- Add `t.Helper()` to any helper you introduce so that failure line numbers point to the test, not the helper.

---

## 7. Working with time in tests

### Why time is tricky

Retention logic compares file ages against `time.Now()`.  If a test creates a file with `mtime = daysAgo(8)` and the retention is 7 days, the test will always work.  But if the retention were 7 days and the file were dated exactly 7 days ago, the test might flip between pass and fail depending on the time of day.

### Rules

1. **Always use `daysAgo(n)`** to create file dates.  The helper truncates to midnight, making the boundary predictable.
2. **Keep a clear margin** from the boundary.  If the retention is 7 days, use `daysAgo(3)` for "inside" and `daysAgo(14)` for "outside".  Never use `daysAgo(7)` when the boundary is exactly 7 days.
3. **For FULL backup tests**, file dates come from the filename (`YYYYMMDD`), not from the file's `mtime`.  Use `makeFullBakFile(t, dir, daysAgo(n))` to embed the date.
4. **For DIFF/LOG tests**, file dates come from `mtime`.  Use `makeFileWithMtime(t, path, daysAgo(n))`.

### The GFS boundary trap

The GFS monthly window is `FULL_MONTHLY_RETENTION_MONTHS × 30 × 86400`.  At 12 months, that is 360 days.  If you create a "very old" test file with `daysAgo(100)`, it will still be within the 360-day monthly window and will be kept — appearing to be a bug.  Use `daysAgo(400)` or more to ensure a file is outside all three GFS tiers.

---

## 8. Test naming convention

All test functions follow the pattern:

```
Test<FunctionOrType>_<Scenario>
```

Examples:

```
TestCleanupFullBackups_KeepsDailyFiles
TestCleanupFullBackups_DeletesOldDailyFiles
TestLoadConfigAndParseArgs_BackupPathFromEnv
TestLoadConfigAndParseArgs_VersionFlagExitsZero
TestIntValueOrDefault_NegativeRejected
TestFindAndProcessBackups_SkipsProcessedMarker
```

This makes `-run` filtering intuitive:

```bash
# All tests for cleanupFullBackups:
go test -v -run TestCleanupFullBackups ./cmd/backup-cleanup/

# All tests for config loading:
go test -v -run TestLoadConfigAndParseArgs ./cmd/backup-cleanup/
```

---

## 9. What is not tested and why

### `sourceConfigWithBash`

This function shells out to `bash`.  It is exercised through `loadConfigAndParseArgs` with a temporary `.conf` file, and the test skips gracefully when `bash` is not installed.

### `emitPulse` / `send-pulse`

The `send-pulse` external command is absent from the development and CI environment.  The code already handles its absence gracefully (`exec.LookPath` → skip).  Tests verify that the cleanup logic runs correctly; verifying the telemetry calls would require either mocking `exec.Command` (complex) or installing `send-pulse` (environment-dependent).

### Syslog output

Syslog writes go to the system's log daemon.  Verifying exact syslog messages in tests would require either intercepting the syslog socket or reading the system log file, which is platform-specific.  The log content is verified informally during development with `--debug`.

### Actual deletion on a production filesystem

Integration tests create files in `t.TempDir()`, not in `/mnt/backup01/remote`.  Running against a real backup tree in CI would be both slow and dangerous.
