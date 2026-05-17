// Package main contains the backup-cleanup command.
//
// <summary>
// backup-cleanup removes old SQL Server backup files according to the same
// retention rules as the original Bash script.
// </summary>
//
// <remarks>
// This file is intentionally kept as a single executable package so it is easy
// to review as a drop-in replacement for the previous shell script.  A C#
// developer can read the main types as roughly equivalent to:
//
//   - Config: an options/settings class.
//   - Totals: a small DTO used for summary counters.
//   - App: the service class that owns the workflow and dependencies.
//
// Go does not use XML documentation comments in the same way C# does.  The
// comments below therefore combine normal Go documentation with XML-style
// <summary> and <remarks> sections where that makes the intent easier for a
// C# reviewer to follow.
// </remarks>
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// version, commit, and buildDate are injected at build time by the Makefile via
// -ldflags "-X main.version=... -X main.commit=... -X main.buildDate=...".
// They remain at their zero values when the binary is built without those flags
// (e.g. `go run ./cmd/backup-cleanup`).
//
// <summary>
// Build-time version information exposed by --version.
// </summary>
var (
	version   = "dev"
	commit    = "unknown"
	buildDate = "unknown"
)

// defaultConfigFile is the same path used by the original Bash script.
//
// <summary>
// Default configuration file location.
// </summary>
const defaultConfigFile = "/etc/backup-utils/backup-cleanup.conf"

// backupFilenameDateRE extracts the YYYYMMDD portion from backup names such as
// database_20250131_230000.bak.
//
// <summary>
// Regular expression used to extract the backup date from FULL backup names.
// </summary>
var backupFilenameDateRE = regexp.MustCompile(`([0-9]{8})_[0-9]{6}\.bak$`)

// Config contains every runtime setting used by the command.
//
// <summary>
// Strongly typed settings object for the cleanup command.
// </summary>
//
// <remarks>
// C# analogy: this is similar to an options class bound from appsettings.json,
// environment variables, and command-line arguments.  The original Bash script
// accepted shell variables, so this program keeps the same names for drop-in
// compatibility.
// </remarks>
type Config struct {
	// ConfigFile is the path to the shell-compatible config file.
	ConfigFile string

	// BackupPath is the root directory that contains database backup folders.
	BackupPath string

	// LogTag is the syslog tag, equivalent to logger -t in the Bash script.
	LogTag string

	// PulseBackupHostID participates in the default telemetry subject.
	PulseBackupHostID string

	// CleanupPulseSubject is the metric subject passed to send-pulse.
	CleanupPulseSubject string

	// CleanupEnabled mirrors CLEANUP_ENABLED; any value other than 1 skips cleanup.
	CleanupEnabled int

	// FullDailyRetentionDays keeps recent FULL backups regardless of weekday/month.
	FullDailyRetentionDays int

	// FullWeeklyRetentionWeeks keeps one FULL backup per week on FullWeeklyDay.
	FullWeeklyRetentionWeeks int

	// FullWeeklyDay is the configured weekday name, for example "Sunday".
	FullWeeklyDay string

	// FullMonthlyRetentionMonths keeps one FULL backup per month within this window.
	FullMonthlyRetentionMonths int

	// DiffRetentionDays controls DIFF backup age retention, based on file mtime.
	DiffRetentionDays int

	// LogRetentionDays controls LOG backup age retention, based on file mtime.
	LogRetentionDays int

	// ExcludePatterns is a space-separated list of path substrings to keep.
	ExcludePatterns string

	// DryRun disables deletion and telemetry, while still logging decisions.
	DryRun int

	// Debug enables extra diagnostic output to stdout.
	Debug int
}

// Totals stores cleanup summary counters.
//
// <summary>
// Summary counters emitted in logs and pulse metrics after the scan completes.
// </summary>
//
// <remarks>
// C# analogy: this is a small mutable DTO updated by each cleanup phase.
// </remarks>
type Totals struct {
	DatabasesProcessed int
	FullDeleted        int
	DiffDeleted        int
	LogDeleted         int
	FilesDeleted       int
	FilesKept          int
}

// App owns the command workflow.
//
// <summary>
// Main application service for scanning backup directories and applying retention.
// </summary>
//
// <remarks>
// Instead of global variables, most state is placed in App.  That keeps the Go
// code easier to test or refactor later while preserving the Bash script's
// externally visible behavior.
// </remarks>
type App struct {
	cfg                 Config
	totals              Totals
	cleanupPulseStarted bool
}

// main is the process entry point.
//
// <summary>
// Loads configuration, parses arguments, executes cleanup, and sets exit code.
// </summary>
func main() {
	cfg, exitNow, exitCode, err := loadConfigAndParseArgs(os.Args[1:])
	if err != nil {
		// This happens before the scheduled job is considered started, matching the
		// Bash script's CLEANUP_PULSE_STARTED=0 behavior during argument/config errors.
		if err.Error() != "" {
			fmt.Fprintln(os.Stderr, err.Error())
		}
		os.Exit(exitCodeOrDefault(exitCode, 1))
	}
	if exitNow {
		os.Exit(exitCode)
	}

	app := &App{cfg: cfg}
	if err := app.run(); err != nil {
		app.logError("Cleanup failed: %v", err)
		if app.cleanupPulseStarted {
			app.emitPulse("job.scheduled.failed", app.cfg.CleanupPulseSubject, 1)
		}
		os.Exit(1)
	}
}

