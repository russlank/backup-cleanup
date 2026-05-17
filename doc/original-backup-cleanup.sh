#!/usr/bin/env bash
###############################################################################
# backup-cleanup
#
# Deployed by Ansible - DO NOT EDIT MANUALLY
# Source: https://github.com/russlank/ms-sql-backup
#
###############################################################################

set -Eeuo pipefail

###############################################################################
# Configuration
###############################################################################
CONFIG_FILE="${CONFIG_FILE:-/etc/backup-utils/backup-cleanup.conf}"

# Load config if exists
if [ -f "$CONFIG_FILE" ]; then
  # shellcheck source=/dev/null
  source "$CONFIG_FILE"
fi

# Defaults (can be overridden in config file or environment)
BACKUP_PATH="${BACKUP_PATH:-/mnt/backup01/remote}"
LOG_TAG="${LOG_TAG:-backup-cleanup}"
PULSE_BACKUP_HOST_ID="${PULSE_BACKUP_HOST_ID:-pulse.monitor.local}"
CLEANUP_PULSE_SUBJECT="${CLEANUP_PULSE_SUBJECT:-backup/${PULSE_BACKUP_HOST_ID}/cleanup}"

# Cleanup enabled (set to 0 to disable)
CLEANUP_ENABLED="${CLEANUP_ENABLED:-1}"

# FULL backup retention (Grandfather-Father-Son)
FULL_DAILY_RETENTION_DAYS="${FULL_DAILY_RETENTION_DAYS:-7}"
FULL_WEEKLY_RETENTION_WEEKS="${FULL_WEEKLY_RETENTION_WEEKS:-4}"
FULL_WEEKLY_DAY="${FULL_WEEKLY_DAY:-Sunday}"
FULL_MONTHLY_RETENTION_MONTHS="${FULL_MONTHLY_RETENTION_MONTHS:-12}"

# DIFF backup retention
DIFF_RETENTION_DAYS="${DIFF_RETENTION_DAYS:-14}"

# LOG backup retention
LOG_RETENTION_DAYS="${LOG_RETENTION_DAYS:-7}"

# Exclusion patterns (space-separated list of patterns to exclude)
EXCLUDE_PATTERNS="${EXCLUDE_PATTERNS:-}"

# Cleanup summary counters
TOTAL_DATABASES_PROCESSED=0
TOTAL_FULL_DELETED=0
TOTAL_DIFF_DELETED=0
TOTAL_LOG_DELETED=0
TOTAL_FILES_DELETED=0
TOTAL_FILES_KEPT=0
CLEANUP_PULSE_STARTED=0

###############################################################################
# Logging
###############################################################################
log_info() {
  logger -t "$LOG_TAG" -p user.info "$*" 2>/dev/null || true
  echo "[$(date '+%Y-%m-%d %H:%M:%S')] INFO: $*"
}

log_error() {
  logger -t "$LOG_TAG" -p user.err "$*" 2>/dev/null || true
  echo "[$(date '+%Y-%m-%d %H:%M:%S')] ERROR: $*" >&2
}

log_warn() {
  logger -t "$LOG_TAG" -p user.warning "$*" 2>/dev/null || true
  echo "[$(date '+%Y-%m-%d %H:%M:%S')] WARN: $*" >&2
}

log_debug() {
  if [ "${DEBUG:-0}" -eq 1 ]; then
    echo "[$(date '+%Y-%m-%d %H:%M:%S')] DEBUG: $*"
  fi
}

emit_pulse() {
  local metric_name="$1"
  local metric_subject="$2"
  local delta="${3:-1}"

  # Dry-run is intentionally excluded from telemetry so operational signals stay clean.
  if [ "${DRY_RUN:-0}" -eq 1 ]; then
    return 0
  fi

  if ! command -v send-pulse >/dev/null 2>&1; then
    return 0
  fi

  send-pulse "$metric_name" "$metric_subject" "$delta" >/dev/null 2>&1 || true
}

on_error() {
  local exit_code="$1"
  local line_no="$2"
  local command="$3"

  log_error "Error in ${FUNCNAME[2]:-main} at line ${line_no}: \"${command}\" (exit code: ${exit_code})"

  if [ "$CLEANUP_PULSE_STARTED" -eq 1 ]; then
    emit_pulse "job.scheduled.failed" "$CLEANUP_PULSE_SUBJECT"
  fi

  exit "$exit_code"
}

