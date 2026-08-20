#!/usr/bin/env bash

set -Eeuo pipefail

readonly BACKUP_ENVIRONMENT_FILE='/etc/apipool-backup/restic.env'

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

main() {
  load_backup_environment
  restic check --read-data-subset=5%
}

main "$@"
