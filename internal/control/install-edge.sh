#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

CONTROL_URL=""
ENROLLMENT_TOKEN=""
BINARY_URL=""
BINARY_SHA256=""
ROOT_PREFIX="${CDN_EDGE_INSTALL_ROOT:-}"
LAYOUT_VERSION=1

while [[ $# -gt 0 ]]; do
  case "$1" in
    --control-url) CONTROL_URL="$2"; shift 2 ;;
    --enrollment-token) ENROLLMENT_TOKEN="$2"; shift 2 ;;
    --binary-url) BINARY_URL="$2"; shift 2 ;;
    --binary-sha256) BINARY_SHA256="$2"; shift 2 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

if [[ -n "$ROOT_PREFIX" ]]; then
  ROOT_PREFIX="${ROOT_PREFIX%/}"
  if [[ "$ROOT_PREFIX" != /* || "$ROOT_PREFIX" == "/" ]]; then
    echo "CDN_EDGE_INSTALL_ROOT must be an absolute non-root path" >&2
    exit 2
  fi
elif [[ $EUID -ne 0 ]]; then
  echo "edge installation must run as root" >&2
  exit 2
fi
if [[ "$CONTROL_URL" != https://* || "$CONTROL_URL" == *[[:space:]]* ||
      ( -n "$ENROLLMENT_TOKEN" && "$ENROLLMENT_TOKEN" == *[[:space:]]* ) ||
      "$BINARY_URL" != https://* || "$BINARY_URL" == *[[:space:]]* ||
      ! "$BINARY_SHA256" =~ ^[0-9a-fA-F]{64}$ ]]; then
  echo "usage: install-edge.sh --control-url HTTPS_URL [--enrollment-token TOKEN] --binary-url HTTPS_URL --binary-sha256 SHA256" >&2
  exit 2
fi
CONTROL_URL="${CONTROL_URL%/}"

root_path() {
  printf '%s%s' "$ROOT_PREFIX" "$1"
}
path_exists() {
  [[ -e "$1" || -L "$1" ]]
}
link_points_to() {
  [[ -L "$1" && "$(readlink "$1")" == "$2" ]]
}
copy_tree() {
  local source="$1" destination="$2"
  if [[ -d "$source" ]]; then
    cp -a "$source/." "$destination/"
  fi
}
read_environment_value() {
  local file="$1" wanted="$2" key value
  [[ -f "$file" ]] || return 1
  while IFS='=' read -r key value; do
    if [[ "$key" == "$wanted" ]]; then
      printf '%s' "$value"
      return 0
    fi
  done <"$file"
  return 1
}

edge_root=$(root_path /opt/cdn-edge)
marker="$edge_root/.layout-version"
agent_unit=$(root_path /etc/systemd/system/cdn-edge-agent.service)
nginx_entry=$(root_path /etc/nginx/conf.d/cdn-platform.conf)
nginx_default=$(root_path /etc/nginx/sites-enabled/default)
new_unit="$edge_root/systemd/cdn-edge-agent.service"
new_nginx_config="$edge_root/config/nginx/cdn-platform.conf"
old_binary=$(root_path /usr/local/bin/cdn-edge-agent)
old_config_dir=$(root_path /etc/cdn-platform)
old_state_dir=$(root_path /var/lib/cdn-platform)
old_log_dir=$(root_path /var/log/cdn-platform)
old_cache_dir=$(root_path /var/cache/cdn-platform)
expected_unit_target="$new_unit"
expected_nginx_target="$new_nginx_config"

new_layout=0
if [[ -f "$marker" ]]; then
  if [[ "$(tr -d '[:space:]' <"$marker")" != "$LAYOUT_VERSION" ]]; then
    echo "unsupported /opt/cdn-edge layout version" >&2
    exit 1
  fi
  new_layout=1
elif [[ -d "$edge_root" && -n "$(find "$edge_root" -mindepth 1 -maxdepth 1 -print -quit)" ]]; then
  echo "$edge_root exists without a recognized layout marker; refusing to merge it" >&2
  exit 1
fi
legacy_layout=0
for path in "$old_binary" "$old_config_dir" "$old_state_dir" "$old_log_dir" "$old_cache_dir"; do
  if path_exists "$path"; then legacy_layout=1; fi
done
if path_exists "$agent_unit" && ! link_points_to "$agent_unit" "$expected_unit_target"; then legacy_layout=1; fi
if path_exists "$nginx_entry" && ! link_points_to "$nginx_entry" "$expected_nginx_target"; then legacy_layout=1; fi
if ((new_layout == 1 && legacy_layout == 1)); then
  echo "both /opt/cdn-edge and legacy CDN paths exist; refusing to guess which layout is authoritative" >&2
  exit 1
fi
if ((new_layout == 1)); then
  for required in "$new_nginx_config" "$edge_root/data/edge-client.key" "$edge_root/data/edge-client.crt" "$edge_root/data/edge-ca.crt"; do
    if [[ ! -s "$required" ]]; then
      echo "incomplete /opt/cdn-edge layout: missing ${required#"$edge_root/"}" >&2
      exit 1
    fi
  done
fi
existing_identity=0
if ((new_layout == 1)) ||
   [[ -s "$old_state_dir/edge-client.key" && -s "$old_state_dir/edge-client.crt" && -s "$old_state_dir/edge-ca.crt" ]]; then
  existing_identity=1
fi
if [[ -z "$ENROLLMENT_TOKEN" && "$existing_identity" == "0" ]]; then
  echo "an enrollment token is required because this host has no complete edge mTLS identity" >&2
  exit 1
fi

old_nginx_active=0
if systemctl is-active --quiet nginx.service 2>/dev/null; then old_nginx_active=1; fi
if [[ -z "$ROOT_PREFIX" ]]; then
  export DEBIAN_FRONTEND=noninteractive
  apt-get update
  apt-get install -y --no-install-recommends nginx ca-certificates curl iproute2
fi

transaction_dir=$(mktemp -d "$(root_path /tmp/cdn-edge-install.XXXXXX)")
trap 'rm -rf "$transaction_dir"' EXIT
temporary_binary="$transaction_dir/cdn-edge-agent"
temporary_unit="$transaction_dir/cdn-edge-agent.service"
curl --fail --location --silent --show-error "$BINARY_URL" --output "$temporary_binary"
echo "$BINARY_SHA256  $temporary_binary" | sha256sum --check --status
curl --fail --location --silent --show-error "${CONTROL_URL}/install-edge.service" --output "$temporary_unit"
if ! grep -Fqx 'ExecStart=/opt/cdn-edge/bin/cdn-edge-agent' "$temporary_unit" ||
   ! grep -Fqx 'EnvironmentFile=/opt/cdn-edge/config/edge.env' "$temporary_unit"; then
  echo "downloaded edge service does not match the /opt/cdn-edge layout" >&2
  exit 1
fi

old_agent_active=0
old_agent_enabled=0
if systemctl is-active --quiet cdn-edge-agent.service 2>/dev/null; then old_agent_active=1; fi
if systemctl is-enabled --quiet cdn-edge-agent.service 2>/dev/null; then old_agent_enabled=1; fi
for item in unit nginx default-site; do
  case "$item" in
    unit) source="$agent_unit" ;;
    nginx) source="$nginx_entry" ;;
    default-site) source="$nginx_default" ;;
  esac
  if path_exists "$source"; then cp -a "$source" "$transaction_dir/$item.backup"; fi
done
if ((new_layout == 1)); then
  for item in bin/cdn-edge-agent config/edge.env systemd/cdn-edge-agent.service; do
    if path_exists "$edge_root/$item"; then
      mkdir -p "$transaction_dir/new-layout/$(dirname "$item")"
      cp -a "$edge_root/$item" "$transaction_dir/new-layout/$item"
    fi
  done
fi

log_moved=0
committed=0
rollback() {
  local code=$?
  trap - ERR
  if ((committed == 0)); then
    systemctl stop cdn-edge-agent.service >/dev/null 2>&1 || true
    systemctl disable cdn-edge-agent.service >/dev/null 2>&1 || true
    rm -f "$agent_unit" "$nginx_entry" "$nginx_default"
    if path_exists "$transaction_dir/unit.backup"; then cp -a "$transaction_dir/unit.backup" "$agent_unit"; fi
    if path_exists "$transaction_dir/nginx.backup"; then cp -a "$transaction_dir/nginx.backup" "$nginx_entry"; fi
    if path_exists "$transaction_dir/default-site.backup"; then cp -a "$transaction_dir/default-site.backup" "$nginx_default"; fi
    if ((log_moved == 1)) && path_exists "$edge_root/logs/access.json"; then
      mkdir -p "$old_log_dir"
      mv "$edge_root/logs/access.json" "$old_log_dir/access.json" || true
    fi
    if ((new_layout == 0)); then
      rm -rf "$edge_root"
    else
      for item in bin/cdn-edge-agent config/edge.env systemd/cdn-edge-agent.service; do
        if path_exists "$transaction_dir/new-layout/$item"; then
          mkdir -p "$edge_root/$(dirname "$item")"
          cp -a "$transaction_dir/new-layout/$item" "$edge_root/$item"
        fi
      done
    fi
    systemctl daemon-reload >/dev/null 2>&1 || true
    nginx -t >/dev/null 2>&1 && systemctl reload nginx >/dev/null 2>&1 || true
    if ((old_nginx_active == 0)); then systemctl stop nginx.service >/dev/null 2>&1 || true; fi
    if ((old_agent_enabled == 1)); then systemctl enable cdn-edge-agent.service >/dev/null 2>&1 || true; fi
    if ((old_agent_active == 1)); then systemctl start cdn-edge-agent.service >/dev/null 2>&1 || true; fi
  fi
  rm -rf "$transaction_dir"
  exit "$code"
}
trap rollback ERR

if ((old_agent_active == 1)); then systemctl stop cdn-edge-agent.service; fi
install -d -m 0755 "$edge_root" "$edge_root/bin" "$edge_root/systemd"
install -d -m 0750 "$edge_root/config" "$edge_root/config/nginx" "$edge_root/data" "$edge_root/logs"
install -d -m 0700 "$edge_root/config/certs"
install -d -m 0750 "$edge_root/cache"
if [[ -z "$ROOT_PREFIX" ]]; then chown www-data:www-data "$edge_root/cache"; fi

poll_seconds=30
if ((legacy_layout == 1)); then
  if value=$(read_environment_value "$old_config_dir/edge.env" EDGE_POLL_SECONDS) && [[ "$value" =~ ^[0-9]+$ ]] && ((value >= 5 && value <= 300)); then
    poll_seconds="$value"
  fi
  copy_tree "$old_state_dir" "$edge_root/data"
  copy_tree "$old_config_dir/certs" "$edge_root/config/certs"
  copy_tree "$old_log_dir" "$edge_root/logs"
  rm -f "$edge_root/logs/access.json"
elif ((new_layout == 1)); then
  if value=$(read_environment_value "$edge_root/config/edge.env" EDGE_POLL_SECONDS) && [[ "$value" =~ ^[0-9]+$ ]] && ((value >= 5 && value <= 300)); then
    poll_seconds="$value"
  fi
fi

install -m 0755 "$temporary_binary" "$edge_root/bin/cdn-edge-agent"
install -m 0644 "$temporary_unit" "$new_unit"
cat >"$edge_root/config/edge.env" <<EOF
CONTROL_URL=${CONTROL_URL}
ENROLLMENT_TOKEN=${ENROLLMENT_TOKEN}
EDGE_POLL_SECONDS=${poll_seconds}
EDGE_STATE_DIR=/opt/cdn-edge/data
NGINX_CONFIG_PATH=/opt/cdn-edge/config/nginx/cdn-platform.conf
EDGE_CERT_DIR=/opt/cdn-edge/config/certs
EDGE_ACCESS_LOG=/opt/cdn-edge/logs/access.json
EOF
chmod 0600 "$edge_root/config/edge.env"

if ((legacy_layout == 1)) && path_exists "$nginx_entry"; then
  sed \
    -e 's#/var/cache/cdn-platform#/opt/cdn-edge/cache#g' \
    -e 's#/etc/cdn-platform/certs#/opt/cdn-edge/config/certs#g' \
    -e 's#/var/log/cdn-platform/access.json#/opt/cdn-edge/logs/access.json#g' \
    "$nginx_entry" >"$new_nginx_config"
  chmod 0640 "$new_nginx_config"
elif ! path_exists "$new_nginx_config"; then
  cat >"$new_nginx_config" <<'EOF'
# Generated by cdn-edge-agent. Do not edit.
server {
    listen 80 default_server;
    server_name _;
    location = /__cdn_health { access_log off; add_header Content-Type text/plain; return 200 "ok\n"; }
    location / { return 404; }
}
EOF
  chmod 0640 "$new_nginx_config"
fi

if ((legacy_layout == 1)) && [[ -f "$old_log_dir/access.json" ]]; then
  if [[ "$(stat -c %d "$old_log_dir/access.json")" != "$(stat -c %d "$edge_root/logs")" ]]; then
    echo "legacy access log and /opt/cdn-edge are on different filesystems; refusing a lossy live migration" >&2
    false
  fi
  mv "$old_log_dir/access.json" "$edge_root/logs/access.json"
  log_moved=1
fi

rm -f "$nginx_entry"
ln -s "$expected_nginx_target" "$nginx_entry"
rm -f "$nginx_default"
nginx -t
if ! systemctl is-active --quiet nginx.service; then systemctl start nginx.service; fi
systemctl reload nginx

rm -f "$agent_unit"
ln -s "$expected_unit_target" "$agent_unit"
systemctl daemon-reload
systemctl enable cdn-edge-agent.service
systemctl restart cdn-edge-agent.service
agent_active=0
for _ in 1 2 3 4 5; do
  sleep 1
  if systemctl is-active --quiet cdn-edge-agent.service; then
    agent_active=1
    break
  fi
done
if ((agent_active == 0)); then
  echo "cdn-edge-agent did not become active after installation" >&2
  false
fi
identity_ready=0
for _ in $(seq 1 30); do
  if [[ -s "$edge_root/data/edge-client.key" && -s "$edge_root/data/edge-client.crt" && -s "$edge_root/data/edge-ca.crt" ]]; then
    identity_ready=1
    break
  fi
  sleep 1
done
if ((identity_ready == 0)); then
  echo "cdn-edge-agent did not establish its mTLS identity after installation" >&2
  false
fi
if grep -Fq 'location = /__cdn_health' "$new_nginx_config"; then
  curl --fail --silent --show-error --max-time 5 http://127.0.0.1/__cdn_health >/dev/null
fi

printf '%s\n' "$LAYOUT_VERSION" >"$marker"
chmod 0644 "$marker"
committed=1
trap - ERR
if ((legacy_layout == 1)); then
  rm -f "$old_binary"
  rm -rf "$old_config_dir" "$old_state_dir" "$old_log_dir" "$old_cache_dir"
fi
rm -rf "$transaction_dir"
trap - EXIT
echo "CDN edge deployment is active under /opt/cdn-edge."