trap 'on_error "$?" "$LINENO" "$BASH_COMMAND"' ERR

###############################################################################
# Help
###############################################################################
print_help() {
  cat <<EOF2
backup-cleanup - Clean up old SQL Server backup files

USAGE:
  backup-cleanup [options]

OPTIONS:
  --backup-path <path>     Path to backup directory (default: /mnt/backup01/remote)
  --dry-run                Show what would be deleted without actually deleting
  --debug                  Enable debug output
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

EOF2
}

###############################################################################
# Parse arguments
###############################################################################
DRY_RUN=0
DEBUG=0

while [ "$#" -gt 0 ]; do
  case "$1" in
    --backup-path)
      BACKUP_PATH="$2"
      shift 2
      ;;
    --dry-run)
      DRY_RUN=1
      shift
      ;;
    --debug)
      DEBUG=1
      shift
      ;;
    -h|--help)
      print_help
      exit 0
      ;;
    *)
      log_error "Unknown argument: $1"
      print_help
      exit 1
      ;;
  esac
done

###############################################################################
# Utility Functions
###############################################################################

# Get weekday number (0=Sunday, 1=Monday, ..., 6=Saturday)
get_weekday_number() {
  local day="$1"
  case "${day,,}" in
    sunday|sun)    echo 0 ;;
    monday|mon)    echo 1 ;;
    tuesday|tue)   echo 2 ;;
    wednesday|wed) echo 3 ;;
    thursday|thu)  echo 4 ;;
    friday|fri)    echo 5 ;;
    saturday|sat)  echo 6 ;;
    *)             echo 0 ;;
  esac
}

# Extract date from backup filename
extract_date_from_filename() {
  local filename="$1"
  local basename
  basename=$(basename "$filename")

  if [[ "$basename" =~ ([0-9]{8})_[0-9]{6}\.bak$ ]]; then
    echo "${BASH_REMATCH[1]}"
  else
    date -r "$filename" '+%Y%m%d' 2>/dev/null || echo ""
  fi
}

# Check if file matches any exclusion pattern
is_excluded() {
  local file="$1"
  local pattern

  if [ -z "$EXCLUDE_PATTERNS" ]; then
    return 1
  fi

  for pattern in $EXCLUDE_PATTERNS; do
    if [[ "$file" == *"$pattern"* ]]; then
      log_debug "File excluded by pattern '$pattern': $file"
      return 0
    fi
  done

  return 1
}

# Delete file (or simulate in dry-run mode)
delete_file() {
  local file="$1"
  local reason="$2"

  if [ "$DRY_RUN" -eq 1 ]; then
    log_info "[DRY-RUN] Would delete: $file ($reason)"
  else
    if rm -f "$file"; then
      log_info "Deleted: $file ($reason)"
    else
      log_error "Failed to delete: $file"
      return 1
    fi
  fi
}

###############################################################################
# Cleanup Functions
###############################################################################