// exitCodeOrDefault returns fallback when code is zero.
//
// <summary>
// Prevents accidental success exits when an error path forgot to set a code.
// </summary>
func exitCodeOrDefault(code int, fallback int) int {
	if code == 0 {
		return fallback
	}
	return code
}

// loadConfigAndParseArgs implements the same load order as the Bash script.
//
// <summary>
// Loads environment/config defaults and then applies command-line overrides.
// </summary>
//
// <remarks>
// Original Bash behavior:
//
//  1. CONFIG_FILE defaults to /etc/backup-utils/backup-cleanup.conf.
//  2. If the config file exists, Bash `source` is used.
//  3. Defaults are applied with ${VAR:-default} semantics.
//  4. DRY_RUN and DEBUG are reset to 0 before CLI parsing.
//  5. CLI flags override relevant settings.
//
// This Go implementation intentionally runs Bash to source the config file.
// That may look unusual, but it preserves compatibility with existing
// shell-style config files, including simple variable expansion.
// </remarks>
func loadConfigAndParseArgs(args []string) (Config, bool, int, error) {
	configFile := os.Getenv("CONFIG_FILE")
	if configFile == "" {
		configFile = defaultConfigFile
	}

	envMap := envSliceToMap(os.Environ())
	if fileExists(configFile) {
		if strings.HasSuffix(strings.ToLower(configFile), ".json") {
			jsonEnv, err := parseJSONConfig(configFile)
			if err != nil {
				return Config{}, false, 1, err
			}
			for k, v := range jsonEnv {
				envMap[k] = v
			}
		} else {
			sourcedEnv, err := sourceConfigWithBash(configFile, os.Environ())
			if err != nil {
				return Config{}, false, 1, fmt.Errorf("failed to source config file %s: %w", configFile, err)
			}
			envMap = sourcedEnv
		}
	}

	cfg := Config{ConfigFile: configFile}

	cfg.BackupPath = valueOrDefault(envMap["BACKUP_PATH"], "/mnt/backup01/remote")
	cfg.LogTag = valueOrDefault(envMap["LOG_TAG"], "backup-cleanup")
	cfg.PulseBackupHostID = valueOrDefault(envMap["PULSE_BACKUP_HOST_ID"], "pulse.monitor.local")
	cfg.CleanupPulseSubject = valueOrDefault(envMap["CLEANUP_PULSE_SUBJECT"], "backup/"+cfg.PulseBackupHostID+"/cleanup")

	var err error
	if cfg.CleanupEnabled, err = intValueOrDefault(envMap["CLEANUP_ENABLED"], 1, "CLEANUP_ENABLED"); err != nil {
		return Config{}, false, 1, err
	}
	if cfg.FullDailyRetentionDays, err = intValueOrDefault(envMap["FULL_DAILY_RETENTION_DAYS"], 7, "FULL_DAILY_RETENTION_DAYS"); err != nil {
		return Config{}, false, 1, err
	}
	if cfg.FullWeeklyRetentionWeeks, err = intValueOrDefault(envMap["FULL_WEEKLY_RETENTION_WEEKS"], 4, "FULL_WEEKLY_RETENTION_WEEKS"); err != nil {
		return Config{}, false, 1, err
	}
	cfg.FullWeeklyDay = valueOrDefault(envMap["FULL_WEEKLY_DAY"], "Sunday")
	if cfg.FullMonthlyRetentionMonths, err = intValueOrDefault(envMap["FULL_MONTHLY_RETENTION_MONTHS"], 12, "FULL_MONTHLY_RETENTION_MONTHS"); err != nil {
		return Config{}, false, 1, err
	}
	if cfg.DiffRetentionDays, err = intValueOrDefault(envMap["DIFF_RETENTION_DAYS"], 14, "DIFF_RETENTION_DAYS"); err != nil {
		return Config{}, false, 1, err
	}
	if cfg.LogRetentionDays, err = intValueOrDefault(envMap["LOG_RETENTION_DAYS"], 7, "LOG_RETENTION_DAYS"); err != nil {
		return Config{}, false, 1, err
	}
	cfg.ExcludePatterns = valueOrDefault(envMap["EXCLUDE_PATTERNS"], "")

	// The Bash script resets these after config/defaults are loaded.  That means
	// DRY_RUN=1 in the config file does not enable dry-run unless --dry-run is
	// also supplied.  This preserves that behavior exactly.
	cfg.DryRun = 0
	cfg.Debug = 0

	for i := 0; i < len(args); {
		switch args[i] {
		case "--backup-path":
			if i+1 >= len(args) {
				return Config{}, false, 1, errors.New("missing value for --backup-path")
			}
			cfg.BackupPath = args[i+1]
			i += 2
		case "--dry-run":
			cfg.DryRun = 1
			i++
		case "--debug":
			cfg.Debug = 1
			i++
		case "--version":
			fmt.Printf("backup-cleanup %s (commit %s, built %s)\n", version, commit, buildDate)
			return Config{}, true, 0, nil
		case "-h", "--help":
			printHelp()
			return Config{}, true, 0, nil
		default:
			logErrorWithTag(cfg.LogTag, "Unknown argument: %s", args[i])
			printHelp()
			return Config{}, true, 1, nil
		}
	}

	return cfg, false, 0, nil
}

