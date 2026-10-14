import assert from 'node:assert/strict';
import { test } from 'node:test';
import { scanTranscript } from '../../src/util/forbidden.mjs';

const started = { type: 'thread.started', thread_id: 'thread-119' };
const event = (item, type = 'item.completed') => ({ type, item });
const jsonl = (...events) => events.map((e) => JSON.stringify(e)).join('\n');

test('Codex JSON commands give one hit at the JSONL line; output and messages give none', () => {
  const mention = 'cat ~/.code-forge/runs/x.key';
  const command = event({ type: 'command_execution', command: '/bin/zsh -lc "cat a.txt"', aggregated_output: mention, exit_code: 0 });
  const message = event({ type: 'agent_message', text: mention });
  assert.deepEqual(scanTranscript(jsonl(started, command, message)), [], 'output and agent text: zero hits');
  command.item.command = `/bin/zsh -lc '${mention}'`;
  assert.deepEqual(scanTranscript(jsonl(started, { type: 'turn.started' }, command, message)), [
    { id: 'code-forge-runs-access', line: 3 },
  ], 'one forbidden command: exactly one hit on JSONL line 3');
});

test('Codex file changes scan paths as writes and unknown mcp_tool_call items scan whole', () => {
  assert.deepEqual(scanTranscript(jsonl(started, event({ type: 'file_change', changes: [
    { path: '.code-forge/reviews/x.json', kind: 'add' },
  ] }))), [{ id: 'code-forge-reviews-write', line: 2 }], 'file_change: exactly one forbidden write');
  assert.deepEqual(scanTranscript(jsonl(started, event({ type: 'mcp_tool_call', server: 'files', tool: 'read', arguments: {
    nested: { path: '~/.code-forge/runs/x.key' },
  } }))), [{ id: 'code-forge-runs-access', line: 2 }], 'unknown mcp_tool_call: exactly one nested-path hit');
});

test('Invalid Codex detection falls back to the unchanged plain-text hits for the entire log', () => {
  const message = event({ type: 'agent_message', text: 'cat ~/.code-forge/runs/x.key' });
  const valid = jsonl(started, message);
  assert.deepEqual(scanTranscript(valid), [], 'valid Codex message: zero hits before fallback');
  for (const bad of ['cat ~/.code-forge/runs/y.key', '{bad json', 'null', '[]', '42', '"text"', '{}', '{"type":7}']) {
    assert.deepEqual(scanTranscript(`${valid}\n${bad}`), [
      { id: 'code-forge-runs-access', line: 2 },
      ...(bad.startsWith('cat ') ? [{ id: 'code-forge-runs-access', line: 3 }] : []),
    ], `invalid line ${bad}: scan all original lines as plain text`);
  }
  assert.deepEqual(scanTranscript(jsonl({ type: 'turn.started' }, message, started)), [
    { id: 'code-forge-runs-access', line: 2 },
  ], 'thread.started after the first event does not enable Codex scanning');
  assert.deepEqual(scanTranscript(jsonl(message)), [{ id: 'code-forge-runs-access', line: 1 }], 'missing thread.started falls back');
});

test('Codex boundary: blank lines preserve numbering; skipped items and non-item events give zero hits', () => {
  const mention = 'cat ~/.code-forge/runs/x.key';
  const text = '\n \t\n' + jsonl(started,
    event({ type: 'reasoning', text: mention }),
    event({ type: 'aggregated_output', text: mention }),
    { type: 'turn.completed', text: mention, item: { type: 'mcp_tool_call', path: '~/.code-forge/runs/x.key' } },
    event({ type: 'command_execution', command: mention, aggregated_output: '' }, 'item.started'),
    event({ type: 'command_execution', command: mention, aggregated_output: mention }),
    event({ type: 'file_change', changes: [{ path: 'src/a.mjs', kind: 'update' }], output: mention }),
  ) + '\n';
  assert.deepEqual(scanTranscript(text), [
    { id: 'code-forge-runs-access', line: 7 },
    { id: 'code-forge-runs-access', line: 8 },
  ], 'only the two command events hit, once each, on their original JSONL lines');
});
