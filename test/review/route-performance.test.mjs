import { cfgFor, freshDir, makeRepo, writeFile } from './helpers.mjs';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import { stringify } from 'yaml';

const routing = await import('../../src/review/route.mjs');
const { createWorker } = await import('../../src/worker/loop.mjs');
const { enqueue, readResult } = await import('../../src/worker/queue.mjs');
const { startRun } = await import('../../src/state/run.mjs');
const { openBlock } = await import('../../src/state/block.mjs');
const { loadKey, verifyRow } = await import('../../src/state/signer.mjs');
const { createKeyStore } = await import('../../src/keys/store.mjs');
const { appendRow } = await import('../../src/ledger/write.mjs');
const { defaultsForProvider } = await import('../../src/config/defaults/index.mjs');
const { validateConfig } = await import('../../src/config/validate.mjs');
const { runDoctor } = await import('../../src/doctor/index.mjs');
const { renderText } = await import('../../src/doctor/rows.mjs');
const { default: status } = await import('../../src/cli/status.mjs');

const NOW = Date.parse('2026-10-31T12:00:00Z');
const A = 'claude-opus-5-5';
const B = 'grok-4.7';
/** @param {Record<string, any>} [review] @returns {Record<string, any>} */
const config = (review = {}) => {
  const cfg = cfgFor({ adaptive: { explore: 0 }, ...review });
  cfg.levels.L2 = { model: A, fallback: [{ provider: 'xai', model: B }] };
  return cfg;
};

/** Actual ledger shapes: one row per attempt, plus the engine's session summaries.
 * @returns {Array<Record<string, any>>}
 */
function history(model, { seconds = 10, failures = 0, waived = 0, n = 10, ts = new Date(NOW).toISOString(), run = 'old-run' } = {}) {
  return Array.from({ length: n }, (_, i) => {
    const file = `${model}-${i}.mjs`;
    const status = i < failures ? 'timeout' : 'ok';
    return [
      { event: 'session', run, block: 'B1', role: 'reviewer', lens: 'full', model, status, duration_ms: seconds * 1000, ts },
      { event: 'review.result', run, block: 'B1', file, ts, sessions: [{ role: 'reviewer', lens: 'full', model, status, findings: [{ id: 'F1', severity: 'critical' }] }] },
      ...(i < waived ? [{ event: 'review.waived', run, block: 'B1', file, finding: 'F1', ts }] : []),
    ];
  }).flat();
}

const choose = (rows, cfg = config(), ticketId = 'ticket-1', lines = 20) =>
  routing.routeTicket(cfg, { lines, timeoutS: 300 }, { rows, now: NOW, ticketId });

test('availability: 30% timeouts or schema failures lose at equal speed and accuracy', () => {
  for (const failure of ['timeout', 'invalid-output', 'failed', 'silence']) {
    const bad = history(A, { failures: 3 }).map((r) => r.event === 'session' && r.status === 'timeout' ? { ...r, status: failure } : r);
    assert.equal(choose([...bad, ...history(B)]).cfg.levels.L2.model, B, `${failure}: reliable reviewer wins`);
  }
});

test('speed: equal availability and accuracy choose the faster model, regardless of file lines', () => {
  const rows = [...history(A, { seconds: 100 }), ...history(B, { seconds: 14 })];
  assert.deepEqual([20, 900].map((n) => choose(rows, config(), 't', n).cfg.levels.L2.model), [B, B]);
});

test('accuracy: waived findings lose to an equally fast accurate reviewer', () => {
  assert.equal(choose([...history(A, { waived: 8 }), ...history(B)]).cfg.levels.L2.model, B);
});

test('cold start keeps configured order, shipped defaults have no size route, explicit routes remain candidates', () => {
  assert.equal(choose([]).cfg.levels.L2.model, A);
  for (const provider of ['anthropic', 'openai', 'xai']) {
    assert.deepEqual(routing.pickRoutes(defaultsForProvider(provider), 900), []);
  }
  const cfg = config({ adaptive: { explore: 0 }, routes: [{ min_lines: 400, provider: 'xai', model: B }] });
  assert.equal(choose([], cfg, 't', 500).cfg.levels.L2.model, B);
  const rows = [...history(A, { seconds: 5 }), ...history(B, { seconds: 100 })];
  assert.equal(choose(rows, cfg, 't', 500).cfg.levels.L2.model, A, 'matching unpinned rule can lose on measured performance');
  cfg.review.routes[0].pin = true;
  assert.equal(choose(rows, cfg, 't', 500).cfg.levels.L2.model, B, 'pin forces the explicit rule');
  assert.equal(choose(rows, cfg, 't', 399).cfg.levels.L2.model, A, 'pin only applies when its route matches');
  const consensus = config({ adaptive: { explore: 0 }, multimodel: true, second_provider: 'xai' });
  assert.equal(choose([...history(A, { seconds: 100 }), ...history(B)], consensus).cfg.levels.L2.model, A,
    'performance selection preserves the distinct-provider consensus requirement');
});