// envSliceToMap converts KEY=value strings into a map.
//
// <summary>
// Converts os.Environ() output into a dictionary-like structure.
// </summary>
//
// <remarks>
// C# analogy: Dictionary&lt;string,string&gt; built from Environment.GetEnvironmentVariables().
// </remarks>
func envSliceToMap(env []string) map[string]string {
	m := make(map[string]string, len(env))
	for _, entry := range env {
		key, val, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		m[key] = val
	}
	return m
}

// parseJSONConfig reads a JSON configuration file and returns its settings as a
// key/value map using the same variable names as the shell config format.
//
// <summary>
// Cross-platform alternative to the bash-source config loader.
// </summary>
//
// <remarks>
// JSON keys use the same names as the shell variable equivalents
// (e.g. "BACKUP_PATH", "CLEANUP_ENABLED").  Both string and integer JSON
// values are accepted; integer values are converted to their decimal string
// representation so they flow through intValueOrDefault unchanged.
// Variable expansion (${VAR}) is not supported in JSON config files.
// </remarks>
func parseJSONConfig(configFile string) (map[string]string, error) {
	data, err := os.ReadFile(configFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read JSON config file %s: %w", configFile, err)
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("failed to parse JSON config file %s: %w", configFile, err)
	}
	m := make(map[string]string, len(raw))
	for k, v := range raw {
		switch val := v.(type) {
		case string:
			m[k] = val
		case float64:
			if val == float64(int64(val)) {
				m[k] = strconv.FormatInt(int64(val), 10)
			} else {
				m[k] = strconv.FormatFloat(val, 'f', -1, 64)
			}
		case bool:
			if val {
				m[k] = "1"
			} else {
				m[k] = "0"
			}
		default:
			// Ignore null and complex nested values.
		}
	}
	return m, nil
}

// sourceConfigWithBash sources a config file using Bash and returns the resulting environment.
//
// <summary>
// Preserves compatibility with shell config files used by the original script.
// </summary>
//
// <remarks>
// A pure Go parser for KEY=value would be simpler, but not equivalent to Bash
// `source`.  This function uses `bash -c` and then reads `env -0` output.  The
// NUL separator avoids ambiguity when values contain spaces or newlines.
// </remarks>
func sourceConfigWithBash(configFile string, baseEnv []string) (map[string]string, error) {
	marker := []byte("\x00__BACKUP_CLEANUP_ENV_START__\x00")
	script := `set -a
source "$1"
printf '\0__BACKUP_CLEANUP_ENV_START__\0'
env -0`

	cmd := exec.Command("bash", "-c", script, "backup-cleanup-config", configFile)
	cmd.Env = baseEnv

	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return nil, fmt.Errorf("%w: %s", err, msg)
		}
		return nil, err
	}

	idx := bytes.LastIndex(out, marker)
	if idx < 0 {
		return nil, errors.New("could not find environment marker after sourcing config")
	}
	envBytes := out[idx+len(marker):]
	parts := bytes.Split(envBytes, []byte{0})
	m := make(map[string]string, len(parts))
	for _, part := range parts {
		if len(part) == 0 {
			continue
		}
		key, val, ok := bytes.Cut(part, []byte("="))
		if !ok {
			continue
		}
		m[string(key)] = string(val)
	}
	return m, nil
}

// valueOrDefault implements Bash-like ${VAR:-default} behavior for strings.
//
// <summary>
// Returns fallback when value is an empty string.
// </summary>
func valueOrDefault(value string, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

// intValueOrDefault implements Bash-like ${VAR:-default} behavior for integers.
//
// <summary>
// Parses an integer setting, returning fallback if the setting is empty.
// </summary>
//
// <remarks>
// Stricter than Bash arithmetic in two ways:
//   - A non-numeric value (e.g. DIFF_RETENTION_DAYS=abc) fails with a clear
//     error message instead of silently evaluating to 0.
//   - A negative value is rejected because no retention period has a meaningful
//     negative interpretation: it would invert the cutoff to the future and
//     cause all files to be considered outside the retention window.
//
// Zero is valid and disables the associated retention tier.
// </remarks>
func intValueOrDefault(value string, fallback int, name string) (int, error) {
	if value == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("invalid integer value for %s: %q", name, value)
	}
	if n < 0 {
		return 0, fmt.Errorf("invalid value for %s: must be non-negative, got %d", name, n)
	}
	return n, nil
}

