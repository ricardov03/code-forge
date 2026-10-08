// helpers FIRST: it pins $HOME under the per-file temp parent before any src module loads.
import { cfgFor } from './helpers.mjs';
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { describe, test } from 'node:test';

const { afterSchemaMiss, diagnoseAnswer, ledgerSchemaHistory, logSecondLevelMiss, packetHash, secondLevelFor, SCHEMA_FAILURES_BEFORE_FALLBACK, DIAGNOSIS_MAX_ERRORS } = await import('../../src/review/schema-fallback.mjs');

const PACKET_LINE = '+export const packetOnlyLine = "do not leak this packet line";';
const PACKET = `# code-forge review packet\n${PACKET_LINE}\n`;
const HASH = createHash('sha256').update(PACKET).digest('hex');
const SESSION = { lens: 'quick', role: 'reviewer', level: 'L2' };
/** The missed session: an answer that failed the schema. */
const MISS = { status: 'invalid-output', reason: 'answer does not match the schema', answer: null, text: '{"passed":"maybe"}', provider: 'anthropic', model: 'claude-opus-5-5', fallback_step: 0 };
const SECOND = { L2: { provider: 'xai', model: 'grok-4.7' } };
/** The diagnosis of MISS's text. */
const MISS_DIAGNOSIS = {
  answer_bytes: 18,
  answer_kind: 'json',
  top_type: 'object',
  top_keys: { passed: 'string' },
  other_keys: 0,
  schema_errors: [
    { path: '', keyword: 'required', params: { missingProperty: 'summary' }, message: "must have required property 'summary'" },
    { path: '', keyword: 'required', params: { missingProperty: 'reviewed_hunks' }, message: "must have required property 'reviewed_hunks'" },
    { path: '', keyword: 'required', params: { missingProperty: 'findings' }, message: "must have required property 'findings'" },
    { path: '/passed', keyword: 'type', params: { type: 'boolean' }, message: 'must be boolean' },
  ],
  errors_total: 4,
};

/**
 * `afterSchemaMiss` on stubs: rows collected, every `spawnAt` config recorded.
 * @param {{failures?: number, fellBack?: boolean, cfg?: Record<string, any>, allowed?: boolean, history?: false, failWrite?: (row: Record<string, any>) => boolean, noWriter?: boolean, spawnThrows?: boolean, res?: Record<string, any>}} [o]
 */
async function miss(o = {}) {
  /** @type {Array<Record<string, any>>} */
  const rows = [];
  /** @type {Array<Record<string, any>>} */
  const spawned = [];
  const out = await afterSchemaMiss({
    res: o.res ?? MISS,
    session: SESSION,
    packetText: PACKET,
    cfg: o.cfg ?? cfgFor({ second_levels: SECOND }),
    allowed: o.allowed ?? true,
    history: o.history === false ? undefined : async () => ({ failures: o.failures ?? 1, fellBack: o.fellBack ?? false }),
    writeRow: o.noWriter
      ? undefined
      : async (row) => {
          if (o.failWrite?.(row)) throw new Error('disk full');
          rows.push(row);
        },
    spawnAt: async (cfg) => {
      spawned.push(cfg);
      if (o.spawnThrows) throw new Error('spawn exploded');
      return { res: { status: 'ok', provider: 'xai', model: 'grok-4.7' }, attempts: 1 };
    },
  });
  return { out, rows, spawned };
}