test('exploration is deterministic for a ticket and gives an under-measured model a turn', () => {
  const rows = [...history(A), ...history(B, { n: 1 })];
  const cfg = config({ adaptive: { explore: 1 } });
  const first = choose(rows, cfg, 'repeatable-ticket');
  assert.equal(first.cfg.levels.L2.model, B);
  assert.equal(first.rows[0].reason, 'explore');
  assert.deepEqual(choose(rows, cfg, 'repeatable-ticket'), first);
  assert.equal(choose(rows).cfg.levels.L2.model, A, 'below min_sessions a fast candidate cannot jump the configured order');
});

test('a retried ticket reuses its recorded choice even after performance history changes', () => {
  const cfg = config();
  const ticket = 'same-ticket';
  const first = choose([...history(A, { seconds: 100 }), ...history(B)], cfg, ticket);
  const recorded = { ...first.rows[0], ticket, ts: new Date(NOW).toISOString() };
  const retried = choose([...history(A), ...history(B, { seconds: 100 }), recorded], cfg, ticket);
  assert.equal(retried.cfg.levels.L2.model, B, 'the signed choice and the actual reviewer must agree on retry');
  assert.equal(choose([...history(A), ...history(B, { seconds: 100 }), recorded], cfg, 'different-ticket').cfg.levels.L2.model, A);
});

test('window statistics count attempts once, ignore other roles and old/future rows, and scope waivers to their finding', () => {
  const rows = [...history(A, { waived: 3 }), ...history(B, { ts: '2026-10-01T00:00:00Z' }),
    ...history(B, { ts: '2026-11-01T00:00:00Z' }),
    { event: 'session', role: 'coder', model: B, ts: new Date(NOW).toISOString(), status: 'ok', duration_ms: 1 },
    { event: 'review.waived', run: 'other-run', block: 'B1', file: `${A}-9.mjs`, finding: 'F1', ts: new Date(NOW).toISOString() }];
  assert.deepEqual(routing.modelStats?.(rows, { now: NOW }), {
    [A]: { sessions: 10, median_seconds: 10, p90_seconds: 10, availability: 1, findings_per_session: 1, waived_share: 0.3, criticals: 10 },
  });
  assert.equal(routing.modelStats?.(rows, { now: NOW, window_days: 31 })[B].sessions, 10);
  const ts = new Date(NOW).toISOString();
  const attempts = [1000, 3000, 2000, 10000].map((duration_ms) => ({ event: 'session', role: 'reviewer', model: A, status: 'ok', duration_ms, ts }));
  const sections = { event: 'review.result', file: 'guide.md', ts, sessions: [1, 2].map((section) => ({ role: 'reviewer', model: A, status: 'ok', section, findings: [{ id: 'F1', severity: 'critical' }] })) };
  assert.deepEqual(routing.modelStats?.([...attempts, sections,
    { event: 'review.schema_invalid', role: 'reviewer', model: A, ts },
    { event: 'review.schema_invalid', role: 'reviewer', model: A, ts },
    { event: 'review.waived', file: 'guide.md', finding: 'S1.F1', ts }], { now: NOW })[A], {
    sessions: 4, median_seconds: 2.5, p90_seconds: 10, availability: 0.5, findings_per_session: 0.5, waived_share: 0.5, criticals: 2,
  });
});

test('adaptive config validates bounds and pin; disabling exactly preserves legacy config, timeout and rows', () => {
  const cfg = { version: 1, project: { slug: 'performance' }, ...config({ adaptive: { enabled: true, window_days: 7, min_sessions: 5, explore: 0.2 }, routes: [{ min_lines: 0, model: B, pin: true }] }) };
  assert.equal(validateConfig(cfg).valid, true);
  for (const adaptive of [{ window_days: 0 }, { min_sessions: 0 }, { explore: -0.1 }, { explore: 1.1 }, { enabled: 'yes' }]) {
    assert.equal(validateConfig({ ...cfg, review: { adaptive } }).valid, false, JSON.stringify(adaptive));
  }
  for (const routes of [[], [{ min_lines: 400, provider: 'xai', model: B, session_timeout_s: 900 }]]) {
    const disabled = config({ adaptive: { enabled: false }, routes });
    const old = routing.routeTicket(disabled, { lines: 500, timeoutS: 300 });
    assert.deepEqual(choose([...history(A), ...history(B, { seconds: 1 })], disabled, 't', 500), old);
  }
});

