#!/bin/sh
# B567: a fake claude, grok, op or git for the doctor. It waits for its own
# pid entry under $B567_TMP/<root>/pids/ and appends one record
# to $FAKELOG (name, first two arguments, pid, the entry's and the folder's
# modes, the root's kind: doctor or proc), then answers. A git child of a
# worker (Node's real worker in the golden run) is not recorded. B567_TMP is
# the temp root (a provider session's env drops CODE_FORGE_TMP_ROOT).
# The parent writes the entry after the child starts (a ps call for the
# start time comes first), so under machine load that can take seconds: the
# wait is capped at 30 s and ends as soon as the entry is there. A temp root
# this user cannot write can hold no entry: the fake says NOENTRY at once
# instead of spending the wait (B661: the 10 s --version timeout fired under
# load while the fake waited for an entry that could never come).
name=${0##*/}
log=1
if [ "$name" = git ] && [ -n "$CODE_FORGE_OWNER_PID" ] &&
  ps -o command= -p "$CODE_FORGE_OWNER_PID" 2>/dev/null | grep -q ' worker'; then
  log=
fi
if [ -n "$log" ]; then
  a="$1${2:+ $2}"
  i=0
  [ -w "$B567_TMP" ] || i=3000
  while [ $i -lt 3000 ]; do
    for f in "$B567_TMP"/*/pids/$$.json; do
      [ -f "$f" ] || continue
      d=${f%/*}; r=${d%/pids}; r=${r##*/}; r=${r%%-*}
      printf '%s %s %s SEEN %s %s %s\n%s\n' "$name" "$a" "$$" "$(ls -l "$f" | cut -c1-10)" "$(ls -ld "$d" | cut -c1-10)" "$r" "$(cat "$f")" >> "$FAKELOG"
      i=99999
    done
    [ $i -eq 99999 ] && break
    i=$((i+1)); sleep 0.01
  done
  [ $i -eq 99999 ] || printf '%s %s %s NOENTRY\n' "$name" "$a" "$$" >> "$FAKELOG"
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
