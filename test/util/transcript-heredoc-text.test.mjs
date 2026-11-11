import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import { mergeForbidden, scanTranscript } from '../../src/util/forbidden.mjs';

// B415: a runs path that is only TEXT written to a file (heredoc body, quoted echo/printf
// argument into plans/) is not an access; a real read, list, cd or Read tool use still is.
const coderRules = mergeForbidden([]);
const RUNS = '~/.code-forge/runs';
const base = { parentUuid: null, isSidechain: true, sessionId: 'session-b415' };
const assistant = (...content) => JSON.stringify({ ...base, type: 'assistant', message: { role: 'assistant', content } });
const tool = (name, input) => ({ type: 'tool_use', id: 'toolu_1', name, input });
const bash = (command) => assistant(tool('Bash', { command, description: 'run' }));
const scan = (...lines) => scanTranscript(lines.join('\n'), coderRules);
const runsHit = (line) => [{ id: 'code-forge-runs-access', line }];

test('B415: the real B406 line 165 (cat > acc.yml heredoc naming the lock path) scans with zero hits', () => {
  const line = readFileSync(new URL('../fixtures/transcripts/b406-line165.jsonl', import.meta.url), 'utf8');
  assert.ok(line.includes(`${RUNS}/R.lock`));
  assert.deepEqual(scanTranscript(line, coderRules), []);
});

test('B415: a cat or tee heredoc body that names the runs path is not an access', () => {
  const body = `- clause: "gates run while ${RUNS}/R.lock exists exits 75"\n  tests: [x_test.go]`;
  assert.deepEqual(scan(
    bash(`mkdir -p plans/reviews/b1 && cat > plans/reviews/b1/acc.yml <<'EOF'\n${body}\nEOF\ngit log --oneline -1`),
    bash(`cat >> notes.txt <<EOF\n${body}\nEOF`),
    bash(`cat <<-'EOF' > plans/x.md\n\t${body}\n\tEOF`),
    bash(`tee plans/x.md <<'EOF' >/dev/null\n${body}\nEOF`),
  ), []);
});

test('B415: a quoted echo/printf argument written into plans/ is not an access; outside plans/ it still is', () => {
  assert.deepEqual(scan(
    bash(`echo "lock: ${RUNS}/R.lock" > plans/reviews/b1/acc.yml`),
    bash(`printf '%s\\n' '${RUNS}/R.lock' >> /repo/plans/notes.md`),
    bash(`echo "lock: ${RUNS}/R.lock" > notes.txt`),
  ), runsHit(3));
});

test('B415: real accesses still hit exactly once on their own line', () => {
  assert.deepEqual(scan(
    bash(`cat ${RUNS}/x.lock`),
    bash('ls .code-forge/runs'),
    bash('cd ~ && ls .code-forge/runs/r1'),
    assistant(tool('Read', { file_path: `${RUNS}/x.lock` })),
    bash(`cat <<'EOF' | sh\ncat ${RUNS}/x.key\nEOF`),
    bash(`cat > out.txt <<EOF\n$(cat ${RUNS}/x.key)\nEOF`),
    bash(`bash <<'EOF'\ncat ${RUNS}/x.key\nEOF`),
    bash(`cat > ${RUNS}/x.key <<'EOF'\nk\nEOF`),
    bash(`cd ${RUNS} && ls`),
    bash(`grep k < ${RUNS}/x.key`),
    bash(`echo "$(cat ${RUNS}/x.key)" > plans/leak.md`),
  ), Array.from({ length: 11 }, (_, i) => runsHit(i + 1)[0]));
});
