#!/bin/sh
# B567: a fake claude, grok, op or git for the doctor. It waits for its own
# pid entry under $B567_TMP/<root>/pids/ and appends one record
# to $FAKELOG (name, first two arguments, pid, the entry's and the folder's
# modes, the root's kind: doctor or proc), then answers. A git child of a
# worker (Node's real worker in the golden run) is not recorded. B567_TMP is
# the temp root (a provider session's env drops CODE_FORGE_TMP_ROOT).
name=${0##*/}
log=1
if [ "$name" = git ] && [ -n "$CODE_FORGE_OWNER_PID" ] &&
  ps -o command= -p "$CODE_FORGE_OWNER_PID" 2>/dev/null | grep -q ' worker'; then
  log=
fi
if [ -n "$log" ]; then
  a="$1${2:+ $2}"
  i=0
  while [ $i -lt 100 ]; do
    for f in "$B567_TMP"/*/pids/$$.json; do
      [ -f "$f" ] || continue
      d=${f%/*}; r=${d%/pids}; r=${r##*/}; r=${r%%-*}
      printf '%s %s %s SEEN %s %s %s\n%s\n' "$name" "$a" "$$" "$(ls -l "$f" | cut -c1-10)" "$(ls -ld "$d" | cut -c1-10)" "$r" "$(cat "$f")" >> "$FAKELOG"
      i=999
    done
    [ $i -eq 999 ] && break
    i=$((i+1)); sleep 0.01
  done
  [ $i -eq 999 ] || printf '%s %s %s NOENTRY\n' "$name" "$a" "$$" >> "$FAKELOG"
fi
case "$name" in
  git) exec /usr/bin/git "$@" ;;
  op) printf 'b567-op-secret\n'; exit 0 ;;
esac
case "$1" in
  --version) echo "9.9.9 (fake $name)"; exit 0 ;;
  --help) cat "$B567_TESTDATA/b525/$name.help"; exit 0 ;;
esac
. "$B567_TESTDATA/b523/fake.sh"
