#!/bin/sh
# B559: a fake npm or gh that waits for its own pid entry in the run root's
# registry and writes what it sees to $FAKELOG.
# The parent writes the entry after the child starts (a ps call for the
# start time comes first), so under machine load that can take seconds: the
# wait is capped at 30 s and ends as soon as the entry is there. A temp root
# this user cannot write can hold no entry: the fake says NOENTRY at once
# instead of spending the wait (B661: the 10 s --version timeout fired under
# load while the fake waited for an entry that could never come).
name=${0##*/}
i=0
[ -w "$CODE_FORGE_TMP_ROOT" ] || i=3000
while [ $i -lt 3000 ]; do
  for f in "$CODE_FORGE_TMP_ROOT"/proc-*/pids/$$.json; do
    [ -f "$f" ] || continue
    d=${f%/*}
    printf '%s %s %s SEEN %s %s %s\n' "$name" "$1" "$$" "$(ls -l "$f" | cut -c1-10)" "$(ls -ld "$d" | cut -c1-10)" "$(ls "$d" | tr '\n' ' ')" >> "$FAKELOG"
    cat "$f" >> "$FAKELOG"
    i=99999
  done
  [ $i -eq 99999 ] && break
  i=$((i+1)); sleep 0.01
done
[ $i -eq 99999 ] || printf '%s %s %s NOENTRY\n' "$name" "$1" "$$" >> "$FAKELOG"
case "$name $1 $2" in
  "npm view"*) printf '%s\n' "${NPMOUT:-0.22.1}" ;;
  "gh issue list") echo "[]" ;;
  "gh issue create") echo "Creating issue"; echo "https://github.com/x/y/issues/42" ;;
esac
exit 0
