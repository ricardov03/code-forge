import assert from 'node:assert/strict';
import { test } from 'node:test';
import { sink } from './helpers.mjs';
import { AGENT_ENV_VARS, detectAgentEnv, maybeEmitAgentJson, writeAgentJson } from '../../src/install/agent-env.mjs';

const SAMPLE_PAYLOAD = Object.freeze({ ok: true, wrote: ['harnesses'], harnesses: ['claude'], doctor: { status: 'OK' } });

// ── detectAgentEnv ─────────────────────────────────────────────────────────────

test('AGENT_ENV_VARS is exactly the 4 names, in the documented order', () => {
  assert.deepEqual(AGENT_ENV_VARS, ['CLAUDECODE', 'CLAUDE_CODE', 'CURSOR_AGENT', 'AI_AGENT']);
});

for (const name of ['CLAUDECODE', 'CLAUDE_CODE', 'CURSOR_AGENT', 'AI_AGENT']) {
  test(`detectAgentEnv detects ${name}=1, and reports it as the source`, () => {
    assert.deepEqual(detectAgentEnv({ [name]: '1' }), { isAgent: true, source: name });
  });
}

test('detectAgentEnv on an empty environment is not an agent', () => {
  assert.deepEqual(detectAgentEnv({}), { isAgent: false, source: null });
});

test('detectAgentEnv ignores an UNRELATED env var of the same general shape (not "any var" detection)', () => {
  assert.deepEqual(detectAgentEnv({ RANDOM_VAR: '1', PATH: '/usr/bin' }), { isAgent: false, source: null });
});

test('detectAgentEnv treats an EMPTY STRING value as not set', () => {
  assert.deepEqual(detectAgentEnv({ CLAUDECODE: '' }), { isAgent: false, source: null });
});

test('detectAgentEnv reports the FIRST matching name in AGENT_ENV_VARS order when several are set', () => {
  assert.deepEqual(detectAgentEnv({ AI_AGENT: 'x', CLAUDE_CODE: '1' }), { isAgent: true, source: 'CLAUDE_CODE' });
});

test('detectAgentEnv defaults to process.env when called with no argument', () => {
  const previous = process.env.CLAUDECODE;
  process.env.CLAUDECODE = '1';
  try {
    assert.equal(detectAgentEnv().isAgent, true);
  } finally {
    if (previous === undefined) {
      delete process.env.CLAUDECODE;
    } else {
      process.env.CLAUDECODE = previous;
    }
  }
});

// ── writeAgentJson ─────────────────────────────────────────────────────────────

test('writeAgentJson makes exactly ONE stream.write() call, ending in exactly one newline', () => {
  const stdout = sink();
  writeAgentJson(SAMPLE_PAYLOAD, { stdout });
  assert.equal(stdout.calls, 1);
  assert.equal((stdout.text.match(/\n/g) ?? []).length, 1);
  assert.equal(stdout.text.endsWith('\n'), true);
});

test('writeAgentJson\'s one line parses back to the exact payload (compact, single-line JSON)', () => {
  const stdout = sink();
  writeAgentJson(SAMPLE_PAYLOAD, { stdout });
  const line = stdout.text.trimEnd();
  assert.equal(line.includes('\n'), false, 'the JSON itself must not be pretty-printed across lines');
  assert.deepEqual(JSON.parse(line), SAMPLE_PAYLOAD);
});

test('writeAgentJson refuses an undefined payload rather than silently writing nothing', () => {
  const stdout = sink();
  assert.throws(() => writeAgentJson(undefined, { stdout }), TypeError);
  assert.equal(stdout.calls, 0);
});

// ── Acceptance: "CLAUDECODE=1 ⇒ exactly 1 JSON line on stdout" ────────────────

test('maybeEmitAgentJson with CLAUDECODE=1 writes exactly 1 JSON line and returns true', () => {
  const stdout = sink();
  const wrote = maybeEmitAgentJson(SAMPLE_PAYLOAD, { env: { CLAUDECODE: '1' }, stdout });

  assert.equal(wrote, true);
  assert.equal(stdout.calls, 1);
  const lines = stdout.text.split('\n').filter((l) => l.length > 0);
  assert.equal(lines.length, 1);
  assert.deepEqual(JSON.parse(lines[0]), SAMPLE_PAYLOAD);
});

test('maybeEmitAgentJson with NO agent env var writes ZERO lines and returns false', () => {
  const stdout = sink();
  const wrote = maybeEmitAgentJson(SAMPLE_PAYLOAD, { env: {}, stdout });

  assert.equal(wrote, false);
  assert.equal(stdout.calls, 0);
  assert.equal(stdout.text, '');
});
