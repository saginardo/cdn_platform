#!/usr/bin/env bash
set -euo pipefail

if (($# > 2)); then
  echo "usage: install-control-compose.sh [root] [control-image]" >&2
  exit 2
fi
if [[ $EUID -ne 0 ]]; then
  echo "install-control-compose.sh must run as root" >&2
  exit 2
fi
if [[ ! -f deploy/docker-compose.yaml || ! -d scripts ]]; then
  echo "run this script from the repository root" >&2
  exit 2
fi

root="${1:-/opt/cdn-platform}"
control_image="${2:-${CDN_CONTROL_IMAGE:-ghcr.io/saginardo/simple_cdn:main}}"
image_pattern='^ghcr\.io/saginardo/simple_cdn(:[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}|@sha256:[a-f0-9]{64})$'
if [[ "$root" != /* || "$root" == "/" ]]; then
  echo "root must be an absolute path below /" >&2
  exit 2
fi
root=$(realpath -m -- "$root")
if [[ "$root" == "/" ]]; then
  echo "root must resolve below /" >&2
  exit 2
fi
if [[ ! "$control_image" =~ $image_pattern ]]; then
  echo "control image must be a ghcr.io/saginardo/simple_cdn tag or digest" >&2
  exit 2
fi

deploy_dir="$root/app"
if [[ -L "$deploy_dir" ]]; then
  echo "deployment support directory must not be a symlink: $deploy_dir" >&2
  exit 2
fi
install -d -m 0750 "$root" "$root/config" "$root/backup/staging/clickhouse"
install -d -o 10001 -g 10001 -m 0750 "$root/backup/status"
install -d -o 10001 -g 101 -m 2750 "$root/backup/online-restore"
touch "$root/backup/online-restore/operations.lock"
chown 10001:10001 "$root/backup/online-restore/operations.lock"
chmod 0660 "$root/backup/online-restore/operations.lock"
touch "$root/backup/online-restore/backup.lock"
chown 10001:10001 "$root/backup/online-restore/backup.lock"
chmod 0660 "$root/backup/online-restore/backup.lock"
install -d -o 10001 -g 10001 -m 0750 \
  "$root/data/control" "$root/data/control-tls" \
  "$root/logs/certbot-sites" "$root/logs/certbot-control"
install -d -o 101 -g 101 -m 0750 "$root/data/clickhouse" "$root/logs/clickhouse"
chown -R 101:101 "$root/backup/staging/clickhouse"

rm -rf "$deploy_dir"
install -d -m 0750 "$deploy_dir"
cp -a deploy scripts "$deploy_dir/"
install -m 0644 deploy/docker-compose.yaml "$root/compose.yaml"
printf 'CDN_CONTROL_IMAGE=%s\nCDN_DEPLOY_DIR=./app\n' "$control_image" >"$root/.env"
chmod 0644 "$root/.env"

if [[ ! -e "$root/config/control.env" ]]; then
  install -m 0600 deploy/examples/compose-control.env.example "$root/config/control.env"
fi

# Retire duplicated artifact metadata and move the legacy default database name
# without changing any unrelated deployment settings or secrets.
control_env="$root/config/control.env"
control_env_migration=$(mktemp "$root/config/.control.env.XXXXXX")
sed \
  -e '/^[[:space:]]*EDGE_BINARY_SHA256[[:space:]]*=/d' \
  -e 's/^\([[:space:]]*CLICKHOUSE_DATABASE[[:space:]]*=[[:space:]]*\)cdn_platform[[:space:]]*$/\1simple_cdn/' \
  "$control_env" >"$control_env_migration"
chown --reference="$control_env" "$control_env_migration"
chmod --reference="$control_env" "$control_env_migration"
mv "$control_env_migration" "$control_env"

if [[ ! -e "$root/config/backup.env" ]]; then
  install -m 0600 deploy/examples/compose-backup.env.example "$root/config/backup.env"
fi
backup_env="$root/config/backup.env"
backup_env_migration=$(mktemp "$root/config/.backup.env.XXXXXX")
sed \
  -e 's/^\([[:space:]]*CLICKHOUSE_DATABASE[[:space:]]*=[[:space:]]*\)cdn_platform[[:space:]]*$/\1simple_cdn/' \
  "$backup_env" >"$backup_env_migration"
chown --reference="$backup_env" "$backup_env_migration"
chmod --reference="$backup_env" "$backup_env_migration"
mv "$backup_env_migration" "$backup_env"

if [[ ! -e "$root/config/restic-password" ]]; then
  install -m 0600 /dev/null "$root/config/restic-password"
fi
chown root:10001 "$root/config/restic-password"
chmod 0640 "$root/config/restic-password"

echo "Installed Compose deployment at $root"
echo "Configure $root/config/control.env, then run: cd $root && docker compose pull"
