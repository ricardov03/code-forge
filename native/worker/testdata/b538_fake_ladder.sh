#!/bin/sh
in=$(cat)
heads=$(printf '%s\n' "$in" | grep -E '^@@ -[0-9]+(,[0-9]+)? \+[0-9]+(,[0-9]+)? @@' | sed -E 's/^(@@ -[0-9,]+ \+[0-9,]+ @@).*/\1/' | awk '!s[$0]++' | sed 's/.*/"&"/' | paste -sd, -)
case " $* " in
*" claude-x "*) printf '%s' '{"type":"result","is_error":false,"result":"done","structured_output":{"passed":true,"reviewed_hunks":[],"findings":[]},"usage":{"input_tokens":10,"output_tokens":3}}'; exit 0 ;;
esac
case "$in" in
*WARNME*) printf '%s' '{"type":"result","is_error":false,"result":"done","structured_output":{"passed":false,"summary":"fake review","reviewed_hunks":['"$heads"'],"findings":[{"id":"F1","file":"b.txt","line_start":2,"line_end":2,"severity":"warning","category":"style","claim":"a warning","evidence":"the line says WARNME","fix":"remove it"}]},"usage":{"input_tokens":1000,"output_tokens":300}}' ;;
*) printf '%s' '{"type":"result","is_error":false,"result":"done","structured_output":{"passed":true,"summary":"fake review","reviewed_hunks":['"$heads"'],"findings":[]},"usage":{"input_tokens":1000,"output_tokens":300}}' ;;
esac
