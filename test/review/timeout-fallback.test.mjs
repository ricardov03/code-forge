import { answerFor, freshDir, git, makeRepo, writeFile } from './helpers.mjs';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import path from 'node:path';
import { test } from 'node:test';

const { createWorker } = await import('../../src/worker/loop.mjs');
const { enqueue, readResult } = await import('../../src/worker/queue.mjs');
const { startRun } = await import('../../src/state/run.mjs');
const { openBlock } = await import('../../src/state/block.mjs');
const { loadKey, signRow, verifyRow } = await import('../../src/state/signer.mjs');
const { appendRow, readAllRows } = await import('../../src/ledger/write.mjs');
const { createKeyStore } = await import('../../src/keys/store.mjs');
const { timeoutFallbackFor } = await import('../../src/review/route.mjs');
const { spawnWithTimeoutRetry } = await import('../../src/review/session-retry.mjs');

const FILE = 'src/small.mjs';
const PRIMARY = 'claude-opus-5-5';
const SLOW = 'claude-fable-5-1';
const FAST = 'claude-sonnet-5';

// Every git/provider child inherits a clean environment, including when run outside the suite.
for (const name of Object.keys(process.env)) if (name.startsWith('CODE_FORGE_')) delete process.env[name];

/** @param {string} slug @param {boolean} alternatives @param {boolean} [fallbackTimeout]
 * @param {{worktree?: boolean, ledgerFailure?: 'history' | 'performance', ledgerOnly?: boolean, projectShape?: boolean,
 * historyEvent?: 'session' | 'review.result', historyRole?: string}} [options] */
async function review(slug, alternatives, fallbackTimeout = false, { worktree = false, ledgerFailure, ledgerOnly = false,
  projectShape = false, historyEvent, historyRole = 'reviewer' } = {}) {
  const repo = await makeRepo();
  writeFile(repo, FILE, 'export const small = 1;\n');
  const runId = `r-${slug}`;
  const cfg = {
    provider: 'anthropic', ...(worktree ? {} : { project: { slug } }),
    levels: projectShape ? {
      L0: { model: 'claude-haiku-4-5-20251001' }, L1: { model: FAST },
      L2: { model: PRIMARY }, L3: { model: SLOW },
    } : {
      L0: { model: PRIMARY }, L1: { model: PRIMARY }, L2: { model: PRIMARY },
      L3: ledgerOnly ? { model: PRIMARY } : { model: SLOW, fallback: [{ model: FAST }] },
    },
    ...(projectShape ? {} : { review: { min_tokens_out: 40, adaptive: { explore: 0 },
      // These models are configured as reviewers even when this small file does not match.
      ...(alternatives && !ledgerOnly ? { routes: [{ min_lines: 10000, model: SLOW, fallback: [{ model: FAST }] }] } : {}),
    } }),
  };
  await startRun({ workspace: repo, project: slug, runId, config: cfg, writeRow: async () => {} });
  const tree = worktree ? path.join(repo, '.claude/worktrees/cf/B1') : null;
  await openBlock({ runId, id: 'B1', level: 'L2', owned: [FILE], acceptance: [{ clause: 'review', tests: ['fake'] }], writeRow: async () => {},
    ...(tree ? { worktree: async ({ baseSha }) => ({ path: tree, make: async () => {
      await git(['worktree', 'add', '-q', '-b', 'cf-B1', tree, baseSha], repo);
      return { worktree: { path: tree, branch: 'cf-B1', base_sha: baseSha }, created: true, rollback: null };
    } }) } : {}),
  });
  if (tree) writeFile(tree, FILE, 'export const small = 1;\n');
  const key = await loadKey(runId);
  const writeRow = (row) => appendRow(row, { slug });
  if (alternatives) {
    for (const [model, duration_ms] of (projectShape ? [[FAST, 1000]] : [[SLOW, 100000], [FAST, 1000]])) {
      if (historyEvent !== 'review.result') {
        for (let i = 0; i < 10; i++) await writeRow(signRow({ event: 'session', run: runId, role: historyRole, provider: cfg.provider, model, status: 'ok', duration_ms, ts: new Date().toISOString() }, key));
      }
      const block = `history-${model}`;
      if (historyEvent !== 'session') {
        await writeRow(signRow({ event: 'review.result', run: runId, block, file: FILE, ts: new Date().toISOString(),
          sessions: [{ role: historyRole, provider: cfg.provider, model, findings: [{ id: 'F1', severity: 'warning' }] }],
        }, key));
      }
      if (model === SLOW) await writeRow(signRow({ event: 'review.waived', run: runId, block, file: FILE, finding: 'F1', ts: new Date().toISOString() }, key));
    }
  }
  const calls = [];
  const packets = [];
  let failedReads = 0;
  let recoveryReads = 0;
  const worker = await createWorker({ runId, repoRoot: repo, cfg, runRootDir: freshDir('worker'), slug, key }, {
    env: {}, store: await createKeyStore({ backends: [], dir: freshDir('keys') }), expiryCheck: async () => {},
    writeRow,
    ...(ledgerFailure ? { readRows: async () => {
      if (calls.length >= 2 && (++recoveryReads > 1 || ledgerFailure === 'history')) {
        failedReads++;
        throw new Error('fixture ledger read failed');
      }
      return readAllRows(slug);
    } } : {}),
    spawn: async (opts) => {
      const { model, provider = cfg.provider } = opts.cfg.levels[opts.level];
      calls.push(model);
      const packet = readFileSync(opts.promptPath, 'utf8');
      packets.push(packet);
      if (model === PRIMARY || fallbackTimeout) return { status: 'timeout', model, provider, duration_ms: 60000, usage: { tokens_out: 0 } };
      const hunks = packet.split('\n').filter((line) => line.startsWith('- @@')).map((line) => line.slice(2));
      return { status: 'ok', model, provider, exit_code: 0, answer: answerFor(hunks), usage: { tokens_in: 100, tokens_out: 100 } };
    },
  });
  const ticket = enqueue({ repoRoot: repo, run: runId, block: 'B1', file: FILE, tree });
  assert.equal(await worker.drain(), 1, 'exactly one file ticket processed');
  return { result: readResult(repo, runId, ticket.ticket), calls, packets, rows: await readAllRows(slug), key, failedReads };
}

