#!/bin/sh
in=$(cat)
heads=$(printf '%s\n' "$in" | grep -E '^@@ -[0-9]+(,[0-9]+)? \+[0-9]+(,[0-9]+)? @@' | sed -E 's/^(@@ -[0-9,]+ \+[0-9,]+ @@).*/\1/' | awk '!s[$0]++' | sed 's/.*/"&"/' | paste -sd, -)
case "$in" in
*"# Answered findings"*"(a new file: its full content)"*) printf '%s\n==\n' "$in" >> "$(dirname "$0")/answers.log"; printf '%s' '{"type":"result","is_error":false,"result":"done","structured_output":{"verdicts":[{"id":"ZZ","verdict":"resolved","reason":"not asked"},{"id":"F1","verdict":"resolved","reason":"the new file removes it"}]},"usage":{"input_tokens":700,"output_tokens":90}}' ;;
*"# Answered findings"*) printf '%s\n==\n' "$in" >> "$(dirname "$0")/answers.log"; printf '%s' '{"type":"result","is_error":false,"result":"done","structured_output":{"verdicts":[{"id":"F1","verdict":"not_resolved","reason":"the diff leaves WARNME"},{"id":"F1","verdict":"resolved","reason":"again"}]},"usage":{"input_tokens":800,"output_tokens":80}}' ;;
*WARNME*) printf '%s' '{"type":"result","is_error":false,"result":"done","structured_output":{"passed":false,"summary":"fake review","reviewed_hunks":['"$heads"'],"findings":[{"id":"F1","file":"b.txt","line_start":2,"line_end":2,"severity":"warning","category":"style","claim":"a warning","evidence":"the line says WARNME","fix":"remove it"}]},"usage":{"input_tokens":1000,"output_tokens":300}}' ;;
*) printf '%s' '{"type":"result","is_error":false,"result":"done","structured_output":{"passed":true,"summary":"fake review","reviewed_hunks":['"$heads"'],"findings":[]},"usage":{"input_tokens":1000,"output_tokens":300}}' ;;
esac
