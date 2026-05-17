# Compatibility notes

This Go implementation is designed to be a practical drop-in replacement for the Bash cleanup script.

## Preserved behavior

The following behavior is intentionally preserved:

- Default config path: `/etc/backup-utils/backup-cleanup.conf`
- Same CLI flags:
  - `--backup-path <path>`
  - `--dry-run`
  - `--debug`
  - `-h`, `--help`
- Same environment/config variable names
- Same default values
- Same FULL/DIFF/LOG directory discovery model
- Same `.cleanup_processed` marker-file behavior during a scan
- Same dry-run rule: no files are deleted and no pulse metrics are emitted
- Same optional telemetry rule: if `send-pulse` is missing, the program continues silently
- Same exclusion rule: a file is excluded if its full path contains any configured pattern
- Same FULL backup date extraction rule:
  1. Try to extract `YYYYMMDD` from a `*_YYYYMMDD_HHMMSS.bak`-style filename
  2. Fall back to file modification time
- Same DIFF and LOG age rule: use file modification time
- Same weekly weekday numbering as GNU `date +%w`
- Same weekly week numbering intent as GNU `date +%Y-%W`
- Same monthly retention approximation: `FULL_MONTHLY_RETENTION_MONTHS × 30 × 86400`
- Same epoch-cutoff boundary semantics as Bash: FULL filename dates are parsed at
  local midnight, while cutoff timestamps use `now`; files exactly on the
  nominal day/week boundary may fall outside retention depending on time-of-day
- Same dry-run counter behavior: `FilesDeleted` reflects would-be deletions even in dry-run mode

## Differences and improvements

### Config file sourcing

The original script used:

```bash
source /etc/backup-utils/backup-cleanup.conf
```

A pure Go parser would not be equivalent. This program therefore invokes Bash to source the config file and then reads the resulting environment.

That means the server should still have `bash` installed when the `.conf` format is used. This is normally true on the same Linux hosts that could run the original script.

#### JSON config format (new)

As a cross-platform alternative, the program also accepts a JSON configuration file.  The format is auto-detected from the file extension: if the path configured in `CONFIG_FILE` ends in `.json`, the file is parsed as JSON instead of being sourced by Bash.

This is the recommended format for Windows deployments and is also useful when configuration management tooling generates JSON.

```bash
CONFIG_FILE=/etc/backup-utils/backup-cleanup.json backup-cleanup
```

JSON keys use the same names as the shell variable equivalents (e.g. `"BACKUP_PATH"`, `"CLEANUP_ENABLED"`).  Unlike the shell format, variable expansion (`${VAR}`) is not supported; all values must be literal strings or numbers.  See `configs/backup-cleanup.json.example` for a complete template.

### Invalid numeric config values

The Go version fails early with a clear error for invalid integer settings.

Example:

```bash
DIFF_RETENTION_DAYS=abc
```

This is safer, but technically stricter than Bash's runtime arithmetic behavior.

### Negative retention values rejected

The Bash script would silently accept negative retention values and produce undefined behavior (the cutoff would move into the future, causing all files to be considered outside the window).

The Go version rejects negative values at startup with a clear error. Zero is accepted and disables the associated retention tier.

### Stale marker cleanup on partial failure *(bug fix)*

The Bash script only deleted `.cleanup_processed` markers at the end of a successful run. If the script aborted partway through (e.g. because `rm` failed on a file), the markers for already-processed databases would be left on disk. On the next scheduled run, those databases would be silently skipped.

The Go version uses `defer` to guarantee marker cleanup regardless of whether the run succeeds or fails.

### Consistent reference time for age-based cleanup *(improvement)*

In the Bash script, `$(date '+%s')` was evaluated inside the per-file loop, so the reference time drifted slightly across a large batch. The Go version captures `time.Now()` once before iterating over files in `cleanupDiffBackups` and `cleanupLogBackups`, giving every file in the same batch a consistent cutoff.

### Final summary log line *(gap filled)*

The Go version logs a summary line at the end of each run:

```
Summary: databases=2 files_deleted=7 (full=3 diff=2 log=2) files_kept=15
```

The Bash script did not emit a summary.

### Syslog behavior

The original script used the external `logger` command. The Go version writes directly to syslog using Go's standard library and also prints the same timestamped messages to stdout/stderr.

Syslog errors remain non-fatal, matching `logger ... 2>/dev/null || true`.

### Integer arithmetic safety

Cutoff epoch calculations now explicitly use `int64` arithmetic (`int64(days) * 86400`) rather than converting an `int` multiplication result to `int64`. This prevents potential overflow on 32-bit platforms when retention values are large.

## Suggested rollout

1. Install the Go binary under a temporary name, for example `/usr/local/bin/backup-cleanup`.
2. Run the Bash script and Go program in `--dry-run` mode against the same backup tree.
3. Compare the `Would delete:` lines.
4. Replace the scheduled command only after the dry-run decisions match or any intended differences are understood.
