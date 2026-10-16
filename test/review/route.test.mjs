// helpers FIRST: it pins $HOME under the per-file temp parent before any src module loads.
import { commitAll, freshDir, git, lines, makeRepo, writeFile } from './helpers.mjs';
import assert from 'node:assert/strict';
import { appendFileSync, readFileSync, rmSync } from 'node:fs';
import path from 'node:path';
import { describe, test } from 'node:test';

// B90: `review.routes` — a big file is reviewed by a named model with its own session timeout and
// second level. The wiring tests drive an in-process worker on a stub session spawner (never a
// real CLI) in a temp HOME and a temp repo; the stub records every spawn's model and timeoutMs.

const { countLines, pickRoutes, routeTicket } = await import('../../src/review/route.mjs');
const { enqueue, queueDir, readResult } = await import('../../src/worker/queue.mjs');
const { createWorker } = await import('../../src/worker/loop.mjs');
const { startRun } = await import('../../src/state/run.mjs');
const { openBlock } = await import('../../src/state/block.mjs');
const { loadKey, verifyRow } = await import('../../src/state/signer.mjs');
const { createKeyStore } = await import('../../src/keys/store.mjs');
const { readStartTime } = await import('../../src/util/reaper.mjs');

const FILE = 'src/big.mjs';
const PRIMARY = 'claude-opus-5-5';
const ROUTED = 'claude-fable-5-1-big';
const ROUTED_800 = 'claude-fable-5-1-huge';
const ROUTE_SECOND = 'claude-sonnet-5-second';
const SECOND = 'grok-4.7';

/** @param {string} text @returns {string[]} the packet's hunk list. */
function hunksOf(text) {
  const at = text.indexOf('\nhunks:\n');
  if (at < 0) return [];
  const listed = text.slice(at + 8).split('\n');
  const end = listed.findIndex((l) => !l.startsWith('- @@'));
  return listed.slice(0, end < 0 ? listed.length : end).map((l) => l.slice(2));
}

/**
 * A config on the stubs: every level on Anthropic, `review` merged in.
 * @param {Record<string, any>} [review] @returns {Record<string, any>}
 */
const cfgWith = (review = {}) => ({
  provider: 'anthropic',
  levels: { L0: { model: 'claude-haiku-4-5-20251001' }, L1: { model: 'claude-sonnet-5' }, L2: { model: PRIMARY }, L3: { model: 'claude-fable-5-1' } },
  review: { min_tokens_out: 40, ...review },
});

const ROUTE_400 = { min_lines: 400, model: ROUTED, session_timeout_s: 900 };

let seq = 0;

/**
 * A run with block B1 owning `src/**` and an in-process worker on a stub spawner. `content` is the
 * file's new content (untracked, after the block opened); `deleted: true` commits `deletedLines`
 * lines before the block opens and deletes it in a commit after. A model in `script.invalid`
 * answers text that fails the schema; any other gives a valid answer (round 1: `firstFindings`, a
 * recheck resolves every listed id). `log` is the order of spawns and ledger rows.
 * `failRouteRow` fails every `review.route` write; `failRouteRowAt` fails only those write attempts
 * (1-based, counted over the whole run).
 * @param {{cfg: Record<string, any>, content?: string, deleted?: boolean, firstFindings?: Array<Record<string, any>>, failRouteRow?: boolean, failRouteRowAt?: number[]}} o
 */