// printHelp writes the same help text as the Bash script.
//
// <summary>
// Shows command-line usage and retention setting names.
// </summary>
func printHelp() {
	fmt.Print(`backup-cleanup - Clean up old SQL Server backup files

USAGE:
  backup-cleanup [options]

OPTIONS:
  --backup-path <path>     Path to backup directory (default: /mnt/backup01/remote)
  --dry-run                Show what would be deleted without actually deleting
  --debug                  Enable debug output
  --version                Print version and exit
  -h, --help               Show this help

RETENTION SETTINGS (via config file or environment):
  FULL_DAILY_RETENTION_DAYS     Days of daily FULL backups to keep
  FULL_WEEKLY_RETENTION_WEEKS   Weeks of weekly FULL backups to keep
  FULL_WEEKLY_DAY               Weekday for weekly backups
  FULL_MONTHLY_RETENTION_MONTHS Months of monthly FULL backups to keep
  DIFF_RETENTION_DAYS           Days of DIFF backups to keep
  LOG_RETENTION_DAYS            Days of LOG backups to keep

CONFIGURATION:
  /etc/backup-utils/backup-cleanup.conf

`)
}

// run executes the top-level workflow.
//
// <summary>
// Performs startup logging, validates the backup path, runs cleanup, emits metrics, and logs completion.
// </summary>
func (a *App) run() error {
	a.logInfo("==========================================")
	a.logInfo("Backup Cleanup Started")
	a.logInfo("==========================================")

	if a.cfg.CleanupEnabled != 1 {
		a.logWarn("Backup cleanup is disabled (CLEANUP_ENABLED=%d)", a.cfg.CleanupEnabled)
		a.emitPulse("job.scheduled.skipped", a.cfg.CleanupPulseSubject, 1)
		return nil
	}

	a.emitPulse("job.scheduled.started", a.cfg.CleanupPulseSubject, 1)
	a.cleanupPulseStarted = true

	if !dirExists(a.cfg.BackupPath) {
		a.logError("Backup path does not exist: %s", a.cfg.BackupPath)
		a.emitPulse("job.scheduled.failed", a.cfg.CleanupPulseSubject, 1)
		return errors.New("backup path does not exist")
	}

	a.logInfo("Backup path: %s", a.cfg.BackupPath)
	a.logInfo("Retention policy:")
	a.logInfo("  FULL daily:   %d days", a.cfg.FullDailyRetentionDays)
	a.logInfo("  FULL weekly:  %d weeks (%s)", a.cfg.FullWeeklyRetentionWeeks, a.cfg.FullWeeklyDay)
	a.logInfo("  FULL monthly: %d months", a.cfg.FullMonthlyRetentionMonths)
	a.logInfo("  DIFF:         %d days", a.cfg.DiffRetentionDays)
	a.logInfo("  LOG:          %d days", a.cfg.LogRetentionDays)

	if a.cfg.DryRun == 1 {
		a.logInfo("DRY RUN MODE - No files will be deleted")
	}

	if err := a.findAndProcessBackups(a.cfg.BackupPath); err != nil {
		return err
	}

	a.emitPulse("job.cleanup.databases.processed", a.cfg.CleanupPulseSubject, a.totals.DatabasesProcessed)
	a.emitPulse("job.cleanup.files.deleted", a.cfg.CleanupPulseSubject, a.totals.FilesDeleted)
	a.emitPulse("job.cleanup.files.kept", a.cfg.CleanupPulseSubject, a.totals.FilesKept)
	a.emitPulse("job.cleanup.full.deleted", a.cfg.CleanupPulseSubject, a.totals.FullDeleted)
	a.emitPulse("job.cleanup.diff.deleted", a.cfg.CleanupPulseSubject, a.totals.DiffDeleted)
	a.emitPulse("job.cleanup.log.deleted", a.cfg.CleanupPulseSubject, a.totals.LogDeleted)
	a.emitPulse("job.scheduled.completed", a.cfg.CleanupPulseSubject, 1)

	a.logInfo("Summary: databases=%d files_deleted=%d (full=%d diff=%d log=%d) files_kept=%d",
		a.totals.DatabasesProcessed, a.totals.FilesDeleted,
		a.totals.FullDeleted, a.totals.DiffDeleted, a.totals.LogDeleted,
		a.totals.FilesKept)
	a.logInfo("==========================================")
	a.logInfo("Backup Cleanup Completed")
	a.logInfo("==========================================")
	return nil
}

// logInfo writes an info message to syslog and stdout.
//
// <summary>
// Mirrors logger -p user.info plus the timestamped console output from Bash.
// </summary>
func (a *App) logInfo(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	writeSyslog(a.cfg.LogTag, "info", msg)
	fmt.Printf("[%s] INFO: %s\n", time.Now().Format("2006-01-02 15:04:05"), msg)
}

