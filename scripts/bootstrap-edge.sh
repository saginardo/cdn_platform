#!/usr/bin/env bash
set -euo pipefail

CONTROL_URL=""
ENROLLMENT_TOKEN=""
BINARY_URL=""
BINARY_SHA256=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --control-url) CONTROL_URL="$2"; shift 2 ;;
    --enrollment-token) ENROLLMENT_TOKEN="$2"; shift 2 ;;
    --binary-url) BINARY_URL="$2"; shift 2 ;;
    --binary-sha256) BINARY_SHA256="$2"; shift 2 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

if [[ "$CONTROL_URL" != https://* || -z "$ENROLLMENT_TOKEN" || "$BINARY_URL" != https://* || ! "$BINARY_SHA256" =~ ^[0-9a-fA-F]{64}$ ]]; then
  echo "usage: bootstrap-edge.sh --control-url HTTPS_URL --enrollment-token TOKEN --binary-url HTTPS_URL --binary-sha256 SHA256" >&2
  exit 2
fi

export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y --no-install-recommends nginx ca-certificates curl iproute2
rm -f /etc/nginx/sites-enabled/default
install -d -m 0750 /var/lib/cdn-platform /var/log/cdn-platform
install -d -o www-data -g www-data -m 0750 /var/cache/cdn-platform
install -d -m 0700 /etc/cdn-platform/certs

temporary_binary="$(mktemp)"
trap 'rm -f "$temporary_binary"' EXIT
curl --fail --location --silent --show-error "$BINARY_URL" --output "$temporary_binary"
echo "$BINARY_SHA256  $temporary_binary" | sha256sum --check --status
install -m 0755 "$temporary_binary" /usr/local/bin/cdn-edge-agent
chmod 0755 /usr/local/bin/cdn-edge-agent

cat >/etc/cdn-platform/edge.env <<EOF
CONTROL_URL=${CONTROL_URL}
ENROLLMENT_TOKEN=${ENROLLMENT_TOKEN}
EOF
chmod 0600 /etc/cdn-platform/edge.env

curl --fail --location --silent --show-error "${CONTROL_URL%/}/install-edge.service" --output /etc/systemd/system/cdn-edge-agent.service
chmod 0644 /etc/systemd/system/cdn-edge-agent.service
systemctl daemon-reload
systemctl enable --now cdn-edge-agent

echo "Edge enrollment started. The one-time token is consumed after a successful CSR registration."