async function wired(o) {
  seq += 1;
  const runId = `r-b90-${process.pid}-${seq}`;
  const repo = await makeRepo();
  if (o.deleted) {
    writeFile(repo, FILE, lines(60));
    await commitAll(repo);
  }
  const noRow = async () => {};
  await startRun({ workspace: repo, project: 'route-test', runId, writeRow: noRow });
  await openBlock({ runId, id: 'B1', level: 'L2', owned: ['src/**'], acceptance: [{ clause: 'c', tests: ['t'] }], writeRow: noRow });
  if (o.deleted) {
    await git(['rm', '-q', '--', FILE], repo);
    await git(['commit', '-q', '-m', 'delete'], repo);
  } else {
    writeFile(repo, FILE, /** @type {string} */ (o.content));
  }
  const key = await loadKey(runId);
  const script = { invalid: new Set() };
  /** @type {Array<Record<string, any>>} every spawn's options */
  const spawned = [];
  /** @type {Array<string>} 'spawn:<model>' / 'row:<event>' in order */
  const log = [];
  /** @type {Array<Record<string, any>>} */
  const rows = [];
  let routeWrites = 0;
  const spawn = async (/** @type {Record<string, any>} */ opts) => {
    const text = readFileSync(opts.promptPath, 'utf8');
    const lens = opts.rowExtra?.lens;
    const level = opts.cfg?.levels?.[opts.level] ?? {};
    const model = level.model;
    const provider = level.provider ?? opts.cfg?.provider;
    spawned.push({ ...opts, model, provider, lens });
    log.push(`spawn:${model}`);
    const head = { provider, model, fallback_step: 0, usage: { tokens_in: 900, tokens_out: 300 } };
    if (script.invalid.has(model)) return { ...head, status: 'invalid-output', reason: 'answer does not match the schema', answer: null, text: '{"passed":"maybe"}', exit_code: 0 };
    const at = text.indexOf('\n## open findings\n');
    const open = at >= 0 ? [...text.slice(at).matchAll(/^- (\S+) \(/gm)].map((m) => m[1]) : [];
    const findings = lens === 'recheck' ? [] : o.firstFindings ?? [];
    const answer = { passed: findings.length === 0, summary: 's', reviewed_hunks: hunksOf(text), findings, resolved: open.map((id) => ({ id, resolved: true, why: 'fixed' })), needs_file: [] };
    return { ...head, status: 'ok', exit_code: 0, answer };
  };
  const jev = async (/** @type {{questions: Record<string, any>}} */ req) => {
    const [id] = Object.keys(req.questions);
    return { ok: true, answers: { [id]: { type: 'noul', noul: id === 'defect' ? 0.95 : 0.2 } } };
  };
  // a new worker for the same run (a restart): same run id, same ledger
  const newWorker = async () => createWorker(
    { runId, repoRoot: repo, cfg: o.cfg, runRootDir: freshDir('runroot'), slug: 'route-test', key },
    {
      store: await createKeyStore({ backends: [], dir: freshDir('store') }),
      env: {},
      writeRow: async (row) => {
        if (row.event === 'review.route') {
          routeWrites += 1;
          if (o.failRouteRow || o.failRouteRowAt?.includes(routeWrites)) throw new Error('ledger full');
        }
        log.push(`row:${row.event}`);
        rows.push(row);
      },
      readRows: async () => rows,
      spawn: /** @type {any} */ (spawn),
      jev,
    },
  );
  let worker = await newWorker();
  const restart = async () => {
    worker = await newWorker();
  };
  /**
   * `review-file` + the worker's drain. `edit` appends a line first (a coder fix); without it the
   * same content runs again (its done marker dropped, as `review-file` does).
   * @param {{edit?: boolean}} [opts]
   */
  const round = async ({ edit = false } = {}) => {
    if (edit) appendFileSync(path.join(repo, FILE), `export const fix = ${spawned.length};\n`);
    let t = enqueue({ repoRoot: repo, run: runId, block: 'B1', file: FILE });
    if (t.status === 'done') {
      rmSync(path.join(queueDir(repo), `${t.ticket}.done`), { force: true });
      t = enqueue({ repoRoot: repo, run: runId, block: 'B1', file: FILE });
    }
    assert.equal(await worker.drain(), 1);
    return { result: /** @type {Record<string, any>} */ (readResult(repo, runId, t.ticket)), contentHash: t.content_hash, ticket: t.ticket };
  };
  const events = (/** @type {string} */ name) => rows.filter((r) => r.event === name);
  return { repo, runId, key, rows, spawned, log, script, round, events, restart };
}

/** @param {Record<string, any>} row @returns {Record<string, any>} the row without its signature and time. */
const bodyOf = (row) => {
  const { mac: _mac, ts: _ts, ...body } = row;
  return body;
};

describe('B90 countLines / pickRoutes / routeTicket (pure)', () => {
  test('countLines: newline bytes, plus one for a last line with no newline; empty and deleted are 0', () => {
    assert.deepEqual(
      [null, '', 'a', 'a\n', 'a\nb', 'a\nb\n', '\n\n', lines(450)].map((t) => countLines(t === null ? null : Buffer.from(t))),
      [0, 0, 1, 1, 2, 2, 2, 450],
    );
  });

  test('pickRoutes: the largest min_lines at most the count wins, per level; a deleted file is never routed', () => {
    const cfg = cfgWith({ routes: [{ min_lines: 800, model: ROUTED_800 }, ROUTE_400, { min_lines: 400, level: 'L3', model: SECOND, provider: 'xai' }] });
    const at = (/** @type {number} */ n, deleted = false) => pickRoutes(cfg, n, { deleted }).map(({ level, rule }) => [level, rule.model]);
    assert.deepEqual(at(399), []);
    assert.deepEqual(at(400), [['L2', ROUTED], ['L3', SECOND]]);
    assert.deepEqual(at(799), [['L2', ROUTED], ['L3', SECOND]]);
    assert.deepEqual(at(800), [['L2', ROUTED_800], ['L3', SECOND]]);
    assert.deepEqual(at(850, true), []);
  });

  test('routeTicket: no route ⇒ the same config object and timeout, no row; a malformed list throws route-invalid', () => {
    const cfg = cfgWith({ routes: [ROUTE_400] });
    const none = routeTicket(cfg, { lines: 120, timeoutS: 300 });
    assert.equal(none.cfg, cfg);
    assert.deepEqual([none.timeoutS, none.rows], [300, []]);
    const plain = cfgWith();
    assert.equal(routeTicket(plain, { lines: 5000, timeoutS: 300 }).cfg, plain);
    for (const routes of ['x', [{ model: ROUTED }], [{ min_lines: -1, model: ROUTED }], [{ min_lines: 400, model: ROUTED, level: 'L1' }], [{ min_lines: 400, model: ROUTED, second: {} }], [null], [ROUTE_400, { min_lines: 400, model: ROUTED_800 }], [ROUTE_400, { min_lines: 400, level: 'L2', model: ROUTED_800 }]]) {
      assert.throws(() => routeTicket(cfgWith({ routes }), { lines: 450, timeoutS: 300 }), { code: 'route-invalid' }, JSON.stringify(routes));
    }
    // the same min_lines at two different levels is not a duplicate
    assert.equal(routeTicket(cfgWith({ routes: [ROUTE_400, { min_lines: 400, level: 'L3', model: ROUTED_800 }] }), { lines: 450, timeoutS: 300 }).rows.length, 2);
  });

  test('a line count that is not a non-negative integer ⇒ route-invalid, also for a deleted file; 0 is a count', () => {
    const cfg = cfgWith({ routes: [ROUTE_400] });
    for (const bad of [-1, 1.5, Number.NaN, Number.POSITIVE_INFINITY, '450', null, undefined]) {
      assert.throws(() => routeTicket(cfg, { lines: /** @type {any} */ (bad), timeoutS: 300 }), { code: 'route-invalid' }, String(bad));
      assert.throws(() => pickRoutes(cfg, /** @type {any} */ (bad), { deleted: true }), { code: 'route-invalid' }, `deleted ${String(bad)}`);
    }
    assert.deepEqual(routeTicket(cfg, { lines: 0, timeoutS: 300 }).rows, []);
  });

  test('a routed level that cannot be resolved (no provider on the rule nor at the top level) ⇒ route-invalid, never a row with no provider', () => {
    const cfg = cfgWith({ routes: [ROUTE_400] });
    delete cfg.provider;
    assert.throws(() => routeTicket(cfg, { lines: 450, timeoutS: 300 }), { code: 'route-invalid' });
    // the same rule with its own provider resolves
    const own = cfgWith({ routes: [{ ...ROUTE_400, provider: 'anthropic' }] });
    delete own.provider;
    assert.deepEqual(routeTicket(own, { lines: 450, timeoutS: 300 }).rows.map((r) => r.provider), ['anthropic']);
  });

  test('one hashing rule: hashBytes is what contentHash gives for the same file; null is the deleted hash and 0 lines', async () => {
    const { contentHash, hashBytes, DELETED_HASH } = await import('../../src/worker/ticket.mjs');
    const repo = await makeRepo();
    writeFile(repo, FILE, lines(3));
    const bytes = readFileSync(path.join(repo, FILE));
    assert.equal(hashBytes(bytes), contentHash(repo, FILE));
    assert.equal(hashBytes(bytes).length, 64);
    assert.deepEqual([hashBytes(null), contentHash(repo, 'src/none.mjs'), countLines(null)], [DELETED_HASH, DELETED_HASH, 0]);
  });

  test('routeTicket: the derived config and rows; an L2 second under multimodel keeps second_levels.L2; L2+L3 take the largest timeout', () => {
    const second = { provider: 'anthropic', model: ROUTE_SECOND };
    const routes = [{ ...ROUTE_400, provider: 'xai', effort: 'high', second, extra_key_never: undefined }, { min_lines: 300, level: 'L3', model: SECOND, provider: 'xai', session_timeout_s: 1200 }];
    const cfg = cfgWith({ routes, second_levels: { L2: { provider: 'xai', model: SECOND } } });
    const r = routeTicket(cfg, { lines: 450, timeoutS: 300 });
    assert.deepEqual(r.cfg.levels.L2, { model: ROUTED, provider: 'xai', effort: 'high' });
    assert.deepEqual(r.cfg.levels.L3, { model: SECOND, provider: 'xai' });
    assert.deepEqual(r.cfg.review.second_levels, { L2: second });
    assert.equal(r.timeoutS, 1200);
    assert.deepEqual(r.rows, [
      { event: 'review.route', level: 'L2', min_lines: 400, lines: 450, provider: 'xai', model: ROUTED, session_timeout_s: 1200, second_model: ROUTE_SECOND },
      { event: 'review.route', level: 'L3', min_lines: 300, lines: 450, provider: 'xai', model: SECOND, session_timeout_s: 1200, second_model: null },
    ]);
    // the input config is never mutated
    assert.deepEqual(cfg.levels.L2, { model: PRIMARY });
    assert.deepEqual(cfg.review.second_levels, { L2: { provider: 'xai', model: SECOND } });
    const multi = routeTicket({ ...cfg, review: { ...cfg.review, multimodel: true } }, { lines: 450, timeoutS: 300 });
    assert.deepEqual(multi.cfg.review.second_levels, { L2: { provider: 'xai', model: SECOND } });
    assert.deepEqual(multi.rows.map((row) => row.second_model), [null, null]);
    // no rule timeout ⇒ the base timeout; no rule provider ⇒ the top-level one
    const bare = routeTicket(cfgWith({ routes: [{ min_lines: 1, model: ROUTED }] }), { lines: 1, timeoutS: 60 });
    assert.deepEqual([bare.timeoutS, bare.rows[0].provider, bare.rows[0].session_timeout_s], [60, 'anthropic', 60]);
  });

  test('the timeout: one value per ticket, the largest session_timeout_s among the matched rules that set one; a matched rule without one is left out of the max (it does not count as the base timeout)', () => {
    const lowL2 = { min_lines: 1, model: ROUTED, session_timeout_s: 100 };
    const noneL3 = { min_lines: 1, level: 'L3', model: SECOND, provider: 'xai' };
    const both = routeTicket(cfgWith({ routes: [lowL2, noneL3] }), { lines: 5, timeoutS: 300 });
    assert.deepEqual([both.timeoutS, both.rows.map((r) => r.session_timeout_s)], [100, [100, 100]]);
    const neither = routeTicket(cfgWith({ routes: [{ min_lines: 1, model: ROUTED }, noneL3] }), { lines: 5, timeoutS: 300 });
    assert.deepEqual([neither.timeoutS, neither.rows.map((r) => r.session_timeout_s)], [300, [300, 300]]);
    // an unmatched rule's timeout never counts
    const unmatched = routeTicket(cfgWith({ routes: [lowL2, { min_lines: 9, level: 'L3', model: SECOND, provider: 'xai', session_timeout_s: 2000 }] }), { lines: 5, timeoutS: 300 });
    assert.equal(unmatched.timeoutS, 100);
  });
});

describe('B90 (1): a routed ticket spawns the route model at the route timeout, after one signed review.route row', () => {
  test('450 lines: round 1 and the recheck spawn the route model at 900000 ms; one signed row per ticket, before its first session', async () => {
    const finding = { id: 'F1', file: FILE, line_start: 1, line_end: 1, severity: 'warning', category: 'correctness', claim: 'claim F1', evidence: 'e', fix: 'f' };
    const w = await wired({ cfg: cfgWith({ routes: [ROUTE_400] }), content: lines(450), firstFindings: [finding] });
    const r1 = await w.round();
    assert.deepEqual([r1.result.status, r1.result.approved], ['reviewed', false]);
    const mark = w.log.length; // the recheck ticket's log starts here
    const r2 = await w.round({ edit: true });
    assert.deepEqual([r2.result.status, r2.result.approved, r2.result.kind], ['reviewed', true, 'recheck']);
    // exactly two sessions, in order: round 1 (full) then the recheck, both on the route at 900 s
    assert.deepEqual(w.spawned.map((s) => `${s.lens}:${s.model}@${s.timeoutMs}`), [`full:${ROUTED}@900000`, `recheck:${ROUTED}@900000`]);
    const routeRows = w.events('review.route');
    assert.deepEqual(routeRows.map(bodyOf), [
      { run: w.runId, event: 'review.route', level: 'L2', min_lines: 400, lines: 450, provider: 'anthropic', model: ROUTED, session_timeout_s: 900, second_model: null, ticket: r1.ticket, block: 'B1', file: FILE, content_hash: r1.contentHash },
      { run: w.runId, event: 'review.route', level: 'L2', min_lines: 400, lines: 451, provider: 'anthropic', model: ROUTED, session_timeout_s: 900, second_model: null, ticket: r2.ticket, block: 'B1', file: FILE, content_hash: r2.contentHash },
    ]);
    assert.equal(routeRows.every((row) => verifyRow(row, w.key).ok), true);
    // each ticket, searched from its own start: exactly one review.route row, before its first session
    for (const [from, to] of [[0, mark], [mark, w.log.length]]) {
      const part = w.log.slice(from, to);
      const row = part.indexOf('row:review.route');
      const spawnAt = part.findIndex((e) => e.startsWith('spawn:'));
      assert.deepEqual([part.filter((e) => e === 'row:review.route').length, row >= 0, spawnAt > row], [1, true, true], part.join(','));
    }
  });

  test('120 lines: the L2 model at 300000 ms and no review.route row', async () => {
    const w = await wired({ cfg: cfgWith({ routes: [ROUTE_400] }), content: lines(120) });
    const r = await w.round();
    assert.deepEqual([r.result.status, r.result.approved], ['reviewed', true]);
    assert.deepEqual(w.spawned.map((s) => [s.model, s.timeoutMs]), [[PRIMARY, 300000]]);
    assert.equal(w.events('review.route').length, 0);
  });

  test('399 lines is under the rule; 400 is routed', async () => {
    const under = await wired({ cfg: cfgWith({ routes: [ROUTE_400] }), content: lines(399) });
    await under.round();
    assert.deepEqual(under.spawned.map((s) => s.model), [PRIMARY]);
    const at = await wired({ cfg: cfgWith({ routes: [ROUTE_400] }), content: lines(400) });
    await at.round();
    assert.deepEqual(at.spawned.map((s) => s.model), [ROUTED]);
    assert.deepEqual(at.events('review.route').map((row) => row.lines), [400]);
  });
});

describe('B90: the ticket timeout through the worker', () => {
  test('an L2 rule at 100 s and a matched L3 rule with no timeout, base 300 s: the session runs at 100000 ms', async () => {
    const cfg = cfgWith({ session_timeout_s: 300, routes: [{ min_lines: 400, model: ROUTED, session_timeout_s: 100 }, { min_lines: 400, level: 'L3', model: 'claude-fable-5-1-judge' }] });
    const w = await wired({ cfg, content: lines(450) });
    await w.round();
    assert.deepEqual(w.spawned.map((s) => [s.model, s.timeoutMs]), [[ROUTED, 100000]]);
    assert.deepEqual(w.events('review.route').map((r) => [r.level, r.session_timeout_s]), [['L2', 100], ['L3', 100]]);
  });
});

describe('B90 (2): two rules', () => {
  test('rules at 400 and 800: an 850-line file takes the 800 rule', async () => {
    const w = await wired({ cfg: cfgWith({ routes: [ROUTE_400, { min_lines: 800, model: ROUTED_800, session_timeout_s: 1500 }] }), content: lines(850) });
    await w.round();
    assert.deepEqual(w.spawned.map((s) => [s.model, s.timeoutMs]), [[ROUTED_800, 1500000]]);
    assert.deepEqual(w.events('review.route').map((row) => [row.min_lines, row.lines, row.model, row.session_timeout_s]), [[800, 850, ROUTED_800, 1500]]);
  });
});

describe('B90 (3): a route\'s second level', () => {
  test('after two schema misses on a routed packet the second-level try spawns the route\'s second, not review.second_levels.L2', async () => {
    const route = { ...ROUTE_400, second: { provider: 'anthropic', model: ROUTE_SECOND } };
    const w = await wired({ cfg: cfgWith({ routes: [route], second_levels: { L2: { provider: 'xai', model: SECOND } } }), content: lines(450) });
    w.script.invalid.add(ROUTED);
    const first = await w.round();
    assert.deepEqual([first.result.status, first.result.reason], ['unavailable', 'schema']);
    const second = await w.round();
    assert.deepEqual([second.result.status, second.result.approved], ['reviewed', true]);
    assert.deepEqual(w.spawned.map((s) => [s.model, s.timeoutMs]), [
      [ROUTED, 900000],
      [ROUTED, 900000],
      [ROUTE_SECOND, 900000],
    ]);
    assert.deepEqual(w.events('review.schema_fallback').map((r) => [r.from_model, r.to, r.to_provider, r.to_model]), [[ROUTED, 'review.second_levels.L2', 'anthropic', ROUTE_SECOND]]);
    // the second round re-runs the same ticket (same content): its route row is not written again
    assert.equal(second.ticket, first.ticket);
    assert.deepEqual(w.events('review.route').map((r) => [r.ticket, r.second_model]), [[first.ticket, ROUTE_SECOND]]);
  });

  test('multimodel on: the route replaces slot A\'s L2; slot B keeps review.second_levels.L2 (the route\'s second is not used)', async () => {
    const route = { ...ROUTE_400, second: { provider: 'anthropic', model: ROUTE_SECOND } };
    // a high-tier path is risk 3: above single_reviewer_max_risk, so the consensus runs
    const cfg = { ...cfgWith({ multimodel: true, second_provider: 'xai', routes: [route], second_levels: { L2: { provider: 'xai', model: SECOND } } }), proof: { tiers: { high: { paths: ['src/**'] } } } };
    const w = await wired({ cfg, content: lines(450) });
    await w.round();
    assert.deepEqual(w.spawned.map((s) => [s.lens, s.provider, s.model]), [
      ['full', 'anthropic', ROUTED],
      ['full', 'xai', SECOND],
      ['judge', 'anthropic', 'claude-fable-5-1'],
    ]);
    assert.deepEqual(w.spawned.map((s) => s.timeoutMs), [900000, 900000, 900000]);
    assert.deepEqual(w.events('review.route').map((r) => [r.level, r.model, r.second_model]), [['L2', ROUTED, null]]);
  });
});

describe('B90 (4): a deleted file is never routed', () => {
  test('a committed deletion with a route from 0 lines (it matches every count): the L2 model at the base timeout, no review.route row', async () => {
    const cfg = cfgWith({ routes: [{ min_lines: 0, model: ROUTED, session_timeout_s: 900 }] });
    // the rule matches the deleted file's 0 lines: only the deleted guard keeps it unrouted
    assert.deepEqual(pickRoutes(cfg, 0).map(({ rule }) => rule.model), [ROUTED]);
    const w = await wired({ cfg, deleted: true });
    const r = await w.round();
    assert.equal(r.contentHash, 'deleted');
    assert.equal(r.result.status, 'reviewed');
    assert.deepEqual(w.spawned.map((s) => [s.model, s.timeoutMs]), [[PRIMARY, 300000]]);
    assert.equal(w.events('review.route').length, 0);
  });
});

describe('B90 (5): no review.routes ⇒ today\'s spawn options and rows', () => {
  // The BASE reference: this same ticket (cfgWith({session_timeout_s: 60}), a 450-line file, one
  // clean round) run through this same harness on the code at 8fadc12 (before B90), in a scratch
  // worktree outside the repo, removed after. Left out, because they differ per run by
  // construction: the run id (`run`), the ticket id (`ticket`), the signature (`mac`), the times
  // (`ts`), the signed state's hash (`state_sha`), and the spawn options' temp paths (`promptPath`,
  // `runRoot`). The finding schema the session gets is pinned by its `$id`. B92 adds one 287-byte
  // rule line to every lens; B98 adds 26 tokens for paired tests and B117 adds 67 for proof guidance: 6687 + 72 + 26 + 67.
  const PER_RUN = new Set(['run', 'ticket', 'mac', 'ts', 'state_sha', 'promptPath', 'runRoot']);
  /** @param {any} v @returns {any} */
  const norm = (v) => (Array.isArray(v) ? v.map(norm) : v && typeof v === 'object' ? Object.fromEntries(Object.entries(v).filter(([k]) => !PER_RUN.has(k)).map(([k, x]) => [k, norm(x)])) : v);
  const HASH = 'a918d72b65614825735f3c6668271bc64174405bd5abad5a2dc9018234d6d947';
  const BASE_SPAWN = {
    level: 'L2',
    role: 'reviewer',
    schema: 'https://code-forge.local/schema/review/finding.schema.json',
    rowExtra: { lens: 'full', context_mode: 'hunks', ctx_tokens_in: 6852 },
    block: 'B1',
    slug: 'route-test',
    timeoutMs: 60000,
    model: PRIMARY,
    provider: 'anthropic',
    lens: 'full',
  };
  const BASE_SPAWN_KEYS = ['cfg', 'level', 'role', 'promptPath', 'schema', 'rowExtra', 'runRoot', 'run', 'block', 'slug', 'timeoutMs', 'model', 'provider', 'lens'];
  const BASE_ROWS = [
    { event: 'review.budget', block: 'B1', budget: 150000, forecast_unconstrained: 7000, forecast_chosen: 7000, actual: null, raised: 0, over_budget: false, degrade_step: 0, file: FILE, content_hash: HASH },
    { event: 'review.plan', depth_unconstrained: 'full', depth_chosen: 'full', degrade_step: 0, risk: 1, context_mode: 'hunks', forecast_tokens: 6852, block: 'B1', file: FILE, content_hash: HASH },
    { event: 'review.round', file: FILE, round: 1, level: 'L2', kind: 'full', open_before: 0, closed: 0, new_in_hunks: 0, late: 0, open_after: 0, tokens_in: null, tokens_out: null, block: 'B1', content_hash: HASH },
    { event: 'review.approved', file: FILE, content_hash: HASH, round: 1, block: 'B1' },
    {
      event: 'review.state',
      block: 'B1',
      file: FILE,
      seq: 1,
      l3_rung_used: false,
      state_body: {
        block: 'B1',
        file: FILE,
        seq: 1,
        state: { file: FILE, level: 'L2', round: 1, rounds_at_level: 1, warn_rounds_at_level: 0, open: [], late: [], reviewed_content: null, l3_rung_used: false, status: 'complete', next: { action: 'complete' }, last_packet: null, pending_kind: null },
        reviewed_content_sha: HASH,
        reviewed_content_blob: 'c82e63519675fccee2c1eb7729bb87ecd057ffca',
      },
      content_hash: HASH,
    },
    { event: 'review.result', block: 'B1', file: FILE, content_hash: HASH, status: 'reviewed', approved: true, open: [] },
  ];
  const BASE_RESULT = {
    event: 'review.result',
    block: 'B1',
    file: FILE,
    content_hash: HASH,
    status: 'reviewed',
    approved: true,
    engine: 'adaptive',
    findings: [],
    round: 1,
    kind: 'full',
    level: 'L2',
    next: { action: 'complete' },
    trigger: null,
    late: 0,
    sessions: [{ lens: 'full', role: 'reviewer', level: 'L2', slot: null, context_mode: 'hunks', ctx_tokens_in: 6852, provider: 'anthropic', model: PRIMARY, fallback_step: 0, attempts: 1, tokens_in: 900, tokens_out: 300, status: 'ok', reason: null, passed: true, findings: [] }],
    open: [],
  };

  /** @param {Awaited<ReturnType<typeof wired>>} w @param {Record<string, any>} result */
  const observed = (w, result) => {
    // B107: verify the signed per-process identity before comparing the pre-B107 fields.
    const resultBody = (/** @type {Record<string, any>} */ row) => {
      const { worker, ...body } = row;
      assert.deepEqual(worker, { pid: process.pid, started_at: readStartTime(process.pid) });
      assert.equal(verifyRow(row, w.key).ok, true, 'worker identity is covered by the signature');
      return norm(body);
    };
    return {
      spawnKeys: w.spawned.map((s) => Object.keys(s)),
      spawned: w.spawned.map((s) => {
        const { cfg: _c, ...rest } = norm(s);
        return { ...rest, schema: rest.schema?.$id };
      }),
      rows: w.rows.map((row) => row.event === 'review.result' ? resultBody(row) : norm(row)),
      result: resultBody(result),
    };
  };
  const BASE = { spawnKeys: [BASE_SPAWN_KEYS], spawned: [BASE_SPAWN], rows: BASE_ROWS, result: BASE_RESULT };

  test('no review.routes: the spawn options, rows and result equal the pinned 8fadc12 reference; the session gets the worker\'s config object itself', async () => {
    const cfg = cfgWith({ session_timeout_s: 60 });
    const w = await wired({ cfg, content: lines(450) });
    const r = await w.round();
    assert.deepEqual(observed(w, r.result), BASE);
    assert.equal(w.spawned[0].cfg, cfg);
  });

  test('a routes list no rule of which matches: the same pinned 8fadc12 reference', async () => {
    const w = await wired({ cfg: cfgWith({ session_timeout_s: 60, routes: [{ min_lines: 5000, model: ROUTED, session_timeout_s: 900 }] }), content: lines(450) });
    const r = await w.round();
    assert.deepEqual(observed(w, r.result), BASE);
  });
});

describe('B90 fail closed', () => {
  test('a malformed routes list in the snapshot ⇒ unavailable: route-invalid, no session', async () => {
    const w = await wired({ cfg: cfgWith({ routes: [{ min_lines: 400 }] }), content: lines(450) });
    const r = await w.round();
    assert.deepEqual([r.result.status, r.result.reason, r.result.approved], ['unavailable', 'route-invalid', false]);
    assert.equal(w.spawned.length, 0);
  });

  test('a review.route row that cannot be written ⇒ unavailable: route-row, no session', async () => {
    const w = await wired({ cfg: cfgWith({ routes: [ROUTE_400] }), content: lines(450), failRouteRow: true });
    const r = await w.round();
    assert.deepEqual([r.result.status, r.result.reason, r.result.approved], ['unavailable', 'route-row', false]);
    assert.equal(w.spawned.length, 0);
  });

  test('a retry after a failed row write gives exactly one row per routed level', async () => {
    // an L2 and an L3 rule both match: two rows; the second write (L3) fails once
    const cfg = cfgWith({ routes: [ROUTE_400, { min_lines: 400, level: 'L3', model: 'claude-fable-5-1-judge' }] });
    const w = await wired({ cfg, content: lines(450), failRouteRowAt: [2] });
    const first = await w.round();
    assert.deepEqual([first.result.status, first.result.reason], ['unavailable', 'route-row']);
    assert.equal(w.spawned.length, 0);
    assert.deepEqual(w.events('review.route').map((r) => [r.ticket, r.level]), [[first.ticket, 'L2']]);
    const retry = await w.round();
    assert.equal(retry.ticket, first.ticket);
    assert.deepEqual([retry.result.status, retry.result.approved], ['reviewed', true]);
    assert.deepEqual(w.events('review.route').map((r) => [r.ticket, r.level]), [[first.ticket, 'L2'], [first.ticket, 'L3']]);
    assert.deepEqual(w.spawned.map((s) => `${s.model}@${s.timeoutMs}`), [`${ROUTED}@900000`]);
  });

  test('a second worker for the same run (a restart) re-running the same ticket writes no extra row', async () => {
    // the worker's runId is the run the ticket belongs to (a ticket of another run is refused
    // other-run), so the "row already written" check holds across workers of that run
    const cfg = cfgWith({ routes: [ROUTE_400, { min_lines: 400, level: 'L3', model: 'claude-fable-5-1-judge' }] });
    const w = await wired({ cfg, content: lines(450), failRouteRowAt: [2] });
    const first = await w.round();
    assert.deepEqual([first.result.status, first.result.reason], ['unavailable', 'route-row']);
    await w.restart();
    const second = await w.round();
    assert.equal(second.ticket, first.ticket);
    assert.deepEqual([second.result.status, second.result.approved], ['reviewed', true]);
    await w.restart();
    const third = await w.round(); // the same content again, on a third worker
    assert.equal(third.ticket, first.ticket);
    assert.deepEqual(w.events('review.route').map((r) => [r.run, r.ticket, r.level]), [[w.runId, first.ticket, 'L2'], [w.runId, first.ticket, 'L3']]);
    assert.deepEqual(w.spawned.map((s) => s.model), [ROUTED, ROUTED]);
  });

  test('an error that is not a route error is not route-invalid: it reaches the worker-error path, named as the routing step with its original text', async () => {
    const cfg = cfgWith({ routes: [ROUTE_400] });
    // a snapshot whose routes cannot even be read (a programming error, not a bad route list)
    Object.defineProperty(cfg.review, 'routes', { enumerable: false, get() { throw new TypeError('boom'); } });
    const w = await wired({ cfg, content: lines(450) });
    const r = await w.round();
    assert.deepEqual([r.result.status, r.result.reason, r.result.detail, r.result.approved], ['unavailable', 'worker-error', 'routing: TypeError: boom', false]);
    assert.equal(w.spawned.length, 0);
    assert.equal(w.events('review.route').length, 0);
  });

  test('a rule whose provider, a fallback item, or its second\'s provider is not a string ⇒ route-invalid, no row, no session', async () => {
    const bad = [
      { ...ROUTE_400, provider: 42 },
      { ...ROUTE_400, effort: 42 },
      { ...ROUTE_400, fallback: [42] },
      { ...ROUTE_400, fallback: [{ provider: 'anthropic' }] },
      { ...ROUTE_400, fallback: [{ provider: 42, model: ROUTED }] },
      { ...ROUTE_400, second: { provider: 42, model: ROUTE_SECOND } },
      { ...ROUTE_400, second: { model: ROUTE_SECOND, fallback: [42] } },
    ];
    const got = [];
    for (const rule of bad) {
      const w = await wired({ cfg: cfgWith({ routes: [rule] }), content: lines(450) });
      const r = await w.round();
      got.push([JSON.stringify(rule), r.result.status, r.result.reason, r.result.approved, w.spawned.length, w.events('review.route').length]);
    }
    assert.deepEqual(got, bad.map((rule) => [JSON.stringify(rule), 'unavailable', 'route-invalid', false, 0, 0]));
    // the control: the same rules with valid values route
    const good = { ...ROUTE_400, provider: 'anthropic', effort: 'high', fallback: [{ provider: 'anthropic', model: PRIMARY }], second: { provider: 'anthropic', model: ROUTE_SECOND, fallback: [{ provider: 'anthropic', model: PRIMARY }] } };
    const w = await wired({ cfg: cfgWith({ routes: [good] }), content: lines(450) });
    await w.round();
    assert.deepEqual([w.spawned.map((s) => s.model), w.events('review.route').length], [[ROUTED], 1]);
  });
});