// logError writes an error message to syslog and stderr.
//
// <summary>
// Mirrors logger -p user.err plus timestamped stderr output.
// </summary>
func (a *App) logError(format string, args ...any) {
	logErrorWithTag(a.cfg.LogTag, format, args...)
}

// logErrorWithTag is used before App exists, for example during CLI parsing.
//
// <summary>
// Logs an error with an explicit syslog tag.
// </summary>
func logErrorWithTag(tag string, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	writeSyslog(tag, "error", msg)
	fmt.Fprintf(os.Stderr, "[%s] ERROR: %s\n", time.Now().Format("2006-01-02 15:04:05"), msg)
}

// logWarn writes a warning message to syslog and stderr.
//
// <summary>
// Mirrors logger -p user.warning plus timestamped stderr output.
// </summary>
func (a *App) logWarn(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	writeSyslog(a.cfg.LogTag, "warning", msg)
	fmt.Fprintf(os.Stderr, "[%s] WARN: %s\n", time.Now().Format("2006-01-02 15:04:05"), msg)
}

// logDebug writes diagnostic output only when --debug is supplied.
//
// <summary>
// Mirrors the DEBUG=1 guarded debug output from the Bash script.
// </summary>
func (a *App) logDebug(format string, args ...any) {
	if a.cfg.Debug == 1 {
		fmt.Printf("[%s] DEBUG: %s\n", time.Now().Format("2006-01-02 15:04:05"), fmt.Sprintf(format, args...))
	}
}

// emitPulse sends a metric by invoking the external send-pulse command.
//
// <summary>
// Emits operational telemetry while preserving dry-run and optional-command behavior.
// </summary>
//
// <remarks>
// The Bash script only emitted telemetry when not in dry-run mode and silently
// skipped metrics if send-pulse was unavailable.  This method does the same.
// </remarks>
func (a *App) emitPulse(metricName string, metricSubject string, delta int) {
	// Dry-run is intentionally excluded from telemetry so operational signals stay clean.
	if a.cfg.DryRun == 1 {
		return
	}
	path, err := exec.LookPath("send-pulse")
	if err != nil {
		return
	}
	_ = exec.Command(path, metricName, metricSubject, strconv.Itoa(delta)).Run()
}

// getWeekdayNumber maps a weekday name to the Bash `date +%w` convention.
//
// <summary>
// Converts Sunday/Sun to 0, Monday/Mon to 1, and so on.
// </summary>
//
// <remarks>
// Unknown values intentionally default to Sunday, matching the Bash script.
// </remarks>
func getWeekdayNumber(day string) int {
	switch strings.ToLower(day) {
	case "sunday", "sun":
		return 0
	case "monday", "mon":
		return 1
	case "tuesday", "tue":
		return 2
	case "wednesday", "wed":
		return 3
	case "thursday", "thu":
		return 4
	case "friday", "fri":
		return 5
	case "saturday", "sat":
		return 6
	default:
		return 0
	}
}

// extractDateFromFilename returns YYYYMMDD from the file name or file mtime.
//
// <summary>
// Extracts the backup date used by FULL backup retention.
// </summary>
//
// <remarks>
// The Bash script first tried the file name pattern and then fell back to
// `date -r`, which uses the file modification time.  This function follows the
// same order.
// </remarks>
func extractDateFromFilename(filename string) string {
	base := filepath.Base(filename)
	matches := backupFilenameDateRE.FindStringSubmatch(base)
	if len(matches) == 2 {
		return matches[1]
	}

	info, err := os.Stat(filename)
	if err != nil {
		return ""
	}
	return info.ModTime().Format("20060102")
}

// isExcluded returns true when a file path contains any configured exclusion pattern.
//
// <summary>
// Implements EXCLUDE_PATTERNS matching.
// </summary>
//
// <remarks>
// This is substring matching against the full file path, equivalent to Bash
// `[[ "$file" == *"$pattern"* ]]`.
// </remarks>
func (a *App) isExcluded(file string) bool {
	if a.cfg.ExcludePatterns == "" {
		return false
	}
	for _, pattern := range strings.Fields(a.cfg.ExcludePatterns) {
		if strings.Contains(file, pattern) {
			a.logDebug("File excluded by pattern '%s': %s", pattern, file)
			return true
		}
	}
	return false
}

// deleteFile removes a file or logs the pending removal in dry-run mode.
//
// <summary>
// Applies the final delete decision for a single backup file.
// </summary>
//
// <remarks>
// This is the only function in the program that physically removes files.
// Isolating the destructive action here makes the decision logic easy to audit.
//
// Three outcomes:
//   - DryRun=1:         log "[DRY-RUN] Would delete" and return nil.
//   - File removed:     log "Deleted" and return nil.
//   - File already gone (ErrNotExist): log a warning and return nil.  This
//     handles the race where another process removed the file between
//     discovery and deletion; the desired end-state is already satisfied.
//   - Other error:      log an error and return the error.
//
// </remarks>
func (a *App) deleteFile(file string, reason string) error {
	if a.cfg.DryRun == 1 {
		a.logInfo("[DRY-RUN] Would delete: %s (%s)", file, reason)
		return nil
	}

	err := os.Remove(file)
	if err == nil {
		a.logInfo("Deleted: %s (%s)", file, reason)
		return nil
	}
	if errors.Is(err, os.ErrNotExist) {
		a.logWarn("File already gone (skipping): %s", file)
		return nil
	}

	a.logError("Failed to delete: %s", file)
	return err
}

