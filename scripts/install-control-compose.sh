#!/usr/bin/env bash
set -euo pipefail

if [[ $EUID -ne 0 ]]; then
  echo "install-control-compose.sh must run as root" >&2
  exit 2
fi
if [[ ! -f compose.yaml || ! -f Dockerfile || ! -f go.mod ]]; then
  echo "run this script from the repository root" >&2
  exit 2
fi

root="${1:-/opt/cdn-platform}"
source_dir="$root/app"
install -d -m 0750 "$root" "$source_dir" "$root/config" "$root/backup/staging/clickhouse"
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

rm -rf "$source_dir/cmd" "$source_dir/internal" "$source_dir/deploy" "$source_dir/docs" "$source_dir/scripts" "$source_dir/frontend"
cp -a cmd internal deploy docs scripts go.mod go.sum Dockerfile .dockerignore AGENTS.md LICENSE README.md "$source_dir/"
install -d -m 0750 "$source_dir/frontend"
cp -a frontend/.oxlintrc.json frontend/.prettierignore frontend/README.md frontend/components.json frontend/index.html \
  frontend/package.json frontend/package-lock.json frontend/playwright.config.ts \
  frontend/tsconfig.json frontend/tsconfig.app.json frontend/tsconfig.node.json frontend/vite.config.ts \
  frontend/e2e frontend/src "$source_dir/frontend/"
install -m 0644 compose.yaml "$root/compose.yaml"
printf 'CDN_SOURCE_DIR=./app\n' >"$root/.env"
chmod 0644 "$root/.env"

if [[ ! -e "$root/config/control.env" ]]; then
  install -m 0600 deploy/examples/compose-control.env.example "$root/config/control.env"
fi
if [[ ! -e "$root/config/backup.env" ]]; then
  install -m 0600 deploy/examples/compose-backup.env.example "$root/config/backup.env"
fi
if [[ ! -e "$root/config/restic-password" ]]; then
  install -m 0600 /dev/null "$root/config/restic-password"
fi
chown root:10001 "$root/config/restic-password"
chmod 0640 "$root/config/restic-password"

echo "Installed Compose deployment at $root"
echo "Configure $root/config/control.env, then run: cd $root && docker compose build control"
