// helpers FIRST: it pins $HOME under the per-file temp parent before any src module loads.
import { freshDir, makeRepo } from './helpers.mjs';
import assert from 'node:assert/strict';
import { appendFileSync, readFileSync, rmSync } from 'node:fs';
import path from 'node:path';
import { describe, test } from 'node:test';

const { enqueue, queueDir, readResult } = await import('../../src/worker/queue.mjs');
const { createWorker } = await import('../../src/worker/loop.mjs');
const { loadKey, signRow, verifyRow } = await import('../../src/state/signer.mjs');
const { createKeyStore } = await import('../../src/keys/store.mjs');

const FILE = 'src/a.mjs';
const PRIMARY = 'claude-opus-5-5';
const SECOND = 'grok-4.7';

/** @param {string} text @returns {string[]} the packet's hunk list. */
function hunksOf(text) {
  const listed = text.slice(text.indexOf('\nhunks:\n') + 8).split('\n');
  return listed.slice(0, listed.findIndex((l) => !l.startsWith('- @@'))).map((l) => l.slice(2));
}

/** A config on the stubs: every level on Anthropic, optionally a `review.second_levels`. */
const cfgWith = (/** @type {Record<string, any>} */ review = {}) => ({
  provider: 'anthropic',
  levels: { L0: { model: 'claude-haiku-4-5-20251001' }, L1: { model: 'claude-sonnet-5' }, L2: { model: PRIMARY }, L3: { model: 'claude-fable-5-1' } },
  review: { min_tokens_out: 40, ...review },
});

/**
 * An in-process worker on a stub session spawner: a session of a model in `script.invalid`
 * answers text that fails the schema (`invalid-output`, as the spawner reports it); a model in
 * `script.budget` is refused by the budget gate; any other gives a valid answer (round 1:
 * `firstFindings`, a recheck resolves every listed id). Jev: `defect` 0.95, `resolved` 0.2.
 * @param {{review?: Record<string, any>, firstFindings?: Array<Record<string, any>>}} [o]
 */