// cleanupFullBackups applies Grandfather-Father-Son retention to FULL backups.
//
// <summary>
// Cleans files in the FULL directory for a single database backup folder.
// </summary>
//
// <remarks>
// Retention tiers, evaluated in order (first match wins):
//
//  1. Daily:   keep every backup whose embedded date is within
//     FULL_DAILY_RETENTION_DAYS × 86 400 seconds of now.
//  2. Weekly:  keep one backup per GNU %W week bucket (Mon-first, week 00
//     before first Monday) that falls on
//     FULL_WEEKLY_DAY, within FULL_WEEKLY_RETENTION_WEEKS × 7 days of now.
//  3. Monthly: keep the chronologically oldest backup per YYYYMM calendar
//     month within FULL_MONTHLY_RETENTION_MONTHS × 30 days of now.
//  4. Delete everything else.
//
// Setting a tier to 0 disables it (the cutoff becomes "now", so no file
// satisfies the ≥ cutoff check).
//
// The monthly window uses months × 30 days because that is what the original
// Bash arithmetic did; it is an approximation, not exact calendar months.
//
// Cutoff arithmetic uses int64 throughout to prevent overflow on 32-bit
// platforms when retention values are large.
// </remarks>
func (a *App) cleanupFullBackups(backupDir string) error {
	filesDeleted := 0
	filesKept := 0

	fullDir := filepath.Join(backupDir, "FULL")
	if !dirExists(fullDir) {
		a.logDebug("No FULL directory found in %s", backupDir)
		return nil
	}

	a.logInfo("Processing FULL backups in: %s", fullDir)

	todayEpoch := time.Now().Unix()
	weeklyDayNum := getWeekdayNumber(a.cfg.FullWeeklyDay)

	// Use int64 arithmetic throughout to prevent overflow on 32-bit systems when
	// retention values are large (e.g. 25 000 daily days × 86 400 s > max int32).
	dailyCutoffEpoch := todayEpoch - int64(a.cfg.FullDailyRetentionDays)*86400
	weeklyCutoffEpoch := todayEpoch - int64(a.cfg.FullWeeklyRetentionWeeks)*7*86400
	monthlyCutoffEpoch := todayEpoch - int64(a.cfg.FullMonthlyRetentionMonths)*30*86400

	keptMonths := map[string]bool{}
	keptWeeks := map[string]bool{}

	files, err := findRegularFilesBySuffix(fullDir, []string{".bak"}, true)
	if err != nil {
		return err
	}
	sort.Strings(files)

	for _, file := range files {
		if a.isExcluded(file) {
			filesKept++
			continue
		}

		fileDate := extractDateFromFilename(file)
		if fileDate == "" {
			a.logWarn("Could not extract date from: %s", file)
			filesKept++
			continue
		}

		parsedDate, err := parseYYYYMMDDLocal(fileDate)
		if err != nil {
			a.logWarn("Could not parse date for: %s", file)
			filesKept++
			continue
		}

		fileEpoch := parsedDate.Unix()
		fileWeekday := int(parsedDate.Weekday())
		fileMonth := fileDate[0:6]
		fileWeek := fmt.Sprintf("%04d-%02d", parsedDate.Year(), weekNumberMondayFirst(parsedDate))

		shouldKeep := false
		keepReason := ""

		if fileEpoch >= dailyCutoffEpoch {
			shouldKeep = true
			keepReason = "daily retention"
		}

		if !shouldKeep && fileWeekday == weeklyDayNum && fileEpoch >= weeklyCutoffEpoch {
			if !keptWeeks[fileWeek] {
				shouldKeep = true
				keepReason = "weekly retention (" + a.cfg.FullWeeklyDay + ")"
				keptWeeks[fileWeek] = true
			}
		}

		if !shouldKeep && fileEpoch >= monthlyCutoffEpoch {
			if !keptMonths[fileMonth] {
				shouldKeep = true
				keepReason = "monthly retention (oldest in " + fileMonth + ")"
				keptMonths[fileMonth] = true
			}
		}

		if shouldKeep {
			a.logDebug("Keeping: %s (%s)", file, keepReason)
			filesKept++
		} else {
			if err := a.deleteFile(file, "exceeded retention policy"); err != nil {
				return err
			}
			filesDeleted++
		}
	}

	a.logInfo("FULL cleanup: %d deleted, %d kept", filesDeleted, filesKept)
	a.totals.FullDeleted += filesDeleted
	a.totals.FilesDeleted += filesDeleted
	a.totals.FilesKept += filesKept
	return nil
}