cleanup_full_backups() {
  local backup_dir="$1"
  local files_deleted=0
  local files_kept=0

  log_info "Processing FULL backups in: $backup_dir"

  local full_dir="$backup_dir/FULL"
  if [ ! -d "$full_dir" ]; then
    log_debug "No FULL directory found in $backup_dir"
    return 0
  fi

  local today_epoch
  today_epoch=$(date '+%s')

  local weekly_day_num
  weekly_day_num=$(get_weekday_number "$FULL_WEEKLY_DAY")

  local daily_cutoff_epoch=$((today_epoch - FULL_DAILY_RETENTION_DAYS * 86400))
  local weekly_cutoff_epoch=$((today_epoch - FULL_WEEKLY_RETENTION_WEEKS * 7 * 86400))
  local monthly_cutoff_epoch=$((today_epoch - FULL_MONTHLY_RETENTION_MONTHS * 30 * 86400))

  declare -A kept_months
  declare -A kept_weeks

  while IFS= read -r -d '' file; do
    if is_excluded "$file"; then
      ((++files_kept))
      continue
    fi

    local file_date
    file_date=$(extract_date_from_filename "$file")

    if [ -z "$file_date" ]; then
      log_warn "Could not extract date from: $file"
      ((++files_kept))
      continue
    fi

    local file_epoch
    file_epoch=$(date -d "${file_date:0:4}-${file_date:4:2}-${file_date:6:2}" '+%s' 2>/dev/null)

    if [ -z "$file_epoch" ]; then
      log_warn "Could not parse date for: $file"
      ((++files_kept))
      continue
    fi

    local file_weekday
    file_weekday=$(date -d "${file_date:0:4}-${file_date:4:2}-${file_date:6:2}" '+%w')
    local file_month="${file_date:0:6}"
    local file_week
    file_week=$(date -d "${file_date:0:4}-${file_date:4:2}-${file_date:6:2}" '+%Y-%W')

    local should_keep=0
    local keep_reason=""

    if [ "$file_epoch" -ge "$daily_cutoff_epoch" ]; then
      should_keep=1
      keep_reason="daily retention"
    fi

    if [ "$should_keep" -eq 0 ] && [ "$file_weekday" -eq "$weekly_day_num" ] && [ "$file_epoch" -ge "$weekly_cutoff_epoch" ]; then
      if [ -z "${kept_weeks[$file_week]:-}" ]; then
        should_keep=1
        keep_reason="weekly retention ($FULL_WEEKLY_DAY)"
        kept_weeks[$file_week]=1
      fi
    fi

    if [ "$should_keep" -eq 0 ] && [ "$file_epoch" -ge "$monthly_cutoff_epoch" ]; then
      if [ -z "${kept_months[$file_month]:-}" ]; then
        should_keep=1
        keep_reason="monthly retention (oldest in $file_month)"
        kept_months[$file_month]=1
      fi
    fi

    if [ "$should_keep" -eq 1 ]; then
      log_debug "Keeping: $file ($keep_reason)"
      ((++files_kept))
    else
      delete_file "$file" "exceeded retention policy"
      ((++files_deleted))
    fi

  # done < <(find "$full_dir" -name '*.bak' -type f -print0 | sort -z)
  done < <(find "$full_dir" -type f -name '*.bak' -print0 | sort -z)

  log_info "FULL cleanup: $files_deleted deleted, $files_kept kept"
  TOTAL_FULL_DELETED=$((TOTAL_FULL_DELETED + files_deleted))
  TOTAL_FILES_DELETED=$((TOTAL_FILES_DELETED + files_deleted))
  TOTAL_FILES_KEPT=$((TOTAL_FILES_KEPT + files_kept))
}

cleanup_diff_backups() {
  local backup_dir="$1"
  local files_deleted=0
  local files_kept=0

  local diff_dir="$backup_dir/DIFF"
  if [ ! -d "$diff_dir" ]; then
    return 0
  fi

  log_info "Processing DIFF backups in: $diff_dir"

  while IFS= read -r -d '' file; do
    if is_excluded "$file"; then
      ((++files_kept))
      continue
    fi

    local file_age_days
    file_age_days=$(( ($(date '+%s') - $(stat -c %Y "$file")) / 86400 ))

    if [ "$file_age_days" -gt "$DIFF_RETENTION_DAYS" ]; then
      delete_file "$file" "age $file_age_days days > $DIFF_RETENTION_DAYS days"
      ((++files_deleted))
    else
      ((++files_kept))
    fi

  done < <(find "$diff_dir" -type f -name '*.bak' -print0)

  log_info "DIFF cleanup: $files_deleted deleted, $files_kept kept"
  TOTAL_DIFF_DELETED=$((TOTAL_DIFF_DELETED + files_deleted))
  TOTAL_FILES_DELETED=$((TOTAL_FILES_DELETED + files_deleted))
  TOTAL_FILES_KEPT=$((TOTAL_FILES_KEPT + files_kept))
}

cleanup_log_backups() {
  local backup_dir="$1"
  local files_deleted=0
  local files_kept=0

  local log_dir="$backup_dir/LOG"
  if [ ! -d "$log_dir" ]; then
    return 0
  fi

  log_info "Processing LOG backups in: $log_dir"

  while IFS= read -r -d '' file; do
    if is_excluded "$file"; then
      ((++files_kept))
      continue
    fi

    local file_age_days
    file_age_days=$(( ($(date '+%s') - $(stat -c %Y "$file")) / 86400 ))

    if [ "$file_age_days" -gt "$LOG_RETENTION_DAYS" ]; then
      delete_file "$file" "age $file_age_days days > $LOG_RETENTION_DAYS days"
      ((++files_deleted))
    else
      ((++files_kept))
    fi

  done < <(find "$log_dir" -type f \( -name '*.bak' -o -name '*.trn' \) -print0)

  log_info "LOG cleanup: $files_deleted deleted, $files_kept kept"
  TOTAL_LOG_DELETED=$((TOTAL_LOG_DELETED + files_deleted))
  TOTAL_FILES_DELETED=$((TOTAL_FILES_DELETED + files_deleted))
  TOTAL_FILES_KEPT=$((TOTAL_FILES_KEPT + files_kept))
}

