#!/usr/bin/env bash
set -euo pipefail
umask 077

: "${CONTROL_TLS_DOMAIN:?CONTROL_TLS_DOMAIN is required}"
: "${ACME_EMAIL:?ACME_EMAIL is required}"

config_dir=/var/lib/cdn-control-tls
logs_dir=/var/log/cdn-control-tls
credentials="${CERTBOT_CREDENTIALS_PATH:-/tmp/cloudflare.ini}"
live_dir="$config_dir/live/$CONTROL_TLS_DOMAIN"
mode="${1:-issue}"

mkdir -p "$config_dir" "$logs_dir" /tmp/certbot-work "$(dirname "$credentials")"

write_credentials() {
  cdn-control cloudflare-credentials "$credentials"
}

issue() {
  if [[ -s "$live_dir/fullchain.pem" && -s "$live_dir/privkey.pem" ]]; then
    return
  fi
  write_credentials
  certbot certonly --non-interactive --agree-tos --email "$ACME_EMAIL" \
    --dns-cloudflare --dns-cloudflare-credentials "$credentials" \
    --config-dir "$config_dir" --work-dir /tmp/certbot-work --logs-dir "$logs_dir" \
    --cert-name "$CONTROL_TLS_DOMAIN" -d "$CONTROL_TLS_DOMAIN"
}

renew() {
  write_credentials
  certbot renew --non-interactive --no-random-sleep-on-renew \
    --config-dir "$config_dir" --work-dir /tmp/certbot-work --logs-dir "$logs_dir" \
    --cert-name "$CONTROL_TLS_DOMAIN"
}

case "$mode" in
  issue)
    issue
    ;;
  renew)
    issue
    renew
    ;;
  loop)
    issue
    while true; do
      renew
      sleep 12h & wait $!
    done
    ;;
  *)
    echo "usage: compose-certbot.sh {issue|renew|loop}" >&2
    exit 2
    ;;
esac