describe('B55: the second-level try after the second schema miss on one packet', () => {
  test('two misses are needed: the packet\'s first miss is logged (structure only) and nothing is spawned', async () => {
    assert.equal(SCHEMA_FAILURES_BEFORE_FALLBACK, 2);
    const { out, rows, spawned } = await miss({ failures: 0 });
    assert.equal(out, null);
    assert.equal(spawned.length, 0);
    assert.deepEqual(rows, [{ event: 'review.schema_invalid', lens: 'quick', role: 'reviewer', level: 'L2', provider: 'anthropic', model: 'claude-opus-5-5', fallback_step: 0, packet_hash: HASH, second_level: false, ...MISS_DIAGNOSIS }]);
  });

  test('the second miss writes the mark FIRST, then spawns the second level once with levels.L2 swapped', async () => {
    const { out, rows, spawned } = await miss({ failures: 1 });
    assert.deepEqual(rows.map((r) => r.event), ['review.schema_invalid', 'review.schema_fallback']);
    assert.deepEqual(rows[1], {
      event: 'review.schema_fallback',
      lens: 'quick',
      role: 'reviewer',
      level: 'L2',
      packet_hash: HASH,
      from_provider: 'anthropic',
      from_model: 'claude-opus-5-5',
      to: 'review.second_levels.L2',
      to_provider: 'xai',
      to_model: 'grok-4.7',
      attempts: 2,
    });
    assert.equal(spawned.length, 1);
    assert.deepEqual(spawned[0].levels.L2, { provider: 'xai', model: 'grok-4.7', fallback: [] });
    assert.equal(spawned[0].levels.L3.model, 'claude-fable-5-1'); // only the session's level is swapped
    assert.deepEqual(out, { res: { status: 'ok', provider: 'xai', model: 'grok-4.7' }, attempts: 1, to: { level: 'review.second_levels.L2', provider: 'xai', model: 'grok-4.7' } });
  });

  test('a second-level spawn that throws is a session with no result (exit through the guard), after its mark row', async () => {
    const { out, rows, spawned } = await miss({ failures: 1, spawnThrows: true });
    assert.equal(spawned.length, 1);
    assert.deepEqual(rows.map((r) => r.event), ['review.schema_invalid', 'review.schema_fallback']);
    assert.deepEqual(out, { res: null, attempts: 1, to: { level: 'review.second_levels.L2', provider: 'xai', model: 'grok-4.7' } });
  });

  test('at most once per packet: an earlier review.schema_fallback row stops every later try', async () => {
    const { out, rows, spawned } = await miss({ failures: 3, fellBack: true });
    assert.deepEqual([out, spawned.length, rows.map((r) => r.event)], [null, 0, ['review.schema_invalid']]);
  });

  test('no second level for the level, no history, consensus mode, or an unwritable mark ⇒ no try (the miss is still logged)', async () => {
    const none = await miss({ cfg: cfgFor() });
    const l3Only = await miss({ cfg: cfgFor({ second_levels: { L3: { provider: 'xai', model: 'grok-4.7' } } }) });
    const noHistory = await miss({ history: false });
    const consensus = await miss({ allowed: false });
    const markFails = await miss({ failWrite: (row) => row.event === 'review.schema_fallback' });
    for (const r of [none, l3Only, noHistory, consensus, markFails]) {
      assert.deepEqual([r.out, r.spawned.length, r.rows.map((row) => row.event)], [null, 0, ['review.schema_invalid']]);
    }
  });

  test('no ledger writer ⇒ nothing is logged and nothing is tried (no throw)', async () => {
    const { out, rows, spawned } = await miss({ failures: 5, noWriter: true });
    assert.deepEqual([out, rows.length, spawned.length], [null, 0, 0]);
    await logSecondLevelMiss({ res: MISS, session: SESSION, packetText: PACKET, writeRow: undefined });
  });

  test('a history that throws ⇒ no try (fail closed), the miss still logged', async () => {
    /** @type {Array<Record<string, any>>} */
    const rows = [];
    let spawned = 0;
    const out = await afterSchemaMiss({
      res: MISS,
      session: SESSION,
      packetText: PACKET,
      cfg: cfgFor({ second_levels: SECOND }),
      allowed: true,
      history: async () => {
        throw new Error('ledger unreadable');
      },
      writeRow: async (row) => void rows.push(row),
      spawnAt: async () => {
        spawned += 1;
        return { res: null, attempts: 1 };
      },
    });
    assert.deepEqual([out, spawned, rows.map((r) => r.event)], [null, 0, ['review.schema_invalid']]);
  });

  test('a Codex second level is refused for a closed-book role (row says so, nothing spawned) unless review.allow_open_book_codex', async () => {
    const codex = { L2: { provider: 'openai', model: 'gpt-6-sol' } };
    const refused = await miss({ cfg: cfgFor({ second_levels: codex }) });
    assert.equal(refused.out, null);
    assert.equal(refused.spawned.length, 0);
    assert.deepEqual([refused.rows[1].event, refused.rows[1].to_provider, refused.rows[1].to_model, refused.rows[1].refused], ['review.schema_fallback', null, null, 'closed-book']);
    const opened = await miss({ cfg: cfgFor({ second_levels: codex, allow_open_book_codex: true }) });
    assert.equal(opened.spawned.length, 1);
    assert.equal(Object.hasOwn(opened.rows[1], 'refused'), false);
  });

  test('closed-book over the whole ladder: Codex models are dropped, the next runnable one runs; none left ⇒ refused: closed-book', async () => {
    const mixed = await miss({ cfg: cfgFor({ second_levels: { L2: { provider: 'openai', model: 'gpt-6-sol', fallback: [{ provider: 'openai', model: 'gpt-6-luna' }, { provider: 'xai', model: 'grok-4.7' }] } } }) });
    assert.equal(mixed.spawned.length, 1);
    assert.deepEqual(mixed.spawned[0].levels.L2, { provider: 'xai', model: 'grok-4.7', fallback: [] });
    assert.deepEqual([mixed.rows[1].to_provider, mixed.rows[1].to_model, Object.hasOwn(mixed.rows[1], 'refused')], ['xai', 'grok-4.7', false]);
    const allCodex = await miss({ cfg: cfgFor({ second_levels: { L2: { provider: 'openai', model: 'gpt-6-sol', fallback: [{ model: 'gpt-6-luna' }] } } }) });
    assert.deepEqual([allCodex.spawned.length, allCodex.rows[1].refused], [0, 'closed-book']);
  });

  test('the history is asked for this packet at this level and role', async () => {
    /** @type {Array<[string, Record<string, any>]>} */
    const asked = [];
    await afterSchemaMiss({
      res: MISS,
      session: SESSION,
      packetText: PACKET,
      cfg: cfgFor({ second_levels: SECOND }),
      allowed: true,
      history: async (hash, who) => {
        asked.push([hash, who]);
        return { failures: 0, fellBack: false };
      },
      writeRow: async () => {},
      spawnAt: async () => ({ res: null, attempts: 1 }),
    });
    assert.deepEqual(asked, [[HASH, { level: 'L2', role: 'reviewer' }]]);
  });

  test('every model of the second level is the failed one ⇒ the mark says refused: same-model, nothing spawned', async () => {
    const r = await miss({ cfg: cfgFor({ second_levels: { L2: { provider: 'anthropic', model: 'claude-opus-5-5', fallback: [{ model: 'claude-opus-5-5' }] } } }) });
    assert.equal(r.spawned.length, 0);
    assert.deepEqual(r.rows[1], {
      event: 'review.schema_fallback',
      lens: 'quick',
      role: 'reviewer',
      level: 'L2',
      packet_hash: HASH,
      from_provider: 'anthropic',
      from_model: 'claude-opus-5-5',
      to: 'review.second_levels.L2',
      to_provider: null,
      to_model: null,
      attempts: 2,
      refused: 'same-model',
    });
  });

  test('the judge (L3) uses review.second_levels.L3', async () => {
    /** @type {Array<Record<string, any>>} */
    const rows = [];
    /** @type {Array<Record<string, any>>} */
    const spawned = [];
    await afterSchemaMiss({
      res: { ...MISS, model: 'claude-fable-5-1' },
      session: { lens: 'judge', role: 'judge', level: 'L3' },
      packetText: PACKET,
      cfg: cfgFor({ second_levels: { L3: { provider: 'xai', model: 'grok-5' } } }),
      allowed: true,
      history: async () => ({ failures: 1, fellBack: false }),
      writeRow: async (row) => void rows.push(row),
      spawnAt: async (cfg) => {
        spawned.push(cfg);
        return { res: null, attempts: 1 };
      },
    });
    assert.deepEqual([rows[1].to, rows[1].from_model, rows[1].to_model, spawned[0].levels.L3.model, spawned[0].levels.L2.model], ['review.second_levels.L3', 'claude-fable-5-1', 'grok-5', 'grok-5', 'claude-opus-5-5']);
  });
});

