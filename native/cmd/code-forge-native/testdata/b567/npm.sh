#!/bin/sh
# B567: a fake npm for init step 1. It waits for its own pid entry in a lazy
# proc-* root under $B567_TMP (the tmp.root of the config at the process
# start) and appends what it sees to $FAKELOG (pid and start time masked).
# The parent writes the entry after the child starts (a ps call for the
# start time comes first), so under machine load that can take seconds: the
# wait is capped at 30 s and ends as soon as the entry is there. A temp root
# this user cannot write can hold no entry: the fake says NOENTRY at once
# instead of spending the wait (B661: the 10 s --version timeout fired under
# load while the fake waited for an entry that could never come).
i=0
[ -w "$B567_TMP" ] || i=3000
while [ $i -lt 3000 ]; do
  for f in "$B567_TMP"/proc-*/pids/$$.json; do
    if [ -f "$f" ]; then printf 'npm %s SEEN %s\n' "$1" "$(cat "$f" | sed 's/"pid":[0-9]*/"pid":<PID>/; s/"start_time":"[^"]*"/"start_time":"<START>"/')" >> "$FAKELOG"; exit 0; fi
  done
  i=$((i+1)); sleep 0.01
done
printf 'npm %s NOENTRY\n' "$1" >> "$FAKELOG"
exit 0
