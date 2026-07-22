#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

usage() {
  echo "usage: deploy-control-compose.sh <ghcr-image-tag-or-digest> [root]" >&2
}

if (($# < 1 || $# > 2)); then
  usage
  exit 2
fi
if [[ $EUID -ne 0 ]]; then
  echo "deploy-control-compose.sh must run as root" >&2
  exit 2
fi

image_ref="$1"
root="${2:-/opt/cdn-platform}"
health_timeout="${DEPLOY_HEALTH_TIMEOUT_SECONDS:-120}"
image_pattern='^ghcr\.io/saginardo/simple_cdn(:[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}|@sha256:[a-f0-9]{64})$'
if [[ ! "$image_ref" =~ $image_pattern ]]; then
  echo "image must be a ghcr.io/saginardo/simple_cdn tag or digest" >&2
  exit 2
fi
if [[ "$root" != /* || "$root" == "/" ]]; then
  echo "root must be an absolute path below /" >&2
  exit 2
fi
root=$(realpath -m -- "$root")
if [[ "$root" == "/" ]]; then
  echo "root must resolve below /" >&2
  exit 2
fi
if [[ ! "$health_timeout" =~ ^[1-9][0-9]*$ ]] || ((health_timeout > 1800)); then
  echo "DEPLOY_HEALTH_TIMEOUT_SECONDS must be between 1 and 1800" >&2
  exit 2
fi

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
source_root=$(cd -- "$script_dir/.." && pwd)
for required in "$source_root/deploy/docker-compose.yaml" "$source_root/scripts/install-control-compose.sh"; do
  if [[ ! -e "$required" ]]; then
    echo "deployment bundle is missing $required" >&2
    exit 2
  fi
done
for required in "$root/compose.yaml" "$root/.env" "$root/app"; do
  if [[ ! -e "$required" ]]; then
    echo "existing Compose deployment is missing $required; run install-control-compose.sh first" >&2
    exit 2
  fi
done

compose_project_name() {
  local file="$1" project
  project=$(sed -nE 's/^name:[[:space:]]*"?([a-z0-9][a-z0-9_-]*)"?[[:space:]]*$/\1/p' "$file" | head -n 1)
  if [[ -z "$project" ]]; then
    echo "Compose definition has no valid top-level project name: $file" >&2
    return 1
  fi
  printf '%s\n' "$project"
}

active_project=$(compose_project_name "$root/compose.yaml")
target_project=$(compose_project_name "$source_root/deploy/docker-compose.yaml")
project_changed=0
if [[ "$active_project" != "$target_project" ]]; then
  project_changed=1
fi

service_is_running() {
  local project="$1" service="$2" running_services
  running_services=$(docker compose -p "$project" --profile backup ps --status running --services 2>/dev/null) || return 1
  grep -Fxq "$service" <<<"$running_services"
}

wait_for_control() {
  local project="$1" deadline=$((SECONDS + health_timeout))
  until docker compose -p "$project" exec -T control curl --fail --silent --insecure https://127.0.0.1:8443/healthz >/dev/null 2>&1; do
    if ((SECONDS >= deadline)); then
      docker compose -p "$project" logs --tail 50 control >&2 || true
      return 1
    fi
    sleep 2 & wait $!
  done
}

prune_obsolete_control_images() {
  local current_image="$1"
  local repository tag digest image_id reference
  local -A obsolete_image_ids=()
  while IFS='|' read -r repository tag digest image_id; do
    [[ -n "$image_id" && "$image_id" != "$current_image" ]] || continue
    case "$repository" in
      ghcr.io/saginardo/simple_cdn|ghcr.io/saginardo/cdn_platform|cdn-platform-control) ;;
      *) continue ;;
    esac
    obsolete_image_ids["$image_id"]=1
    if [[ "$tag" != "<none>" ]]; then
      reference="$repository:$tag"
      docker image rm "$reference" >/dev/null 2>&1 || true
    fi
    if [[ ("$repository" == ghcr.io/saginardo/simple_cdn || "$repository" == ghcr.io/saginardo/cdn_platform) && "$digest" != "<none>" ]]; then
      reference="$repository@$digest"
      docker image rm "$reference" >/dev/null 2>&1 || true
    fi
  done < <(docker image ls --digests --no-trunc --format '{{.Repository}}|{{.Tag}}|{{.Digest}}|{{.ID}}')
  for image_id in "${!obsolete_image_ids[@]}"; do
    docker image rm "$image_id" >/dev/null 2>&1 || true
  done
}

cd "$root"
if ! service_is_running "$active_project" control || ! service_is_running "$active_project" clickhouse; then
  echo "control and clickhouse must both be running before an automated deployment" >&2
  exit 1
fi
control_renew_was_running=0
backup_was_running=0
if service_is_running "$active_project" control-cert-renew; then control_renew_was_running=1; fi
if service_is_running "$active_project" backup; then backup_was_running=1; fi

echo "Pulling verified control image $image_ref"
docker pull "$image_ref"

rollback_root=$(mktemp -d "$root/.deploy-rollback.XXXXXX")
chmod 0700 "$rollback_root"
deployment_changed=0
deployment_committed=0
rollback_incomplete=0

restore_deployment_files() {
  rm -rf "$root/app" || return 1
  cp -a "$rollback_root/app" "$root/app" || return 1
  install -m 0644 "$rollback_root/compose.yaml" "$root/compose.yaml" || return 1
  install -m 0644 "$rollback_root/.env" "$root/.env" || return 1
}

rollback_deployment() {
  echo "Deployment failed; restoring the previous Compose definition and image" >&2
  set +e
  if ((project_changed)); then
    cd "$root" || return 1
    docker compose -p "$target_project" down || return 1
  fi
  restore_deployment_files || return 1
  cd "$root" || return 1
  docker compose -p "$active_project" config --quiet || return 1
  if ((project_changed)); then
    docker compose -p "$active_project" up -d --no-build control || return 1
  else
    docker compose -p "$active_project" up -d --no-build --no-deps control || return 1
  fi
  wait_for_control "$active_project" || return 1
  if ((control_renew_was_running)); then
    docker compose -p "$active_project" up -d --no-build --no-deps control-cert-renew || return 1
  fi
  if ((backup_was_running)); then
    docker compose -p "$active_project" --profile backup up -d --no-build --no-deps backup || return 1
  fi
  echo "Previous control deployment restored" >&2
  return 0
}

cleanup() {
  local exit_code=$?
  trap - EXIT
  if ((exit_code != 0 && deployment_changed && !deployment_committed)); then
    if ! rollback_deployment; then
      rollback_incomplete=1
      echo "automatic deployment rollback is incomplete; evidence retained at $rollback_root" >&2
    fi
  fi
  if ((!rollback_incomplete)); then
    rm -rf "$rollback_root"
  fi
  exit "$exit_code"
}
trap cleanup EXIT

cp -a "$root/compose.yaml" "$rollback_root/compose.yaml"
cp -a "$root/.env" "$rollback_root/.env"
cp -a "$root/app" "$rollback_root/app"
deployment_changed=1
(cd "$source_root" && ./scripts/install-control-compose.sh "$root" "$image_ref")
cd "$root"
docker compose -p "$target_project" --profile backup config --quiet

if ((project_changed)); then
  docker compose -p "$active_project" -f "$rollback_root/compose.yaml" --project-directory "$root" down
  docker compose -p "$target_project" up -d --no-build control
else
  docker compose -p "$target_project" up -d --no-build --no-deps control
fi
wait_for_control "$target_project"

expected_image=$(docker image inspect --format '{{.Id}}' "$image_ref")
control_container=$(docker compose -p "$target_project" ps -q control)
if [[ -z "$control_container" ]]; then
  echo "control container was not created" >&2
  exit 1
fi
running_image=$(docker inspect --format '{{.Image}}' "$control_container")
if [[ "$running_image" != "$expected_image" ]]; then
  echo "control container is not running the requested image" >&2
  exit 1
fi

if ((control_renew_was_running)); then
  docker compose -p "$target_project" up -d --no-build --no-deps control-cert-renew
  service_is_running "$target_project" control-cert-renew
fi
if ((backup_was_running)); then
  docker compose -p "$target_project" --profile backup up -d --no-build --no-deps backup
  service_is_running "$target_project" backup
fi

# A completed one-shot bootstrap container would otherwise pin an obsolete image.
docker compose -p "$target_project" rm -f control-cert-bootstrap >/dev/null 2>&1 || true
deployment_committed=1
prune_obsolete_control_images "$expected_image"
echo "Deployed $image_ref and verified control-plane health"
