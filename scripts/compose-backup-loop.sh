#!/usr/bin/env bash
set -euo pipefail

runtime_dir=$(mktemp -d /tmp/cdn-backup-schedule.XXXXXX)
trap 'rm -rf "$runtime_dir"' EXIT
# shellcheck source=/dev/null
source /usr/local/lib/cdn-platform/compose-backup-common.sh

schedule_key=""
next_epoch=0
while true; do
  rm -rf "$runtime_dir"/*
  load_backup_runtime "$runtime_dir"
  backup_time="$BACKUP_TIME"
  random_delay="$BACKUP_RANDOM_DELAY_SECONDS"
  current_key="$backup_time|$random_delay"
  now_epoch=$(date +%s)
  if [[ "$current_key" != "$schedule_key" || "$next_epoch" -eq 0 ]]; then
    next_epoch=$(date -d "today $backup_time" +%s)
    if ((next_epoch <= now_epoch)); then
      next_epoch=$(date -d "tomorrow $backup_time" +%s)
    fi
    if ((random_delay > 0)); then
      random_value=$(((RANDOM << 15) | RANDOM))
      next_epoch=$((next_epoch + random_value % (random_delay + 1)))
    fi
    schedule_key="$current_key"
    echo "next backup at $(date -d "@$next_epoch" --iso-8601=seconds) (settings: $BACKUP_SETTINGS_SOURCE)"
  fi
  if ((now_epoch >= next_epoch)); then
    /usr/local/lib/cdn-platform/compose-backup.sh
    next_epoch=0
    continue
  fi
  sleep_seconds=$((next_epoch - now_epoch))
  if ((sleep_seconds > 60)); then
    sleep_seconds=60
  fi
  sleep "$sleep_seconds" & wait $!
done