test('second timeout selects one different configured reviewer by measured performance and signs its fallback row', async () => {
  for (const fallbackTimeout of [false, true]) {
    const { result, calls, packets, rows, key } = await review(`timeout-alternative-${Number(fallbackTimeout)}`, true, fallbackTimeout);
    assert.deepEqual(calls, [PRIMARY, PRIMARY, FAST], 'two primary timeouts then exactly one measured fallback attempt');
    assert.equal(new Set(packets).size, 1, 'all three sessions review the same file packet');
    assert.equal(result.sessions[0].attempts, 3);
    assert.deepEqual([result.status, result.approved], fallbackTimeout ? ['unavailable', false] : ['reviewed', true]);
    const fallback = rows.filter((row) => row.event === 'review.schema_fallback' && row.trigger === 'timeout');
    assert.equal(fallback.length, 1, 'exactly one signed timeout fallback row');
    const recorded = [fallback[0].file, fallback[0].level, fallback[0].from_model, fallback[0].to_model, fallback[0].reason, fallback[0].routing_reason];
    assert.deepEqual(recorded, [FILE, 'L2', PRIMARY, FAST, 'repeated-timeout', 'performance']);
    assert.deepEqual(verifyRow(fallback[0], key), { ok: true });
  }
});

test('worktree reviews without project.slug use the run project ledger for measured timeout recovery', async () => {
  const { result, calls, rows, key } = await review('timeout-worktree', true, false, { worktree: true });
  assert.deepEqual(calls, [PRIMARY, PRIMARY, FAST], 'measured model wins over the first configured alternative');
  assert.deepEqual([result.status, result.approved, result.sessions[0].attempts], ['reviewed', true, 3]);
  const fallback = rows.filter((row) => row.event === 'review.schema_fallback' && row.trigger === 'timeout');
  assert.equal(fallback.length, 1);
  assert.deepEqual([fallback[0].to_model, fallback[0].routing_reason], [FAST, 'performance']);
  assert.deepEqual(verifyRow(fallback[0], key), { ok: true });
});

