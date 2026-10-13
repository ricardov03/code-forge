// helpers FIRST: it pins $HOME under the per-file temp parent before any src module loads.
import { answerFor, cfgFor, freshDir, lines, makeRepo, writeFile } from './helpers.mjs';
import assert from 'node:assert/strict';
import { appendFileSync, readFileSync, rmSync } from 'node:fs';
import path from 'node:path';
import { describe, test } from 'node:test';

// B91: a timed-out review attempt is a BROKEN result, like a schema miss. The packet's second
// broken result (schema misses and timeouts together, per packet, level and role) starts the B55
// second-level try once, in the same ticket, with `trigger: timeout`. The wiring tests drive an
// in-process worker on a stub session spawner (never a real CLI) in a temp HOME and a temp repo.

const { packetHash, fallbackTrigger, ledgerSchemaHistory } = await import('../../src/review/schema-fallback.mjs');
const { enqueue, queueDir, readResult } = await import('../../src/worker/queue.mjs');
const { createWorker } = await import('../../src/worker/loop.mjs');
const { startRun } = await import('../../src/state/run.mjs');
const { openBlock } = await import('../../src/state/block.mjs');
const { loadKey, signRow, verifyRow } = await import('../../src/state/signer.mjs');
const { createKeyStore } = await import('../../src/keys/store.mjs');
const { reviewFile } = await import('../../src/review/engine.mjs');
const { spawnWithTimeoutRetry } = await import('../../src/review/session-retry.mjs');

const FILE = 'src/a.mjs';
const PRIMARY = 'claude-opus-5-5';
const SECOND = 'grok-4.7';
const ROUTED = 'claude-fable-5-1-big';
const ROUTE_SECOND = 'claude-sonnet-5-second';

/** @param {string} text @returns {string[]} the packet's hunk list. */
function hunksOf(text) {
  const at = text.indexOf('\nhunks:\n');
  if (at < 0) return [];
  const listed = text.slice(at + 8).split('\n');
  const end = listed.findIndex((l) => !l.startsWith('- @@'));
  return listed.slice(0, end < 0 ? listed.length : end).map((l) => l.slice(2));
}

/** @param {Record<string, any>} [review] @returns {Record<string, any>} */
const cfgWith = (review = {}) => ({
  provider: 'anthropic',
  levels: { L0: { model: 'claude-haiku-4-5-20251001' }, L1: { model: 'claude-sonnet-5' }, L2: { model: PRIMARY }, L3: { model: 'claude-fable-5-1' } },
  review: { min_tokens_out: 40, ...review },
});

const WITH_SECOND = { second_levels: { L2: { provider: 'xai', model: SECOND } } };

let seq = 0;

/**
 * A run with block B1 owning `src/**` and an in-process worker on a stub spawner. Each spawn takes
 * the next outcome of `script.perModel[model]`, else of `script.queue` ('timeout' | 'invalid' |
 * 'budget' | 'ok' | 'timeout-bare', a timeout result that names no provider or model); both empty
 * ⇒ 'ok'. An 'ok' answer has `firstFindings` on a full review and resolves every open id on a
 * recheck. `failRow(row)` true ⇒ the ledger writer throws on that row (nothing is kept).
 * @param {{cfg: Record<string, any>, content?: string, firstFindings?: Array<Record<string, any>>, failRow?: (row: Record<string, any>) => boolean}} o
 */
