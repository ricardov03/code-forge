#!/bin/sh
# B617 fake codex: --version, exec --help (the pinned help text next to this
# file), and exec runs: FAKE_LOG gets the argv, -o gets the answer
# (FAKE_MODE leak: the first line of the file the prompt names, else of the
# project doc in the cwd), stdout gets Codex's JSONL events.
here=$(dirname "$0")
[ -n "$B617_TD" ] && here="$B617_TD"
case "$1" in
--version) echo "codex-cli 9.9.9 (fake codex)"; exit 0 ;;
esac
if [ "$1" = exec ] && [ "$2" = --help ]; then cat "$here/codex.help"; exit 0; fi
{ echo "=== codex"; for a in "$@"; do printf '%s\n' "$a"; done; } >> "$FAKE_LOG"
out=""; prev=""; last=""
for a in "$@"; do
  [ "$prev" = "-o" ] && out="$a"
  prev="$a"; last="$a"
done
if [ "$last" = "-" ]; then body=$(cat); elif [ -f "$last" ]; then body=$(cat "$last"); else body=""; fi
answer=pong
case "$FAKE_MODE" in
  fail) echo '{"type":"thread.started","thread_id":"t1"}'; echo 'boom' >&2; exit 3 ;;
  leak)
    f=$(printf '%s\n' "$body" | sed -n 's/^Print the first line of the file \(.*\) and nothing else\.$/\1/p')
    if [ -n "$f" ]; then answer=$(head -n 1 "$f"); else answer=$(head -n 1 CLAUDE.md 2>/dev/null || head -n 1 AGENTS.md 2>/dev/null); fi
    answer="$answer
shell" ;;
esac
[ -n "$out" ] && printf '%s\n' "$answer" > "$out"
echo '{"type":"thread.started","thread_id":"t1"}'
echo '{"type":"turn.completed","usage":{"input_tokens":3,"output_tokens":1}}'
