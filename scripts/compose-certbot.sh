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
restore_root="${ONLINE_RESTORE_ROOT:-/var/lib/cdn-platform-restore}"

mkdir -p "$config_dir" "$logs_dir" /tmp/certbot-work "$(dirname "$credentials")"

run_certificate_operation() {
  mkdir -p "$restore_root"
  operation_lock="$restore_root/operations.lock"
  touch "$operation_lock"
  chmod 0660 "$operation_lock"
  exec 9<>"$operation_lock"
  flock --shared 9
  if [[ -e "$restore_root/maintenance.lock" ]]; then
    echo "certificate operation skipped while an online restore cutover is pending"
    flock --unlock 9
    exec 9>&-
    return 0
  fi
  status=0
  "$@" || status=$?
  flock --unlock 9
  exec 9>&-
  return "$status"
}

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
    run_certificate_operation issue
    ;;
  renew)
    run_certificate_operation issue
    run_certificate_operation renew
    ;;
  loop)
    run_certificate_operation issue
    while true; do
      run_certificate_operation renew
      sleep 12h & wait $!
    done
    ;;
  *)
    echo "usage: compose-certbot.sh {issue|renew|loop}" >&2
    exit 2
    ;;
esac
