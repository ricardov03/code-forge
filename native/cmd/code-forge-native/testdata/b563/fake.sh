#!/bin/sh
# B563: a fake recommended tool (or npm) that waits for its own pid entry in
# the run root's registry and writes what it sees to $FAKELOG.
name=${0##*/}
i=0
while [ $i -lt 100 ]; do
  for f in "$CODE_FORGE_TMP_ROOT"/proc-*/pids/$$.json; do
    [ -f "$f" ] || continue
    d=${f%/*}
    printf '%s %s %s SEEN %s %s %s\n' "$name" "$1" "$$" "$(ls -l "$f" | cut -c1-10)" "$(ls -ld "$d" | cut -c1-10)" "$(ls "$d" | tr '\n' ' ')" >> "$FAKELOG"
    cat "$f" >> "$FAKELOG"
    i=999
  done
  [ $i -eq 999 ] && break
  i=$((i+1)); sleep 0.01
done
[ $i -eq 999 ] || printf '%s %s %s NOENTRY\n' "$name" "$1" "$$" >> "$FAKELOG"
case "$name $1" in
  *" --version") printf '%s 1.2.3\n' "$name" ;;
  "npm install") printf 'added 1 package\n' ;;
esac
exit 0