for (const historyEvent of /** @type {const} */ (['session', 'review.result'])) {
  const historySlug = historyEvent.replace('.', '-');
  test(`project ledger ${historyEvent} reviewer models absent from config cannot recover a timed out reviewer`, async () => {
    const { result, calls, rows } = await review(`timeout-ledger-only-${historySlug}`, true, false, { ledgerOnly: true, historyEvent });
    assert.deepEqual(calls, [PRIMARY, PRIMARY]);
    assert.deepEqual([result.status, result.reason, result.approved, result.next.action, result.sessions[0].attempts],
      ['unavailable', 'timeout', false, 'retry', 2]);
    assert.equal(rows.filter((row) => row.event === 'review.schema_fallback').length, 0);
  });

  test(`a configured L1 model with project ${historyEvent} reviewer history recovers without review keys`, async () => {
    const { result, calls, rows, key } = await review(`timeout-project-reviewer-history-${historySlug}`, true, false, { projectShape: true, historyEvent });
    assert.deepEqual(calls, [PRIMARY, PRIMARY, FAST]);
    assert.deepEqual([result.status, result.approved, result.sessions[0].attempts], ['reviewed', true, 3]);
    const fallback = rows.filter((row) => row.event === 'review.schema_fallback' && row.trigger === 'timeout');
    assert.equal(fallback.length, 1);
    assert.deepEqual([fallback[0].to_model, fallback[0].routing_reason],
      [FAST, historyEvent === 'session' ? 'performance' : 'configured-order']);
    assert.deepEqual(verifyRow(fallback[0], key), { ok: true });
  });
}

for (const hasCoderHistory of [false, true]) {
  test(`distinct project levels with ${hasCoderHistory ? 'coder history only' : 'no history'} preserve the original timeout result`, async () => {
    const { result, calls, rows } = await review(`timeout-project-no-reviewer-history-${hasCoderHistory}`, hasCoderHistory, false,
      { projectShape: true, historyRole: 'coder' });
    assert.deepEqual(calls, [PRIMARY, PRIMARY]);
    assert.deepEqual([result.status, result.reason, result.approved, result.next.action, result.sessions[0].attempts],
      ['unavailable', 'timeout', false, 'retry', 2]);
    assert.equal(rows.filter((row) => row.event === 'review.schema_fallback').length, 0);
  });
}

for (const ledgerFailure of /** @type {const} */ (['history', 'performance'])) {
  test(`a failed ${ledgerFailure} ledger read still recovers once in configured order and records why`, async () => {
    for (const fallbackTimeout of [false, true]) {
      const { result, calls, rows, key, failedReads } = await review(`timeout-ledger-failure-${ledgerFailure}-${Number(fallbackTimeout)}`, true, fallbackTimeout, { ledgerFailure });
      assert.ok(failedReads > 0, 'the recovery ledger read failed');
      assert.deepEqual(calls, [PRIMARY, PRIMARY, SLOW], 'exactly one fallback, using configured order without measurements');
      assert.equal(result.sessions[0].attempts, 3);
      assert.deepEqual([result.status, result.approved], fallbackTimeout ? ['unavailable', false] : ['reviewed', true]);
      const fallback = rows.filter((row) => row.event === 'review.schema_fallback' && row.trigger === 'timeout');
      assert.equal(fallback.length, 1);
      assert.deepEqual([fallback[0].to_model, fallback[0].routing_reason, fallback[0].routing_error], [SLOW, 'configured-order', 'ledger-read-failed']);
      assert.deepEqual(verifyRow(fallback[0], key), { ok: true });
    }
  });
}

test('without another configured reviewer two timeouts remain unavailable with retry', async () => {
  const { result, calls, rows } = await review('timeout-no-alternative', false);
  assert.deepEqual(calls, [PRIMARY, PRIMARY], 'exactly two original-model attempts');
  assert.deepEqual([result.status, result.reason, result.approved, result.next.action], ['unavailable', 'timeout', false, 'retry']);
  assert.equal(rows.filter((row) => row.event === 'review.schema_fallback').length, 0, 'no fallback row without another model');
});

