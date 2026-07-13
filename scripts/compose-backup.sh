#!/usr/bin/env bash
set -euo pipefail
umask 077

: "${RESTIC_REPOSITORY:?RESTIC_REPOSITORY is required}"
: "${RESTIC_PASSWORD_FILE:?RESTIC_PASSWORD_FILE is required}"

data_dir="${CONTROL_DATA_DIR:-/var/lib/cdn-platform}"
tls_dir="${CONTROL_TLS_DIR:-/var/lib/cdn-control-tls}"
staging_dir="${BACKUP_STAGING_DIR:-/backup/staging}"
clickhouse_url="${CLICKHOUSE_URL:-http://127.0.0.1:8123}"
clickhouse_database="${CLICKHOUSE_DATABASE:-cdn_platform}"
snapshot_name="cdn-platform-current"
control_staging="$staging_dir/control"
clickhouse_staging="$staging_dir/clickhouse/$snapshot_name"

for required in "$data_dir/control.db" "$RESTIC_PASSWORD_FILE"; do
  if [[ ! -r "$required" ]]; then
    echo "required backup input is not readable: $required" >&2
    exit 2
  fi
done

rm -rf "$control_staging" "$clickhouse_staging"
mkdir -p "$control_staging"
sqlite3 "$data_dir/control.db" ".backup '$control_staging/control.db'"
chmod 0600 "$control_staging/control.db"

archive_inputs=()
for directory in pki letsencrypt; do
  if [[ -d "$data_dir/$directory" ]]; then
    archive_inputs+=("$directory")
  fi
done
if ((${#archive_inputs[@]})); then
  tar --create --gzip --file "$control_staging/control-secrets.tar.gz" --directory "$data_dir" "${archive_inputs[@]}"
else
  tar --create --gzip --file "$control_staging/control-secrets.tar.gz" --files-from /dev/null
fi
tar --create --gzip --file "$control_staging/control-tls.tar.gz" --directory "$tls_dir" .

backup_query="BACKUP DATABASE ${clickhouse_database} TO Disk('backups', '${snapshot_name}')"
curl --fail-with-body --silent --show-error --data-binary "$backup_query" "$clickhouse_url/"

restic backup \
  "$control_staging" \
  "$clickhouse_staging" \
  /deployment/config/control.env \
  /deployment/config/backup.env \
  /deployment/compose.yaml \
  /deployment/Dockerfile \
  --tag cdn-control-compose
restic forget --keep-daily 7 --keep-weekly 4 --keep-monthly 6 --prune
rm -rf "$control_staging" "$clickhouse_staging"
