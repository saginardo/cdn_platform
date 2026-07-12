#!/usr/bin/env bash
set -euo pipefail

BINARY_PATH="${1:-./cdn-control}"
EDGE_BINARY_PATH="${2:-}"
if [[ ! -x "$BINARY_PATH" ]]; then
  echo "control binary is not executable: $BINARY_PATH" >&2
  exit 2
fi
if [[ -n "$EDGE_BINARY_PATH" && ! -x "$EDGE_BINARY_PATH" ]]; then
  echo "edge binary is not executable: $EDGE_BINARY_PATH" >&2
  exit 2
fi

export DEBIAN_FRONTEND=noninteractive
packages=(ca-certificates certbot python3-certbot-dns-cloudflare restic sqlite3)
if [[ "${INSTALL_CLICKHOUSE:-1}" != "0" ]]; then
  packages+=(clickhouse-server)
fi
apt-get update
apt-get install -y --no-install-recommends "${packages[@]}"
id -u cdn-platform >/dev/null 2>&1 || useradd --system --home /var/lib/cdn-platform --shell /usr/sbin/nologin cdn-platform
install -d -o cdn-platform -g cdn-platform -m 0750 /var/lib/cdn-platform /var/log/cdn-platform
install -d -m 0750 /etc/cdn-platform
install -m 0755 "$BINARY_PATH" /usr/local/bin/cdn-control
install -m 0644 deploy/systemd/cdn-control.service /etc/systemd/system/cdn-control.service
install -d -m 0755 /usr/local/lib/cdn-platform
if [[ -n "$EDGE_BINARY_PATH" ]]; then
  install -m 0755 "$EDGE_BINARY_PATH" /usr/local/lib/cdn-platform/cdn-edge-agent-linux-amd64
fi
install -m 0755 scripts/backup.sh /usr/local/lib/cdn-platform/backup.sh
install -m 0755 scripts/prepare-control-tls.sh /usr/local/lib/cdn-platform/prepare-control-tls.sh
install -m 0755 scripts/sync-control-tls.sh /usr/local/lib/cdn-platform/sync-control-tls.sh
install -m 0644 deploy/systemd/cdn-backup.service /etc/systemd/system/cdn-backup.service
install -m 0644 deploy/systemd/cdn-backup.timer /etc/systemd/system/cdn-backup.timer
install -m 0644 deploy/systemd/cdn-control-tls-sync.service /etc/systemd/system/cdn-control-tls-sync.service
install -m 0644 deploy/systemd/cdn-control-tls-sync.timer /etc/systemd/system/cdn-control-tls-sync.timer
if [[ "${INSTALL_CLICKHOUSE:-1}" != "0" ]]; then
  install -d -m 0755 /etc/clickhouse-server/config.d
  install -m 0644 deploy/clickhouse/cdn-platform.xml /etc/clickhouse-server/config.d/cdn-platform.xml
fi
if [[ ! -e /etc/cdn-platform/control.env ]]; then
  install -m 0600 /dev/null /etc/cdn-platform/control.env
fi
if [[ ! -e /etc/cdn-platform/backup.env ]]; then
	install -m 0600 /dev/null /etc/cdn-platform/backup.env
fi
if [[ ! -e /etc/cdn-platform/tls-sync.env ]]; then
	install -m 0600 /dev/null /etc/cdn-platform/tls-sync.env
fi

echo "Create /etc/cdn-platform/control.env, /etc/cdn-platform/backup.env, and /etc/cdn-platform/tls-sync.env from deploy/examples; then run systemctl enable --now cdn-control cdn-backup.timer cdn-control-tls-sync.timer."