test('a ladder without another runnable model preserves the original attempts and timeout notes', async () => {
  for (const fallback of [[], [{ model: PRIMARY }], [{ provider: 'openai', model: 'gpt-6-astra' }]]) {
    const cfg = { provider: 'anthropic', levels: { L2: { model: PRIMARY, fallback } } };
    const opts = { cfg, level: 'L2', role: 'reviewer', rowExtra: { lens: 'quick' } };
    const timeout = { status: 'timeout', provider: cfg.provider, model: PRIMARY,
      duration_ms: 300000, stderr_tail: 'stalled', stdout_bytes: 0, stdout_events: [] };
    const calls = [];
    const notes = [];
    const result = await spawnWithTimeoutRetry(async (attempt) => {
      calls.push(attempt);
      return timeout;
    }, opts, async (row) => void notes.push(row));
    assert.deepEqual(result, { res: timeout, attempts: 2 });
    assert.equal(calls.length, 2);
    for (const call of calls) assert.equal(call, opts, 'both attempts receive the original options');
    assert.deepEqual(notes, [1, 2].map((attempt) => ({
      event: 'review.session_timeout', lens: 'quick', role: 'reviewer', level: 'L2',
      provider: cfg.provider, model: PRIMARY, attempt, retried: attempt === 1,
      duration_ms: 300000, stderr_tail: 'stalled', stdout_bytes: 0, stdout_events: [],
    })));
    assert.equal(timeoutFallbackFor(cfg, 'L2', timeout, { rows: [], ticketId: 'no-runnable-model', role: 'reviewer' }), null);
  }
});

test('judge levels, routes, second levels and ledger rows never configure reviewer fallback models', () => {
  const cfg = {
    provider: 'anthropic',
    levels: { L2: { model: PRIMARY }, L3: { model: SLOW, fallback: [{ model: FAST }] } },
    review: {
      second_levels: { L3: { model: FAST } },
      routes: [{ level: 'L3', min_lines: 0, model: SLOW, fallback: [{ model: FAST }], second: { model: FAST } }],
    },
  };
  const rows = [
    { event: 'session', role: 'judge', provider: 'anthropic', model: SLOW, status: 'ok' },
    { event: 'review.result', sessions: [{ role: 'judge', provider: 'anthropic', model: FAST }] },
  ];
  assert.equal(timeoutFallbackFor(cfg, 'L2', { model: PRIMARY, provider: 'anthropic' }, {
    rows, ticketId: 'role-boundary', role: 'reviewer',
  }), null);
});

test('ledger confirmation requires the configured provider and reviewer role for every config location', () => {
  const alternative = { model: FAST, effort: 'high' };
  const configs = [
    { levels: { L1: alternative } },
    { levels: { L3: { model: SLOW, fallback: [alternative] } } },
    { review: { second_levels: { L3: alternative } } },
    { review: { routes: [{ level: 'L3', min_lines: 0, ...alternative }] } },
    { review: { routes: [{ level: 'L3', min_lines: 0, model: SLOW, fallback: [alternative] }] } },
    { review: { routes: [{ level: 'L3', min_lines: 0, model: SLOW, second: alternative }] } },
    { review: { routes: [{ level: 'L3', min_lines: 0, model: SLOW, second: { model: SLOW, fallback: [alternative] } }] } },
  ];
  for (const extra of configs) {
    const cfg = { ...extra, provider: 'anthropic', levels: { L2: { model: PRIMARY }, ...extra.levels } };
    const pick = (rows) => timeoutFallbackFor(cfg, 'L2', { model: PRIMARY, provider: 'anthropic' }, {
      rows, ticketId: 'config-confirmation', role: 'reviewer',
    });
    assert.equal(pick([]), null, 'another role or level alone does not configure a reviewer');
    for (const event of ['session', 'review.result']) {
      for (const [provider, role] of [['anthropic', 'reviewer'], ['xai', 'reviewer'], ['anthropic', 'judge']]) {
        const session = { provider, role, model: FAST };
        const choice = pick([event === 'session' ? { event, ...session } : { event, sessions: [session] }]);
        if (provider !== 'anthropic' || role !== 'reviewer') {
          assert.equal(choice, null, 'neither a different provider nor a different role confirms the model');
        } else {
          assert.deepEqual([choice.model, choice.provider, choice.cfg.levels.L2.effort], [FAST, 'anthropic', 'high']);
        }
      }
    }
  }
});