async function wired(o) {
  seq += 1;
  const runId = `r-b91-${process.pid}-${seq}`;
  const repo = await makeRepo();
  const noRow = async () => {};
  await startRun({ workspace: repo, project: 'broken-test', runId, writeRow: noRow });
  await openBlock({ runId, id: 'B1', level: 'L2', owned: ['src/**'], acceptance: [{ clause: 'c', tests: ['t'] }], writeRow: noRow });
  writeFile(repo, FILE, o.content ?? lines(30));
  const key = await loadKey(runId);
  /** @type {{queue: string[], perModel: Record<string, string[]>}} */
  const script = { queue: [], perModel: {} };
  /** @type {Array<Record<string, any>>} every spawn's options */
  const spawned = [];
  /** @type {string[]} the packet text of every spawn */
  const texts = [];
  /** @type {Array<Record<string, any>>} */
  const rows = [];
  const spawn = async (/** @type {Record<string, any>} */ opts) => {
    const text = readFileSync(opts.promptPath, 'utf8');
    texts.push(text);
    const lens = opts.rowExtra?.lens;
    const level = opts.cfg?.levels?.[opts.level] ?? {};
    const model = level.model;
    const provider = level.provider ?? opts.cfg?.provider;
    spawned.push({ ...opts, model, provider, lens });
    const head = { provider, model, fallback_step: 0, usage: { tokens_in: 900, tokens_out: 300 } };
    const next = (script.perModel[model]?.length ? script.perModel[model].shift() : script.queue.shift()) ?? 'ok';
    if (next === 'timeout-bare') return { status: 'timeout', reason: 'killed', answer: null, exit_code: null, duration_ms: 1000, stderr_tail: 'still thinking', stdout_bytes: 12, stdout_events: ['system'] };
    if (next === 'timeout') return { provider, model, status: 'timeout', reason: 'killed', answer: null, exit_code: null, duration_ms: 1000, stderr_tail: 'still thinking', stdout_bytes: 12, stdout_events: ['system'] };
    if (next === 'budget') return { provider, model, status: 'unavailable', reason: 'budget', message: 'budget.usd reached', answer: null };
    if (next === 'invalid') return { ...head, status: 'invalid-output', reason: 'answer does not match the schema', answer: null, text: '{"passed":"maybe"}', exit_code: 0 };
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
  const worker = await createWorker(
    { runId, repoRoot: repo, cfg: o.cfg, runRootDir: freshDir('runroot'), slug: 'broken-test', key },
    {
      store: await createKeyStore({ backends: [], dir: freshDir('store') }),
      env: {},
      writeRow: async (row) => {
        if (o.failRow?.(row)) throw new Error('disk full');
        rows.push(row);
      },
      readRows: async () => rows,
      spawn: /** @type {any} */ (spawn),
      jev,
    },
  );
  /** @param {{edit?: boolean}} [opts] */
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
  /** every spawn as `lens model@timeoutMs`, in order */
  const calls = () => spawned.map((s) => `${s.lens} ${s.model}@${s.timeoutMs}`);
  /** [session_timeout, schema_invalid, schema_fallback] rows written, exactly */
  const counts = () => ['review.session_timeout', 'review.schema_invalid', 'review.schema_fallback'].map((e) => events(e).length);
  return { repo, runId, key, rows, spawned, texts, script, round, events, calls, counts };
}

/** @param {Record<string, any>} row @returns {Record<string, any>} the row without its signature and time. */
const bodyOf = (row) => {
  const { mac: _mac, ts: _ts, ...body } = row;
  return body;
};

/** @param {{runId: string, key: Buffer}} w @param {Record<string, any>} row */
const allSigned = (w, row) => verifyRow(row, w.key).ok === true;

describe('B91 (1): two timeouts on one packet try the second level in the same ticket', () => {
  test('timeout, timeout ⇒ one signed trigger: timeout mark, then the second level; its valid answer is the round\'s', async () => {
    const w = await wired({ cfg: cfgWith(WITH_SECOND) });
    w.script.queue.push('timeout', 'timeout', 'ok');
    const r = await w.round();
    assert.deepEqual([r.result.status, r.result.approved], ['reviewed', true]);
    assert.deepEqual(w.calls(), [`quick ${PRIMARY}@300000`, `quick ${PRIMARY}@300000`, `quick ${SECOND}@300000`]);
    const hash = packetHash(w.texts[0]);
    assert.deepEqual(w.texts.map(packetHash), [hash, hash, hash]);
    const marks = w.events('review.schema_fallback');
    assert.equal(marks.length, 1);
    assert.equal(allSigned(w, marks[0]), true);
    assert.deepEqual(bodyOf(marks[0]), {
      run: w.runId,
      event: 'review.schema_fallback',
      lens: 'quick',
      role: 'reviewer',
      level: 'L2',
      packet_hash: hash,
      from_provider: 'anthropic',
      from_model: PRIMARY,
      to: 'review.second_levels.L2',
      to_provider: 'xai',
      to_model: SECOND,
      attempts: 2,
      trigger: 'timeout',
      block: 'B1',
      file: FILE,
      content_hash: r.contentHash,
    });
    // the mark is written before the second-level session: after the 2nd note, before the result
    const order = w.rows.map((x) => x.event).filter((e) => ['review.session_timeout', 'review.schema_fallback', 'review.result'].includes(e));
    assert.deepEqual(order.slice(0, 3), ['review.session_timeout', 'review.session_timeout', 'review.schema_fallback']);
    const notes = w.events('review.session_timeout');
    assert.equal(notes.length, 2);
    assert.deepEqual(notes.map((n) => [n.attempt, n.retried, n.packet_hash, n.second_level, allSigned(w, n)]), [
      [1, true, hash, undefined, true],
      [2, false, hash, undefined, true],
    ]);
    // a timeout is never logged as a schema miss
    assert.deepEqual(w.counts(), [2, 0, 1]);
  });

  test('a timed-out result that names no model ⇒ the mark\'s from is the level\'s resolved provider and model', async () => {
    const w = await wired({ cfg: cfgWith(WITH_SECOND) });
    w.script.queue.push('timeout-bare', 'timeout-bare', 'ok');
    const r = await w.round();
    assert.deepEqual([r.result.status, r.result.approved], ['reviewed', true]);
    assert.deepEqual(w.calls(), [`quick ${PRIMARY}@300000`, `quick ${PRIMARY}@300000`, `quick ${SECOND}@300000`]);
    assert.deepEqual(w.events('review.schema_fallback').map((m) => [m.from_provider, m.from_model, m.to_model, m.trigger]), [['anthropic', PRIMARY, SECOND, 'timeout']]);
    assert.deepEqual(w.counts(), [2, 0, 1]);
  });

  test('ONE timeout, then a valid answer ⇒ no second-level try: the fresh note counts once', async () => {
    const w = await wired({ cfg: cfgWith(WITH_SECOND) });
    w.script.queue.push('timeout', 'ok');
    const r = await w.round();
    assert.deepEqual([r.result.status, r.result.approved], ['reviewed', true]);
    assert.deepEqual(w.calls(), [`quick ${PRIMARY}@300000`, `quick ${PRIMARY}@300000`]);
    assert.deepEqual(w.events('review.session_timeout').map((n) => [n.attempt, n.retried, n.packet_hash === packetHash(w.texts[0])]), [[1, true, true]]);
    assert.deepEqual(w.counts(), [1, 0, 0]);
  });

  test('a routed ticket (B90 review.routes): two timeouts spawn the rule\'s second at the route timeout', async () => {
    const route = { min_lines: 400, model: ROUTED, session_timeout_s: 900, second: { provider: 'anthropic', model: ROUTE_SECOND } };
    const w = await wired({ cfg: cfgWith({ routes: [route], ...WITH_SECOND }), content: lines(450) });
    w.script.queue.push('timeout', 'timeout', 'ok');
    const r = await w.round();
    assert.deepEqual([r.result.status, r.result.approved], ['reviewed', true]);
    assert.deepEqual(w.calls(), [`full ${ROUTED}@900000`, `full ${ROUTED}@900000`, `full ${ROUTE_SECOND}@900000`]);
    assert.deepEqual(w.events('review.schema_fallback').map((m) => [m.from_model, m.to_provider, m.to_model, m.trigger, m.attempts, allSigned(w, m)]), [[ROUTED, 'anthropic', ROUTE_SECOND, 'timeout', 2, true]]);
    assert.equal(w.events('review.route').length, 1);
    const hash = packetHash(w.texts[0]);
    assert.deepEqual(w.texts.map(packetHash), [hash, hash, hash]);
    assert.deepEqual(w.events('review.session_timeout').map((n) => [n.lens, n.model, n.attempt, n.packet_hash, n.second_level, allSigned(w, n)]), [
      ['full', ROUTED, 1, hash, undefined, true],
      ['full', ROUTED, 2, hash, undefined, true],
    ]);
    assert.deepEqual(w.counts(), [2, 0, 1]);
  });

  test('a routed ticket with no review.second_levels: the rule\'s second alone is the ticket\'s second level', async () => {
    const route = { min_lines: 400, model: ROUTED, session_timeout_s: 900, second: { provider: 'anthropic', model: ROUTE_SECOND } };
    const w = await wired({ cfg: cfgWith({ routes: [route] }), content: lines(450) });
    w.script.queue.push('timeout', 'timeout', 'ok');
    const r = await w.round();
    assert.deepEqual([r.result.status, r.result.approved], ['reviewed', true]);
    assert.deepEqual(w.calls(), [`full ${ROUTED}@900000`, `full ${ROUTED}@900000`, `full ${ROUTE_SECOND}@900000`]);
    assert.deepEqual(w.events('review.schema_fallback').map((m) => [m.from_model, m.to_model, m.trigger, m.attempts]), [[ROUTED, ROUTE_SECOND, 'timeout', 2]]);
    assert.deepEqual(w.counts(), [2, 0, 1]);
  });

  test('a recheck (fix loop) that times out twice is tried on the second level too', async () => {
    const finding = { id: 'F1', file: FILE, line_start: 1, line_end: 1, severity: 'warning', category: 'correctness', claim: 'claim F1', evidence: 'e', fix: 'f' };
    const w = await wired({ cfg: cfgWith(WITH_SECOND), firstFindings: [finding] });
    const first = await w.round();
    assert.deepEqual([first.result.status, first.result.approved], ['reviewed', false]);
    w.script.queue.push('timeout', 'timeout', 'ok');
    const second = await w.round({ edit: true });
    assert.deepEqual([second.result.status, second.result.approved, second.result.kind], ['reviewed', true, 'recheck']);
    assert.deepEqual(w.calls(), [`quick ${PRIMARY}@300000`, `recheck ${PRIMARY}@300000`, `recheck ${PRIMARY}@300000`, `recheck ${SECOND}@300000`]);
    assert.deepEqual(w.events('review.schema_fallback').map((m) => [m.lens, m.to_model, m.attempts, m.trigger, allSigned(w, m)]), [['recheck', SECOND, 2, 'timeout', true]]);
    assert.deepEqual(w.events('review.session_timeout').map((n) => [n.lens, n.attempt, n.packet_hash === packetHash(w.texts[1])]), [['recheck', 1, true], ['recheck', 2, true]]);
    // the recheck's timeouts are never logged as schema misses
    assert.deepEqual(w.counts(), [2, 0, 1]);
  });

  test('a recheck whose second level misses the schema with a throwing ledger writer ⇒ a non-approval result, not an exception', async () => {
    const finding = { id: 'F1', file: FILE, line_start: 1, line_end: 1, severity: 'warning', category: 'correctness', claim: 'claim F1', evidence: 'e', fix: 'f' };
    const w = await wired({ cfg: cfgWith(WITH_SECOND), firstFindings: [finding], failRow: (row) => row.event === 'review.schema_invalid' && row.second_level === true });
    await w.round();
    w.script.queue.push('timeout', 'timeout', 'invalid');
    const second = await w.round({ edit: true });
    assert.deepEqual([second.result.status, second.result.reason, second.result.approved], ['unavailable', 'schema', false]);
    assert.deepEqual(w.calls(), [`quick ${PRIMARY}@300000`, `recheck ${PRIMARY}@300000`, `recheck ${PRIMARY}@300000`, `recheck ${SECOND}@300000`]);
    assert.deepEqual(w.counts(), [2, 0, 1]);
  });

  test('the second-level session refused by budget.usd ⇒ unavailable: budget, like any session', async () => {
    const w = await wired({ cfg: cfgWith(WITH_SECOND) });
    w.script.queue.push('timeout', 'timeout', 'budget');
    const r = await w.round();
    assert.deepEqual([r.result.status, r.result.reason, r.result.approved], ['unavailable', 'budget', false]);
    assert.deepEqual(w.calls(), [`quick ${PRIMARY}@300000`, `quick ${PRIMARY}@300000`, `quick ${SECOND}@300000`]);
    assert.deepEqual(w.events('review.schema_fallback').map((m) => [m.trigger, m.attempts, m.to_model, m.refused, allSigned(w, m)]), [['timeout', 2, SECOND, undefined, true]]);
    // the rows around the refusal: two notes, the mark, then the budget refusal and the result
    const around = w.rows.filter((x) => x.event !== 'review.budget' && x.event !== 'review.plan' && x.event !== 'review.state').map((x) => [x.event, x.reason ?? null]);
    assert.deepEqual(around, [
      ['review.session_timeout', null],
      ['review.session_timeout', null],
      ['review.schema_fallback', null],
      ['review.unavailable', 'budget'],
      ['review.result', 'budget'],
    ]);
    assert.deepEqual(w.counts(), [2, 0, 1]);
  });
});

describe('B91 (2): a schema miss and a timeout on one packet', () => {
  test('miss (round 1), then a timeout on the re-run ⇒ the try happens with trigger: timeout; no same-model retry', async () => {
    const w = await wired({ cfg: cfgWith(WITH_SECOND) });
    w.script.queue.push('invalid');
    const first = await w.round();
    assert.deepEqual([first.result.status, first.result.reason], ['unavailable', 'schema']);
    w.script.queue.push('timeout', 'ok');
    const second = await w.round();
    assert.equal(second.ticket, first.ticket);
    assert.deepEqual([second.result.status, second.result.approved], ['reviewed', true]);
    assert.deepEqual(w.calls(), [`quick ${PRIMARY}@300000`, `quick ${PRIMARY}@300000`, `quick ${SECOND}@300000`]);
    assert.deepEqual(w.counts(), [1, 1, 1]);
    assert.deepEqual(w.events('review.schema_fallback').map((m) => [m.attempts, m.trigger, m.from_model, m.to_model]), [[2, 'timeout', PRIMARY, SECOND]]);
  });

  test('timeout, then a schema miss in the same round ⇒ the try happens with trigger: schema', async () => {
    const w = await wired({ cfg: cfgWith(WITH_SECOND) });
    w.script.queue.push('timeout', 'invalid', 'ok');
    const r = await w.round();
    assert.deepEqual([r.result.status, r.result.approved], ['reviewed', true]);
    assert.deepEqual(w.calls(), [`quick ${PRIMARY}@300000`, `quick ${PRIMARY}@300000`, `quick ${SECOND}@300000`]);
    assert.deepEqual(w.events('review.schema_fallback').map((m) => [m.attempts, m.trigger]), [[2, 'schema']]);
    assert.deepEqual(w.counts(), [1, 1, 1]);
  });
});

describe('B91 (3): at most once per packet', () => {
  test('the second level times out too ⇒ its own B30 retry only; a third broken result on the re-run spawns no other try', async () => {
    const w = await wired({ cfg: cfgWith(WITH_SECOND) });
    w.script.queue.push('timeout', 'timeout', 'timeout', 'timeout');
    const first = await w.round();
    assert.deepEqual([first.result.status, first.result.reason], ['unavailable', 'timeout']);
    assert.deepEqual(w.calls(), [`quick ${PRIMARY}@300000`, `quick ${PRIMARY}@300000`, `quick ${SECOND}@300000`, `quick ${SECOND}@300000`]);
    // the second level's own timeouts say second_level: true and never count
    assert.deepEqual(w.events('review.session_timeout').map((n) => [n.model, n.attempt, n.second_level]), [
      [PRIMARY, 1, undefined],
      [PRIMARY, 2, undefined],
      [SECOND, 1, true],
      [SECOND, 2, true],
    ]);
    w.script.queue.push('timeout', 'timeout');
    const second = await w.round();
    assert.equal(second.ticket, first.ticket);
    assert.deepEqual([second.result.status, second.result.reason], ['unavailable', 'timeout']);
    assert.deepEqual(w.calls().slice(4), [`quick ${PRIMARY}@300000`, `quick ${PRIMARY}@300000`]);
    assert.equal(w.spawned.length, 6);
    assert.deepEqual(w.counts(), [6, 0, 1]);
  });

  /**
   * Round 1 is a clean review; one note of the round-2 packet is planted, then round 2 times out
   * once. Each planted kind differs from the control in ONE field only.
   * @param {(base: Record<string, any>, key: Buffer) => Record<string, any>} plant
   */
  async function plantedRound(plant) {
    const w = await wired({ cfg: cfgWith(WITH_SECOND) });
    const first = await w.round();
    assert.equal(first.result.status, 'reviewed');
    const hash = packetHash(w.texts[0]);
    const base = { run: w.runId, event: 'review.session_timeout', lens: 'quick', role: 'reviewer', level: 'L2', provider: 'anthropic', model: PRIMARY, attempt: 1, retried: true, packet_hash: hash, block: 'B1', file: FILE, content_hash: first.contentHash };
    w.rows.push(plant(base, w.key));
    w.script.queue.push('timeout', 'ok');
    const second = await w.round();
    assert.equal(second.ticket, first.ticket);
    // the planted rows name the very packet round 2 reads
    assert.equal(packetHash(w.texts[1]), hash);
    assert.deepEqual([second.result.status, second.result.approved], ['reviewed', true]);
    return w;
  }

  test('control: a signed note of the packet with packet_hash, not second_level, DOES count (one more timeout tries)', async () => {
    const w = await plantedRound((base, key) => signRow(base, key));
    assert.deepEqual(w.calls(), [`quick ${PRIMARY}@300000`, `quick ${PRIMARY}@300000`, `quick ${SECOND}@300000`]);
    assert.deepEqual(w.events('review.schema_fallback').map((m) => [m.attempts, m.trigger]), [[2, 'timeout']]);
    assert.deepEqual(w.counts(), [2, 0, 1]);
  });

  test('old rows never count: a note with no packet_hash (before B91)', async () => {
    const w = await plantedRound((base, key) => {
      const { packet_hash: _gone, ...old } = base;
      return signRow(old, key);
    });
    assert.deepEqual(w.calls(), [`quick ${PRIMARY}@300000`, `quick ${PRIMARY}@300000`, `quick ${PRIMARY}@300000`]);
    assert.deepEqual(w.counts(), [2, 0, 0]);
  });

  test('an unsigned note never counts', async () => {
    const w = await plantedRound((base) => ({ ...base }));
    assert.deepEqual(w.calls(), [`quick ${PRIMARY}@300000`, `quick ${PRIMARY}@300000`, `quick ${PRIMARY}@300000`]);
    assert.deepEqual(w.counts(), [2, 0, 0]);
  });

  test('a second_level note never counts', async () => {
    const w = await plantedRound((base, key) => signRow({ ...base, second_level: true }, key));
    assert.deepEqual(w.calls(), [`quick ${PRIMARY}@300000`, `quick ${PRIMARY}@300000`, `quick ${PRIMARY}@300000`]);
    assert.deepEqual(w.counts(), [2, 0, 0]);
  });
});

describe('B91 (4): no second level configured', () => {
  test('two timeouts ⇒ unavailable: timeout as before; each note carries packet_hash (pinned rows)', async () => {
    const w = await wired({ cfg: cfgWith() });
    w.script.queue.push('timeout', 'timeout');
    const r = await w.round();
    assert.deepEqual([r.result.status, r.result.reason, r.result.approved], ['unavailable', 'timeout', false]);
    assert.deepEqual(w.calls(), [`quick ${PRIMARY}@300000`, `quick ${PRIMARY}@300000`]);
    const hash = packetHash(w.texts[0]);
    const notes = w.events('review.session_timeout');
    assert.equal(notes.length, 2);
    const note = (/** @type {number} */ attempt) => ({
      run: w.runId,
      event: 'review.session_timeout',
      lens: 'quick',
      role: 'reviewer',
      level: 'L2',
      provider: 'anthropic',
      model: PRIMARY,
      attempt,
      retried: attempt === 1,
      duration_ms: 1000,
      stderr_tail: 'still thinking',
      stdout_bytes: 12,
      stdout_events: ['system'],
      packet_hash: hash,
      block: 'B1',
      file: FILE,
      content_hash: r.contentHash,
    });
    assert.deepEqual(notes.map(bodyOf), [note(1), note(2)]);
    assert.deepEqual(notes.map((n) => allSigned(w, n)), [true, true]);
    assert.deepEqual(w.counts(), [2, 0, 0]);
  });

  test('a schema miss, then a timeout on the re-run ⇒ as before: one same-model retry, then unavailable: timeout; no mark', async () => {
    const w = await wired({ cfg: cfgWith() });
    w.script.queue.push('invalid');
    const first = await w.round();
    assert.deepEqual([first.result.status, first.result.reason], ['unavailable', 'schema']);
    w.script.queue.push('timeout', 'timeout');
    const second = await w.round();
    assert.equal(second.ticket, first.ticket);
    assert.deepEqual([second.result.status, second.result.reason, second.result.approved], ['unavailable', 'timeout', false]);
    assert.deepEqual(w.calls(), [`quick ${PRIMARY}@300000`, `quick ${PRIMARY}@300000`, `quick ${PRIMARY}@300000`]);
    assert.deepEqual(w.counts(), [2, 1, 0]);
  });
});

describe('B91 (5): consensus never tries', () => {
  test('multimodel on a high-tier path: slot A times out twice ⇒ no mark, no extra spawn', async () => {
    const cfg = { ...cfgWith({ multimodel: true, second_provider: 'xai', ...WITH_SECOND }), proof: { tiers: { high: { paths: ['src/**'] } } } };
    const w = await wired({ cfg });
    w.script.perModel[PRIMARY] = ['timeout', 'timeout'];
    const r = await w.round();
    assert.deepEqual([r.result.status, r.result.reason, r.result.approved], ['unavailable', 'timeout', false]);
    assert.equal(w.events('review.schema_fallback').length, 0);
    // slots A and B run side by side: the order between them is not fixed, the counts are
    assert.deepEqual([...w.calls()].sort(), [`full ${PRIMARY}@300000`, `full ${PRIMARY}@300000`, `full ${SECOND}@300000`]);
    assert.deepEqual(w.events('review.session_timeout').map((n) => [n.model, n.attempt]), [[PRIMARY, 1], [PRIMARY, 2]]);
    assert.deepEqual(w.counts(), [2, 0, 0]);
  });
});

describe('B91 readers', () => {
  test('fallbackTrigger: an old mark with no trigger reads as schema', () => {
    assert.deepEqual([fallbackTrigger({}), fallbackTrigger({ trigger: 'schema' }), fallbackTrigger({ trigger: 'timeout' }), fallbackTrigger({ trigger: 'other' })], ['schema', 'schema', 'timeout', 'schema']);
  });

  test('ledgerSchemaHistory counts signed timeout notes of the packet, level and role; not other packets, levels, roles or second-level ones', async () => {
    const key = Buffer.alloc(32, 7);
    const at = { run: 'r1', file: FILE, content_hash: 'c1', packet_hash: 'p1', level: 'L2', role: 'reviewer' };
    const rows = [
      signRow({ ...at, event: 'review.session_timeout' }, key),
      signRow({ ...at, event: 'review.schema_invalid' }, key),
      signRow({ ...at, event: 'review.session_timeout', packet_hash: 'p2' }, key),
      signRow({ ...at, event: 'review.session_timeout', level: 'L3' }, key),
      signRow({ ...at, event: 'review.session_timeout', role: 'judge' }, key),
      signRow({ ...at, event: 'review.session_timeout', second_level: true }, key),
      signRow({ ...at, event: 'review.session_timeout', packet_hash: undefined }, key),
    ];
    const history = ledgerSchemaHistory({ readRows: async () => rows, verify: (/** @type {any} */ r) => verifyRow(r, key).ok === true, runId: 'r1', file: FILE, contentHash: 'c1' });
    assert.deepEqual(await history('p1', { level: 'L2', role: 'reviewer' }), { failures: 2, fellBack: false });
  });
});

describe('B91 engine: the session summary on the timeout path', () => {
  const ONE = 'src/one.mjs';
  const HUNKS = ['@@ -0,0 +1,12 @@'];
  const OK = { status: 'ok', answer: answerFor(HUNKS), usage: { tokens_in: 100, tokens_out: 80 }, exit_code: 0, provider: 'xai', model: SECOND };
  const TIMEOUT = { status: 'timeout', reason: 'killed', answer: null, exit_code: null, provider: 'anthropic', model: PRIMARY, duration_ms: 1000, stderr_tail: 't', stdout_bytes: 1, stdout_events: [] };
  const INVALID = { status: 'invalid-output', reason: 'answer does not match the schema', answer: null, text: '{"passed":"maybe"}', exit_code: 0, provider: 'xai', model: SECOND, usage: { tokens_in: 100, tokens_out: 80 } };

  /**
   * reviewFile on one new 12-line file (one quick session) with a scripted spawn and an in-memory
   * history over the rows written; `failRow(row)` true ⇒ the writer throws on that row.
   * @param {Array<Record<string, any>>} script @param {(row: Record<string, any>) => boolean} [failRow]
   */
  async function direct(script, failRow = () => false) {
    const repo = await makeRepo();
    writeFile(repo, ONE, lines(12));
    /** @type {Array<Record<string, any>>} */
    const rows = [];
    let k = 0;
    const spawn = async () => structuredClone(script[k++] ?? OK);
    const writeRow = async (/** @type {Record<string, any>} */ row) => {
      if (failRow(row)) throw new Error('disk full');
      rows.push(row);
    };
    const schemaHistory = async (/** @type {string} */ hash, /** @type {{level: string, role: string}} */ who) => ({
      failures: rows.filter((r) => ['review.session_timeout', 'review.schema_invalid'].includes(r.event) && r.packet_hash === hash && r.level === who.level && r.role === who.role && r.second_level !== true).length,
      fellBack: rows.some((r) => r.event === 'review.schema_fallback' && r.packet_hash === hash),
    });
    const outcome = await reviewFile({ repoRoot: repo, file: ONE, base: null, cfg: cfgFor(WITH_SECOND), risk: 0, workDir: path.join(freshDir('runroot'), 'packets', 't') }, { spawn, writeRow, schemaHistory });
    const count = (/** @type {string} */ e) => rows.filter((r) => r.event === e).length;
    return { outcome, spawns: k, counts: [count('review.session_timeout'), count('review.schema_invalid'), count('review.schema_fallback')] };
  }

  test('timeout, timeout, second level ok ⇒ attempts 3 in the summary', async () => {
    const { outcome, spawns, counts } = await direct([TIMEOUT, TIMEOUT, OK]);
    assert.equal(spawns, 3);
    assert.deepEqual([outcome.status, outcome.approved], ['reviewed', true]);
    assert.deepEqual(outcome.sessions.map((/** @type {any} */ s) => [s.status, s.attempts, s.model]), [['ok', 3, SECOND]]);
    assert.deepEqual(counts, [2, 0, 1]);
  });

  test('timeout, timeout, the second level times out then answers ⇒ attempts 4', async () => {
    const { outcome, spawns, counts } = await direct([TIMEOUT, TIMEOUT, TIMEOUT, OK]);
    assert.equal(spawns, 4);
    assert.deepEqual(outcome.sessions.map((/** @type {any} */ s) => [s.status, s.attempts]), [['ok', 4]]);
    assert.deepEqual(counts, [3, 0, 1]);
  });

  test('the second level misses the schema and the writer throws on its miss row ⇒ unavailable: schema, never an exception', async () => {
    const { outcome, spawns, counts } = await direct([TIMEOUT, TIMEOUT, INVALID], (row) => row.event === 'review.schema_invalid');
    assert.equal(spawns, 3);
    assert.deepEqual([outcome.status, outcome.reason, outcome.approved], ['unavailable', 'schema', false]);
    assert.deepEqual(outcome.sessions.map((/** @type {any} */ s) => [s.status, s.reason, s.attempts, s.schema_fallback]), [['unavailable', 'schema', 3, { level: 'review.second_levels.L2', provider: 'xai', model: SECOND }]]);
    assert.deepEqual(counts, [2, 0, 1]);
  });

  test('the writer throws on the schema_fallback mark ⇒ no try: P, P exactly, unavailable: timeout, no schema_fallback in the summary', async () => {
    const { outcome, spawns, counts } = await direct([TIMEOUT, TIMEOUT, OK], (row) => row.event === 'review.schema_fallback');
    assert.equal(spawns, 2);
    assert.deepEqual([outcome.status, outcome.reason, outcome.approved], ['unavailable', 'timeout', false]);
    assert.deepEqual(outcome.sessions.map((/** @type {any} */ s) => [s.status, s.reason, s.attempts, s.schema_fallback]), [['unavailable', 'timeout', 2, undefined]]);
    assert.deepEqual(counts, [2, 0, 0]);
  });
});

describe('B91 round 2: a throw never brings a same-model spawn after the try; the guard asks the decision', () => {
  test('spawnWithTimeoutRetry: an onBroken that spawns the second level, then throws ⇒ rejects; never the B30 same-model retry', async () => {
    /** @type {string[]} */
    const calls = [];
    const spawn = async (/** @type {Record<string, any>} */ opts) => {
      calls.push(opts.level);
      return { status: 'timeout', reason: 'killed', answer: null, provider: 'anthropic', model: PRIMARY };
    };
    /** @type {number[]} */
    const seen = [];
    let notes = 0;
    const onBroken = async () => {
      await spawn({ level: 'second' });
      throw new Error('the hook failed after its try started');
    };
    await assert.rejects(
      spawnWithTimeoutRetry(
        /** @type {any} */ (spawn),
        { level: 'L2' },
        async () => {
          notes += 1;
        },
        (n) => seen.push(n),
        { packetHash: 'h', onBroken },
      ),
      /the hook failed after its try started/,
    );
    // one first-level spawn, its note written BEFORE the hook, the hook's spawn, nothing after it
    assert.deepEqual([calls, seen, notes], [['L2', 'second'], [1], 1]);
  });

  test('worker: the writer throws on the review.schema_fallback mark ⇒ no try; P, P exactly; unavailable: timeout', async () => {
    const w = await wired({ cfg: cfgWith(WITH_SECOND), failRow: (row) => row.event === 'review.schema_fallback' });
    w.script.queue.push('timeout', 'timeout', 'ok');
    const r = await w.round();
    assert.deepEqual([r.result.status, r.result.reason, r.result.approved], ['unavailable', 'timeout', false]);
    assert.deepEqual(w.calls(), [`quick ${PRIMARY}@300000`, `quick ${PRIMARY}@300000`]);
    assert.deepEqual(w.counts(), [2, 0, 0]);
  });

  test('a recheck: the writer throws on the mark ⇒ no try; recheck P, P exactly; a non-approval result', async () => {
    const finding = { id: 'F1', file: FILE, line_start: 1, line_end: 1, severity: 'warning', category: 'correctness', claim: 'claim F1', evidence: 'e', fix: 'f' };
    const w = await wired({ cfg: cfgWith(WITH_SECOND), firstFindings: [finding], failRow: (row) => row.event === 'review.schema_fallback' });
    await w.round();
    w.script.queue.push('timeout', 'timeout', 'ok');
    const second = await w.round({ edit: true });
    assert.deepEqual([second.result.status, second.result.reason, second.result.approved], ['unavailable', 'timeout', false]);
    assert.deepEqual(w.calls(), [`quick ${PRIMARY}@300000`, `recheck ${PRIMARY}@300000`, `recheck ${PRIMARY}@300000`]);
    assert.deepEqual(w.counts(), [2, 0, 0]);
  });

  test('role matters: a closed-book second level (Codex for a reviewer) ⇒ a refused closed-book mark with trigger: timeout, no try', async () => {
    const w = await wired({ cfg: cfgWith({ second_levels: { L2: { provider: 'openai', model: 'gpt-6-sol' } } }) });
    w.script.queue.push('timeout', 'timeout', 'ok');
    const r = await w.round();
    assert.deepEqual([r.result.status, r.result.reason, r.result.approved], ['unavailable', 'timeout', false]);
    assert.deepEqual(w.calls(), [`quick ${PRIMARY}@300000`, `quick ${PRIMARY}@300000`]);
    assert.deepEqual(w.events('review.schema_fallback').map((m) => [m.role, m.trigger, m.refused, m.attempts, allSigned(w, m)]), [['reviewer', 'timeout', 'closed-book', 2, true]]);
    assert.deepEqual(w.counts(), [2, 0, 1]);
  });

  test('a second level that is only the failed model ⇒ a refused same-model mark with trigger: timeout, no try', async () => {
    const w = await wired({ cfg: cfgWith({ second_levels: { L2: { provider: 'anthropic', model: PRIMARY } } }) });
    w.script.queue.push('timeout', 'timeout', 'ok');
    const r = await w.round();
    assert.deepEqual([r.result.status, r.result.reason, r.result.approved], ['unavailable', 'timeout', false]);
    assert.deepEqual(w.calls(), [`quick ${PRIMARY}@300000`, `quick ${PRIMARY}@300000`]);
    assert.deepEqual(w.events('review.schema_fallback').map((m) => [m.trigger, m.refused, m.from_model]), [['timeout', 'same-model', PRIMARY]]);
    assert.deepEqual(w.counts(), [2, 0, 1]);
  });
});
