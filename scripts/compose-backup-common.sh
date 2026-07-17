#!/usr/bin/env bash

_backup_env_repository="${RESTIC_REPOSITORY-}"
_backup_env_password="${RESTIC_PASSWORD-}"
_backup_env_password_file="${RESTIC_PASSWORD_FILE-}"
_backup_env_access_key_id="${AWS_ACCESS_KEY_ID-}"
_backup_env_secret_access_key="${AWS_SECRET_ACCESS_KEY-}"
_backup_env_session_token="${AWS_SESSION_TOKEN-}"
_backup_env_region="${AWS_DEFAULT_REGION-}"
_backup_env_time="${BACKUP_TIME-}"
_backup_env_random_delay="${BACKUP_RANDOM_DELAY_SECONDS-}"

load_backup_runtime() {
  local runtime_dir="${1:?backup runtime directory is required}"

  export RESTIC_REPOSITORY="$_backup_env_repository"
  export RESTIC_PASSWORD="$_backup_env_password"
  export RESTIC_PASSWORD_FILE="$_backup_env_password_file"
  export AWS_ACCESS_KEY_ID="$_backup_env_access_key_id"
  export AWS_SECRET_ACCESS_KEY="$_backup_env_secret_access_key"
  export AWS_SESSION_TOKEN="$_backup_env_session_token"
  export AWS_DEFAULT_REGION="$_backup_env_region"
  export BACKUP_TIME="$_backup_env_time"
  export BACKUP_RANDOM_DELAY_SECONDS="$_backup_env_random_delay"

  cdn-control backup-runtime "$runtime_dir"
  export RESTIC_REPOSITORY="$(<"$runtime_dir/repository")"
  export RESTIC_PASSWORD_FILE="$runtime_dir/restic-password"
  export AWS_ACCESS_KEY_ID="$(<"$runtime_dir/access-key-id")"
  export AWS_SECRET_ACCESS_KEY="$(<"$runtime_dir/secret-access-key")"
  export AWS_DEFAULT_REGION="$(<"$runtime_dir/region")"
  BACKUP_TIME="$(<"$runtime_dir/backup-time")"
  BACKUP_RANDOM_DELAY_SECONDS="$(<"$runtime_dir/random-delay-seconds")"
  BACKUP_SETTINGS_SOURCE="$(<"$runtime_dir/source")"
  unset RESTIC_PASSWORD RESTIC_PASSWORD_COMMAND AWS_REGION
  if [[ "$BACKUP_SETTINGS_SOURCE" != "environment" || -z "$_backup_env_session_token" ]]; then
    unset AWS_SESSION_TOKEN
  fi
}
