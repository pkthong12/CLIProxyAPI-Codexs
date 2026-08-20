#!/usr/bin/env bash

set -Eeuo pipefail

readonly BACKUP_ENVIRONMENT_FILE='/etc/apipool-backup/restic.env'
readonly LOCK_FILE='/run/lock/apipool-restic-backup.lock'
readonly STAGING_BASE_DIRECTORY='/var/lib/apipool-backup'
readonly CLI_PROXY_DATA_DIRECTORY='/var/lib/docker/volumes/apipool_cpa-data/_data'
readonly CPA_MANAGER_DATA_DIRECTORY='/var/lib/docker/volumes/apipool_cpa-manager-plus-data/_data'
readonly TKPROXY_DATA_DIRECTORY='/home/ubuntu/ApiPool/tkproxy-data'
readonly API_POOL_DIRECTORY='/home/ubuntu/ApiPool'
readonly BACKUP_TAG='apipool'

STAGING_DIRECTORY=''

load_backup_environment() {
  if [[ ! -r "$BACKUP_ENVIRONMENT_FILE" ]]; then
    printf '%s\n' "Backup environment file is unavailable: $BACKUP_ENVIRONMENT_FILE" >&2
    exit 1
  fi

  set -a
  source "$BACKUP_ENVIRONMENT_FILE"
  set +a
  : "${RESTIC_REPOSITORY:?RESTIC_REPOSITORY must be configured}"
  : "${RESTIC_PASSWORD_FILE:?RESTIC_PASSWORD_FILE must be configured}"
}

create_staging_directory() {
  umask 077
  mkdir -p "$STAGING_BASE_DIRECTORY"
  STAGING_DIRECTORY="$(mktemp -d "$STAGING_BASE_DIRECTORY/snapshot.XXXXXX")"
}

remove_staging_directory() {
  if [[ -n "$STAGING_DIRECTORY" && -d "$STAGING_DIRECTORY" ]]; then
    find "$STAGING_DIRECTORY" -mindepth 1 -delete
    rmdir "$STAGING_DIRECTORY"
  fi
}

require_source_file() {
  local pSourceFile="$1"

  if [[ ! -f "$pSourceFile" ]]; then
    printf '%s\n' "Required backup source is missing: $pSourceFile" >&2
    exit 1
  fi
}

require_source_directory() {
  local pSourceDirectory="$1"

  if [[ ! -d "$pSourceDirectory" ]]; then
    printf '%s\n' "Required backup source is missing: $pSourceDirectory" >&2
    exit 1
  fi
}

copy_protected_file() {
  local pSourceFile="$1"
  local pTargetFile="$2"

  install -m 600 "$pSourceFile" "$pTargetFile"
}

create_sqlite_snapshot() {
  local pSourceDatabase="$1"
  local pTargetDatabase="$2"

  sqlite3 "$pSourceDatabase" ".backup '$pTargetDatabase'"
}

create_cpa_manager_snapshot() {
  local snapshotDirectory="$STAGING_DIRECTORY/cpa-manager-plus"

  mkdir -p "$snapshotDirectory"
  create_sqlite_snapshot "$CPA_MANAGER_DATA_DIRECTORY/usage.sqlite" "$snapshotDirectory/usage.sqlite"
  copy_protected_file "$CPA_MANAGER_DATA_DIRECTORY/data.key" "$snapshotDirectory/data.key"
}

create_tkproxy_snapshot() {
  local snapshotDirectory="$STAGING_DIRECTORY/tkproxy-manager"

  mkdir -p "$snapshotDirectory"
  create_sqlite_snapshot "$TKPROXY_DATA_DIRECTORY/tkproxy-manager.db" "$snapshotDirectory/tkproxy-manager.db"

  if [[ -f "$TKPROXY_DATA_DIRECTORY/INITIAL_ACCESS.txt" ]]; then
    copy_protected_file "$TKPROXY_DATA_DIRECTORY/INITIAL_ACCESS.txt" "$snapshotDirectory/INITIAL_ACCESS.txt"
  fi
}

create_backup_manifest() {
  local manifestFile="$STAGING_DIRECTORY/manifest.txt"

  {
    printf 'created_at_utc=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    printf 'backup_tag=%s\n' "$BACKUP_TAG"
    printf 'database_snapshots=cpa-manager-plus,tkproxy-manager\n'
  } > "$manifestFile"
}

validate_backup_sources() {
  require_source_directory "$CLI_PROXY_DATA_DIRECTORY"
  require_source_directory "$API_POOL_DIRECTORY/auth-files"
  require_source_directory "$API_POOL_DIRECTORY/secrets"
  require_source_directory "$API_POOL_DIRECTORY/ssl"
  require_source_directory "$API_POOL_DIRECTORY/letsencrypt"
  require_source_directory "$API_POOL_DIRECTORY/certbot-webroot"
  require_source_file "$CPA_MANAGER_DATA_DIRECTORY/usage.sqlite"
  require_source_file "$CPA_MANAGER_DATA_DIRECTORY/data.key"
  require_source_file "$TKPROXY_DATA_DIRECTORY/tkproxy-manager.db"
  require_source_file "$API_POOL_DIRECTORY/config.local.yaml"
  require_source_file "$API_POOL_DIRECTORY/docker-compose.yml"
  require_source_file "$API_POOL_DIRECTORY/docker-compose.override.yml"
  require_source_file "$API_POOL_DIRECTORY/nginx.conf"
}

backup_apipool_data() {
  restic backup \
    --tag "$BACKUP_TAG" \
    --tag 'automatic' \
    "$STAGING_DIRECTORY" \
    "$CLI_PROXY_DATA_DIRECTORY" \
    "$API_POOL_DIRECTORY/config.local.yaml" \
    "$API_POOL_DIRECTORY/docker-compose.yml" \
    "$API_POOL_DIRECTORY/docker-compose.override.yml" \
    "$API_POOL_DIRECTORY/nginx.conf" \
    "$API_POOL_DIRECTORY/auth-files" \
    "$API_POOL_DIRECTORY/secrets" \
    "$API_POOL_DIRECTORY/ssl" \
    "$API_POOL_DIRECTORY/letsencrypt" \
    "$API_POOL_DIRECTORY/certbot-webroot"
}

prune_expired_snapshots() {
  restic forget \
    --prune \
    --tag "$BACKUP_TAG" \
    --keep-daily 14 \
    --keep-weekly 8 \
    --keep-monthly 12
}

main() {
  exec 9>"$LOCK_FILE"
  flock -n 9 || { printf '%s\n' 'Another Apipool backup is already running.' >&2; exit 1; }
  load_backup_environment
  validate_backup_sources
  create_staging_directory
  trap remove_staging_directory EXIT
  create_cpa_manager_snapshot
  create_tkproxy_snapshot
  create_backup_manifest
  backup_apipool_data
  prune_expired_snapshots
}

main "$@"
