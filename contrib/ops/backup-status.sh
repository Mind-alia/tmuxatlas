# backup-status.sh — sourced by a backup script to publish its progress in
# $TF_OPS_DIR/backup-status.json (schema 1) and, after a fully successful run
# (local copy and R2 copy), the same content in $TF_OPS_DIR/last-success.json. TmuxAtlas reads these files to
# show the Backups panel and to warn when a backup fails or is late.
#
# Usage, in the backup script (bash):
#
#   source ~/.local/libexec/tf-ops/backup-status.sh
#   tf_status_begin db-nightly 86400        # job name, expected interval (s)
#   tf_status_step dump;     pg_dump ... > "$file"
#   tf_status_step compress; zstd ...
#   tf_status_step local-write
#   tf_status_local "$file" "$(ls "$dir" | wc -l)"
#   tf_status_step r2-upload; rclone copy "$file" r2:bucket/ ; rc=$?
#   tf_status_step r2-verify; rclone check ... ; vrc=$?
#   tf_status_remote "$rc" "$vrc" "$(rclone lsf r2:bucket/ | wc -l)"
#   tf_status_step prune-local; ...
#   # the EXIT trap installed by tf_status_begin writes the final result.
#
# The library writes nothing else, never prints secrets it is not given and
# runs no command on behalf of TmuxAtlas. Messages are cut to 300 chars.
# Steps: dump compress local-write r2-upload r2-verify prune-local prune-r2.

TF_OPS_DIR=${TF_OPS_DIR:-/srv/tf-ops}
TF_LOCAL_RETENTION_DAYS=${TF_LOCAL_RETENTION_DAYS:-3}
TF_R2_RETENTION_DAYS=${TF_R2_RETENTION_DAYS:-14}

_tf_json_string() {
  local s=${1:0:300}
  s=${s//\\/\\\\}
  s=${s//\"/\\\"}
  s=$(printf '%s' "$s" | tr '\000-\037' ' ')
  printf '"%s"' "$s"
}

_tf_bool() { if [ "${1:-}" = 1 ] || [ "${1:-}" = true ]; then printf true; else printf false; fi; }

_tf_int() { case ${1:-} in '' | *[!0-9]*) printf 0 ;; *) printf '%s' "$1" ;; esac; }

_tf_now() { date -u +%Y-%m-%dT%H:%M:%SZ; }

_tf_write() { # _tf_write <file name> — writes the current state atomically
  local name=$1 tmp
  (umask 027 && mkdir -p "$TF_OPS_DIR") || return 0
  tmp=$(umask 027 && mktemp "$TF_OPS_DIR/.$name.XXXXXX") || return 0
  printf '{"schema":1,"job":%s,"run_id":%s,"result":%s,"started_at":%s,"finished_at":%s,"exit_code":%s,"step":%s,"failed_step":%s,"message":%s,' \
    "$(_tf_json_string "$_TF_JOB")" "$(_tf_json_string "$_TF_RUN_ID")" "$(_tf_json_string "$_TF_RESULT")" \
    "$(_tf_json_string "$_TF_STARTED_AT")" "$(_tf_json_string "$_TF_FINISHED_AT")" "$(_tf_int "$_TF_EXIT_CODE")" \
    "$(_tf_json_string "$_TF_STEP")" "$(_tf_json_string "$_TF_FAILED_STEP")" "$(_tf_json_string "$_TF_MESSAGE")" >"$tmp"
  printf '"local":{"ok":%s,"file":%s,"size":%s,"sha256":%s,"retention_days":%s,"count":%s},' \
    "$(_tf_bool "$_TF_LOCAL_OK")" "$(_tf_json_string "$_TF_LOCAL_FILE")" "$(_tf_int "$_TF_LOCAL_SIZE")" \
    "$(_tf_json_string "$_TF_LOCAL_SHA")" "$(_tf_int "$TF_LOCAL_RETENTION_DAYS")" "$(_tf_int "$_TF_LOCAL_COUNT")" >>"$tmp"
  printf '"remote":{"provider":"r2","attempted":%s,"ok":%s,"verified":%s,"rclone_exit_code":%s,"retention_days":%s,"count":%s},' \
    "$(_tf_bool "$_TF_REMOTE_ATTEMPTED")" "$(_tf_bool "$_TF_REMOTE_OK")" "$(_tf_bool "$_TF_REMOTE_VERIFIED")" \
    "$(_tf_int "$_TF_REMOTE_EXIT")" "$(_tf_int "$TF_R2_RETENTION_DAYS")" "$(_tf_int "$_TF_REMOTE_COUNT")" >>"$tmp"
  printf '"schedule":{"expected_interval_seconds":%s,"grace_seconds":%s,"max_duration_seconds":%s}}\n' \
    "$(_tf_int "$_TF_INTERVAL")" "$(_tf_int "$_TF_GRACE")" "$(_tf_int "$_TF_MAX_DURATION")" >>"$tmp"
  chmod 0640 "$tmp" && mv -f "$tmp" "$TF_OPS_DIR/$name" || rm -f "$tmp"
}