describe('secondLevelFor', () => {
  const from = { provider: 'anthropic', model: 'claude-opus-5-5' };
  test('provider from the override, else review.second_provider, else the top-level one', () => {
    const own = secondLevelFor(cfgFor({ second_levels: { L2: { provider: 'xai', model: 'grok-4.7' } } }), 'L2', from);
    assert.deepEqual(own.ok && [own.provider, own.model], ['xai', 'grok-4.7']);
    const viaSecond = secondLevelFor(cfgFor({ second_provider: 'xai', second_levels: { L2: { model: 'grok-4.7' } } }), 'L2', from);
    assert.deepEqual(viaSecond.ok && [viaSecond.provider, viaSecond.model], ['xai', 'grok-4.7']);
    const top = secondLevelFor(cfgFor({ second_levels: { L2: { model: 'claude-sonnet-5' } } }), 'L2', from);
    assert.deepEqual(top.ok && [top.provider, top.model], ['anthropic', 'claude-sonnet-5']);
  });

  test('the ladder: each entry gets its effective provider before it is compared with the failed model', () => {
    // a provider-less entry inherits the level's provider (here the top-level anthropic), so it IS the failed model
    const ladder = [{ model: 'claude-opus-5-5' }, { provider: 'xai', model: 'claude-opus-5-5' }, { model: 'claude-sonnet-5', effort: 'high' }];
    const r = secondLevelFor(cfgFor({ second_levels: { L2: { model: 'claude-haiku-4-5-20251001', fallback: ladder } } }), 'L2', from);
    assert.deepEqual(r.ok && r.cfg.levels.L2, {
      provider: 'anthropic',
      model: 'claude-haiku-4-5-20251001',
      fallback: [
        { provider: 'xai', model: 'claude-opus-5-5' },
        { provider: 'anthropic', model: 'claude-sonnet-5', effort: 'high' },
      ],
    });
  });

  test('the level\'s own model is the failed one ⇒ the next model of its ladder runs (with its effort), the rest is its ladder', () => {
    const cfg = cfgFor({ second_levels: { L2: { provider: 'anthropic', model: 'claude-opus-5-5', effort: 'max', fallback: [{ model: 'claude-sonnet-5', effort: 'high' }, { provider: 'xai', model: 'grok-4.7' }] } } });
    const r = secondLevelFor(cfg, 'L2', from);
    assert.deepEqual(r.ok && [r.provider, r.model, r.cfg.levels.L2], ['anthropic', 'claude-sonnet-5', { provider: 'anthropic', model: 'claude-sonnet-5', effort: 'high', fallback: [{ provider: 'xai', model: 'grok-4.7' }] }]);
  });

  test('none configured or no model ⇒ no-second-level; nothing left but the failed model ⇒ same-model', () => {
    assert.deepEqual(secondLevelFor(cfgFor(), 'L2', from), { ok: false, reason: 'no-second-level' });
    assert.deepEqual(secondLevelFor(cfgFor({ second_levels: null }), 'L2', from), { ok: false, reason: 'no-second-level' });
    assert.deepEqual(secondLevelFor(cfgFor({ second_levels: { L2: { provider: 'xai' } } }), 'L2', from), { ok: false, reason: 'no-second-level' });
    assert.deepEqual(secondLevelFor(cfgFor({ second_levels: { L2: { provider: 'anthropic', model: 'claude-opus-5-5' } } }), 'L2', from), { ok: false, reason: 'same-model' });
  });
});