test('worker signs its measured choice and reason before invoking a fake reviewer', async () => {
  const repo = await makeRepo();
  const runId = `r-performance-${process.pid}`;
  await startRun({ workspace: repo, project: 'performance', runId, writeRow: async () => {} });
  await openBlock({ runId, id: 'B1', level: 'L2', owned: ['src/**'], acceptance: [{ clause: 'c', tests: ['t'] }], writeRow: async () => {} });
  writeFile(repo, 'src/example.mjs', 'export const value = 1;\n');
  const key = await loadKey(runId);
  const rows = [...history(A, { seconds: 100, ts: new Date().toISOString() }), ...history(B, { ts: new Date().toISOString() })];
  const spawned = [];
  const worker = await createWorker({ runId, repoRoot: repo, cfg: config(), runRootDir: freshDir('worker'), slug: 'performance', key }, {
    store: await createKeyStore({ backends: [], dir: freshDir('keys') }), env: {}, expiryCheck: async () => {},
    readRows: async () => rows, writeRow: async (row) => { rows.push(row); },
    spawn: async (opts) => {
      const route = rows.filter((r) => r.event === 'review.route');
      assert.equal(route.length, 1, 'choice row precedes the session');
      spawned.push(opts.cfg.levels.L2.model);
      const text = readFileSync(opts.promptPath, 'utf8');
      const hunks = text.split('\n').filter((s) => s.startsWith('- @@')).map((s) => s.slice(2));
      return { status: 'ok', provider: 'xai', model: B, exit_code: 0, usage: { tokens_in: 900, tokens_out: 300 }, answer: { passed: true, summary: 'ok', reviewed_hunks: hunks, findings: [], resolved: [], needs_file: [] } };
    },
  });
  const queued = enqueue({ repoRoot: repo, run: runId, block: 'B1', file: 'src/example.mjs' });
  assert.equal(await worker.drain(), 1);
  assert.equal(readResult(repo, runId, queued.ticket).approved, true);
  assert.deepEqual(spawned, [B]);
  const routes = rows.filter((r) => r.event === 'review.route');
  assert.deepEqual(routes.map((r) => [r.model, r.reason, r.ticket]), [[B, 'performance', queued.ticket]]);
  assert.deepEqual(verifyRow(routes[0], key), { ok: true });
});

test('status human text and doctor show measured models; status JSON stays unchanged', async (t) => {
  const repo = await makeRepo();
  const slug = `performance-status-${process.pid}`;
  const runId = `r-performance-status-${process.pid}`;
  const cfg = { version: 1, project: { slug }, ...config() };
  writeFile(repo, '.code-forge.yml', stringify(cfg));
  await startRun({ workspace: repo, project: slug, runId, writeRow: async () => {} });
  for (const row of history(A, { waived: 3, ts: new Date().toISOString() })) await appendRow(row, { slug });
  let output = '';
  t.mock.method(process.stdout, 'write', (s) => { output += s; return true; });
  assert.equal(await status(['--run', runId]), 0);
  const expected = `reviewer ${A}: 10 sessions, median 10 s, ok 100%, waived 30%`;
  assert.deepEqual(output.split('\n').filter((s) => s.startsWith('reviewer ')), [expected]);
  output = '';
  assert.equal(await status(['--run', runId, '--json']), 0);
  assert.equal(output.includes('reviewer '), false);
  assert.deepEqual(Object.keys(JSON.parse(output)), ['runs']);
  const doctor = await runDoctor({ cwd: repo, quick: true }, { store: await createKeyStore({ backends: [], dir: freshDir('doctor-keys') }) });
  assert.deepEqual(renderText(doctor).split('\n').filter((s) => s.includes('reviewer ')).map((s) => s.trim().replace(/^INFO +/, '')), [expected]);
  const idle = await makeRepo();
  writeFile(idle, '.code-forge.yml', stringify(cfg));
  const before = process.cwd();
  process.chdir(idle);
  try {
    output = '';
    assert.equal(await status([]), 0);
    assert.deepEqual(output.split('\n').filter((s) => s.startsWith('reviewer ')), [expected], 'project history stays visible without an active run');
  } finally { process.chdir(before); }
});
