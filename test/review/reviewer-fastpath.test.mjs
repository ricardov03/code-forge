import { answerFor, BINS, cfgFor, flag, freshDir, lines, makeRepo, writeFile } from './helpers.mjs';
import assert from 'node:assert/strict';
import { test } from 'node:test';

const { reviewFile } = await import('../../src/review/engine.mjs');
const { validateReview } = await import('../../src/review/validate-review.mjs');
const { spawnSession } = await import('../../src/session/spawn.mjs');
const { exec } = await import('../../src/util/exec.mjs');

const FILE = 'src/feature.mjs';
const HUNKS = ['@@ -0,0 +1,12 @@'];
const PRIMARY = 'claude-opus-5-5';
const FALLBACK = 'claude-sonnet-5';
const FINDING = { id: 'F1', file: FILE, line_start: 1, line_end: 1, severity: 'warning', category: 'correctness', claim: 'Missing boundary check', evidence: 'The input can be empty', fix: 'Check the input' };

/** @param {{answer?: any, strict?: boolean, ladder?: boolean, silence?: number, timeoutMs?: number, mode?: 'silent' | 'timeout' | 'capacity'}} [options] */
async function review({ answer = answerFor(HUNKS), strict = false, ladder = false, silence = 60, timeoutMs = 5000, mode } = {}) {
  const repo = await makeRepo();
  writeFile(repo, FILE, lines(12));
  const cfg = /** @type {Record<string, any>} */ (cfgFor({ silence_timeout_s: silence }));
  if (strict) {
    cfg.review.allow_open_book_codex = true;
    cfg.review.min_tokens_out = 17; // fake-codex reports 17 output tokens
    cfg.levels.L2 = { provider: 'openai', model: 'gpt-6-astra' };
  }
  if (ladder) cfg.levels.L2.fallback = [{ provider: 'anthropic', model: FALLBACK }];
  const calls = [];
  const results = [];
  const rows = [];
  const writeRow = async (row) => { rows.push(row); };
  const runRoot = freshDir('sessions');
  const spawn = (opts) => spawnSession({ cfg, ...opts, runRoot, timeoutMs }, {
    bins: BINS, writeRow, stderr: { write: () => true },
    env: { PATH: process.env.PATH ?? '', HOME: process.env.HOME ?? '', FAKE_ANSWER: JSON.stringify(answer) },
    exec: async (argv, options) => {
      const model = flag(argv, '--model') ?? flag(argv, '-m');
      calls.push(model);
      if (model === PRIMARY && mode === 'capacity') {
        return { result: 'failed', code: 1, signal: null, timedOut: false, stderr: '',
          stdout: JSON.stringify({ type: 'result', is_error: true, api_error_status: 529, result: 'API Error: at capacity' }) };
      }
      const sleeping = model === PRIMARY && (mode === 'silent' || mode === 'timeout');
      const result = await exec(argv, { ...options, env: { ...options?.env, ...(sleeping ? { FAKE_SLEEP_MS: '10000' } : {}) } });
      results.push({ model, ...result });
      return result;
    },
  });
  const started = Date.now();
  const outcome = await reviewFile({ repoRoot: repo, file: FILE, base: null, cfg, risk: 0, workDir: freshDir('packets') }, { spawn, writeRow });
  return { outcome, calls, results, rows, elapsed: Date.now() - started };
}

test('passed null with one finding is a failing review, with its finding and coercion recorded', async () => {
  for (const strict of [false, true]) {
    const { outcome, calls } = await review({ strict, answer: answerFor(HUNKS, {
      passed: null, findings: [strict ? { ...FINDING, agreement: null } : FINDING],
      ...(strict ? { resolved: null, needs_file: null } : {}),
    }) });
    assert.deepEqual([outcome.status, outcome.approved], ['reviewed', false], JSON.stringify(outcome));
    assert.deepEqual(outcome.findings, [FINDING]);
    assert.deepEqual(outcome.sessions.map((s) => [s.passed, s.passed_coerced]), [[false, true]]);
    assert.deepEqual(calls, [strict ? 'gpt-6-astra' : PRIMARY]);
  }
});

