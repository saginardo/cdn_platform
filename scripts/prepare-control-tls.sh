#!/usr/bin/env bash
set -euo pipefail

source /etc/cdn-platform/control.env
: "${CONTROL_TLS_SOURCE_CERT:?CONTROL_TLS_SOURCE_CERT is required}"
: "${CONTROL_TLS_SOURCE_KEY:?CONTROL_TLS_SOURCE_KEY is required}"
: "${CONTROL_TLS_CERT:?CONTROL_TLS_CERT is required}"
: "${CONTROL_TLS_KEY:?CONTROL_TLS_KEY is required}"

install -d -o cdn-platform -g cdn-platform -m 0750 "$(dirname "$CONTROL_TLS_CERT")" "$(dirname "$CONTROL_TLS_KEY")"
install -o cdn-platform -g cdn-platform -m 0640 "$CONTROL_TLS_SOURCE_CERT" "$CONTROL_TLS_CERT"
install -o cdn-platform -g cdn-platform -m 0640 "$CONTROL_TLS_SOURCE_KEY" "$CONTROL_TLS_KEY"
