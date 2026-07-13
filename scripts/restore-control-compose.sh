#!/usr/bin/env bash
set -euo pipefail
umask 077

if [[ $EUID -ne 0 ]]; then
  echo "restore-control-compose.sh must run as root" >&2
  exit 2
fi

root="${CDN_PLATFORM_ROOT:-/srv/cdn-platform}"
snapshot="${1:-latest}"
restore_dir=$(mktemp -d /tmp/cdn-platform-restore.XXXXXX)
trap 'rm -rf "$restore_dir"' EXIT

cd "$root"
set -a
source config/backup.env
set +a
: "${RESTIC_REPOSITORY:?RESTIC_REPOSITORY is required}"
if [[ ! -s "$root/config/restic-password" ]]; then
  echo "restic password file is empty: $root/config/restic-password" >&2
  exit 2
fi

if [[ -e data/control/control.db && "${ALLOW_NONEMPTY_RESTORE:-0}" != "1" ]]; then
  echo "data/control/control.db already exists; set ALLOW_NONEMPTY_RESTORE=1 for an intentional replacement" >&2
  exit 2
fi

docker compose stop control control-cert-renew backup 2>/dev/null || true
docker compose --profile backup run --rm \
  -v "$restore_dir:/restore" --entrypoint restic backup \
  restore "$snapshot" --target /restore

restored_staging="$restore_dir/backup/staging"
test -r "$restored_staging/control/control.db"
test -d "$restored_staging/clickhouse/cdn-platform-current"
test -r "$restore_dir/deployment/config/control.env"

install -d -o 10001 -g 10001 -m 0750 data/control data/control-tls backup/staging/clickhouse
rm -rf data/control/* data/control-tls/*
install -o 10001 -g 10001 -m 0600 "$restored_staging/control/control.db" data/control/control.db
tar --extract --gzip --file "$restored_staging/control/control-secrets.tar.gz" --directory data/control
tar --extract --gzip --file "$restored_staging/control/control-tls.tar.gz" --directory data/control-tls
chown -R 10001:10001 data/control data/control-tls
install -m 0600 "$restore_dir/deployment/config/control.env" config/control.env
rm -rf backup/staging/clickhouse/cdn-platform-current
cp -a "$restored_staging/clickhouse/cdn-platform-current" backup/staging/clickhouse/
chown -R 101:101 backup/staging/clickhouse

docker compose up -d clickhouse
until docker compose exec -T clickhouse clickhouse-client --query 'SELECT 1' >/dev/null 2>&1; do sleep 2; done
docker compose exec -T clickhouse clickhouse-client --query 'DROP DATABASE IF EXISTS cdn_platform SYNC'
docker compose exec -T clickhouse clickhouse-client --query "RESTORE DATABASE cdn_platform FROM Disk('backups', 'cdn-platform-current')"
docker compose up -d control-cert-bootstrap control control-cert-renew
echo "Restore completed from restic snapshot $snapshot"
