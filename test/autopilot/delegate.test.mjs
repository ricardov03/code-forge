// B46 autopilot delegate: the closed-book `delegate` role (never Codex, opt-in or not), askDelegate
// (grant first, one session, one signed autopilot.decision row, the acted / to-owner rules), the
// redaction of the question, the report bucket and the `autopilot ask` exit codes. Sessions run
// against the fake CLIs (`test/fixtures/bin/`), the clock is fake. `../session/helpers.mjs` is
// imported FIRST: it points HOME and the cwd at one per-file temp parent, removed in `after()`.
import { BINS, fakeDeps, readRecords } from '../session/helpers.mjs';
import assert from 'node:assert/strict';
import { mkdirSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { test } from 'node:test';

const { askDelegate, buildDelegatePrompt, DELEGATE_MAX_PROMPT_TOKENS, DELEGATE_SCHEMA } = await import('../../src/autopilot/delegate.mjs');
const { startGrant, stopGrant } = await import('../../src/autopilot/grant.mjs');
const { runAutopilot } = await import('../../src/cli/autopilot.mjs');
const { closedBookRefused, NO_TOOL_ROLES } = await import('../../src/config/closed-book.mjs');
const { buildReport } = await import('../../src/ledger/report.mjs');
const { ROLES, SessionError, spawnSession } = await import('../../src/session/spawn.mjs');
const { readRun, saveRun, startRun } = await import('../../src/state/run.mjs');
const { loadKey, verifyRow } = await import('../../src/state/signer.mjs');
const { clearSecrets, registerSecret } = await import('../../src/util/redact.mjs');

const T0 = new Date('2026-10-06T20:00:00.000Z');
/** @param {number} minutes */
const at = (minutes) => new Date(T0.getTime() + minutes * 60000);
const UNTIL = '2026-10-06T22:00:00Z'; // T0 + 2 h
const REFUSAL = 'codex cannot run closed-book yet: it always has a shell; use anthropic or xai for reviewer, judge, S2 and plan author';
const ANSWER = { decision: 'waive', within_scope: true, confidence: 0.82, reason: 'style only; no behaviour change', escalate: false };
/** Each level its own model, so a session's model names the level it ran at. */
const LEVELS = {
  L0: { provider: 'anthropic', model: 'claude-haiku-4-5-20251001' },
  L1: { provider: 'anthropic', model: 'claude-sonnet-5' },
  L2: { provider: 'anthropic', model: 'claude-opus-5-5' },
  L3: { provider: 'anthropic', model: 'claude-fable-5-1' },
};

/**
 * A run (its config snapshot = `cfg`) with a ledger sink; optionally an active grant.
 * @param {string} runId @param {{cfg?: Record<string, any>, allow?: string, delegate?: string, snapshot?: boolean}} [o]
 */
async function newRun(runId, { cfg = {}, allow = 'waive:nit,waive:warning', delegate = 'L2', snapshot = true } = {}) {
  /** @type {Record<string, any>[]} */
  const rows = [];
  const writeRow = async (/** @type {Record<string, any>} */ row) => void rows.push(row);
  const workspace = path.join(process.cwd(), `ws-${runId}`);
  mkdirSync(workspace, { recursive: true });
  const config = { provider: 'anthropic', levels: LEVELS, ...cfg };
  await startRun({ workspace, project: 'proj', runId, writeRow, now: T0, ...(snapshot ? { config } : {}) });
  if (allow !== '') await startGrant({ runId, input: { until: UNTIL, delegate, allow }, now: T0, writeRow, grantId: `ap-${runId}` });
  rows.length = 0;
  return { rows, writeRow };
}

/** @param {Record<string, string>} env */
function fakes(env = {}) {
  const f = fakeDeps(env);
  return { ...f, sessions: () => readRecords(f.records).length };
}

/** @param {Record<string, any>[]} rows */
const decisions = (rows) => rows.filter((r) => r.event === 'autopilot.decision');

const ASK = { scope: 'waive:nit', question: 'Waive the nit "rename tmp to scratch" in src/a.mjs?', options: ['waive', 'fix'] };

test('the delegate role: in ROLES and NO_TOOL_ROLES; Codex refused for it even with allow_open_book_codex (reviewer is opened)', () => {
  assert.deepEqual([...ROLES], ['coder', 'reviewer', 'judge', 's2', 'author', 'facts', 'delegate']);
  assert.deepEqual([...NO_TOOL_ROLES], ['reviewer', 'judge', 's2', 'author', 'delegate']);
  assert.deepEqual(
    [closedBookRefused('openai', 'delegate', true), closedBookRefused('openai', 'delegate', false), closedBookRefused('openai', 'reviewer', true), closedBookRefused('anthropic', 'delegate', false)],
    [true, true, false, false],
  );
});

test('no grant → refused before any session: to_owner, reason no-grant, 0 sessions, 0 rows', async () => {
  const { rows, writeRow } = await newRun('d-none', { allow: '' });
  const f = fakes({ FAKE_ANSWER: JSON.stringify(ANSWER) });
  const res = await askDelegate({ runId: 'd-none', ...ASK }, { ...f.deps, writeRow, now: () => at(10) });
  assert.deepEqual(res, { answered: false, acted: false, to_owner: true, reason: 'no-grant', grant_id: null, answer: null, row: null });
  assert.equal(f.sessions(), 0);
  assert.equal(rows.length, 0);
});

test('a scope outside the allow list (not-allowed), on the fixed deny list (denied) or after stop (stopped) → refused, 0 sessions', async () => {
  const { rows, writeRow } = await newRun('d-scope');
  const f = fakes({ FAKE_ANSWER: JSON.stringify(ANSWER) });
  const deps = { ...f.deps, writeRow, now: () => at(10) };
  const out = [];
  out.push((await askDelegate({ runId: 'd-scope', ...ASK, scope: 'round:extra' }, deps)).reason);
  out.push((await askDelegate({ runId: 'd-scope', ...ASK, scope: 'waive:critical' }, deps)).reason);
  await stopGrant({ runId: 'd-scope', now: at(11), writeRow });
  out.push((await askDelegate({ runId: 'd-scope', ...ASK }, { ...deps, now: () => at(12) })).reason);
  assert.deepEqual(out, ['not-allowed', 'denied', 'stopped']);
  assert.equal(f.sessions(), 0);
  assert.deepEqual(rows.map((r) => r.event), ['autopilot.stop']);
  await assert.rejects(askDelegate({ runId: 'd-scope', ...ASK, scope: 'waive:all' }, deps), { name: 'StateError', code: 'unknown-scope' });
});

test('an in-scope confident answer acts: one L2 delegate session (closed-book, empty cwd), ONE signed decision row with the literal fields', async () => {
  const { rows, writeRow } = await newRun('d-ok');
  const f = fakes({ FAKE_ANSWER: JSON.stringify(ANSWER) });
  const res = await askDelegate({ runId: 'd-ok', ...ASK, context: 'finding: nit, src/a.mjs:12' }, { ...f.deps, writeRow, now: () => at(10) });
  assert.deepEqual([res.answered, res.acted, res.to_owner, res.reason, res.grant_id], [true, true, false, null, 'ap-d-ok']);
  assert.deepEqual(res.answer, ANSWER);

  const records = readRecords(f.records);
  assert.equal(records.length, 1);
  assert.equal(records[0].name, 'claude');
  assert.deepEqual(records[0].argv.slice(records[0].argv.indexOf('--tools'), records[0].argv.indexOf('--tools') + 2), ['--tools', '']);
  assert.equal(path.basename(records[0].cwd), 'cwd'); // a fresh empty dir inside the session dir
  const packet = Buffer.from(records[0].stdin_b64, 'base64').toString('utf8');
  assert.equal(packet.includes('## Scope asked\nwaive:nit: waive a nit finding\n'), true);
  assert.equal(packet.includes('- waive:warning: waive a warning finding\n'), true);
  assert.equal(packet.includes('- pr:merge: merge a pull request\n'), true);
  assert.equal(packet.includes('## Options\n- waive\n- fix\n'), true);
  assert.equal(packet.includes('## Context\nfinding: nit, src/a.mjs:12\n'), true);

  const sessions = rows.filter((r) => r.event === 'session');
  assert.deepEqual(sessions.map((r) => [r.role, r.level, r.provider, r.model, r.status, r.grant_id]), [['delegate', 'L2', 'anthropic', 'claude-opus-5-5', 'ok', 'ap-d-ok']]);
  const [row] = decisions(rows);
  assert.equal(decisions(rows).length, 1);
  const { mac, decision_id, ...fields } = row;
  assert.match(decision_id, /^ad-[0-9a-f]{12}$/); // B47: a stable id the delegated action names
  assert.deepEqual(fields, {
    run: 'd-ok',
    event: 'autopilot.decision',
    grant_id: 'ap-d-ok',
    scope: 'waive:nit',
    subject: null,
    question: ASK.question,
    options: ['waive', 'fix'],
    decision: 'waive',
    within_scope: true,
    confidence: 0.82,
    reason: 'style only; no behaviour change',
    escalate: false,
    acted: true,
    to_owner: false,
    owner_reason: null,
    failure: null,
    level: 'L2',
    provider: 'anthropic',
    model: 'claude-opus-5-5',
    ts: at(10).toISOString(),
  });
  assert.deepEqual(verifyRow(row, await loadKey('d-ok')), { ok: true });
  assert.deepEqual(res.row, row);
});

test('to the owner, never acted: low confidence, escalate, outside scope, a decision not in the options, the min_confidence of the snapshot', async () => {
  const cases = [
    ['d-low', { ...ANSWER, confidence: 0.69 }, {}, 'low-confidence'],
    ['d-esc', { ...ANSWER, escalate: true }, {}, 'escalated'],
    ['d-out', { ...ANSWER, within_scope: false }, {}, 'outside-scope'],
    ['d-opt', { ...ANSWER, decision: 'defer' }, {}, 'not-an-option'],
    ['d-min', ANSWER, { autopilot: { min_confidence: 0.9 } }, 'low-confidence'],
    ['d-edge-', { ...ANSWER, confidence: 0.6999 }, {}, 'low-confidence'],
  ];
  const got = [];
  for (const [runId, answer, cfg, _why] of cases) {
    const { rows, writeRow } = await newRun(/** @type {string} */ (runId), { cfg: /** @type {any} */ (cfg) });
    const f = fakes({ FAKE_ANSWER: JSON.stringify(answer) });
    const res = await askDelegate({ runId: /** @type {string} */ (runId), ...ASK }, { ...f.deps, writeRow, now: () => at(10) });
    const rowsOf = decisions(rows);
    assert.equal(rowsOf.length, 1, String(runId));
    assert.deepEqual(verifyRow(rowsOf[0], await loadKey(/** @type {string} */ (runId))), { ok: true });
    got.push([runId, res.answered, res.acted, res.to_owner, res.reason, rowsOf[0].acted, rowsOf[0].to_owner, rowsOf[0].owner_reason, f.sessions()]);
  }
  assert.deepEqual(
    got,
    cases.map(([runId, , , why]) => [runId, true, false, true, why, false, true, why, 1]),
  );
});

test('a session failure (exit 1, or an answer off the schema) → to the owner, the row still written with the failure', async () => {
  const one = await newRun('d-fail');
  const f1 = fakes({ FAKE_ANSWER: JSON.stringify(ANSWER), FAKE_EXIT: '1' });
  const r1 = await askDelegate({ runId: 'd-fail', ...ASK }, { ...f1.deps, writeRow: one.writeRow, now: () => at(10) });
  const two = await newRun('d-bad');
  const f2 = fakes(); // the default fake answer is an S2 answer: not the delegate schema
  const r2 = await askDelegate({ runId: 'd-bad', ...ASK }, { ...f2.deps, writeRow: two.writeRow, now: () => at(10) });
  assert.deepEqual([r1.answered, r1.acted, r1.to_owner, r1.reason], [false, false, true, 'session failed (exit 1)']);
  assert.deepEqual([r2.answered, r2.acted, r2.to_owner, r2.reason], [false, false, true, 'session invalid-output (answer does not match the schema)']);
  for (const [runId, rows] of /** @type {const} */ ([['d-fail', one.rows], ['d-bad', two.rows]])) {
    const d = decisions(rows);
    assert.equal(d.length, 1);
    assert.deepEqual([d[0].decision, d[0].confidence, d[0].acted, d[0].to_owner, d[0].owner_reason, d[0].provider], [null, null, false, true, 'session-failed', 'anthropic']);
    assert.deepEqual(verifyRow(d[0], await loadKey(runId)), { ok: true });
  }
  assert.deepEqual([decisions(one.rows)[0].failure, decisions(two.rows)[0].failure], ['session failed (exit 1)', 'session invalid-output (answer does not match the schema)']);
});

test('Codex delegate refused even with review.allow_open_book_codex: spawnSession throws closed-book; askDelegate → to the owner, 0 sessions, row says why', async () => {
  const openai = { provider: 'openai', model: 'gpt-6-sol' };
  const cfg = { provider: 'openai', levels: { L0: openai, L1: openai, L2: openai, L3: openai }, review: { allow_open_book_codex: true } };
  const { rows, writeRow } = await newRun('d-codex', { cfg });
  const f = fakes({ FAKE_ANSWER: JSON.stringify(ANSWER) });
  const packet = path.join(process.cwd(), 'codex-packet.md');
  writeFileSync(packet, 'q');
  await assert.rejects(spawnSession({ cfg, level: 'L2', role: 'delegate', promptPath: packet, schema: DELEGATE_SCHEMA }, f.deps), (err) => err instanceof SessionError && err.code === 'closed-book' && err.message === REFUSAL);
  const res = await askDelegate({ runId: 'd-codex', ...ASK }, { ...f.deps, writeRow, now: () => at(10) });
  assert.deepEqual([res.answered, res.acted, res.to_owner, res.reason], [false, false, true, `closed-book: ${REFUSAL}`]);
  assert.equal(f.sessions(), 0);
  const d = decisions(rows);
  assert.deepEqual([d.length, d[0].failure, d[0].provider, d[0].level], [1, `closed-book: ${REFUSAL}`, null, 'L2']);
  assert.equal(rows.filter((r) => r.event === 'session').length, 0);
});

test('redaction: a secret-shaped token and a registered secret in the question, options and context appear 0 times in the packet and the row', async () => {
  const shaped = 'sk-ant-FAKE0123456789abcdefB46';
  const registered = 'FAKE-registered-secret-b46';
  registerSecret(registered);
  try {
    const { rows, writeRow } = await newRun('d-red');
    const f = fakes({ FAKE_ANSWER: JSON.stringify(ANSWER) });
    const question = `Waive the nit? The log shows ${shaped} and ${registered}.`;
    await askDelegate({ runId: 'd-red', scope: 'waive:nit', question, options: ['waive', `fix ${shaped}`, `defer ${registered}`], context: `env: ${shaped} ${registered}` }, { ...f.deps, writeRow, now: () => at(10) });
    const packet = Buffer.from(readRecords(f.records)[0].stdin_b64, 'base64').toString('utf8');
    const rowText = JSON.stringify(decisions(rows));
    assert.deepEqual([packet.split(shaped).length - 1, packet.split(registered).length - 1, rowText.split(shaped).length - 1, rowText.split(registered).length - 1], [0, 0, 0, 0]);
    assert.equal(decisions(rows)[0].question, 'Waive the nit? The log shows [REDACTED] and [REDACTED].');
    assert.deepEqual(decisions(rows)[0].options, ['waive', 'fix [REDACTED]', 'defer [REDACTED]']);
  } finally {
    clearSecrets();
  }
});

test('the grant is asked again after the answer: a window that closed during the session → to the owner (grant-expired), one expire row', async () => {
  const { rows } = await newRun('d-late');
  const f = fakes({ FAKE_ANSWER: JSON.stringify(ANSWER) });
  let answered = false; // the clock jumps past `until` (T0 + 120 min) once the session row is in
  const writeRow = async (/** @type {Record<string, any>} */ row) => {
    rows.push(row);
    if (row.event === 'session') answered = true;
  };
  const res = await askDelegate({ runId: 'd-late', ...ASK }, { ...f.deps, writeRow, now: () => (answered ? at(121) : at(110)) });
  assert.deepEqual([res.answered, res.acted, res.to_owner, res.reason], [true, false, true, 'grant-expired']);
  assert.equal(f.sessions(), 1);
  assert.deepEqual(rows.filter((r) => r.event.startsWith('autopilot.')).map((r) => r.event), ['autopilot.expire', 'autopilot.decision']);
  const [d] = decisions(rows);
  assert.deepEqual([d.acted, d.to_owner, d.owner_reason, d.decision], [false, true, 'grant-expired', 'waive']);
});

test('confidence exactly at autopilot.min_confidence (0.7) acts — the bound is inclusive', async () => {
  const { rows, writeRow } = await newRun('d-edge');
  const f = fakes({ FAKE_ANSWER: JSON.stringify({ ...ANSWER, confidence: 0.7 }) });
  const res = await askDelegate({ runId: 'd-edge', ...ASK }, { ...f.deps, writeRow, now: () => at(10) });
  assert.deepEqual([res.acted, res.to_owner, decisions(rows).length, decisions(rows)[0].owner_reason], [true, false, 1, null]);
});

test('no options → answered for the owner, never acted (no-options); options collapsing to < 2 distinct (after scrubbing) are usage, 0 sessions', async () => {
  const { rows, writeRow } = await newRun('d-open');
  const f = fakes({ FAKE_ANSWER: JSON.stringify(ANSWER) });
  const deps = { ...f.deps, writeRow, now: () => at(10) };
  const open = await askDelegate({ runId: 'd-open', scope: 'waive:nit', question: 'What should happen to the nit?' }, deps);
  const empty = await askDelegate({ runId: 'd-open', scope: 'waive:nit', question: 'And now?', options: [] }, deps);
  assert.deepEqual([open.answered, open.acted, open.to_owner, open.reason], [true, false, true, 'no-options']);
  assert.deepEqual([empty.acted, empty.reason], [false, 'no-options']);
  assert.deepEqual(decisions(rows).map((r) => [r.acted, r.owner_reason, r.options]), [[false, 'no-options', []], [false, 'no-options', []]]);
  assert.equal(f.sessions(), 2);
  const usage = { name: 'SessionError', code: 'usage', message: 'delegate: give at least 2 distinct options' };
  await assert.rejects(askDelegate({ runId: 'd-open', ...ASK, options: ['waive', ' waive '] }, deps), usage);
  await assert.rejects(askDelegate({ runId: 'd-open', ...ASK, options: ['use sk-ant-FAKE0123456789aaaa', 'use sk-ant-FAKE0123456789bbbb'] }, deps), usage);
  assert.equal(f.sessions(), 2);
});

test('the grant\'s level picks the model: an L3 grant runs the L3 model (claude-fable-5-1), an L2 grant the L2 one', async () => {
  const got = [];
  for (const level of ['L2', 'L3']) {
    const runId = `d-lv-${level.toLowerCase()}`;
    const { rows, writeRow } = await newRun(runId, { delegate: level });
    const f = fakes({ FAKE_ANSWER: JSON.stringify(ANSWER) });
    await askDelegate({ runId, ...ASK }, { ...f.deps, writeRow, now: () => at(10) });
    const [rec] = readRecords(f.records);
    const [d] = decisions(rows);
    got.push([rec.argv[rec.argv.indexOf('--model') + 1], d.level, d.model]);
  }
  assert.deepEqual(got, [['claude-opus-5-5', 'L2', 'claude-opus-5-5'], ['claude-fable-5-1', 'L3', 'claude-fable-5-1']]);
});

test('an error before the row that is not a spawner refusal → still ONE row, failure internal-error (no text), to the owner; a row that cannot be written throws', async () => {
  const one = await newRun('d-int');
  const f = fakes({ FAKE_ANSWER: JSON.stringify(ANSWER) });
  const boom = async () => {
    throw new Error('boom /private/FAKE-path');
  };
  const r1 = await askDelegate({ runId: 'd-int', ...ASK }, { ...f.deps, exec: /** @type {any} */ (boom), writeRow: one.writeRow, now: () => at(10) });
  // the packet directory cannot be made: tmp.root is a regular file
  const blocker = path.join(process.cwd(), 'not-a-dir');
  writeFileSync(blocker, 'x');
  const two = await newRun('d-int2', { cfg: { tmp: { root: blocker } } });
  const r2 = await askDelegate({ runId: 'd-int2', ...ASK }, { ...f.deps, writeRow: two.writeRow, now: () => at(10) });
  for (const [r, rows] of /** @type {const} */ ([[r1, one.rows], [r2, two.rows]])) {
    assert.deepEqual([r.answered, r.acted, r.to_owner, r.reason], [false, false, true, 'internal-error']);
    const d = decisions(rows);
    assert.deepEqual([d.length, d[0].failure, d[0].owner_reason, d[0].acted], [1, 'internal-error', 'session-failed', false]);
    assert.equal(JSON.stringify(d).split('boom').length - 1, 0);
  }
  assert.equal(f.sessions(), 0);
  await newRun('d-int3');
  const failing = async (/** @type {Record<string, any>} */ row) => {
    if (row.event === 'autopilot.decision') throw new Error('disk full');
  };
  await assert.rejects(askDelegate({ runId: 'd-int3', ...ASK }, { ...fakes({ FAKE_ANSWER: JSON.stringify(ANSWER) }).deps, writeRow: failing, now: () => at(10) }), { message: 'disk full' });
});

test('config: no snapshot and no fallback → no-config; a snapshot whose hash does not match → config-snapshot; both before any session', async () => {
  const { rows, writeRow } = await newRun('d-nocfg', { snapshot: false });
  const f = fakes({ FAKE_ANSWER: JSON.stringify(ANSWER) });
  const deps = { ...f.deps, writeRow, now: () => at(10) };
  await assert.rejects(askDelegate({ runId: 'd-nocfg', ...ASK }, deps), { name: 'StateError', code: 'no-config' });
  await newRun('d-badsnap');
  const record = await readRun('d-badsnap');
  record.config.snapshots[record.config.hash].provider = 'xai'; // tampered: no longer matches its hash
  await saveRun(record);
  await assert.rejects(askDelegate({ runId: 'd-badsnap', ...ASK }, deps), { name: 'StateError', code: 'config-snapshot' });
  assert.deepEqual([f.sessions(), decisions(rows).length], [0, 0]);
});

test('the packet is capped at 4000 estimated tokens: a huge context is cut on a UTF-8 boundary with a note', () => {
  const prompt = buildDelegatePrompt({ scope: 'waive:nit', grantScopes: ['waive:nit'], question: 'q?', options: [], context: 'é'.repeat(20000) });
  assert.equal(Buffer.byteLength(prompt) <= DELEGATE_MAX_PROMPT_TOKENS * 4, true);
  assert.equal(prompt.endsWith('\n[context cut: 40000 bytes given]\n'), true);
  assert.equal(prompt.includes('�'), false);
  assert.throws(() => buildDelegatePrompt({ scope: 'waive:nit', grantScopes: ['waive:nit'], question: 'x'.repeat(20000) }), { code: 'delegate-too-large' });
});

test('report: a delegate session spends into the s1s2 bucket', () => {
  const [entry] = buildReport([{ event: 'session', run: 'r', block: 'B1', role: 'delegate', usd: 0.25 }]).sections.cost_per_block;
  assert.deepEqual([entry.s1s2Usd, entry.otherUsd, entry.totalUsd], [0.25, 0, 0.25]);
});

test('autopilot ask: exit 0 acted, 3 to the owner (1 session, 1 row each), 1 refused (no session), 2 usage; --json prints the decision', async () => {
  /** @param {string[]} args @param {Record<string, string>} env @param {(row: any) => Promise<void>} writeRow @param {string} [sub] */
  async function cli(args, env, writeRow, sub = 'ask') {
    let out = '';
    let err = '';
    const f = fakes(env);
    const code = await runAutopilot([sub, ...args], {
      stdout: { write: (s) => ((out += s), true) },
      stderr: { write: (s) => ((err += s), true) },
      now: () => at(10),
      writeRow,
      cwd: process.cwd(),
      session: { bins: BINS, env: f.deps.env },
    });
    return { code, out, err, sessions: f.sessions() };
  }
  const a = await newRun('c-act');
  const acted = await cli(['--run', 'c-act', '--scope', 'waive:nit', '--question', 'Waive it?', '--options', 'waive, fix', '--json'], { FAKE_ANSWER: JSON.stringify(ANSWER) }, a.writeRow);
  assert.deepEqual([acted.code, acted.sessions, decisions(a.rows).length], [0, 1, 1]);
  const { decision_id: actedId, ...actedJson } = JSON.parse(acted.out);
  assert.equal(actedId, decisions(a.rows)[0].decision_id);
  assert.deepEqual(actedJson, { run: 'c-act', scope: 'waive:nit', grant_id: 'ap-c-act', answered: true, acted: true, to_owner: false, reason: null, decision: 'waive', confidence: 0.82, within_scope: true, escalate: false, answer_reason: 'style only; no behaviour change' });

  const o = await newRun('c-own');
  const ctx = path.join(process.cwd(), 'ctx.txt');
  writeFileSync(ctx, 'the finding text');
  const owner = await cli(['--run', 'c-own', '--scope', 'waive:nit', '--question', 'Waive it?', '--options', 'waive,fix', '--context-file', ctx], { FAKE_ANSWER: JSON.stringify({ ...ANSWER, escalate: true }) }, o.writeRow);
  assert.deepEqual([owner.code, owner.sessions, decisions(o.rows).length], [3, 1, 1]);
  assert.equal(owner.out, 'autopilot ask run c-own: to the owner (escalated)\n  decision  waive · confidence 0.82 · within scope true · escalate true\n  reason    style only; no behaviour change\n');

  const n = await newRun('c-none', { allow: '' });
  const refused = await cli(['--run', 'c-none', '--scope', 'waive:nit', '--question', 'Waive it?', '--options', 'waive,fix'], {}, n.writeRow);
  assert.deepEqual([refused.code, refused.out, refused.sessions, n.rows.length], [1, 'autopilot ask run c-none: refused (no-grant) — no session; the question goes to the owner\n', 0, 0]);
  const noRun = await cli(['--run', 'c-missing', '--scope', 'waive:nit', '--question', 'q', '--options', 'a,b'], {}, n.writeRow);
  assert.deepEqual([noRun.code, noRun.err], [1, 'autopilot ask: no run record for c-missing\n']);

  // usage (exit 2), each with its exact message and no session
  const missingCtx = path.join(process.cwd(), 'no-such-ctx.txt');
  const usageCases = [
    [['--run', 'c-act', '--question', 'Waive it?'], 'autopilot ask needs --scope <scope>'],
    [['--run', 'c-act', '--scope', '  ', '--question', 'Waive it?'], 'autopilot ask needs --scope <scope>'],
    [['--run', 'c-act', '--scope', 'waive:nit', '--question', '   '], 'autopilot ask needs --question <text>'],
    [['--run', 'c-act', '--scope', 'waive:nit', '--question', 'q', '--options', ','], '--options needs at least 2 distinct options, e.g. --options waive,fix'],
    [['--run', 'c-act', '--scope', 'waive:nit', '--question', 'q', '--options', 'waive,waive'], 'delegate: give at least 2 distinct options'],
    [['--run', 'c-act', '--scope', 'waive:nit', '--question', 'q', '--context-file', missingCtx], `--context-file ${missingCtx} cannot be read`],
    [['--run', 'c-act', '--scope', 'waive:all', '--question', 'q'], 'unknown autopilot scope "waive:all"'],
  ];
  for (const [args, message] of usageCases) {
    const r = await cli(/** @type {string[]} */ (args), {}, a.writeRow);
    assert.deepEqual([r.code, r.err, r.sessions], [2, `autopilot ask: ${message}\n`, 0]);
  }
  assert.equal(decisions(a.rows).length, 1); // still only the first ask's row

  // `start` keeps B45's codes: an unknown --allow scope is usage (2), a second active grant is 1
  const badStart = await cli(['--run', 'c-act', '--until', UNTIL, '--delegate', 'L2', '--allow', 'waive:all', '--yes'], {}, a.writeRow, 'start');
  assert.deepEqual([badStart.code, badStart.err], [2, 'autopilot start: unknown scope "waive:all" in --allow; allowable scopes: waive:warning, waive:nit, round:extra, model:choose\n']);
  const second = await cli(['--run', 'c-act', '--until', UNTIL, '--delegate', 'L2', '--allow', 'waive:nit', '--yes'], {}, a.writeRow, 'start');
  assert.equal(second.code, 1);
});
