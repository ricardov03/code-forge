#!/bin/sh
# B567: a fake npm for init step 1. It waits for its own pid entry in a lazy
# proc-* root under $B567_TMP (the tmp.root of the config at the process
# start) and appends what it sees to $FAKELOG (pid and start time masked).
i=0
while [ $i -lt 100 ]; do
  for f in "$B567_TMP"/proc-*/pids/$$.json; do
    if [ -f "$f" ]; then printf 'npm %s SEEN %s\n' "$1" "$(cat "$f" | sed 's/"pid":[0-9]*/"pid":<PID>/; s/"start_time":"[^"]*"/"start_time":"<START>"/')" >> "$FAKELOG"; exit 0; fi
  done
  i=$((i+1)); sleep 0.01
done
printf 'npm %s NOENTRY\n' "$1" >> "$FAKELOG"
exit 0
