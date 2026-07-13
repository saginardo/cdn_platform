#!/usr/bin/env bash
set -euo pipefail

backup_time="${BACKUP_TIME:-03:25}"
random_delay="${BACKUP_RANDOM_DELAY_SECONDS:-1200}"
if [[ ! "$backup_time" =~ ^([01][0-9]|2[0-3]):[0-5][0-9]$ ]]; then
  echo "BACKUP_TIME must use HH:MM" >&2
  exit 2
fi
if [[ ! "$random_delay" =~ ^[0-9]+$ ]]; then
  echo "BACKUP_RANDOM_DELAY_SECONDS must be a non-negative integer" >&2
  exit 2
fi

while true; do
  now_epoch=$(date +%s)
  next_epoch=$(date -d "today $backup_time" +%s)
  if ((next_epoch <= now_epoch)); then
    next_epoch=$(date -d "tomorrow $backup_time" +%s)
  fi
  if ((random_delay > 0)); then
    next_epoch=$((next_epoch + RANDOM % (random_delay + 1)))
  fi
  sleep_seconds=$((next_epoch - now_epoch))
  echo "next backup at $(date -d "@$next_epoch" --iso-8601=seconds)"
  sleep "$sleep_seconds" & wait $!
  /usr/local/lib/cdn-platform/compose-backup.sh
done