process_database_backups() {
  local db_dir="$1"

  log_info "Processing database: $(basename "$db_dir")"

  cleanup_full_backups "$db_dir"
  cleanup_diff_backups "$db_dir"
  cleanup_log_backups "$db_dir"
}

find_and_process_backups() {
  local base_path="$1"
  local databases_processed=0

  while IFS= read -r -d '' dir; do
    local parent_dir
    parent_dir=$(dirname "$dir")

    if [ -d "$parent_dir/FULL" ] || [ -d "$parent_dir/DIFF" ] || [ -d "$parent_dir/LOG" ]; then
      if [ ! -f "$parent_dir/.cleanup_processed" ]; then
        process_database_backups "$parent_dir"
        touch "$parent_dir/.cleanup_processed" 2>/dev/null || true
        ((++databases_processed))
      fi
    fi
  done < <(find "$base_path" -type d \( -name 'FULL' -o -name 'DIFF' -o -name 'LOG' \) -print0 2>/dev/null)

  find "$base_path" -name '.cleanup_processed' -type f -delete 2>/dev/null || true

  log_info "Processed $databases_processed database backup directories"
  TOTAL_DATABASES_PROCESSED=$databases_processed
}

###############################################################################
# Main
###############################################################################
main() {
  log_info "=========================================="
  log_info "Backup Cleanup Started"
  log_info "=========================================="

  if [ "$CLEANUP_ENABLED" -ne 1 ]; then
    log_warn "Backup cleanup is disabled (CLEANUP_ENABLED=$CLEANUP_ENABLED)"
    emit_pulse "job.scheduled.skipped" "$CLEANUP_PULSE_SUBJECT"
    exit 0
  fi

  emit_pulse "job.scheduled.started" "$CLEANUP_PULSE_SUBJECT"
  CLEANUP_PULSE_STARTED=1

  if [ ! -d "$BACKUP_PATH" ]; then
    log_error "Backup path does not exist: $BACKUP_PATH"
    emit_pulse "job.scheduled.failed" "$CLEANUP_PULSE_SUBJECT"
    exit 1
  fi

  log_info "Backup path: $BACKUP_PATH"
  log_info "Retention policy:"
  log_info "  FULL daily:   $FULL_DAILY_RETENTION_DAYS days"
  log_info "  FULL weekly:  $FULL_WEEKLY_RETENTION_WEEKS weeks ($FULL_WEEKLY_DAY)"
  log_info "  FULL monthly: $FULL_MONTHLY_RETENTION_MONTHS months"
  log_info "  DIFF:         $DIFF_RETENTION_DAYS days"
  log_info "  LOG:          $LOG_RETENTION_DAYS days"

  if [ "$DRY_RUN" -eq 1 ]; then
    log_info "DRY RUN MODE - No files will be deleted"
  fi

  find_and_process_backups "$BACKUP_PATH"

  emit_pulse "job.cleanup.databases.processed" "$CLEANUP_PULSE_SUBJECT" "$TOTAL_DATABASES_PROCESSED"
  emit_pulse "job.cleanup.files.deleted" "$CLEANUP_PULSE_SUBJECT" "$TOTAL_FILES_DELETED"
  emit_pulse "job.cleanup.files.kept" "$CLEANUP_PULSE_SUBJECT" "$TOTAL_FILES_KEPT"
  emit_pulse "job.cleanup.full.deleted" "$CLEANUP_PULSE_SUBJECT" "$TOTAL_FULL_DELETED"
  emit_pulse "job.cleanup.diff.deleted" "$CLEANUP_PULSE_SUBJECT" "$TOTAL_DIFF_DELETED"
  emit_pulse "job.cleanup.log.deleted" "$CLEANUP_PULSE_SUBJECT" "$TOTAL_LOG_DELETED"
  emit_pulse "job.scheduled.completed" "$CLEANUP_PULSE_SUBJECT"

  log_info "=========================================="
  log_info "Backup Cleanup Completed"
  log_info "=========================================="
}

main "$@"
