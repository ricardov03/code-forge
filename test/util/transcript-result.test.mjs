import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import { mergeForbidden, scanTranscript } from '../../src/util/forbidden.mjs';

// B628 (#37): the reporter's session log is one line, the final result object of
// `claude -p --output-format json`. Its result text quotes an owner note naming the owner-only
// close flag; the coder never ran it. Same fixture as the Go test.
const rules = mergeForbidden([]);
const LINE = readFileSync(new URL('../../native/gatecheck/testdata/b628_claude_result.json', import.meta.url), 'utf8').replace(/\n$/, '');
const FLAG = 'code-forge-no-require-reviews';
const ids = (text) => scanTranscript(text, rules).map((h) => `${h.line}:${h.id}`);

test('B628: a Claude result object has no command hits', () => {
  assert.ok(!LINE.includes('\n') && LINE.includes('--no-require-reviews'));
  for (const text of [LINE, `${LINE}\n`, `\n${LINE}\n\n`, `${LINE}\n${LINE}`]) {
    assert.deepEqual(ids(text), []);
  }
});

test('B628: anything else stays a plain-text grep', () => {
  const obj = JSON.parse(LINE);
  const variant = (edit) => { const m = { ...obj }; edit(m); return JSON.stringify(m); };
  /** @type {[string, number][]} */
  const cases = [
    ['The owner will close this block with `--no-require-reviews`.', 1],
    [`${LINE}\nnote: code-forge block close B1 --no-require-reviews`, 1],
    [`hello\n${LINE}`, 2],
    [`Reading additional input from stdin...\n${LINE}`, 2],
    [variant((m) => { m.type = 'summary'; }), 1],
    [variant((m) => { m.result = [m.result]; }), 1],
    [variant((m) => { m.text = m.result; delete m.result; }), 1],
    [`${LINE}\n[1]`, 1],
  ];
  for (const [text, line] of cases) assert.ok(ids(text).includes(`${line}:${FLAG}`), text.slice(0, 60));
});

test('B628: a Claude stream Bash tool_use running the flag is still flagged', () => {
  const { result } = JSON.parse(LINE);
  const answer = JSON.stringify({ type: 'assistant', sessionId: 's1', message: { role: 'assistant', content: [{ type: 'text', text: result }] } });
  assert.deepEqual(ids(answer), []);
  const bash = JSON.stringify({ type: 'assistant', sessionId: 's1', message: { role: 'assistant', content: [
    { type: 'tool_use', id: 't1', name: 'Bash', input: { command: 'code-forge block close B1 --no-require-reviews' } }] } });
  assert.deepEqual(ids(`${answer}\n${bash}`), [`2:${FLAG}`]);
});