// cleanupDiffBackups deletes DIFF .bak files older than DIFF_RETENTION_DAYS.
//
// <summary>
// Cleans files in the DIFF directory for a single database backup folder.
// </summary>
//
// <remarks>
// Age is based on file modification time, matching `stat -c %Y` in Bash.
// The reference time is captured once before the file loop so that all files
// in the same batch are evaluated against the same instant.
// </remarks>
func (a *App) cleanupDiffBackups(backupDir string) error {
	filesDeleted := 0
	filesKept := 0

	diffDir := filepath.Join(backupDir, "DIFF")
	if !dirExists(diffDir) {
		return nil
	}

	a.logInfo("Processing DIFF backups in: %s", diffDir)

	files, err := findRegularFilesBySuffix(diffDir, []string{".bak"}, false)
	if err != nil {
		return err
	}

	nowUnix := time.Now().Unix()

	for _, file := range files {
		if a.isExcluded(file) {
			filesKept++
			continue
		}

		info, err := os.Stat(file)
		if err != nil {
			return err
		}
		fileAgeDays := int((nowUnix - info.ModTime().Unix()) / 86400)

		if fileAgeDays > a.cfg.DiffRetentionDays {
			if err := a.deleteFile(file, fmt.Sprintf("age %d days > %d days", fileAgeDays, a.cfg.DiffRetentionDays)); err != nil {
				return err
			}
			filesDeleted++
		} else {
			filesKept++
		}
	}

	a.logInfo("DIFF cleanup: %d deleted, %d kept", filesDeleted, filesKept)
	a.totals.DiffDeleted += filesDeleted
	a.totals.FilesDeleted += filesDeleted
	a.totals.FilesKept += filesKept
	return nil
}

// cleanupLogBackups deletes LOG .bak and .trn files older than LOG_RETENTION_DAYS.
//
// <summary>
// Cleans files in the LOG directory for a single database backup folder.
// </summary>
//
// <remarks>
// Age is based on file modification time, matching `stat -c %Y` in Bash.
// The reference time is captured once before the file loop so that all files
// in the same batch are evaluated against the same instant.
// </remarks>
func (a *App) cleanupLogBackups(backupDir string) error {
	filesDeleted := 0
	filesKept := 0

	logDir := filepath.Join(backupDir, "LOG")
	if !dirExists(logDir) {
		return nil
	}

	a.logInfo("Processing LOG backups in: %s", logDir)

	files, err := findRegularFilesBySuffix(logDir, []string{".bak", ".trn"}, false)
	if err != nil {
		return err
	}

	nowUnix := time.Now().Unix()

	for _, file := range files {
		if a.isExcluded(file) {
			filesKept++
			continue
		}

		info, err := os.Stat(file)
		if err != nil {
			return err
		}
		fileAgeDays := int((nowUnix - info.ModTime().Unix()) / 86400)

		if fileAgeDays > a.cfg.LogRetentionDays {
			if err := a.deleteFile(file, fmt.Sprintf("age %d days > %d days", fileAgeDays, a.cfg.LogRetentionDays)); err != nil {
				return err
			}
			filesDeleted++
		} else {
			filesKept++
		}
	}

	a.logInfo("LOG cleanup: %d deleted, %d kept", filesDeleted, filesKept)
	a.totals.LogDeleted += filesDeleted
	a.totals.FilesDeleted += filesDeleted
	a.totals.FilesKept += filesKept
	return nil
}

// processDatabaseBackups runs FULL, DIFF, and LOG cleanup for one database directory.
//
// <summary>
// Processes one database backup folder.
// </summary>
func (a *App) processDatabaseBackups(dbDir string) error {
	a.logInfo("Processing database: %s", filepath.Base(dbDir))

	if err := a.cleanupFullBackups(dbDir); err != nil {
		return err
	}
	if err := a.cleanupDiffBackups(dbDir); err != nil {
		return err
	}
	if err := a.cleanupLogBackups(dbDir); err != nil {
		return err
	}
	return nil
}

// findAndProcessBackups discovers database backup directories and processes each once.
//
// <summary>
// Walks BACKUP_PATH looking for FULL, DIFF, or LOG directories.
// </summary>
//
// <remarks>
// Discovery algorithm:
//  1. Walk the entire tree looking for directories named FULL, DIFF, or LOG.
//  2. For each such directory, take its parent as the candidate database root.
//  3. Verify the parent actually has at least one of FULL / DIFF / LOG.
//  4. Create a .cleanup_processed marker in the parent after successful processing so
//     that other siblings (e.g. the DIFF subdir when FULL was already visited)
//     do not trigger a second pass.
//  5. After all directories are processed (or on early return due to error),
//     delete all .cleanup_processed markers via defer.  Using defer instead of
//     an end-of-loop call means stale markers cannot persist across runs if a
//     database fails to process — a bug present in the original Bash script
//     that is fixed here.
//
// </remarks>
func (a *App) findAndProcessBackups(basePath string) error {
	// Always remove marker files, even on early return due to error.
	defer func() { _ = deleteCleanupProcessedMarkers(basePath) }()

	databasesProcessed := 0

	dirs, err := findBackupTypeDirs(basePath)
	if err != nil {
		return err
	}

	for _, dir := range dirs {
		parentDir := filepath.Dir(dir)

		if dirExists(filepath.Join(parentDir, "FULL")) || dirExists(filepath.Join(parentDir, "DIFF")) || dirExists(filepath.Join(parentDir, "LOG")) {
			marker := filepath.Join(parentDir, ".cleanup_processed")
			if !fileExists(marker) {
				if err := a.processDatabaseBackups(parentDir); err != nil {
					return err
				}
				_ = touch(marker)
				databasesProcessed++
			}
		}
	}

	a.logInfo("Processed %d database backup directories", databasesProcessed)
	a.totals.DatabasesProcessed = databasesProcessed
	return nil
}