test('null clean passes require a summary, and coercion never hides other schema or stub failures', async () => {
  const { outcome } = await review({ answer: answerFor(HUNKS, { passed: null }) });
  assert.deepEqual([outcome.status, outcome.approved, outcome.findings], ['reviewed', true, []]);
  assert.deepEqual(outcome.sessions.map((s) => [s.passed, s.passed_coerced]), [[true, true]]);
  for (const extra of [{ summary: ' ' }, { summary: 3 }, { findings: [{}] }, { unexpected: true }]) {
    const bad = await review({ answer: answerFor(HUNKS, { passed: null, ...extra }) });
    assert.deepEqual([bad.outcome.status, bad.outcome.reason, bad.outcome.approved], ['unavailable', 'schema', false]);
    assert.deepEqual(bad.calls, [PRIMARY]);
  }
  const session = { status: 'ok', exit_code: 0, answer: answerFor(HUNKS, { passed: null }), usage: { tokens_out: 39 } };
  assert.equal(validateReview(session, { hunkHeaders: HUNKS }).reason, 'too_short');
  assert.equal(validateReview({ ...session, usage: { tokens_out: 120 } }, { hunkHeaders: [] }).reason, 'hunks_mismatch');
});

test('a silent reviewer is killed at one second and the fallback answers immediately', async () => {
  const { outcome, calls, results, rows, elapsed } = await review({ ladder: true, silence: 1, mode: 'silent' });
  assert.deepEqual(calls, [PRIMARY, FALLBACK]);
  assert.deepEqual([outcome.status, outcome.approved, outcome.sessions[0].model], ['reviewed', true, FALLBACK]);
  assert.deepEqual(results.map((r) => [r.model, r.timedOut, r.stdout.length, r.stderr.length]), [[PRIMARY, true, 0, 0], [FALLBACK, false, results[1].stdout.length, 0]]);
  assert.ok(results[0].signal, 'the silent process was terminated by a signal');
  assert.ok(elapsed >= 900, `silence fallback took ${elapsed} ms`);
  assert.equal(results[0].silenceTimedOut, true, 'the startup deadline, rather than the full session timeout, killed the process');
  assert.deepEqual(rows.filter((r) => r.event === 'session').map((r) => [r.model, r.status]), [[PRIMARY, 'timeout'], [FALLBACK, 'ok']]);
  assert.equal(rows.find((r) => r.event === 'session' && r.model === PRIMARY).reason, 'killed after 1000 ms without output');
  for (const stream of ['stdout', 'stderr']) {
    const output = await exec([process.execPath, '-e', `process.${stream}.write('ready'); setTimeout(() => {}, 1300);`], { silenceTimeoutMs: 1000, timeoutMs: 3000 });
    assert.equal(output.result, 'ok', `${stream} output disables the silence deadline`);
    assert.equal(output.timedOut, false);
    assert.deepEqual([output.stdout, output.stderr], stream === 'stdout' ? ['ready', ''] : ['', 'ready']);
  }
});

test('ordinary timeouts skip the primary retry when a fallback is configured', async () => {
  const { outcome, calls } = await review({ ladder: true, silence: 60, timeoutMs: 1000, mode: 'timeout' });
  assert.deepEqual(calls, [PRIMARY, FALLBACK]);
  assert.deepEqual([outcome.status, outcome.approved, outcome.sessions[0].model], ['reviewed', true, FALLBACK]);
  const alone = await review({ timeoutMs: 1000, mode: 'timeout' });
  assert.deepEqual(alone.calls, [PRIMARY, PRIMARY]);
  assert.deepEqual([alone.outcome.status, alone.outcome.reason], ['unavailable', 'timeout']);
});

test('an at-capacity answer uses one primary session then the fallback', async () => {
  const { outcome, calls, rows } = await review({ ladder: true, mode: 'capacity' });
  assert.deepEqual(calls, [PRIMARY, FALLBACK]);
  assert.deepEqual([outcome.status, outcome.approved, outcome.sessions[0].model], ['reviewed', true, FALLBACK]);
  assert.deepEqual(rows.filter((r) => r.event === 'session').map((r) => [r.model, r.status]), [[PRIMARY, 'unavailable'], [FALLBACK, 'ok']]);
  const alone = await review({ mode: 'capacity' });
  assert.deepEqual(alone.calls, [PRIMARY]);
  assert.deepEqual([alone.outcome.status, alone.outcome.reason, alone.outcome.approved], ['unavailable', 'exit', false]);
});

test('without a fallback, silence timeouts retain the existing single retry', async () => {
  const { outcome, calls, results, elapsed } = await review({ silence: 1, mode: 'silent' });
  assert.deepEqual([outcome.status, outcome.reason, outcome.approved], ['unavailable', 'timeout', false]);
  assert.deepEqual(calls, [PRIMARY, PRIMARY]);
  assert.deepEqual(results.map((r) => [r.timedOut, r.silenceTimedOut, r.stdout.length, r.stderr.length]), [[true, true, 0, 0], [true, true, 0, 0]]);
  assert.ok(elapsed >= 1800, `two silence kills took ${elapsed} ms`);
});