describe('the logged diagnosis: structure, never the answer text', () => {
  test('a packet line copied into a JSON string never reaches the row', () => {
    const answer = { passed: true, summary: PACKET_LINE, reviewed_hunks: [PACKET_LINE], findings: 'none', [PACKET_LINE]: 1 };
    const d = diagnoseAnswer({ text: JSON.stringify(answer) });
    const row = JSON.stringify(d);
    assert.equal(row.includes('packetOnlyLine'), false);
    assert.equal(row.includes('do not leak'), false);
    assert.deepEqual(d, {
      answer_bytes: Buffer.byteLength(JSON.stringify(answer)),
      answer_kind: 'json',
      top_type: 'object',
      top_keys: { passed: 'boolean', summary: 'string', reviewed_hunks: 'array', findings: 'string' },
      other_keys: 1,
      schema_errors: [
        { path: '', keyword: 'additionalProperties', params: { additionalProperty: '*' }, message: 'must NOT have additional properties' },
        { path: '/findings', keyword: 'type', params: { type: 'array' }, message: 'must be array' },
      ],
      errors_total: 2,
    });
  });

  test('not JSON: the parse error without any quoted input, and its byte offset', () => {
    assert.deepEqual(diagnoseAnswer({ text: `I reviewed it: ${PACKET_LINE}` }), { answer_bytes: Buffer.byteLength(`I reviewed it: ${PACKET_LINE}`), answer_kind: 'not-json', parse_error: 'Unexpected token', parse_offset: null });
    assert.deepEqual(diagnoseAnswer({ text: '{"é": 1,}' }), { answer_bytes: 10, answer_kind: 'not-json', parse_error: 'Expected double-quoted property name in JSON at position 8', parse_offset: 9 });
    assert.deepEqual(diagnoseAnswer({ text: '{"passed":' }), { answer_bytes: 10, answer_kind: 'not-json', parse_error: 'Unexpected end of JSON input', parse_offset: 10 });
  });

  test('empty, a parsed answer object, and the error cap', () => {
    assert.deepEqual(diagnoseAnswer(null), { answer_bytes: 0, answer_kind: 'empty' });
    assert.deepEqual(diagnoseAnswer({ text: '  \n' }), { answer_bytes: 3, answer_kind: 'empty' });
    assert.deepEqual(diagnoseAnswer({ text: '', answer: { passed: 'maybe' } }), { ...MISS_DIAGNOSIS, answer_bytes: 0 });
    const findings = Array.from({ length: 30 }, () => ({}));
    const capped = diagnoseAnswer({ answer: { passed: true, summary: 's', reviewed_hunks: [], findings } });
    assert.equal(capped.schema_errors.length, DIAGNOSIS_MAX_ERRORS);
    assert.equal(capped.errors_total > DIAGNOSIS_MAX_ERRORS, true);
    assert.deepEqual(diagnoseAnswer({ text: '[1]' }), { answer_bytes: 3, answer_kind: 'json', top_type: 'array', top_keys: {}, other_keys: 0, schema_errors: [{ path: '', keyword: 'type', params: { type: 'object' }, message: 'must be object' }], errors_total: 1 });
  });

  test('a second-level miss is logged with second_level: true', async () => {
    /** @type {Array<Record<string, any>>} */
    const rows = [];
    await logSecondLevelMiss({ res: { ...MISS, provider: 'xai', model: 'grok-4.7' }, session: SESSION, packetText: PACKET, writeRow: async (row) => void rows.push(row) });
    assert.deepEqual(rows, [{ event: 'review.schema_invalid', lens: 'quick', role: 'reviewer', level: 'L2', provider: 'xai', model: 'grok-4.7', fallback_step: 0, packet_hash: HASH, second_level: true, ...MISS_DIAGNOSIS }]);
    assert.equal(packetHash(PACKET), HASH);
  });
});

