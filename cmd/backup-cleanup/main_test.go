package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// truncToDay returns t as midnight in the local timezone.
func truncToDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.Local)
}

// daysAgo returns midnight of n calendar days before today in local time.
func daysAgo(n int) time.Time {
	return truncToDay(time.Now().AddDate(0, 0, -n))
}

// makeFullBakFile creates an empty FULL .bak file with the given backup date
// embedded in the filename (format: testdb_YYYYMMDD_000000.bak).
// It returns the absolute path.
func makeFullBakFile(t *testing.T, dir string, date time.Time) string {
	t.Helper()
	name := fmt.Sprintf("testdb_%s_000000.bak", date.Format("20060102"))
	path := filepath.Join(dir, name)
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("makeFullBakFile: %v", err)
	}
	f.Close()
	return path
}

// makeFileWithMtime creates an empty file at path and sets its mtime.
// It returns the absolute path.
func makeFileWithMtime(t *testing.T, path string, mtime time.Time) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("makeFileWithMtime mkdir: %v", err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("makeFileWithMtime create: %v", err)
	}
	f.Close()
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("makeFileWithMtime chtimes: %v", err)
	}
	return path
}

// writeTextFile writes UTF-8 text to path, creating parent directories as needed.
func writeTextFile(t *testing.T, path string, content string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("writeTextFile mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writeTextFile write: %v", err)
	}
	return path
}

// newTestApp returns an App with the given Config and an empty Totals.
func newTestApp(cfg Config) *App {
	return &App{cfg: cfg}
}

// defaultFullCfg returns a Config suitable for cleanupFullBackups tests.
func defaultFullCfg() Config {
	return Config{
		FullDailyRetentionDays:     7,
		FullWeeklyRetentionWeeks:   4,
		FullWeeklyDay:              "Sunday",
		FullMonthlyRetentionMonths: 12,
	}
}

// assertExists fails the test when path does not exist on disk.
func assertExists(t *testing.T, path, label string) {
	t.Helper()
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Errorf("expected %s (%s) to exist", label, path)
	}
}

// assertNotExists fails the test when path still exists on disk.
func assertNotExists(t *testing.T, path, label string) {
	t.Helper()
	if _, err := os.Stat(path); err == nil {
		t.Errorf("expected %s (%s) to be deleted", label, path)
	}
}

func TestGetWeekdayNumberMatchesBashDatePercentWConvention(t *testing.T) {
	tests := map[string]int{
		"Sunday":    0,
		"sun":       0,
		"Monday":    1,
		"mon":       1,
		"Tuesday":   2,
		"Wednesday": 3,
		"Thursday":  4,
		"Friday":    5,
		"Saturday":  6,
		"unknown":   0,
	}

	for input, want := range tests {
		if got := getWeekdayNumber(input); got != want {
			t.Fatalf("getWeekdayNumber(%q) = %d, want %d", input, got, want)
		}
	}
}

func TestWeekNumberMondayFirstMatchesGNUDatePercentWExamples(t *testing.T) {
	tests := []struct {
		date string
		want int
	}{
		{"2024-01-01", 1},  // Monday, first day of the first Monday-based week.
		{"2025-01-01", 0},  // Wednesday before the first Monday of 2025.
		{"2025-01-06", 1},  // First Monday of 2025.
		{"2025-12-31", 52}, // Last day of 2025.
	}

	for _, tt := range tests {
		parsed, err := time.ParseInLocation("2006-01-02", tt.date, time.Local)
		if err != nil {
			t.Fatal(err)
		}
		if got := weekNumberMondayFirst(parsed); got != tt.want {
			t.Fatalf("weekNumberMondayFirst(%s) = %d, want %d", tt.date, got, tt.want)
		}
	}
}

func TestWeekNumberMondayFirst_SundayJan1(t *testing.T) {
	// 2023-01-01 is a Sunday: GNU date +%W = 00 (before first Monday of year).
	d, _ := time.ParseInLocation("2006-01-02", "2023-01-01", time.Local)
	if got := weekNumberMondayFirst(d); got != 0 {
		t.Fatalf("2023-01-01 (Sunday) week = %d, want 0", got)
	}
	// 2023-01-02 is the first Monday: GNU date +%W = 01.
	d2, _ := time.ParseInLocation("2006-01-02", "2023-01-02", time.Local)
	if got := weekNumberMondayFirst(d2); got != 1 {
		t.Fatalf("2023-01-02 (Monday) week = %d, want 1", got)
	}
}

func TestWeekNumberMondayFirst_MondayJan1(t *testing.T) {
	// 2024-01-01 is a Monday: GNU date +%W = 01 (it IS the first Monday).
	d, _ := time.ParseInLocation("2006-01-02", "2024-01-01", time.Local)
	if got := weekNumberMondayFirst(d); got != 1 {
		t.Fatalf("2024-01-01 (Monday) week = %d, want 1", got)
	}
}

// ---------------------------------------------------------------------------
// parseYYYYMMDDLocal
// ---------------------------------------------------------------------------

