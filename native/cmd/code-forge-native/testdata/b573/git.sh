#!/bin/sh
# B573: a fake git on PATH. A child started with exec.mjs's owner variables
# waits for its own pid entry in a root under $CODE_FORGE_TMP_ROOT and writes
# what it sees to $FAKELOG (who spawned it: the worker, the daemon or the CLI
# verb); then the real git runs.
if [ -n "$CODE_FORGE_OWNER_PID" ]; then
  i=0
  seen=
  while [ $i -lt 100 ]; do
    for f in "$CODE_FORGE_TMP_ROOT"/*/pids/$$.json; do
      [ -f "$f" ] && seen=$f
    done
    [ -n "$seen" ] && break
    i=$((i+1)); sleep 0.01
  done
  cmd=$(/bin/ps -o command= -p "$CODE_FORGE_OWNER_PID" 2>/dev/null)
  case "$cmd" in
    *"internal serve"*) who=serve ;;
    *" worker "*|*" worker") who=worker ;;
    *) who=cli ;;
  esac
  if [ -n "$seen" ]; then
    root=${seen%/pids/*}
    root=${root##*/}
    printf '%s git SEEN %s %s self=%s owner=%s start=%s run=%s\n' "$who" "$root" "$(cat "$seen")" "$$" "$CODE_FORGE_OWNER_PID" "${CODE_FORGE_OWNER_START_TIME:-none}" "${CODE_FORGE_RUN_ID:-none}" >> "$FAKELOG"
  else
    printf '%s git NOENTRY self=%s owner=%s start=%s run=%s\n' "$who" "$$" "$CODE_FORGE_OWNER_PID" "${CODE_FORGE_OWNER_START_TIME:-none}" "${CODE_FORGE_RUN_ID:-none}" >> "$FAKELOG"
  fi
fi
exec /usr/bin/git "$@"
