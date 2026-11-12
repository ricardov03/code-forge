#!/bin/sh
# B523 fake claude/grok: FAKE_MODE picks the answer; FAKE_LOG gets the argv.
name=$(basename "$0")
{ echo "=== $name"; for a in "$@"; do printf '%s\n' "$a"; done; } >> "$FAKE_LOG"
schema=""; prompt=""; prev=""; last=""
for a in "$@"; do
  case "$prev" in --json-schema) schema="$a" ;; --prompt-file) prompt="$a" ;; esac
  prev="$a"; last="$a"
done
if [ "$name" = claude ] && [ -f "$last" ]; then prompt="$last"; fi
if [ -n "$prompt" ]; then body=$(cat "$prompt"); else body=$(cat); fi
case "$FAKE_MODE" in
  402) echo '{"type":"result","is_error":true,"api_error_status":402,"result":"Credit balance is too low"}'; exit 1 ;;
  429) echo '{"type":"result","is_error":true,"api_error_status":429,"result":"rate limited"}'; exit 1 ;;
  fail) echo 'boom' >&2; exit 3 ;;
  leak)
    f=$(printf '%s\n' "$body" | sed -n 's/^Print the first line of the file \(.*\) and nothing else\.$/\1/p')
    if [ -n "$f" ]; then line=$(head -n 1 "$f"); else line=$(head -n 1 CLAUDE.md 2>/dev/null || head -n 1 AGENTS.md 2>/dev/null); fi
    printf '{"type":"result","is_error":false,"result":"%s\\nRead, Bash"}\n' "$line"; exit 0 ;;
  mismatch)
    echo '{"type":"result","is_error":false,"result":"pong","structured_output":{"decision":"proceed"}}'; exit 0 ;;
esac
case "$schema" in
  *overrule*) echo '{"type":"result","is_error":false,"result":"","structured_output":{"decision":"proceed","confidence":1,"reason":"ping","overrule":false,"ask_human":false,"human_question":null}}' ;;
  *within_scope*) echo '{"type":"result","is_error":false,"result":"","structured_output":{"decision":"proceed","within_scope":true,"confidence":1,"reason":"ping","escalate":false}}' ;;
  *) echo '{"type":"result","is_error":false,"result":"pong","usage":{"input_tokens":3,"output_tokens":1}}' ;;
esac