async function wired(o = {}) {
  const { repo, runId } = await makeRepo();
  const key = await loadKey(runId);
  const runRootDir = freshDir('runroot');
  const script = { invalid: new Set([PRIMARY]), budget: new Set(), throws: new Set(), failHistory: false, readsFail: false };
  /** @type {Array<[string, string]>} [lens, model] per spawn */
  const spawns = [];
  /** @type {Array<Record<string, any>>} */
  const rows = [];
  const spawn = async (/** @type {Record<string, any>} */ opts) => {
    const text = readFileSync(opts.promptPath, 'utf8');
    const lens = opts.rowExtra?.lens;
    const level = opts.cfg?.levels?.[opts.level] ?? {};
    const model = level.model;
    const provider = level.provider ?? opts.cfg?.provider;
    spawns.push([lens, model]);
    if (script.throws.has(model)) throw new Error('spawn exploded');
    // the history read that follows this session's miss throws (cleared when the miss is logged)
    if (script.failHistory && script.invalid.has(model)) script.readsFail = true;
    const head = { provider, model, fallback_step: 0, usage: { tokens_in: 900, tokens_out: 300 } };
    if (script.budget.has(model)) return { ...head, status: 'unavailable', reason: 'budget', message: 'budget.usd reached', answer: null };
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
  const worker = await createWorker(
    { runId, repoRoot: repo, cfg: cfgWith(o.review), runRootDir, slug: 'worker-test', key },
    {
      store: await createKeyStore({ backends: [], dir: freshDir('store') }),
      env: {},
      writeRow: async (row) => {
        if (row.event === 'review.schema_invalid') script.readsFail = false;
        rows.push(row);
      },
      readRows: async () => {
        if (script.readsFail) throw new Error('ledger unreadable');
        return rows;
      },
      spawn: /** @type {any} */ (spawn),
      jev,
    },
  );
  /**
   * `review-file` + the worker's drain. `edit` changes the file first (a coder fix); without it the
   * same content is enqueued again after an `unavailable` result — the existing retry: its done
   * marker is dropped (as `review-file` does) so the ticket runs again on the same packet.
   * @param {{edit?: boolean}} [opts]
   */
  const round = async ({ edit = false } = {}) => {
    if (edit) appendFileSync(path.join(repo, FILE), `export const fix = ${spawns.length};\n`);
    let t = enqueue({ repoRoot: repo, run: runId, block: 'B11', file: FILE });
    if (t.status === 'done') {
      rmSync(path.join(queueDir(repo), `${t.ticket}.done`), { force: true });
      t = enqueue({ repoRoot: repo, run: runId, block: 'B11', file: FILE });
    }
    assert.equal(await worker.drain(), 1);
    return { result: /** @type {Record<string, any>} */ (readResult(repo, runId, t.ticket)), contentHash: t.content_hash };
  };
  const events = (/** @type {string} */ name) => rows.filter((r) => r.event === name);
  return { repo, runId, key, rows, spawns, script, round, events };
}

const SECOND_LEVEL = { second_levels: { L2: { provider: 'xai', model: SECOND } } };

describe('B55: a packet\'s second schema miss is tried once on review.second_levels', () => {
  test('round 1: miss, then the retry misses again and the second level reviews it cleanly ⇒ approved; one signed review.schema_fallback row', async () => {
    const w = await wired({ review: SECOND_LEVEL });
    const first = await w.round();
    assert.deepEqual([first.result.status, first.result.reason, first.result.approved], ['unavailable', 'schema', false]);
    assert.deepEqual(w.spawns, [['quick', PRIMARY]]);
    assert.equal(w.events('review.schema_fallback').length, 0);

    const again = await w.round();
    assert.deepEqual([again.result.status, again.result.approved], ['reviewed', true]);
    assert.deepEqual(w.spawns, [['quick', PRIMARY], ['quick', PRIMARY], ['quick', SECOND]]);
    const [session] = again.result.sessions;
    assert.deepEqual([session.provider, session.model, session.schema_fallback, session.attempts, session.status], ['xai', SECOND, { level: 'review.second_levels.L2', provider: 'xai', model: SECOND }, 2, 'ok']);

    const misses = w.events('review.schema_invalid');
    assert.equal(misses.length, 2);
    const packetHash = misses[0].packet_hash;
    assert.match(packetHash, /^[0-9a-f]{64}$/);
    assert.deepEqual(misses.map((r) => [r.packet_hash, r.model, r.second_level, r.answer_kind, r.top_keys, r.errors_total, r.file, r.content_hash]), [
      [packetHash, PRIMARY, false, 'json', { passed: 'string' }, 4, FILE, again.contentHash],
      [packetHash, PRIMARY, false, 'json', { passed: 'string' }, 4, FILE, again.contentHash],
    ]);
    assert.equal(misses.every((r) => !Object.hasOwn(r, 'answer_tail')), true);
    const [fb] = w.events('review.schema_fallback');
    const { mac: _mac, ts: _ts, ...body } = fb;
    assert.deepEqual(body, {
      run: w.runId,
      event: 'review.schema_fallback',
      lens: 'quick',
      role: 'reviewer',
      level: 'L2',
      packet_hash: packetHash,
      from_provider: 'anthropic',
      from_model: PRIMARY,
      to: 'review.second_levels.L2',
      to_provider: 'xai',
      to_model: SECOND,
      attempts: 2,
      block: 'B11',
      file: FILE,
      content_hash: again.contentHash,
    });
    assert.equal([...misses, fb].every((r) => verifyRow(r, w.key).ok), true);
    assert.deepEqual(w.events('review.approved').map((r) => [r.file, r.content_hash]), [[FILE, again.contentHash]]);
  });

  test('the second level fails too ⇒ unavailable as before; a third try on the packet never spawns it again', async () => {
    const w = await wired({ review: SECOND_LEVEL });
    w.script.invalid.add(SECOND);
    const results = [];
    for (let i = 0; i < 3; i += 1) results.push((await w.round()).result);
    assert.deepEqual(results.map((r) => [r.status, r.reason, r.approved]), [
      ['unavailable', 'schema', false],
      ['unavailable', 'schema', false],
      ['unavailable', 'schema', false],
    ]);
    assert.deepEqual(w.spawns.map(([, model]) => model), [PRIMARY, PRIMARY, SECOND, PRIMARY]);
    assert.deepEqual(w.events('review.schema_invalid').map((r) => [r.model, r.second_level]), [
      [PRIMARY, false],
      [PRIMARY, false],
      [SECOND, true],
      [PRIMARY, false],
    ]);
    assert.equal(w.events('review.schema_fallback').length, 1);
    assert.equal(w.events('review.approved').length, 0);
  });

  test('no second level configured ⇒ nothing changes but the logged answer: one session per try, no review.schema_fallback', async () => {
    const w = await wired();
    for (let i = 0; i < 3; i += 1) assert.equal((await w.round()).result.reason, 'schema');
    assert.deepEqual(w.spawns.map(([, model]) => model), [PRIMARY, PRIMARY, PRIMARY]);
    assert.equal(w.events('review.schema_invalid').length, 3);
    assert.equal(w.events('review.schema_fallback').length, 0);
  });

  test('budget.usd reached: the second-level session is refused by the budget gate ⇒ unavailable: budget, and never tried again', async () => {
    const w = await wired({ review: SECOND_LEVEL });
    w.script.budget.add(SECOND);
    await w.round();
    const second = (await w.round()).result;
    assert.deepEqual([second.status, second.reason, second.approved], ['unavailable', 'budget', false]);
    await w.round();
    assert.deepEqual(w.spawns.map(([, model]) => model), [PRIMARY, PRIMARY, SECOND, PRIMARY]);
    assert.equal(w.events('review.schema_fallback').length, 1);
  });

  test('a Codex second level is refused for a reviewer (closed-book): the row says so and nothing is spawned', async () => {
    const w = await wired({ review: { second_levels: { L2: { provider: 'openai', model: 'gpt-6-sol' } } } });
    await w.round();
    assert.equal((await w.round()).result.reason, 'schema');
    assert.deepEqual(w.spawns.map(([, model]) => model), [PRIMARY, PRIMARY]);
    assert.deepEqual(w.events('review.schema_fallback').map((r) => [r.to_provider, r.to_model, r.refused]), [[null, null, 'closed-book']]);
  });

  test('a miss on ANOTHER packet of the same content never counts: this packet\'s first miss tries nothing', async () => {
    const w = await wired({ review: SECOND_LEVEL });
    const t = enqueue({ repoRoot: w.repo, run: w.runId, block: 'B11', file: FILE });
    w.rows.push(signRow({ run: w.runId, event: 'review.schema_invalid', block: 'B11', file: FILE, content_hash: t.content_hash, packet_hash: 'f'.repeat(64), level: 'L2', role: 'reviewer', second_level: false }, w.key));
    const r = (await w.round()).result;
    assert.deepEqual([r.status, r.reason], ['unavailable', 'schema']);
    assert.deepEqual(w.spawns.map(([, model]) => model), [PRIMARY]);
    assert.equal(w.events('review.schema_fallback').length, 0);
  });

  test('another level\'s mark on the same packet never blocks this level\'s try', async () => {
    const w = await wired({ review: SECOND_LEVEL });
    await w.round();
    const [first] = w.events('review.schema_invalid');
    w.rows.push(signRow({ run: w.runId, event: 'review.schema_fallback', block: 'B11', file: FILE, content_hash: first.content_hash, packet_hash: first.packet_hash, level: 'L3', role: 'judge', from_model: PRIMARY, to: 'review.second_levels.L3', attempts: 2 }, w.key));
    const r = (await w.round()).result;
    assert.deepEqual([r.status, r.approved], ['reviewed', true]);
    assert.deepEqual(w.spawns.map(([, model]) => model), [PRIMARY, PRIMARY, SECOND]);
    assert.deepEqual(w.events('review.schema_fallback').map((row) => [row.level, row.role]), [['L3', 'judge'], ['L2', 'reviewer']]);
  });

  test('a ledger read that throws during the round disables the try: unavailable: schema, never a crash', async () => {
    const w = await wired({ review: SECOND_LEVEL });
    await w.round();
    w.script.failHistory = true;
    const r = (await w.round()).result;
    assert.deepEqual([r.status, r.reason, r.approved], ['unavailable', 'schema', false]);
    assert.deepEqual(w.spawns.map(([, model]) => model), [PRIMARY, PRIMARY]);
    assert.equal(w.events('review.schema_fallback').length, 0);
    assert.equal(w.events('review.schema_invalid').length, 2);
  });

  test('a second-level spawn that throws ⇒ unavailable: exit after its mark row; never tried again', async () => {
    const w = await wired({ review: SECOND_LEVEL });
    w.script.throws.add(SECOND);
    await w.round();
    const r = (await w.round()).result;
    assert.deepEqual([r.status, r.reason, r.approved], ['unavailable', 'exit', false]);
    await w.round();
    assert.deepEqual(w.spawns.map(([, model]) => model), [PRIMARY, PRIMARY, SECOND, PRIMARY]);
    assert.equal(w.events('review.schema_fallback').length, 1);
  });

  test('a recheck round: its packet\'s second miss is tried on the second level, which resolves the finding ⇒ approved', async () => {
    const finding = { id: 'F1', file: FILE, line_start: 1, line_end: 1, severity: 'warning', category: 'correctness', claim: 'claim F1', evidence: 'e', fix: 'f' };
    const w = await wired({ review: SECOND_LEVEL, firstFindings: [finding] });
    w.script.invalid.clear(); // round 1 is a valid review with F1 open
    const r1 = (await w.round()).result;
    assert.deepEqual([r1.status, r1.approved, r1.findings.map((/** @type {any} */ f) => f.id)], ['reviewed', false, ['F1']]);
    w.script.invalid.add(PRIMARY);
    const miss = (await w.round({ edit: true })).result;
    assert.deepEqual([miss.status, miss.reason, miss.kind], ['unavailable', 'schema', 'recheck']);
    const fixed = (await w.round()).result;
    assert.deepEqual([fixed.status, fixed.approved, fixed.kind], ['reviewed', true, 'recheck']);
    assert.deepEqual(w.spawns, [['quick', PRIMARY], ['recheck', PRIMARY], ['recheck', PRIMARY], ['recheck', SECOND]]);
    assert.deepEqual(w.events('review.schema_fallback').map((r) => [r.lens, r.from_model, r.to_model, r.attempts]), [['recheck', PRIMARY, SECOND, 2]]);
    // the worker's writeRow stamps the recheck ticket's content hash (the fixed content's) on the
    // misses and the mark, and the history matched them by that same hash and the same packet
    const misses = w.events('review.schema_invalid');
    const [fb] = w.events('review.schema_fallback');
    assert.deepEqual(misses.map((r) => [r.lens, r.content_hash === fb.content_hash, r.packet_hash === fb.packet_hash, r.run, r.file]), [
      ['recheck', true, true, w.runId, FILE],
      ['recheck', true, true, w.runId, FILE],
    ]);
    assert.equal(w.events('review.approved')[0].content_hash, fb.content_hash);
    // the round's tokens are both sessions' (each stub session reports 900 in, 300 out)
    assert.deepEqual(w.events('review.round').map((r) => [r.kind, r.tokens_in, r.tokens_out]), [
      ['full', null, null],
      ['recheck', 1800, 600],
    ]);
  });
});