func TestParseYYYYMMDDLocal_ValidDate(t *testing.T) {
	got, err := parseYYYYMMDDLocal("20250115")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := time.Date(2025, time.January, 15, 0, 0, 0, 0, time.Local)
	if !got.Equal(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestParseYYYYMMDDLocal_TooShort(t *testing.T) {
	if _, err := parseYYYYMMDDLocal("2025011"); err == nil {
		t.Fatal("expected error for 7-char input")
	}
}

func TestParseYYYYMMDDLocal_TooLong(t *testing.T) {
	if _, err := parseYYYYMMDDLocal("202501150"); err == nil {
		t.Fatal("expected error for 9-char input")
	}
}

func TestParseYYYYMMDDLocal_InvalidMonth(t *testing.T) {
	if _, err := parseYYYYMMDDLocal("20251301"); err == nil {
		t.Fatal("expected error for month 13")
	}
}

func TestParseYYYYMMDDLocal_InvalidDay(t *testing.T) {
	if _, err := parseYYYYMMDDLocal("20250132"); err == nil {
		t.Fatal("expected error for day 32")
	}
}

// ---------------------------------------------------------------------------
// extractDateFromFilename
// ---------------------------------------------------------------------------

func TestExtractDateFromFilename_MatchesPattern(t *testing.T) {
	// Does not need a real file on disk when the name matches the regex.
	got := extractDateFromFilename("mydb_20250115_230000.bak")
	if got != "20250115" {
		t.Fatalf("got %q, want %q", got, "20250115")
	}
}

func TestExtractDateFromFilename_PatternWithLeadingPath(t *testing.T) {
	got := extractDateFromFilename("/mnt/backup01/remote/MyDB/FULL/MyDB_20231231_235959.bak")
	if got != "20231231" {
		t.Fatalf("got %q, want %q", got, "20231231")
	}
}

func TestExtractDateFromFilename_FallsBackToMtime(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nodate.bak")
	mtime := time.Date(2024, time.March, 5, 0, 0, 0, 0, time.Local)
	makeFileWithMtime(t, path, mtime)

	got := extractDateFromFilename(path)
	if got != "20240305" {
		t.Fatalf("got %q, want %q", got, "20240305")
	}
}

func TestExtractDateFromFilename_MissingFileFallback(t *testing.T) {
	// A file that doesn't exist and doesn't match the pattern → empty string.
	got := extractDateFromFilename("/nonexistent/path/nodate.bak")
	if got != "" {
		t.Fatalf("expected empty string for missing file, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// valueOrDefault / intValueOrDefault
// ---------------------------------------------------------------------------

func TestValueOrDefault_EmptyUsesDefault(t *testing.T) {
	if got := valueOrDefault("", "fallback"); got != "fallback" {
		t.Fatalf("got %q, want %q", got, "fallback")
	}
}

func TestValueOrDefault_NonEmptyPreserved(t *testing.T) {
	if got := valueOrDefault("value", "fallback"); got != "value" {
		t.Fatalf("got %q, want %q", got, "value")
	}
}

func TestIntValueOrDefault_EmptyUsesDefault(t *testing.T) {
	got, err := intValueOrDefault("", 7, "X")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 7 {
		t.Fatalf("got %d, want 7", got)
	}
}

func TestIntValueOrDefault_ValidInteger(t *testing.T) {
	got, err := intValueOrDefault("14", 7, "X")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 14 {
		t.Fatalf("got %d, want 14", got)
	}
}

func TestIntValueOrDefault_InvalidString(t *testing.T) {
	if _, err := intValueOrDefault("abc", 7, "X"); err == nil {
		t.Fatal("expected error for non-numeric value")
	}
}

func TestIntValueOrDefault_NegativeRejected(t *testing.T) {
	if _, err := intValueOrDefault("-1", 7, "X"); err == nil {
		t.Fatal("expected error for negative value")
	}
}

func TestIntValueOrDefault_ZeroAccepted(t *testing.T) {
	got, err := intValueOrDefault("0", 7, "X")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 0 {
		t.Fatalf("got %d, want 0", got)
	}
}

// ---------------------------------------------------------------------------
// envSliceToMap
// ---------------------------------------------------------------------------

func TestEnvSliceToMap_BasicParsing(t *testing.T) {
	input := []string{"FOO=bar", "BAZ=qux=extra", "NOEQUAL"}
	m := envSliceToMap(input)
	if m["FOO"] != "bar" {
		t.Fatalf("FOO = %q, want %q", m["FOO"], "bar")
	}
	// Values may contain '=' — only the first '=' is the separator.
	if m["BAZ"] != "qux=extra" {
		t.Fatalf("BAZ = %q, want %q", m["BAZ"], "qux=extra")
	}
	// Entries without '=' are skipped.
	if _, ok := m["NOEQUAL"]; ok {
		t.Fatal("expected entry without '=' to be skipped")
	}
}

func TestEnvSliceToMap_EmptyValue(t *testing.T) {
	m := envSliceToMap([]string{"EMPTY="})
	v, ok := m["EMPTY"]
	if !ok {
		t.Fatal("expected EMPTY key to exist")
	}
	if v != "" {
		t.Fatalf("EMPTY = %q, want empty string", v)
	}
}

// ---------------------------------------------------------------------------
// isExcluded
// ---------------------------------------------------------------------------

func TestIsExcluded_EmptyPatterns(t *testing.T) {
	app := newTestApp(Config{ExcludePatterns: ""})
	if app.isExcluded("/mnt/backup/db1/FULL/file.bak") {
		t.Fatal("expected false with no patterns")
	}
}

func TestIsExcluded_MatchingPattern(t *testing.T) {
	app := newTestApp(Config{ExcludePatterns: "db1"})
	if !app.isExcluded("/mnt/backup/db1/FULL/file.bak") {
		t.Fatal("expected true when path contains pattern")
	}
}

func TestIsExcluded_NonMatchingPattern(t *testing.T) {
	app := newTestApp(Config{ExcludePatterns: "db2"})
	if app.isExcluded("/mnt/backup/db1/FULL/file.bak") {
		t.Fatal("expected false when path does not contain pattern")
	}
}

func TestIsExcluded_MultiplePatterns_OneMatches(t *testing.T) {
	app := newTestApp(Config{ExcludePatterns: "db99 db1"})
	if !app.isExcluded("/mnt/backup/db1/FULL/file.bak") {
		t.Fatal("expected true when one of multiple patterns matches")
	}
}

func TestIsExcluded_MultiplePatterns_NoneMatch(t *testing.T) {
	app := newTestApp(Config{ExcludePatterns: "db99 db98"})
	if app.isExcluded("/mnt/backup/db1/FULL/file.bak") {
		t.Fatal("expected false when no pattern matches")
	}
}

// ---------------------------------------------------------------------------
// fileExists / dirExists / touch
// ---------------------------------------------------------------------------

func TestFileExists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")

	if fileExists(path) {
		t.Fatal("expected false before creation")
	}
	f, _ := os.Create(path)
	f.Close()
	if !fileExists(path) {
		t.Fatal("expected true after creation")
	}
	// A directory is not a file.
	if fileExists(dir) {
		t.Fatal("expected false for a directory path")
	}
}

func TestDirExists(t *testing.T) {
	dir := t.TempDir()

	if !dirExists(dir) {
		t.Fatal("expected true for a known directory")
	}
	if dirExists(filepath.Join(dir, "nope")) {
		t.Fatal("expected false for a non-existent path")
	}
	// A regular file is not a directory.
	p := filepath.Join(dir, "file.txt")
	f, _ := os.Create(p)
	f.Close()
	if dirExists(p) {
		t.Fatal("expected false for a regular file path")
	}
}

func TestTouch_CreatesThenUpdates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "marker")

	if err := touch(path); err != nil {
		t.Fatalf("touch (create): %v", err)
	}
	if !fileExists(path) {
		t.Fatal("expected marker file to exist after touch")
	}

	info1, _ := os.Stat(path)
	time.Sleep(10 * time.Millisecond)
	if err := touch(path); err != nil {
		t.Fatalf("touch (update): %v", err)
	}
	info2, _ := os.Stat(path)
	if !info2.ModTime().After(info1.ModTime()) {
		t.Fatal("expected mtime to advance on second touch")
	}
}

// ---------------------------------------------------------------------------
// deleteFile
// ---------------------------------------------------------------------------

func TestDeleteFile_DryRun_DoesNotDeleteFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "old.bak")
	f, _ := os.Create(path)
	f.Close()

	app := newTestApp(Config{DryRun: 1})
	if err := app.deleteFile(path, "test reason"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertExists(t, path, "bak file")
}

func TestDeleteFile_ActualDeletion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "old.bak")
	f, _ := os.Create(path)
	f.Close()

	app := newTestApp(Config{DryRun: 0})
	if err := app.deleteFile(path, "test reason"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertNotExists(t, path, "bak file")
}