// findBackupTypeDirs returns directories named FULL, DIFF, or LOG.
//
// <summary>
// Implements the discovery equivalent of `find "$base_path" -type d \( -name FULL -o -name DIFF -o -name LOG \)`.
// </summary>
func findBackupTypeDirs(basePath string) ([]string, error) {
	var dirs []string
	err := filepath.WalkDir(basePath, func(path string, d os.DirEntry, err error) error {
		// Bash script suppresses discovery errors with 2>/dev/null.
		if err != nil {
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		name := d.Name()
		if name == "FULL" || name == "DIFF" || name == "LOG" {
			dirs = append(dirs, path)
		}
		return nil
	})
	return dirs, err
}

// findRegularFilesBySuffix returns regular files whose names end with one of the supplied suffixes.
//
// <summary>
// Implements the file-discovery parts of the Bash `find` commands.
// </summary>
//
// <remarks>
// The FULL cleanup path suppresses traversal errors like the original pipeline.
// DIFF and LOG cleanup are stricter because the original script would fail on
// `stat` or `rm` errors under `set -e`.
// </remarks>
func findRegularFilesBySuffix(root string, suffixes []string, ignoreWalkErrors bool) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if ignoreWalkErrors {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			if ignoreWalkErrors {
				return nil
			}
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		name := d.Name()
		for _, suffix := range suffixes {
			if strings.HasSuffix(name, suffix) {
				files = append(files, path)
				break
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

// parseYYYYMMDDLocal parses YYYYMMDD as midnight in the local timezone.
//
// <summary>
// Converts a compact backup date into a time.Time value.
// </summary>
//
// <remarks>
// Bash `date -d YYYY-MM-DD +%s` interprets the date in local time.  This uses
// time.Local for the same reason.
// </remarks>
func parseYYYYMMDDLocal(value string) (time.Time, error) {
	if len(value) != 8 {
		return time.Time{}, fmt.Errorf("invalid YYYYMMDD value: %q", value)
	}
	dateString := value[0:4] + "-" + value[4:6] + "-" + value[6:8]
	return time.ParseInLocation("2006-01-02", dateString, time.Local)
}

// weekNumberMondayFirst reproduces GNU date's %W week numbering.
//
// <summary>
// Returns week number with Monday as first day of week and week 00 before first Monday.
// </summary>
//
// <remarks>
// Go's ISOWeek is not the same as GNU `date +%Y-%W`, so this helper implements
// the specific week numbering used by the Bash script.
// </remarks>
func weekNumberMondayFirst(t time.Time) int {
	local := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.Local)
	yearStart := time.Date(local.Year(), time.January, 1, 0, 0, 0, 0, time.Local)
	yday := local.YearDay() - 1
	jan1Weekday := int(yearStart.Weekday()) // Sunday=0, Monday=1, ..., Saturday=6
	firstMondayYDay := (8 - jan1Weekday) % 7
	if yday < firstMondayYDay {
		return 0
	}
	return ((yday - firstMondayYDay) / 7) + 1
}

// deleteCleanupProcessedMarkers removes temporary marker files.
//
// <summary>
// Implements `find "$base_path" -name .cleanup_processed -type f -delete`.
// </summary>
func deleteCleanupProcessedMarkers(basePath string) error {
	return filepath.WalkDir(basePath, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if d.Name() == ".cleanup_processed" {
			_ = os.Remove(path)
		}
		return nil
	})
}

// touch creates or updates a marker file.
//
// <summary>
// Implements the subset of Unix `touch` used for .cleanup_processed markers.
// </summary>
func touch(path string) error {
	now := time.Now()
	if _, err := os.Stat(path); err == nil {
		return os.Chtimes(path, now, now)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o666)
	if err != nil {
		return err
	}
	return file.Close()
}

// fileExists returns true when path exists and is not a directory.
//
// <summary>
// Small filesystem helper used for config files and marker files.
// </summary>
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// dirExists returns true when path exists and is a directory.
//
// <summary>
// Small filesystem helper used for backup directory checks.
// </summary>
func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
