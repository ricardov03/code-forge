#!/bin/sh
in=$(cat)
heads=$(printf '%s\n' "$in" | grep -E '^@@ -[0-9]+(,[0-9]+)? \+[0-9]+(,[0-9]+)? @@' | sed -E 's/^(@@ -[0-9,]+ \+[0-9,]+ @@).*/\1/' | sed 's/.*/"&"/' | paste -sd, -)
case "$in" in
*OTHERFILE*) printf '%s' '{"type":"result","is_error":false,"result":"done","structured_output":{"passed":false,"summary":"fake review","reviewed_hunks":['"$heads"'],"findings":[{"id":"F1","file":"c.txt","line_start":2,"line_end":2,"severity":"warning","category":"style","claim":"a warning","evidence":"elsewhere","fix":"remove it"}]},"usage":{"input_tokens":1000,"output_tokens":300}}' ;;
*WARNME*) printf '%s' '{"type":"result","is_error":false,"result":"done","structured_output":{"passed":false,"summary":"fake review","reviewed_hunks":['"$heads"'],"findings":[{"id":"F1","file":"b.txt","line_start":2,"line_end":2,"severity":"warning","category":"style","claim":"a warning","evidence":"the line says WARNME","fix":"remove it"}]},"usage":{"input_tokens":1000,"output_tokens":300}}' ;;
*) printf '%s' '{"type":"result","is_error":false,"result":"done","structured_output":{"passed":true,"summary":"fake review","reviewed_hunks":['"$heads"'],"findings":[]},"usage":{"input_tokens":1000,"output_tokens":300}}' ;;
esac
