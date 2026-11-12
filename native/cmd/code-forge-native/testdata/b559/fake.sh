#!/bin/sh
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
case "$name $1 $2" in
  "npm view"*) printf '%s\n' "${NPMOUT:-0.22.1}" ;;
  "gh issue list") echo "[]" ;;
  "gh issue create") echo "Creating issue"; echo "https://github.com/x/y/issues/42" ;;
esac
exit 0