# tf_status_begin <job> [expected_interval_s=86400] [grace_s=3600] [max_duration_s=7200]
# Starts a run and installs an EXIT trap that calls tf_status_end. A script
# with its own EXIT trap should call `tf_status_end $?` from it instead.
tf_status_begin() {
  _TF_JOB=${1:?tf_status_begin: job name}
  _TF_INTERVAL=${2:-86400}
  _TF_GRACE=${3:-3600}
  _TF_MAX_DURATION=${4:-7200}
  _TF_STARTED_AT=$(_tf_now)
  _TF_RUN_ID="$(date -u +%Y%m%dT%H%M%SZ)-$$"
  _TF_RESULT=running _TF_FINISHED_AT="" _TF_EXIT_CODE=0 _TF_STEP="" _TF_FAILED_STEP="" _TF_MESSAGE=""
  _TF_LOCAL_OK=0 _TF_LOCAL_FILE="" _TF_LOCAL_SIZE=0 _TF_LOCAL_SHA="" _TF_LOCAL_COUNT=0
  _TF_REMOTE_ATTEMPTED=0 _TF_REMOTE_OK=0 _TF_REMOTE_VERIFIED=0 _TF_REMOTE_EXIT=0 _TF_REMOTE_COUNT=0 _TF_REMOTE_VERIFY_EXIT=""
  _TF_ENDED=0
  _tf_write backup-status.json
  trap 'tf_status_end $?' EXIT
}

# tf_status_step <step> — the step about to run; named in failed_step on failure.
tf_status_step() {
  _TF_STEP=$1
  _tf_write backup-status.json
}

# tf_status_message <text> — a short human message (no secrets).
tf_status_message() { _TF_MESSAGE=$1; }

# tf_status_local <file> [count of local backups kept]
tf_status_local() {
  _TF_LOCAL_FILE=$(basename -- "$1")
  _TF_LOCAL_SIZE=$(stat -c %s -- "$1" 2>/dev/null || echo 0)
  _TF_LOCAL_SHA=$(sha256sum -- "$1" 2>/dev/null | cut -d' ' -f1)
  _TF_LOCAL_COUNT=${2:-0}
  if [ -s "$1" ]; then _TF_LOCAL_OK=1; else _TF_LOCAL_OK=0; fi
  _tf_write backup-status.json
}

# tf_status_remote <rclone exit code> [verify exit code] [count kept on R2]
# The copy is ok when rclone exits 0; verified when the check also exits 0.
# Without a verify exit code the copy counts as ok but not verified.
tf_status_remote() {
  _TF_REMOTE_ATTEMPTED=1
  _TF_REMOTE_EXIT=${1:-1}
  if [ "$_TF_REMOTE_EXIT" = 0 ]; then _TF_REMOTE_OK=1; else _TF_REMOTE_OK=0; fi
  _TF_REMOTE_VERIFY_EXIT=${2:-}
  if [ "$_TF_REMOTE_OK" = 1 ] && [ "$_TF_REMOTE_VERIFY_EXIT" = 0 ]; then _TF_REMOTE_VERIFIED=1; else _TF_REMOTE_VERIFIED=0; fi
  _TF_REMOTE_COUNT=${3:-0}
  _tf_write backup-status.json
}

# tf_status_end <exit code> — writes the final result. Called by the EXIT trap.
#   success: exit 0, local copy and R2 copy ok
#   partial: local copy ok, then the R2 copy or a later step failed
#   failure: no valid local copy
tf_status_end() {
  local code=${1:-0}
  [ "${_TF_ENDED:-0}" = 1 ] && return 0
  _TF_ENDED=1
  _TF_EXIT_CODE=$code
  _TF_FINISHED_AT=$(_tf_now)
  if [ "$_TF_LOCAL_OK" != 1 ]; then
    _TF_RESULT=failure
    _TF_FAILED_STEP=${_TF_STEP:-unknown}
  elif [ "$code" = 0 ] && [ "$_TF_REMOTE_OK" = 1 ] && { [ -z "$_TF_REMOTE_VERIFY_EXIT" ] || [ "$_TF_REMOTE_VERIFIED" = 1 ]; }; then
    _TF_RESULT=success
  else
    _TF_RESULT=partial
    if [ "$code" != 0 ]; then
      _TF_FAILED_STEP=${_TF_STEP:-unknown}
    elif [ "$_TF_REMOTE_OK" = 1 ]; then
      _TF_FAILED_STEP=r2-verify
    else
      _TF_FAILED_STEP=r2-upload
      [ "$_TF_REMOTE_ATTEMPTED" = 1 ] || [ -n "$_TF_MESSAGE" ] || _TF_MESSAGE="copie R2 jamais tentée"
    fi
  fi
  _tf_write backup-status.json
  if [ "$_TF_RESULT" = success ]; then
    _tf_write last-success.json
  fi
  return 0
}