test('explicit reviewer alternatives remain eligible without ledger history', () => {
  for (const extra of [
    { levels: { L2: { model: PRIMARY, fallback: [{ model: FAST }] } } },
    { review: { second_levels: { L2: { model: FAST } } } },
    { review: { routes: [{ min_lines: 10000, model: FAST }] } },
    { review: { routes: [{ min_lines: 10000, model: PRIMARY, fallback: [{ model: FAST }] }] } },
    { review: { routes: [{ min_lines: 10000, model: PRIMARY, second: { model: FAST } }] } },
  ]) {
    const cfg = { provider: 'anthropic', levels: { L2: { model: PRIMARY } }, ...extra };
    const choice = timeoutFallbackFor(cfg, 'L2', { model: PRIMARY, provider: 'anthropic' }, {
      rows: [], ticketId: 'explicit-reviewer', role: 'reviewer',
    });
    assert.deepEqual([choice.model, choice.provider, choice.reason], [FAST, 'anthropic', 'configured-order']);
  }
});

test('removing a configured reviewer or changing its provider invalidates its ledger history', () => {
  const cfg = { provider: 'anthropic', levels: {
    L1: { model: FAST }, L2: { model: PRIMARY },
  } };
  for (const event of ['session', 'review.result']) {
    const session = { role: 'reviewer', provider: 'anthropic', model: FAST };
    const rows = [event === 'session' ? { event, ...session } : { event, sessions: [session] }];
    const performance = { rows, ticketId: 'removed-reviewer', role: 'reviewer' };
    const from = { model: PRIMARY, provider: 'anthropic' };
    assert.equal(timeoutFallbackFor(cfg, 'L2', from, performance)?.model, FAST);

    for (const levels of [
      { L2: cfg.levels.L2 },
      { L2: cfg.levels.L2, L1: { model: SLOW } },
      { L2: cfg.levels.L2, L1: { model: FAST, provider: 'xai' } },
    ]) {
      assert.equal(timeoutFallbackFor({ ...cfg, levels }, 'L2', from, performance), null,
        'stale history cannot restore a removed provider/model pair');
    }
    assert.equal(timeoutFallbackFor(cfg, 'L2', from, performance)?.model, FAST,
      'candidate selection does not change the config or ledger history');
  }
});

test('a configured timeout ladder never retries a stalled model, even with duplicates or reported aliases', async () => {
  for (const alias of [false, true]) {
    const cfg = { provider: 'anthropic', levels: { L2: { model: PRIMARY,
      fallback: [PRIMARY, FAST, PRIMARY, FAST].map((model) => ({ model })),
    } } };
    const original = structuredClone(cfg);
    const calls = [];
    const notes = [];
    const result = await spawnWithTimeoutRetry(async (opts) => {
      const { model } = opts.cfg.levels.L2;
      calls.push(model);
      return { status: 'timeout', provider: 'anthropic', model: alias ? `alias-${model}` : model };
    }, { cfg, level: 'L2', role: 'reviewer' }, async (row) => void notes.push(row));
    assert.deepEqual(calls, [PRIMARY, FAST]);
    assert.equal(result.attempts, 2);
    assert.equal(result.res.status, 'timeout');
    assert.deepEqual(notes.map((row) => [row.model, row.attempt, row.retried]), [
      [alias ? `alias-${PRIMARY}` : PRIMARY, 1, false],
      [alias ? `alias-${FAST}` : FAST, 2, false],
    ]);
    assert.deepEqual(cfg, original, 'recovery never changes the project config');
  }
});

test('default models at other levels cannot recover an unidentified or aliased reviewer', () => {
  const cfg = { provider: 'anthropic', levels: {
    L0: { model: 'claude-haiku-4-5-20251001' }, L1: { model: FAST },
    L2: { model: PRIMARY }, L3: { model: SLOW },
  } };
  for (const model of [PRIMARY, 'fake-model', undefined]) {
    assert.equal(timeoutFallbackFor(cfg, 'L2', { provider: 'anthropic', model }, {
      rows: [], ticketId: 'no-configured-alternative', role: 'reviewer',
    }), null);
  }
});
