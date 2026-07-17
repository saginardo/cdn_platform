#!/usr/bin/env bash
set -euo pipefail
umask 077

runtime_dir=$(mktemp -d /tmp/cdn-backup-runtime.XXXXXX)
trap 'rm -rf "$runtime_dir"' EXIT
# shellcheck source=/dev/null
source /usr/local/lib/cdn-platform/compose-backup-common.sh
load_backup_runtime "$runtime_dir"
restic "$@"
