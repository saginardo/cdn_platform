#!/usr/bin/env bash
set -euo pipefail

source /etc/cdn-platform/tls-sync.env
: "${CONTROL_TLS_UPSTREAM_CERT:?CONTROL_TLS_UPSTREAM_CERT is required}"
: "${CONTROL_TLS_UPSTREAM_KEY:?CONTROL_TLS_UPSTREAM_KEY is required}"
: "${CONTROL_TLS_STAGED_CERT:?CONTROL_TLS_STAGED_CERT is required}"
: "${CONTROL_TLS_STAGED_KEY:?CONTROL_TLS_STAGED_KEY is required}"

install -d -m 0750 "$(dirname "$CONTROL_TLS_STAGED_CERT")" "$(dirname "$CONTROL_TLS_STAGED_KEY")"
changed=0
if ! cmp -s "$CONTROL_TLS_UPSTREAM_CERT" "$CONTROL_TLS_STAGED_CERT"; then
  install -m 0644 "$CONTROL_TLS_UPSTREAM_CERT" "$CONTROL_TLS_STAGED_CERT"
  changed=1
fi
if ! cmp -s "$CONTROL_TLS_UPSTREAM_KEY" "$CONTROL_TLS_STAGED_KEY"; then
  install -m 0600 "$CONTROL_TLS_UPSTREAM_KEY" "$CONTROL_TLS_STAGED_KEY"
  changed=1
fi
if (( changed )); then
  systemctl try-restart cdn-control.service
fi
