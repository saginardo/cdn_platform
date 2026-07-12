#!/usr/bin/env bash
set -euo pipefail
umask 077

: "${RESTIC_REPOSITORY:?RESTIC_REPOSITORY is required}"
: "${RESTIC_PASSWORD_FILE:?RESTIC_PASSWORD_FILE is required}"

DATA_DIR="${CONTROL_DATA_DIR:-/var/lib/cdn-platform}"
BACKUP_DIR="${BACKUP_STAGING_DIR:-/var/lib/cdn-platform/backup-staging}"
CONTROL_CONFIG_PATH="${CONTROL_CONFIG_PATH:-/etc/cdn-platform/control.env}"
if [[ ! -r "$CONTROL_CONFIG_PATH" ]]; then
  echo "control configuration is not readable: $CONTROL_CONFIG_PATH" >&2
  exit 2
fi
# The control environment is already required for recovery. Source it only to
# discover the two TLS source files that the running service copies into /run.
source "$CONTROL_CONFIG_PATH"
: "${CONTROL_TLS_SOURCE_CERT:?CONTROL_TLS_SOURCE_CERT is required for backup}"
: "${CONTROL_TLS_SOURCE_KEY:?CONTROL_TLS_SOURCE_KEY is required for backup}"
if [[ ! -r "$CONTROL_TLS_SOURCE_CERT" || ! -r "$CONTROL_TLS_SOURCE_KEY" ]]; then
  echo "control TLS source certificate or key is not readable" >&2
  exit 2
fi
mkdir -p "$BACKUP_DIR"
chmod 0700 "$BACKUP_DIR"

# Use SQLite's online backup API; copying an active WAL database can capture an
# inconsistent pair of database and WAL files.
rm -f "$BACKUP_DIR/control.db"
sqlite3 "$DATA_DIR/control.db" ".backup '$BACKUP_DIR/control.db'"
chmod 0600 "$BACKUP_DIR/control.db"
tar --dereference --create --gzip --file "$BACKUP_DIR/control-tls.tar.gz" "$CONTROL_TLS_SOURCE_CERT" "$CONTROL_TLS_SOURCE_KEY"
archive_inputs=()
for directory in pki letsencrypt; do
  if [[ -d "$DATA_DIR/$directory" ]]; then
    archive_inputs+=("$directory")
  fi
done
if (( ${#archive_inputs[@]} )); then
	tar --dereference --create --gzip --file "$BACKUP_DIR/control-secrets.tar.gz" --directory "$DATA_DIR" "${archive_inputs[@]}"
else
  tar --create --gzip --file "$BACKUP_DIR/control-secrets.tar.gz" --files-from /dev/null
fi

restic backup "$BACKUP_DIR/control.db" "$BACKUP_DIR/control-secrets.tar.gz" "$BACKUP_DIR/control-tls.tar.gz" "$CONTROL_CONFIG_PATH" --tag cdn-control
restic forget --keep-daily 7 --keep-weekly 4 --keep-monthly 6 --prune
rm -f "$BACKUP_DIR/control.db" "$BACKUP_DIR/control-secrets.tar.gz" "$BACKUP_DIR/control-tls.tar.gz"