func TestDeleteFile_AlreadyGone_ReturnsNil(t *testing.T) {
	app := newTestApp(Config{DryRun: 0})
	// Deleting a non-existent file must return nil (race-condition tolerance).
	if err := app.deleteFile("/nonexistent/path/file.bak", "test"); err != nil {
		t.Fatalf("expected nil for already-gone file, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// parseJSONConfig
// ---------------------------------------------------------------------------

func TestParseJSONConfig_SupportedTypesAndIgnoredNestedValues(t *testing.T) {
	dir := t.TempDir()
	path := writeTextFile(t, filepath.Join(dir, "cfg.json"), `{
  "BACKUP_PATH": "/tmp/json-backups",
  "CLEANUP_ENABLED": 1,
  "DRY_RUN": true,
  "LOG_RETENTION_DAYS": 7.5,
  "NESTED": {"x": 1},
  "NULL_VALUE": null
}`)

	got, err := parseJSONConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got["BACKUP_PATH"] != "/tmp/json-backups" {
		t.Fatalf("BACKUP_PATH = %q, want %q", got["BACKUP_PATH"], "/tmp/json-backups")
	}
	if got["CLEANUP_ENABLED"] != "1" {
		t.Fatalf("CLEANUP_ENABLED = %q, want %q", got["CLEANUP_ENABLED"], "1")
	}
	if got["DRY_RUN"] != "1" {
		t.Fatalf("DRY_RUN = %q, want %q", got["DRY_RUN"], "1")
	}
	if got["LOG_RETENTION_DAYS"] != "7.5" {
		t.Fatalf("LOG_RETENTION_DAYS = %q, want %q", got["LOG_RETENTION_DAYS"], "7.5")
	}
	if _, exists := got["NESTED"]; exists {
		t.Fatal("NESTED must be ignored for complex JSON values")
	}
	if _, exists := got["NULL_VALUE"]; exists {
		t.Fatal("NULL_VALUE must be ignored for null JSON values")
	}
}

func TestParseJSONConfig_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := writeTextFile(t, filepath.Join(dir, "cfg.json"), `{"BACKUP_PATH":`)

	if _, err := parseJSONConfig(path); err == nil {
		t.Fatal("expected parse error for invalid JSON")
	}
}

// ---------------------------------------------------------------------------
// loadConfigAndParseArgs
// ---------------------------------------------------------------------------

func TestLoadConfigAndParseArgs_DefaultValues(t *testing.T) {
	// Clear any conflicting env vars that the host might have set.
	for _, k := range []string{
		"BACKUP_PATH", "LOG_TAG", "PULSE_BACKUP_HOST_ID", "CLEANUP_PULSE_SUBJECT",
		"CLEANUP_ENABLED", "FULL_DAILY_RETENTION_DAYS", "FULL_WEEKLY_RETENTION_WEEKS",
		"FULL_WEEKLY_DAY", "FULL_MONTHLY_RETENTION_MONTHS",
		"DIFF_RETENTION_DAYS", "LOG_RETENTION_DAYS", "EXCLUDE_PATTERNS",
		"CONFIG_FILE",
	} {
		t.Setenv(k, "")
	}

	cfg, exitNow, code, err := loadConfigAndParseArgs([]string{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exitNow {
		t.Fatal("unexpected exitNow=true for empty args")
	}
	if code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}
	if cfg.BackupPath != "/mnt/backup01/remote" {
		t.Errorf("BackupPath = %q", cfg.BackupPath)
	}
	if cfg.LogTag != "backup-cleanup" {
		t.Errorf("LogTag = %q", cfg.LogTag)
	}
	if cfg.FullDailyRetentionDays != 7 {
		t.Errorf("FullDailyRetentionDays = %d", cfg.FullDailyRetentionDays)
	}
	if cfg.FullWeeklyRetentionWeeks != 4 {
		t.Errorf("FullWeeklyRetentionWeeks = %d", cfg.FullWeeklyRetentionWeeks)
	}
	if cfg.FullWeeklyDay != "Sunday" {
		t.Errorf("FullWeeklyDay = %q", cfg.FullWeeklyDay)
	}
	if cfg.FullMonthlyRetentionMonths != 12 {
		t.Errorf("FullMonthlyRetentionMonths = %d", cfg.FullMonthlyRetentionMonths)
	}
	if cfg.DiffRetentionDays != 14 {
		t.Errorf("DiffRetentionDays = %d", cfg.DiffRetentionDays)
	}
	if cfg.LogRetentionDays != 7 {
		t.Errorf("LogRetentionDays = %d", cfg.LogRetentionDays)
	}
	if cfg.DryRun != 0 {
		t.Errorf("DryRun = %d after parsing empty args, want 0", cfg.DryRun)
	}
	if cfg.Debug != 0 {
		t.Errorf("Debug = %d after parsing empty args, want 0", cfg.Debug)
	}
}

func TestLoadConfigAndParseArgs_BackupPathFlag(t *testing.T) {
	t.Setenv("CONFIG_FILE", "")
	cfg, _, _, err := loadConfigAndParseArgs([]string{"--backup-path", "/tmp/mybackups"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.BackupPath != "/tmp/mybackups" {
		t.Fatalf("BackupPath = %q, want %q", cfg.BackupPath, "/tmp/mybackups")
	}
}

func TestLoadConfigAndParseArgs_DryRunFlag(t *testing.T) {
	t.Setenv("CONFIG_FILE", "")
	cfg, _, _, err := loadConfigAndParseArgs([]string{"--dry-run"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.DryRun != 1 {
		t.Fatalf("DryRun = %d, want 1", cfg.DryRun)
	}
}

func TestLoadConfigAndParseArgs_DebugFlag(t *testing.T) {
	t.Setenv("CONFIG_FILE", "")
	cfg, _, _, err := loadConfigAndParseArgs([]string{"--debug"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Debug != 1 {
		t.Fatalf("Debug = %d, want 1", cfg.Debug)
	}
}

func TestLoadConfigAndParseArgs_VersionFlagExitsZero(t *testing.T) {
	t.Setenv("CONFIG_FILE", "")
	_, exitNow, code, err := loadConfigAndParseArgs([]string{"--version"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !exitNow {
		t.Fatal("expected exitNow=true for --version")
	}
	if code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}
}

func TestLoadConfigAndParseArgs_HelpFlagExitsZero(t *testing.T) {
	t.Setenv("CONFIG_FILE", "")
	for _, flag := range []string{"-h", "--help"} {
		_, exitNow, code, err := loadConfigAndParseArgs([]string{flag})
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", flag, err)
		}
		if !exitNow {
			t.Fatalf("%s: expected exitNow=true", flag)
		}
		if code != 0 {
			t.Fatalf("%s: code = %d, want 0", flag, code)
		}
	}
}

func TestLoadConfigAndParseArgs_UnknownFlagExitsOne(t *testing.T) {
	t.Setenv("CONFIG_FILE", "")
	_, exitNow, code, _ := loadConfigAndParseArgs([]string{"--unknown-flag"})
	if !exitNow {
		t.Fatal("expected exitNow=true for unknown flag")
	}
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
}

func TestLoadConfigAndParseArgs_MissingBackupPathValue(t *testing.T) {
	t.Setenv("CONFIG_FILE", "")
	_, _, _, err := loadConfigAndParseArgs([]string{"--backup-path"})
	if err == nil {
		t.Fatal("expected error for missing --backup-path value")
	}
}

func TestLoadConfigAndParseArgs_InvalidIntegerEnv(t *testing.T) {
	t.Setenv("CONFIG_FILE", "")
	t.Setenv("DIFF_RETENTION_DAYS", "notanumber")
	defer t.Setenv("DIFF_RETENTION_DAYS", "")
	_, _, _, err := loadConfigAndParseArgs([]string{})
	if err == nil {
		t.Fatal("expected error for non-numeric DIFF_RETENTION_DAYS")
	}
}

func TestLoadConfigAndParseArgs_NegativeIntegerEnv(t *testing.T) {
	t.Setenv("CONFIG_FILE", "")
	t.Setenv("FULL_DAILY_RETENTION_DAYS", "-1")
	defer t.Setenv("FULL_DAILY_RETENTION_DAYS", "")
	_, _, _, err := loadConfigAndParseArgs([]string{})
	if err == nil {
		t.Fatal("expected error for negative FULL_DAILY_RETENTION_DAYS")
	}
}

// DRY_RUN set in environment must NOT enable dry-run; only --dry-run should.
// The Bash script resets DRY_RUN to 0 after loading config/defaults.
func TestLoadConfigAndParseArgs_DryRunEnvIgnored(t *testing.T) {
	t.Setenv("CONFIG_FILE", "")
	t.Setenv("DRY_RUN", "1")
	defer t.Setenv("DRY_RUN", "")
	cfg, _, _, err := loadConfigAndParseArgs([]string{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.DryRun != 0 {
		t.Fatal("DRY_RUN env must not enable dry-run; only --dry-run flag should")
	}
}

func TestLoadConfigAndParseArgs_JSONConfigApplied(t *testing.T) {
	dir := t.TempDir()
	cfgFile := writeTextFile(t, filepath.Join(dir, "backup-cleanup.json"), `{
  "BACKUP_PATH": "/json/path",
  "CLEANUP_ENABLED": 1,
  "FULL_DAILY_RETENTION_DAYS": 3,
  "FULL_WEEKLY_RETENTION_WEEKS": 2,
  "FULL_WEEKLY_DAY": "Saturday",
  "FULL_MONTHLY_RETENTION_MONTHS": 6,
  "DIFF_RETENTION_DAYS": 11,
  "LOG_RETENTION_DAYS": 9
}`)

	t.Setenv("CONFIG_FILE", cfgFile)
	t.Setenv("BACKUP_PATH", "/env/path")

	cfg, exitNow, code, err := loadConfigAndParseArgs([]string{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exitNow {
		t.Fatal("unexpected exitNow=true")
	}
	if code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}
	if cfg.BackupPath != "/json/path" {
		t.Fatalf("BackupPath = %q, want %q", cfg.BackupPath, "/json/path")
	}
	if cfg.FullWeeklyDay != "Saturday" {
		t.Fatalf("FullWeeklyDay = %q, want %q", cfg.FullWeeklyDay, "Saturday")
	}
	if cfg.DiffRetentionDays != 11 {
		t.Fatalf("DiffRetentionDays = %d, want 11", cfg.DiffRetentionDays)
	}
}

func TestLoadConfigAndParseArgs_JSONConfigAndCLIFlagPrecedence(t *testing.T) {
	dir := t.TempDir()
	cfgFile := writeTextFile(t, filepath.Join(dir, "backup-cleanup.json"), `{
  "BACKUP_PATH": "/json/path"
}`)
	t.Setenv("CONFIG_FILE", cfgFile)

	cfg, _, _, err := loadConfigAndParseArgs([]string{"--backup-path", "/cli/path"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.BackupPath != "/cli/path" {
		t.Fatalf("BackupPath = %q, want %q", cfg.BackupPath, "/cli/path")
	}
}

func TestLoadConfigAndParseArgs_InvalidJSONConfigFile(t *testing.T) {
	dir := t.TempDir()
	cfgFile := writeTextFile(t, filepath.Join(dir, "backup-cleanup.json"), `{"BACKUP_PATH":`)
	t.Setenv("CONFIG_FILE", cfgFile)

	_, _, _, err := loadConfigAndParseArgs([]string{})
	if err == nil {
		t.Fatal("expected error for invalid JSON config")
	}
}

func TestLoadConfigAndParseArgs_ShellConfigApplied(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not found")
	}

	dir := t.TempDir()
	cfgFile := writeTextFile(t, filepath.Join(dir, "backup-cleanup.conf"), `
PULSE_BACKUP_HOST_ID="shell-host"
BACKUP_PATH="/shell/path"
CLEANUP_PULSE_SUBJECT="backup/${PULSE_BACKUP_HOST_ID}/cleanup"
FULL_WEEKLY_DAY="Tuesday"
`)

	t.Setenv("CONFIG_FILE", cfgFile)
	t.Setenv("BACKUP_PATH", "/env/path")

	cfg, exitNow, code, err := loadConfigAndParseArgs([]string{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exitNow {
		t.Fatal("unexpected exitNow=true")
	}
	if code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}
	if cfg.BackupPath != "/shell/path" {
		t.Fatalf("BackupPath = %q, want %q", cfg.BackupPath, "/shell/path")
	}
	if cfg.CleanupPulseSubject != "backup/shell-host/cleanup" {
		t.Fatalf("CleanupPulseSubject = %q, want %q", cfg.CleanupPulseSubject, "backup/shell-host/cleanup")
	}
	if cfg.FullWeeklyDay != "Tuesday" {
		t.Fatalf("FullWeeklyDay = %q, want %q", cfg.FullWeeklyDay, "Tuesday")
	}
}

// ---------------------------------------------------------------------------
// cleanupFullBackups
// ---------------------------------------------------------------------------

func TestCleanupFullBackups_NoFullDirectory(t *testing.T) {
	dir := t.TempDir()
	// No FULL subdirectory created.
	app := newTestApp(defaultFullCfg())
	if err := app.cleanupFullBackups(dir); err != nil {
		t.Fatalf("expected nil for missing FULL dir, got: %v", err)
	}
	if app.totals.FilesDeleted != 0 || app.totals.FilesKept != 0 {
		t.Fatal("expected zero totals when FULL dir absent")
	}
}

func TestCleanupFullBackups_EmptyFullDirectory(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "FULL"), 0o755)
	app := newTestApp(defaultFullCfg())
	if err := app.cleanupFullBackups(dir); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if app.totals.FilesDeleted != 0 || app.totals.FilesKept != 0 {
		t.Fatal("expected zero totals for empty FULL dir")
	}
}

// TestCleanupFullBackups_DailyRetention verifies that all FULL backups whose
// embedded date falls within the daily window are kept.  Weekly and monthly
// retention are disabled (set to 0) so only the daily tier fires.
func TestCleanupFullBackups_DailyRetention(t *testing.T) {
	dir := t.TempDir()
	fullDir := filepath.Join(dir, "FULL")
	os.MkdirAll(fullDir, 0o755)

	cfg := Config{
		FullDailyRetentionDays:     7,
		FullWeeklyRetentionWeeks:   0, // disabled
		FullMonthlyRetentionMonths: 0, // disabled
		FullWeeklyDay:              "Sunday",
	}
	app := newTestApp(cfg)

	// Days 1–6 ago: safely inside the 7-day daily window.
	var kept []string
	for _, d := range []int{1, 2, 3, 4, 5, 6} {
		kept = append(kept, makeFullBakFile(t, fullDir, daysAgo(d)))
	}
	// Days 8–14 ago: safely outside the 7-day window; other tiers are disabled
	// so these should all be deleted.
	var deleted []string
	for _, d := range []int{8, 10, 12, 14} {
		deleted = append(deleted, makeFullBakFile(t, fullDir, daysAgo(d)))
	}

	if err := app.cleanupFullBackups(dir); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, f := range kept {
		assertExists(t, f, "daily-window file")
	}
	for _, f := range deleted {
		assertNotExists(t, f, "outside-daily-window file")
	}
	if app.totals.FilesKept != len(kept) {
		t.Errorf("FilesKept = %d, want %d", app.totals.FilesKept, len(kept))
	}
	if app.totals.FilesDeleted != len(deleted) {
		t.Errorf("FilesDeleted = %d, want %d", app.totals.FilesDeleted, len(deleted))
	}
	if app.totals.FullDeleted != len(deleted) {
		t.Errorf("FullDeleted = %d, want %d", app.totals.FullDeleted, len(deleted))
	}
}

// TestCleanupFullBackups_WeeklyRetention verifies that one FULL backup per
// calendar week that falls on the configured weekday is kept within the weekly
// window, and that files on other weekdays within the same window are deleted.
func TestCleanupFullBackups_WeeklyRetention(t *testing.T) {
	dir := t.TempDir()
	fullDir := filepath.Join(dir, "FULL")
	os.MkdirAll(fullDir, 0o755)

	const weeklyDay = time.Sunday
	cfg := Config{
		FullDailyRetentionDays:     0, // disabled
		FullWeeklyRetentionWeeks:   4, // 28-day window
		FullMonthlyRetentionMonths: 0, // disabled
		FullWeeklyDay:              "Sunday",
	}
	app := newTestApp(cfg)

	// Collect Sundays in the 1–27 day range.
	//
	// We intentionally avoid exactly 28 days because the implementation (matching
	// the original Bash script) compares a midnight file timestamp to a cutoff
	// built from "now", which includes time-of-day.
	var sundayFiles []string
	for d := 1; d <= 27; d++ {
		if daysAgo(d).Weekday() == weeklyDay {
			sundayFiles = append(sundayFiles, makeFullBakFile(t, fullDir, daysAgo(d)))
		}
	}
	if len(sundayFiles) == 0 {
		t.Skip("no Sundays in the 1–27 day window; skipping")
	}

	// Collect up to 3 non-Sunday files inside the same window.
	var nonSundayFiles []string
	for d := 1; d <= 27 && len(nonSundayFiles) < 3; d++ {
		if daysAgo(d).Weekday() != weeklyDay {
			nonSundayFiles = append(nonSundayFiles, makeFullBakFile(t, fullDir, daysAgo(d)))
		}
	}

	if err := app.cleanupFullBackups(dir); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Every Sunday within the window must be kept (one per unique week).
	for _, f := range sundayFiles {
		assertExists(t, f, "Sunday file (weekly retention)")
	}
	// Non-Sunday files in the window with daily and monthly disabled must be deleted.
	for _, f := range nonSundayFiles {
		assertNotExists(t, f, "non-Sunday file in weekly window")
	}
}

// TestCleanupFullBackups_WeeklyRetention_OnlyOldestPerWeek verifies that when
// two files share the same Sunday date, only the lexicographically earliest
// (oldest) is kept.
func TestCleanupFullBackups_WeeklyRetention_OnlyOldestPerWeek(t *testing.T) {
	dir := t.TempDir()
	fullDir := filepath.Join(dir, "FULL")
	os.MkdirAll(fullDir, 0o755)

	// Find the most recent past Sunday (1–7 days ago).
	var lastSunday time.Time
	for d := 1; d <= 7; d++ {
		if daysAgo(d).Weekday() == time.Sunday {
			lastSunday = daysAgo(d)
			break
		}
	}
	if lastSunday.IsZero() {
		t.Skip("no Sunday found in the last 7 days")
	}

	cfg := Config{
		FullDailyRetentionDays:     0,
		FullWeeklyRetentionWeeks:   4,
		FullMonthlyRetentionMonths: 0,
		FullWeeklyDay:              "Sunday",
	}
	app := newTestApp(cfg)

	// Two files with the same YYYYMMDD Sunday date but different prefix so both
	// can exist on disk simultaneously.
	older := filepath.Join(fullDir, fmt.Sprintf("db1_%s_010000.bak", lastSunday.Format("20060102")))
	newer := filepath.Join(fullDir, fmt.Sprintf("db2_%s_230000.bak", lastSunday.Format("20060102")))
	for _, p := range []string{older, newer} {
		f, _ := os.Create(p)
		f.Close()
	}

	if err := app.cleanupFullBackups(dir); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// After ascending sort, db1_… comes before db2_…, so db1 (older sort key)
	// is the weekly representative.
	assertExists(t, older, "first (oldest-sorted) Sunday file")
	assertNotExists(t, newer, "second Sunday file for the same week")
}

// TestCleanupFullBackups_MonthlyRetention verifies that the oldest FULL backup
// per calendar month is kept within the monthly window while additional files
// in the same month are deleted.
func TestCleanupFullBackups_MonthlyRetention(t *testing.T) {
	dir := t.TempDir()
	fullDir := filepath.Join(dir, "FULL")
	os.MkdirAll(fullDir, 0o755)

	cfg := Config{
		FullDailyRetentionDays:     0, // disabled
		FullWeeklyRetentionWeeks:   0, // disabled
		FullMonthlyRetentionMonths: 3, // 90-day window
		FullWeeklyDay:              "Sunday",
	}
	app := newTestApp(cfg)

	// Find two dates in the same calendar month, both within 88 days.
	var older, newer time.Time
	for d := 10; d <= 80; d++ {
		candidate := daysAgo(d)
		prev := daysAgo(d + 3)
		if candidate.Month() == prev.Month() && candidate.Year() == prev.Year() {
			newer = candidate // d days ago  (more recent)
			older = prev      // d+3 days ago (older date = earlier in sort)
			break
		}
	}
	if older.IsZero() {
		t.Skip("could not find two dates in the same calendar month within the window")
	}

	olderFile := makeFullBakFile(t, fullDir, older)
	newerFile := makeFullBakFile(t, fullDir, newer)

	if err := app.cleanupFullBackups(dir); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Ascending sort: older file (earlier YYYYMMDD) is processed first and kept
	// as the monthly representative.
	assertExists(t, olderFile, "oldest-in-month file")
	assertNotExists(t, newerFile, "second file in the same calendar month")
}

// TestCleanupFullBackups_AllWindowsExpired verifies that files outside all
// retention tiers are deleted.
func TestCleanupFullBackups_AllWindowsExpired(t *testing.T) {
	dir := t.TempDir()
	fullDir := filepath.Join(dir, "FULL")
	os.MkdirAll(fullDir, 0o755)

	cfg := Config{
		FullDailyRetentionDays:     1,
		FullWeeklyRetentionWeeks:   1,
		FullMonthlyRetentionMonths: 1, // 30-day window
		FullWeeklyDay:              "Sunday",
	}
	app := newTestApp(cfg)

	// 35+ days old → outside all retention windows.
	var expired []string
	for _, d := range []int{35, 50, 60} {
		expired = append(expired, makeFullBakFile(t, fullDir, daysAgo(d)))
	}

	if err := app.cleanupFullBackups(dir); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, f := range expired {
		assertNotExists(t, f, "expired file")
	}
}

// TestCleanupFullBackups_ExcludedFiles verifies that files matching the
// exclusion pattern are kept regardless of age.
func TestCleanupFullBackups_ExcludedFiles(t *testing.T) {
	dir := t.TempDir()
	fullDir := filepath.Join(dir, "FULL")
	os.MkdirAll(fullDir, 0o755)

	cfg := Config{
		FullDailyRetentionDays:     0,
		FullWeeklyRetentionWeeks:   0,
		FullMonthlyRetentionMonths: 0,
		FullWeeklyDay:              "Sunday",
		ExcludePatterns:            "keep-this",
	}
	app := newTestApp(cfg)

	path := filepath.Join(fullDir, "keep-this_20230101_000000.bak")
	f, _ := os.Create(path)
	f.Close()

	if err := app.cleanupFullBackups(dir); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertExists(t, path, "excluded file")
	if app.totals.FilesKept != 1 {
		t.Errorf("FilesKept = %d, want 1", app.totals.FilesKept)
	}
}

// TestCleanupFullBackups_UnparseableName verifies that a .bak file with an
// unrecognised filename format falls back to mtime without crashing.
func TestCleanupFullBackups_UnparseableName(t *testing.T) {
	dir := t.TempDir()
	fullDir := filepath.Join(dir, "FULL")
	os.MkdirAll(fullDir, 0o755)

	cfg := Config{
		FullDailyRetentionDays:     7,
		FullWeeklyRetentionWeeks:   0,
		FullMonthlyRetentionMonths: 0,
		FullWeeklyDay:              "Sunday",
	}
	app := newTestApp(cfg)

	// Create a file with a non-standard name; mtime is today so it will be
	// within the daily window via the mtime fallback.
	path := makeFileWithMtime(t, filepath.Join(fullDir, "nodateformat.bak"), time.Now())

	if err := app.cleanupFullBackups(dir); err != nil {
		t.Fatalf("expected no error for non-standard filename, got: %v", err)
	}
	// The file was just created (today) so the mtime fallback returns today's
	// date; daily window is 7 days, so the file should be kept.
	assertExists(t, path, "recent file with non-standard name")
}

// TestCleanupFullBackups_DryRun verifies that dry-run counts would-be
// deletions without removing any files from disk.
func TestCleanupFullBackups_DryRun(t *testing.T) {
	dir := t.TempDir()
	fullDir := filepath.Join(dir, "FULL")
	os.MkdirAll(fullDir, 0o755)

	cfg := Config{
		FullDailyRetentionDays:     0,
		FullWeeklyRetentionWeeks:   0,
		FullMonthlyRetentionMonths: 0,
		FullWeeklyDay:              "Sunday",
		DryRun:                     1,
	}
	app := newTestApp(cfg)

	path := makeFullBakFile(t, fullDir, daysAgo(30))

	if err := app.cleanupFullBackups(dir); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertExists(t, path, "dry-run file must remain on disk")
	if app.totals.FilesDeleted != 1 {
		t.Errorf("FilesDeleted = %d, want 1 (counter must reflect would-be deletion)", app.totals.FilesDeleted)
	}
}

// ---------------------------------------------------------------------------
// cleanupDiffBackups
// ---------------------------------------------------------------------------

func TestCleanupDiffBackups_NoDiffDirectory(t *testing.T) {
	dir := t.TempDir()
	app := newTestApp(Config{DiffRetentionDays: 14})
	if err := app.cleanupDiffBackups(dir); err != nil {
		t.Fatalf("expected nil for missing DIFF dir, got: %v", err)
	}
}

func TestCleanupDiffBackups_AgeBasedDeletion(t *testing.T) {
	dir := t.TempDir()
	diffDir := filepath.Join(dir, "DIFF")
	os.MkdirAll(diffDir, 0o755)

	app := newTestApp(Config{DiffRetentionDays: 14})

	// 5-day-old file → kept.
	keptPath := makeFileWithMtime(t, filepath.Join(diffDir, "new.bak"),
		time.Now().Add(-5*24*time.Hour))
	// 20-day-old file → deleted.
	deletedPath := makeFileWithMtime(t, filepath.Join(diffDir, "old.bak"),
		time.Now().Add(-20*24*time.Hour))

	if err := app.cleanupDiffBackups(dir); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertExists(t, keptPath, "new DIFF file")
	assertNotExists(t, deletedPath, "old DIFF file")
	if app.totals.FilesKept != 1 {
		t.Errorf("FilesKept = %d, want 1", app.totals.FilesKept)
	}
	if app.totals.DiffDeleted != 1 {
		t.Errorf("DiffDeleted = %d, want 1", app.totals.DiffDeleted)
	}
}

func TestCleanupDiffBackups_ExcludedFiles(t *testing.T) {
	dir := t.TempDir()
	diffDir := filepath.Join(dir, "DIFF")
	os.MkdirAll(diffDir, 0o755)

	app := newTestApp(Config{DiffRetentionDays: 1, ExcludePatterns: "preserve"})

	path := makeFileWithMtime(t, filepath.Join(diffDir, "preserve_db.bak"),
		time.Now().Add(-30*24*time.Hour))

	if err := app.cleanupDiffBackups(dir); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertExists(t, path, "excluded DIFF file")
}

func TestCleanupDiffBackups_DryRun(t *testing.T) {
	dir := t.TempDir()
	diffDir := filepath.Join(dir, "DIFF")
	os.MkdirAll(diffDir, 0o755)

	app := newTestApp(Config{DiffRetentionDays: 1, DryRun: 1})
	path := makeFileWithMtime(t, filepath.Join(diffDir, "old.bak"),
		time.Now().Add(-30*24*time.Hour))

	if err := app.cleanupDiffBackups(dir); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertExists(t, path, "dry-run DIFF file must remain")
	if app.totals.FilesDeleted != 1 {
		t.Errorf("FilesDeleted = %d, want 1", app.totals.FilesDeleted)
	}
}

// ---------------------------------------------------------------------------
// cleanupLogBackups
// ---------------------------------------------------------------------------

func TestCleanupLogBackups_NoLogDirectory(t *testing.T) {
	dir := t.TempDir()
	app := newTestApp(Config{LogRetentionDays: 7})
	if err := app.cleanupLogBackups(dir); err != nil {
		t.Fatalf("expected nil for missing LOG dir, got: %v", err)
	}
}

func TestCleanupLogBackups_BakAndTrnFilesDeleted(t *testing.T) {
	dir := t.TempDir()
	logDir := filepath.Join(dir, "LOG")
	os.MkdirAll(logDir, 0o755)

	app := newTestApp(Config{LogRetentionDays: 7})

	oldBak := makeFileWithMtime(t, filepath.Join(logDir, "old.bak"),
		time.Now().Add(-10*24*time.Hour))
	oldTrn := makeFileWithMtime(t, filepath.Join(logDir, "old.trn"),
		time.Now().Add(-10*24*time.Hour))
	newBak := makeFileWithMtime(t, filepath.Join(logDir, "new.bak"),
		time.Now().Add(-3*24*time.Hour))

	if err := app.cleanupLogBackups(dir); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertNotExists(t, oldBak, "old .bak LOG file")
	assertNotExists(t, oldTrn, "old .trn LOG file")
	assertExists(t, newBak, "new .bak LOG file")
	if app.totals.LogDeleted != 2 {
		t.Errorf("LogDeleted = %d, want 2", app.totals.LogDeleted)
	}
}

func TestCleanupLogBackups_DryRun(t *testing.T) {
	dir := t.TempDir()
	logDir := filepath.Join(dir, "LOG")
	os.MkdirAll(logDir, 0o755)

	app := newTestApp(Config{LogRetentionDays: 1, DryRun: 1})
	path := makeFileWithMtime(t, filepath.Join(logDir, "old.trn"),
		time.Now().Add(-30*24*time.Hour))

	if err := app.cleanupLogBackups(dir); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertExists(t, path, "dry-run LOG file must remain")
}

// ---------------------------------------------------------------------------
// findAndProcessBackups
// ---------------------------------------------------------------------------

func TestFindAndProcessBackups_EmptyDirectory(t *testing.T) {
	dir := t.TempDir()
	app := newTestApp(Config{
		FullDailyRetentionDays: 7, FullWeeklyRetentionWeeks: 4,
		FullMonthlyRetentionMonths: 12, FullWeeklyDay: "Sunday",
		DiffRetentionDays: 14, LogRetentionDays: 7,
	})
	if err := app.findAndProcessBackups(dir); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if app.totals.DatabasesProcessed != 0 {
		t.Errorf("DatabasesProcessed = %d, want 0", app.totals.DatabasesProcessed)
	}
}

func TestFindAndProcessBackups_SingleDatabase(t *testing.T) {
	root := t.TempDir()
	dbDir := filepath.Join(root, "mydb")
	os.MkdirAll(filepath.Join(dbDir, "FULL"), 0o755)
	os.MkdirAll(filepath.Join(dbDir, "DIFF"), 0o755)
	os.MkdirAll(filepath.Join(dbDir, "LOG"), 0o755)

	// 400 days ago is outside the 12-month (360-day) monthly window, so this
	// file should be deleted by the full cleanup.
	oldFull := makeFullBakFile(t, filepath.Join(dbDir, "FULL"), daysAgo(400))
	oldDiff := makeFileWithMtime(t, filepath.Join(dbDir, "DIFF", "old.bak"),
		time.Now().Add(-30*24*time.Hour))

	app := newTestApp(Config{
		FullDailyRetentionDays: 7, FullWeeklyRetentionWeeks: 4,
		FullMonthlyRetentionMonths: 12, FullWeeklyDay: "Sunday",
		DiffRetentionDays: 14, LogRetentionDays: 7,
	})
	if err := app.findAndProcessBackups(root); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if app.totals.DatabasesProcessed != 1 {
		t.Errorf("DatabasesProcessed = %d, want 1", app.totals.DatabasesProcessed)
	}
	assertNotExists(t, oldFull, "old FULL file")
	assertNotExists(t, oldDiff, "old DIFF file")
}

func TestFindAndProcessBackups_MultipleDatabases(t *testing.T) {
	root := t.TempDir()
	for _, db := range []string{"db1", "db2", "db3"} {
		os.MkdirAll(filepath.Join(root, db, "FULL"), 0o755)
	}

	app := newTestApp(Config{
		FullDailyRetentionDays: 7, FullWeeklyRetentionWeeks: 4,
		FullMonthlyRetentionMonths: 12, FullWeeklyDay: "Sunday",
		DiffRetentionDays: 14, LogRetentionDays: 7,
	})
	if err := app.findAndProcessBackups(root); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if app.totals.DatabasesProcessed != 3 {
		t.Errorf("DatabasesProcessed = %d, want 3", app.totals.DatabasesProcessed)
	}
}

// TestFindAndProcessBackups_MarkersCleanedUpOnSuccess verifies that no stale
// .cleanup_processed marker files remain after a successful run.
func TestFindAndProcessBackups_MarkersCleanedUpOnSuccess(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "mydb", "FULL"), 0o755)

	app := newTestApp(Config{
		FullDailyRetentionDays: 7, FullWeeklyRetentionWeeks: 4,
		FullMonthlyRetentionMonths: 12, FullWeeklyDay: "Sunday",
		DiffRetentionDays: 14, LogRetentionDays: 7,
	})
	if err := app.findAndProcessBackups(root); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, _ error) error {
		if d != nil && d.Name() == ".cleanup_processed" {
			t.Errorf("stale marker left at: %s", path)
		}
		return nil
	})
}

// TestFindAndProcessBackups_EachDatabaseProcessedOnce verifies that a
// database directory containing both FULL and DIFF subdirectories is counted
// and processed exactly once, not once per subdirectory.
func TestFindAndProcessBackups_EachDatabaseProcessedOnce(t *testing.T) {
	root := t.TempDir()
	dbDir := filepath.Join(root, "mydb")
	os.MkdirAll(filepath.Join(dbDir, "FULL"), 0o755)
	os.MkdirAll(filepath.Join(dbDir, "DIFF"), 0o755)

	app := newTestApp(Config{
		FullDailyRetentionDays: 7, FullWeeklyRetentionWeeks: 4,
		FullMonthlyRetentionMonths: 12, FullWeeklyDay: "Sunday",
		DiffRetentionDays: 14, LogRetentionDays: 7,
	})
	if err := app.findAndProcessBackups(root); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if app.totals.DatabasesProcessed != 1 {
		t.Errorf("DatabasesProcessed = %d, want 1 (same DB must not be counted twice)",
			app.totals.DatabasesProcessed)
	}
}

// ---------------------------------------------------------------------------
// App.run() integration tests
// ---------------------------------------------------------------------------

func TestApp_Run_CleanupDisabled(t *testing.T) {
	dir := t.TempDir()
	app := newTestApp(Config{CleanupEnabled: 0, BackupPath: dir})
	if err := app.run(); err != nil {
		t.Fatalf("run with CLEANUP_ENABLED=0 must not error, got: %v", err)
	}
}

func TestApp_Run_BackupPathNotExist(t *testing.T) {
	app := newTestApp(Config{
		CleanupEnabled: 1,
		BackupPath:     "/nonexistent/backup/path",
	})
	if err := app.run(); err == nil {
		t.Fatal("expected error when backup path does not exist")
	}
}

// TestApp_Run_Integration creates a realistic backup tree and asserts the
// complete end-to-end cleanup behavior: old files are deleted, recent files
// are kept, and summary counters are correct.
func TestApp_Run_Integration(t *testing.T) {
	root := t.TempDir()

	// ── db1: FULL + DIFF + LOG ──────────────────────────────────────────────
	db1 := filepath.Join(root, "db1")
	os.MkdirAll(filepath.Join(db1, "FULL"), 0o755)
	os.MkdirAll(filepath.Join(db1, "DIFF"), 0o755)
	os.MkdirAll(filepath.Join(db1, "LOG"), 0o755)

	recentFull := makeFullBakFile(t, filepath.Join(db1, "FULL"), daysAgo(5))
	ancientFull := makeFullBakFile(t, filepath.Join(db1, "FULL"), daysAgo(400))
	recentDiff := makeFileWithMtime(t, filepath.Join(db1, "DIFF", "recent.bak"),
		time.Now().Add(-10*24*time.Hour))
	oldDiff := makeFileWithMtime(t, filepath.Join(db1, "DIFF", "old.bak"),
		time.Now().Add(-20*24*time.Hour))
	recentLog := makeFileWithMtime(t, filepath.Join(db1, "LOG", "recent.trn"),
		time.Now().Add(-3*24*time.Hour))
	oldLog := makeFileWithMtime(t, filepath.Join(db1, "LOG", "old.trn"),
		time.Now().Add(-10*24*time.Hour))

	// ── db2: FULL only ─────────────────────────────────────────────────────
	db2 := filepath.Join(root, "db2")
	os.MkdirAll(filepath.Join(db2, "FULL"), 0o755)
	recentFull2 := makeFullBakFile(t, filepath.Join(db2, "FULL"), daysAgo(2))
	oldFull2 := makeFullBakFile(t, filepath.Join(db2, "FULL"), daysAgo(400))

	app := newTestApp(Config{
		CleanupEnabled:             1,
		BackupPath:                 root,
		LogTag:                     "test",
		FullDailyRetentionDays:     7,
		FullWeeklyRetentionWeeks:   4,
		FullMonthlyRetentionMonths: 12,
		FullWeeklyDay:              "Sunday",
		DiffRetentionDays:          14,
		LogRetentionDays:           7,
	})

	if err := app.run(); err != nil {
		t.Fatalf("run() error: %v", err)
	}

	assertExists(t, recentFull, "recent FULL db1")
	assertExists(t, recentDiff, "recent DIFF db1")
	assertExists(t, recentLog, "recent LOG db1")
	assertExists(t, recentFull2, "recent FULL db2")

	assertNotExists(t, ancientFull, "ancient FULL db1")
	assertNotExists(t, oldDiff, "old DIFF db1")
	assertNotExists(t, oldLog, "old LOG db1")
	assertNotExists(t, oldFull2, "old FULL db2")

	if app.totals.DatabasesProcessed != 2 {
		t.Errorf("DatabasesProcessed = %d, want 2", app.totals.DatabasesProcessed)
	}
}

// TestApp_Run_DryRunDeletesNothing verifies that in dry-run mode no files are
// physically removed while would-be deletions are still reflected in counters.
func TestApp_Run_DryRunDeletesNothing(t *testing.T) {
	root := t.TempDir()
	db1 := filepath.Join(root, "db1")
	os.MkdirAll(filepath.Join(db1, "FULL"), 0o755)

	ancient := makeFullBakFile(t, filepath.Join(db1, "FULL"), daysAgo(400))

	app := newTestApp(Config{
		CleanupEnabled:             1,
		BackupPath:                 root,
		LogTag:                     "test",
		FullDailyRetentionDays:     7,
		FullWeeklyRetentionWeeks:   4,
		FullMonthlyRetentionMonths: 12,
		FullWeeklyDay:              "Sunday",
		DiffRetentionDays:          14,
		LogRetentionDays:           7,
		DryRun:                     1,
	})

	if err := app.run(); err != nil {
		t.Fatalf("run() error: %v", err)
	}

	assertExists(t, ancient, "ancient file must survive dry-run")
	if app.totals.FilesDeleted == 0 {
		t.Error("FilesDeleted counter must be > 0 even in dry-run mode")
	}
}