describe('ledgerSchemaHistory', () => {
  const base = { run: 'r1', file: 'src/a.mjs', content_hash: 'h1', packet_hash: HASH, level: 'L2', role: 'reviewer' };
  const L2 = { level: 'L2', role: 'reviewer' };
  const L3 = { level: 'L3', role: 'judge' };
  test('counts only verified rows of this run, file, content hash, packet, level and role; second-level misses are not counted', async () => {
    /** @type {Array<Record<string, any>>} */
    const rows = [
      { event: 'review.schema_invalid', ...base, second_level: false, mac: 'good' },
      { event: 'review.schema_invalid', ...base, second_level: true, mac: 'good' },
      { event: 'review.schema_invalid', ...base, second_level: false, mac: 'bad' },
      { event: 'review.schema_invalid', ...base, second_level: false, mac: 'throws' },
      { event: 'review.schema_invalid', ...base, run: 'r2', second_level: false, mac: 'good' },
      { event: 'review.schema_invalid', ...base, file: 'src/b.mjs', second_level: false, mac: 'good' },
      { event: 'review.schema_invalid', ...base, content_hash: 'h2', second_level: false, mac: 'good' },
      { event: 'review.schema_invalid', ...base, packet_hash: 'other', second_level: false, mac: 'good' },
      { event: 'review.schema_invalid', ...base, ...L3, second_level: false, mac: 'good' },
      { event: 'review.schema_invalid', ...base, role: 'judge', second_level: false, mac: 'good' },
      { event: 'review.unavailable', ...base, reason: 'schema', mac: 'good' },
    ];
    const verify = (/** @type {Record<string, any>} */ r) => {
      if (r.mac === 'throws') throw new Error('bad mac');
      return r.mac === 'good';
    };
    const history = ledgerSchemaHistory({ readRows: async () => rows, verify, runId: 'r1', file: 'src/a.mjs', contentHash: 'h1' });
    assert.deepEqual(await history(HASH, L2), { failures: 1, fellBack: false });
    assert.deepEqual(await history('other', L2), { failures: 1, fellBack: false });
    assert.deepEqual(await history(HASH, L3), { failures: 1, fellBack: false });
    rows.push({ event: 'review.schema_fallback', ...base, mac: 'bad' });
    assert.deepEqual(await history(HASH, L2), { failures: 1, fellBack: false });
    // one level's mark never blocks another level's try
    rows.push({ event: 'review.schema_fallback', ...base, ...L3, mac: 'good' });
    assert.deepEqual(await history(HASH, L2), { failures: 1, fellBack: false });
    assert.deepEqual(await history(HASH, L3), { failures: 1, fellBack: true });
    rows.push({ event: 'review.schema_fallback', ...base, mac: 'good' });
    assert.deepEqual(await history(HASH, L2), { failures: 1, fellBack: true });
    assert.deepEqual(await history('other', L2), { failures: 1, fellBack: false });
  });
});
