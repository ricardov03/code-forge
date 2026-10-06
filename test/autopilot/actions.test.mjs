// B47 delegated actions: `autopilot waive` (severity read from the signed review rows, grant
// first, the delegate's acted decision named by --decision, one tracked gh issue deduped by a
// body marker, no gh ⇒ the waiver still stands), the gate rule for `by: autopilot` waivers, one
// extra fix round past the cap, and the coder level from the recorded lane. Fake gh (an `exec`
// seam), fake clock, in-memory ledger. HOME and the temp parent come from the review helpers
// (imported first), removed in their `after()`.
import { BINS, PARENT, answerFor, cfgFor, freshDir, lines, makeRepo, writeFile } from '../review/helpers.mjs';
import assert from 'node:assert/strict';
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { test } from 'node:test';

const { CANCELLED_COMMENT, chooseCoderLevel, chooseLevel, grantExtraRound, waiveForOwner, waiverMarker } = await import('../../src/autopilot/actions.mjs');
const { findSecret } = await import('../../src/util/gh.mjs');
const { askDelegate } = await import('../../src/autopilot/delegate.mjs');
const { startGrant, stopGrant } = await import('../../src/autopilot/grant.mjs');
const { checkBlockReviews } = await import('../../src/review/gate-check.mjs');
const { converge, extraRoundsFor, newFileState, reopenForExtraRound } = await import('../../src/review/fixloop.mjs');
const { readRun, saveRun, startRun } = await import('../../src/state/run.mjs');
const { loadKey, signRow, verifyRow } = await import('../../src/state/signer.mjs');
const { runAutopilot } = await import('../../src/cli/autopilot.mjs');
const { runBlock } = await import('../../src/cli/block.mjs');
const { appendRow } = await import('../../src/ledger/write.mjs');

const T0 = new Date('2026-10-06T20:00:00.000Z');
/** @param {number} minutes */
const at = (minutes) => new Date(T0.getTime() + minutes * 60000);
const UNTIL = '2026-10-06T22:00:00Z'; // T0 + 2 h
const ALL = 'waive:warning,waive:nit,round:extra,model:choose';
const FILE = 'src/a.mjs';
const REPO = 'acme/widgets';
const ISSUE = 'https://github.com/acme/widgets/issues/7';
const LEVELS = { L0: { provider: 'anthropic', model: 'claude-haiku-4-5-20251001' }, L1: { provider: 'anthropic', model: 'claude-sonnet-5' }, L2: { provider: 'anthropic', model: 'claude-opus-5-5' }, L3: { provider: 'anthropic', model: 'claude-fable-5-1' } };

/**
 * A run with block B1, an in-memory ledger and (optionally) a grant started at T0.
 * @param {string} runId @param {{allow?: string | null, config?: Record<string, any>}} [opts]
 */
async function newRun(runId, opts = {}) {
  /** @type {Record<string, any>[]} */
  const rows = [];
  const writeRow = async (/** @type {Record<string, any>} */ row) => void rows.push(row);
  const workspace = freshDir(`ws-${runId}`);
  await startRun({ workspace, project: 'proj', runId, writeRow, now: T0, ...(opts.config ? { config: opts.config } : {}) });
  const record = await readRun(runId);
  record.blocks = { B1: { block: 'B1', base_sha: 'a'.repeat(40), owned_files: [FILE], level: 'L1', status: 'open' } };
  await saveRun(record);
  rows.length = 0;
  const allow = opts.allow === undefined ? ALL : opts.allow;
  if (allow !== null) await startGrant({ runId, input: { until: UNTIL, delegate: 'L2', allow }, now: T0, writeRow, grantId: `ap-${runId}` });
  const key = await loadKey(runId);
  /** A signed review row, as the worker writes it. @param {Record<string, any>} row */
  const review = (row) => rows.push(signRow({ run: runId, block: 'B1', file: FILE, content_hash: 'h1', ...row }, key));
  let n = 0;
  /**
   * A signed delegate decision row (B46's shape); returns its decision_id. The answer defaults to
   * the action's fixed option word: waive / allow / L1.
   * @param {string} scope @param {Record<string, string>} subject @param {{acted?: boolean, ts?: Date, grantId?: string, sign?: boolean, decision?: string}} [o]
   */
  const decide = (scope, subject, o = {}) => {
    n += 1;
    const id = `ad-${runId}-${n}`;
    const row = { run: runId, event: 'autopilot.decision', decision_id: id, grant_id: o.grantId ?? `ap-${runId}`, scope, subject, decision: o.decision ?? (scope.startsWith('waive:') ? 'waive' : scope === 'round:extra' ? 'allow' : 'L1'), acted: o.acted ?? true, to_owner: !(o.acted ?? true), ts: (o.ts ?? at(5)).toISOString() };
    rows.push(o.sign === false ? row : signRow(row, key));
    return id;
  };
  return { rows, writeRow, workspace, key, review, decide, readRows: async () => rows };
}

/**
 * A fake `gh` that remembers the issues it created (a later search returns them with their
 * bodies); every call recorded (argv, cwd, the body file's text while it exists).
 * @param {{list?: Array<Record<string, any>>, create?: Record<string, any>, auth?: boolean, onCreate?: () => Promise<unknown>}} [o]
 */
function fakeGh(o = {}) {
  /** @type {Array<{argv: string[], cwd: string | undefined, body: string | null}>} */
  const calls = [];
  /** @type {Array<Record<string, any>>} */
  const created = [];
  const ok = (/** @type {string} */ stdout = '') => ({ result: 'ok', code: 0, signal: null, stdout, stderr: '', timedOut: false });
  /** @type {any} the `exec` seam's shape, answers built here */
  const exec = async (/** @type {string[]} */ argv, /** @type {Record<string, any>} */ opts = {}) => {
    const i = argv.indexOf('--body-file');
    calls.push({ argv: [...argv], cwd: opts.cwd, body: i > 0 ? readFileSync(argv[i + 1], 'utf8') : null });
    await new Promise((resolve) => setImmediate(resolve)); // let a concurrent call interleave here
    if (argv[1] === 'auth') return o.auth === false ? { ...ok(), result: 'failed', code: 1, stderr: 'not logged in' } : ok();
    if (argv[1] === 'repo') return ok(JSON.stringify({ nameWithOwner: REPO }));
    if (argv[2] === 'list') return ok(JSON.stringify([...(o.list ?? []), ...created]));
    if (argv[2] === 'create') {
      if (o.onCreate) await o.onCreate();
      if (o.create) return o.create;
      const number = 7 + created.length;
      created.push({ number, url: `https://github.com/acme/widgets/issues/${number}`, state: 'OPEN', body: calls[calls.length - 1].body });
      return ok(`Creating issue\nhttps://github.com/acme/widgets/issues/${number}\n`);
    }
    return ok();
  };
  return { exec, calls, created, deps: { exec, env: { PATH: '/fake' }, onPath: (/** @type {string} */ c) => c === 'gh' } };
}

const WAIVE = { block: 'B1', file: FILE, finding: 'W1', severity: 'warning', reason: 'cosmetic; tracked in an issue' };
const W1 = { block: 'B1', file: FILE, finding: 'W1' };
/** @param {any} res */
const reasonOf = (res) => res.reason;

test('waive a warning on the delegate decision: one signed review.waived row (by autopilot, grant_id, decision_id, severity, issue), one gh issue in the PROJECT repo', async () => {
  const run = await newRun('a-ok');
  run.review({ event: 'review.triage', finding: 'W1', severity: 'warning', verdict: 'fix_now' });
  const decisionId = run.decide('waive:warning', W1);
  const gh = fakeGh();
  const res = await waiveForOwner({ runId: 'a-ok', ...WAIVE, decisionId }, { ...gh.deps, writeRow: run.writeRow, readRows: run.readRows, now: () => at(10) });
  assert.equal(res.ok, true);
  const waived = run.rows.filter((r) => r.event === 'review.waived');
  assert.equal(waived.length, 1);
  const { mac, ...fields } = waived[0];
  assert.deepEqual(fields, { run: 'a-ok', event: 'review.waived', block: 'B1', file: FILE, finding: 'W1', reason: 'cosmetic; tracked in an issue', by: 'autopilot', grant_id: 'ap-a-ok', decision_id: decisionId, severity: 'warning', issue: ISSUE, ts: at(10).toISOString() });
  assert.deepEqual(verifyRow(waived[0], run.key), { ok: true });
  assert.equal(run.rows.filter((r) => r.event === 'autopilot.issue_failed').length, 0);
  // the repo comes from `gh repo view` IN THE WORKSPACE; the exact quoted marker is searched (100 results), then create
  const marker = waiverMarker('a-ok', 'B1', FILE, 'W1');
  assert.deepEqual(gh.calls.map((c) => [c.argv.slice(0, 3).join(' '), c.cwd ?? null]), [['gh auth status', null], ['gh repo view', run.workspace], ['gh issue list', null], ['gh issue create', null]]);
  assert.deepEqual(gh.calls[2].argv.slice(3), ['--repo', REPO, '--state', 'all', '--search', `"${marker}" in:body`, '--json', 'number,url,state,body', '--limit', '100']);
  assert.deepEqual(gh.calls[3].argv.slice(3, 7), ['--repo', REPO, '--title', `code-forge autopilot waived warning W1 in ${FILE} (block B1)`]);
  assert.deepEqual(gh.calls[3].argv.slice(-2), ['--label', 'autopilot-waiver']);
  const body = /** @type {string} */ (gh.calls[3].body);
  assert.equal(body.endsWith(`${marker}\n`), true);
  assert.equal(body.split('| grant | ap-a-ok |').length - 1, 1);
});

test('the flag is never trusted: a finding recorded critical is refused even with --severity warning; no row, no gh call', async () => {
  const run = await newRun('a-crit');
  // a triage row says warning, a later late_finding row for the same id says critical: the worst wins
  run.review({ event: 'review.triage', finding: 'W1', severity: 'warning', verdict: 'fix_now' });
  run.review({ event: 'review.late_finding', finding: 'W1', severity: 'critical', round: 2 });
  run.review({ event: 'review.triage', finding: 'N1', severity: 'nit', verdict: 'nit' });
  const decisionId = run.decide('waive:warning', W1);
  const gh = fakeGh();
  const deps = { ...gh.deps, writeRow: run.writeRow, readRows: run.readRows, now: () => at(10) };
  const before = run.rows.length;
  const req = { runId: 'a-crit', ...WAIVE, decisionId };
  assert.deepEqual(await waiveForOwner(req, deps), { ok: false, reason: 'critical', message: `finding W1 in ${FILE} is recorded as critical; critical findings are never delegated` });
  assert.deepEqual(await waiveForOwner({ ...req, finding: 'X9' }, deps), { ok: false, reason: 'severity-unknown', message: `finding X9 in ${FILE} has no verifiable recorded severity (not-recorded); critical findings are never delegated` });
  assert.deepEqual(await waiveForOwner({ ...req, finding: 'N1' }, deps), { ok: false, reason: 'severity-mismatch', message: `finding N1 in ${FILE} is recorded as nit, not warning` });
  // an unsigned (forged) row for the finding fails closed
  run.rows.push({ run: 'a-crit', block: 'B1', file: FILE, event: 'review.triage', finding: 'F2', severity: 'nit' });
  assert.equal(reasonOf(await waiveForOwner({ ...req, finding: 'F2', severity: 'nit' }, deps)), 'severity-unknown');
  await assert.rejects(waiveForOwner({ ...req, severity: 'critical' }, deps), { code: 'usage', message: '--severity must be warning or nit (critical findings are never delegated)' });
  assert.equal(run.rows.length, before + 1); // only the forged row this test pushed
  assert.equal(gh.calls.length, 0);
});

test('proof is never delegated: refused before anything is read or written', async () => {
  const run = await newRun('a-proof');
  run.review({ event: 'review.triage', finding: 'proof', severity: 'nit', verdict: 'nit' });
  const decisionId = run.decide('waive:nit', { ...W1, finding: 'proof' });
  const gh = fakeGh();
  const before = run.rows.length;
  const res = await waiveForOwner({ runId: 'a-proof', ...WAIVE, finding: 'proof', severity: 'nit', decisionId }, { ...gh.deps, writeRow: run.writeRow, readRows: run.readRows, now: () => at(10) });
  assert.deepEqual(res, { ok: false, reason: 'never-delegated', message: 'proof is never delegated; only the owner can waive it (block waive)' });
  assert.deepEqual([run.rows.length - before, gh.calls.length], [0, 0]);
});

test('outside the window, after a stop, without the scope or a grant: refused, no waiver row, no gh call', async () => {
  const cases = [
    ['a-late', ALL, at(121), 'expired'],
    ['a-stop', ALL, at(30), 'stopped'],
    ['a-scope', 'waive:nit', at(10), 'not-allowed'],
    ['a-none', null, at(10), 'no-grant'],
  ];
  for (const [runId, allow, now, reason] of /** @type {Array<[string, string | null, Date, string]>} */ (cases)) {
    const run = await newRun(runId, { allow });
    run.review({ event: 'review.triage', finding: 'W1', severity: 'warning', verdict: 'fix_now' });
    const decisionId = run.decide('waive:warning', W1);
    if (reason === 'stopped') await stopGrant({ runId, now: at(20), writeRow: run.writeRow });
    const gh = fakeGh();
    const res = await waiveForOwner({ runId, ...WAIVE, decisionId }, { ...gh.deps, writeRow: run.writeRow, readRows: run.readRows, now: () => now });
    assert.deepEqual(res, { ok: false, reason, message: `the grant refuses (${reason}); the question goes to the owner` });
    assert.deepEqual([run.rows.filter((r) => r.event === 'review.waived').length, gh.calls.length], [0, 0]);
    // the expired grant got its one expire row from the check
    assert.equal(run.rows.filter((r) => r.event === 'autopilot.expire').length, reason === 'expired' ? 1 : 0);
  }
});

test('the delegate must have decided: no --decision, another finding / scope / grant, not acted, unsigned or older than 30 min → refused', async () => {
  const run = await newRun('a-dec');
  run.review({ event: 'review.triage', finding: 'W1', severity: 'warning', verdict: 'fix_now' });
  const gh = fakeGh();
  const deps = { ...gh.deps, writeRow: run.writeRow, readRows: run.readRows, now: () => at(40) };
  await assert.rejects(waiveForOwner(/** @type {any} */ ({ runId: 'a-dec', ...WAIVE }), deps), { code: 'usage', message: "autopilot waive needs --decision <id> (the delegate's acted decision from autopilot ask)" });
  const cases = [
    [run.decide('waive:warning', { ...W1, finding: 'W2' }, { ts: at(30) }), 'is about another finding'],
    [run.decide('waive:warning', { ...W1, file: 'src/b.mjs' }, { ts: at(30) }), 'is about another file'],
    [run.decide('waive:nit', W1, { ts: at(30) }), 'is for waive:nit, not waive:warning'],
    [run.decide('waive:warning', W1, { ts: at(30), grantId: 'ap-other' }), 'is under another grant'],
    [run.decide('waive:warning', W1, { ts: at(30), acted: false }), 'did not act (it went to the owner)'],
    [run.decide('waive:warning', W1, { ts: at(30), sign: false }), 'has no valid MAC'],
    [run.decide('waive:warning', W1, { ts: at(9) }), 'is older than 30 min (or not yet made)'],
    [run.decide('waive:warning', W1, { ts: at(30), decision: 'fix' }), 'decided "fix", not "waive"'],
    ['ad-missing', null],
  ];
  for (const [decisionId, why] of cases) {
    const res = await waiveForOwner({ runId: 'a-dec', ...WAIVE, decisionId: /** @type {string} */ (decisionId) }, deps);
    const message = why === null ? `no autopilot.decision row ad-missing in this run; the question goes to the owner (autopilot ask, then --decision <id>)` : `decision ${decisionId} ${why}; the question goes to the owner (autopilot ask, then --decision <id>)`;
    assert.deepEqual(res, { ok: false, reason: 'no-decision', message });
  }
  assert.deepEqual([run.rows.filter((r) => r.event === 'review.waived').length, gh.calls.length], [0, 0]);
  // exactly 30 min old still counts
  const fresh = run.decide('waive:warning', W1, { ts: at(10) });
  assert.equal((await waiveForOwner({ runId: 'a-dec', ...WAIVE, decisionId: fresh }, deps)).ok, true);
});

test('a real delegate decision (autopilot ask with a subject) lets the waiver through', async () => {
  const run = await newRun('a-real', { config: { provider: 'anthropic', levels: LEVELS } });
  run.review({ event: 'review.triage', finding: 'W1', severity: 'warning', verdict: 'fix_now' });
  const answer = { decision: 'waive', within_scope: true, confidence: 0.9, reason: 'naming only', escalate: false };
  const env = { PATH: process.env.PATH ?? '', HOME: process.env.HOME ?? '', TMPDIR: process.env.TMPDIR ?? '', FAKE_RECORD: freshDir('records'), FAKE_ANSWER: JSON.stringify(answer) };
  const asked = await askDelegate({ runId: 'a-real', scope: 'waive:warning', question: 'Waive warning W1?', options: ['waive', 'fix'], subject: { block: 'B1', file: `./${FILE}`, finding: 'W1' } }, { bins: BINS, env, stderr: { write: () => true }, writeRow: run.writeRow, now: () => at(5) });
  assert.equal(asked.acted, true);
  assert.deepEqual(/** @type {any} */ (asked.row).subject, { block: 'B1', file: FILE, finding: 'W1' });
  const res = await waiveForOwner({ runId: 'a-real', ...WAIVE, decisionId: /** @type {any} */ (asked.row).decision_id }, { onPath: () => false, writeRow: run.writeRow, readRows: run.readRows, now: () => at(10) });
  assert.equal(res.ok, true);
});

/** The gate on a block whose file stopped at review_cap with `open` findings; nothing else to check. */
function gate(/** @type {string} */ runId, /** @type {Record<string, any>[]} */ rows, /** @type {Buffer} */ key) {
  return checkBlockReviews({ block: 'B1', runId, files: [], rows, key, proofFiles: [], highPaths: [] }).refusals.map((r) => [r.code, r.finding, r.detail]);
}

test('gate: a valid autopilot waiver clears the cap finding; stopped-before (or an undated stop), no grant row, no decision, critical or a forged MAC do not', async () => {
  const run = await newRun('a-gate');
  run.review({ event: 'review.triage', finding: 'W1', severity: 'warning', verdict: 'fix_now' });
  run.review({ event: 'review.triage', finding: 'C1', severity: 'critical', verdict: 'fix_now' });
  run.review({ event: 'review.cap', round: 4, reason: 'review_cap', open: ['W1'] });
  const decisionId = run.decide('waive:warning', W1);
  const base = run.rows.filter((r) => r.event !== 'autopilot.grant'); // review + decision rows
  const grantRow = run.rows.filter((r) => r.event === 'autopilot.grant');
  assert.deepEqual(gate('a-gate', [...base, ...grantRow], run.key), [['review_cap', 'W1', `${FILE} stopped at review_cap; finding W1 is open and not waived`]]);

  const res = await waiveForOwner({ runId: 'a-gate', ...WAIVE, decisionId }, { onPath: () => false, writeRow: run.writeRow, readRows: run.readRows, now: () => at(10) });
  assert.equal(res.ok, true);
  assert.deepEqual(gate('a-gate', run.rows, run.key), []);

  const waiver = /** @type {Record<string, any>} */ (run.rows.find((r) => r.event === 'review.waived'));
  const { mac, ...unsigned } = waiver;
  const invalid = (/** @type {string} */ why) => [['autopilot_waiver_invalid', 'W1', `the autopilot waiver of W1 in ${FILE} does not count: ${why}`]];
  // the grant was stopped at minute 20; a waiver dated minute 25 does not count
  const stopped = [...base, ...grantRow];
  await stopGrant({ runId: 'a-gate', now: at(20), writeRow: async (r) => void stopped.push(r) });
  stopped.push(signRow({ ...unsigned, ts: at(25).toISOString() }, run.key));
  assert.deepEqual(gate('a-gate', stopped, run.key), invalid('grant ap-a-gate was stopped before the waiver'));
  // a stop row with no time (or an unparsable one) counts as having stopped it (fail closed)
  for (const ts of [undefined, 'yesterday']) {
    const undated = [...base, ...grantRow, { run: 'a-gate', event: 'autopilot.stop', grant_id: 'ap-a-gate', ...(ts ? { ts } : {}) }, waiver];
    assert.deepEqual(gate('a-gate', undated, run.key), invalid('grant ap-a-gate was stopped before the waiver'));
  }
  // no autopilot.grant row in the ledger for its grant_id
  assert.deepEqual(gate('a-gate', [...base, waiver], run.key), invalid('no signed autopilot.grant row for grant ap-a-gate in this run'));
  // its decision is not in the ledger
  assert.deepEqual(gate('a-gate', [...base.filter((r) => r.event !== 'autopilot.decision'), ...grantRow, waiver], run.key), invalid(`no autopilot.decision row ${decisionId} in this run`));
  // a signed waiver for a CRITICAL finding (the row says warning; the review rows say critical)
  const critical = [...base.filter((r) => r.event !== 'review.cap'), signRow({ run: 'a-gate', block: 'B1', file: FILE, event: 'review.cap', round: 4, reason: 'review_cap', open: ['C1'] }, run.key)];
  critical.push(...grantRow, signRow({ ...unsigned, finding: 'C1' }, run.key));
  assert.deepEqual(gate('a-gate', critical, run.key), [['autopilot_waiver_invalid', 'C1', `the autopilot waiver of C1 in ${FILE} does not count: the finding is critical; critical findings are never delegated`]]);
  // its decision acted but answered "fix": it never authorised a waiver
  const fixId = run.decide('waive:warning', W1, { decision: 'fix' });
  assert.deepEqual(gate('a-gate', [...run.rows.filter((r) => r.event !== 'review.waived'), signRow({ ...unsigned, decision_id: fixId }, run.key)], run.key), invalid(`decision ${fixId} decided "fix", not "waive"`));
  // a waiver whose MAC does not verify
  assert.deepEqual(gate('a-gate', [...base, ...grantRow, { ...waiver, reason: 'edited' }], run.key), invalid('the waiver has no valid MAC'));
  // outside the window: dated after `until`
  assert.deepEqual(gate('a-gate', [...base, ...grantRow, signRow({ ...unsigned, ts: at(121).toISOString() }, run.key)], run.key), invalid("the waiver is outside grant ap-a-gate's window"));
  // a stop row for this grant id written under ANOTHER run still counts
  const crossRun = [...base, ...grantRow, { run: 'r-other', event: 'autopilot.stop', grant_id: 'ap-a-gate', ts: at(5).toISOString() }, waiver];
  assert.deepEqual(gate('a-gate', crossRun, run.key), invalid('grant ap-a-gate was stopped before the waiver'));
  // by: human is unchanged — a signed human waiver clears it with no grant at all
  assert.deepEqual(gate('a-gate', [...base, signRow({ run: 'a-gate', event: 'review.waived', block: 'B1', file: FILE, finding: 'W1', reason: 'ok', by: 'human' }, run.key)], run.key), []);
});

test('dedupe: an issue holding the exact marker gets a comment, not a second issue; a full page without it is search-failed (no issue)', async () => {
  const run = await newRun('a-dup');
  run.review({ event: 'review.triage', finding: 'W1', severity: 'warning', verdict: 'fix_now' });
  const marker = waiverMarker('a-dup', 'B1', FILE, 'W1');
  // the fuzzy search also returns an issue WITHOUT the exact marker: it is skipped
  const list = [
    { number: 3, url: 'https://github.com/acme/widgets/issues/3', state: 'OPEN', body: waiverMarker('a-dup', 'B1', FILE, 'W10') },
    { number: 7, url: ISSUE, state: 'OPEN', body: `earlier waiver\n\n${marker}\n` },
  ];
  const gh = fakeGh({ list });
  const res = await waiveForOwner({ runId: 'a-dup', ...WAIVE, decisionId: run.decide('waive:warning', W1) }, { ...gh.deps, writeRow: run.writeRow, readRows: run.readRows, now: () => at(10) });
  assert.equal(res.ok, true);
  assert.deepEqual(gh.calls.map((c) => c.argv.slice(0, 3).join(' ')), ['gh auth status', 'gh repo view', 'gh issue list', 'gh issue comment']);
  assert.deepEqual(gh.calls[3].argv.slice(3, 6), ['7', '--repo', REPO]);
  assert.equal(String(gh.calls[3].body).endsWith(`${marker}\n`), true);
  assert.deepEqual(run.rows.filter((r) => r.event === 'review.waived').map((r) => r.issue), [ISSUE]);

  // 100 results, none with the exact marker: the match may be on a page not read ⇒ no new issue
  const run2 = await newRun('a-full');
  run2.review({ event: 'review.triage', finding: 'W1', severity: 'warning', verdict: 'fix_now' });
  const page = Array.from({ length: 100 }, (_, i) => ({ number: 100 + i, url: `https://github.com/acme/widgets/issues/${100 + i}`, state: 'OPEN', body: 'code-forge-waiver: other' }));
  const full = fakeGh({ list: page });
  const res2 = await waiveForOwner({ runId: 'a-full', ...WAIVE, decisionId: run2.decide('waive:warning', W1) }, { ...full.deps, writeRow: run2.writeRow, readRows: run2.readRows, now: () => at(10) });
  assert.deepEqual([/** @type {any} */ (res2).issue, /** @type {any} */ (res2).issueFailure], [null, 'search-failed']);
  assert.equal(full.calls.filter((c) => c.argv[2] === 'create').length, 0);
});

test('two concurrent waives of one finding open ONE issue (the second comments); both rows link it', async () => {
  const run = await newRun('a-conc');
  run.review({ event: 'review.triage', finding: 'W1', severity: 'warning', verdict: 'fix_now' });
  const decisionId = run.decide('waive:warning', W1);
  const gh = fakeGh();
  const deps = { ...gh.deps, writeRow: run.writeRow, readRows: run.readRows, now: () => at(10) };
  const both = await Promise.all([waiveForOwner({ runId: 'a-conc', ...WAIVE, decisionId }, deps), waiveForOwner({ runId: 'a-conc', ...WAIVE, decisionId }, deps)]);
  // which of the two takes the lock first is not fixed: one creates, the other comments
  assert.deepEqual(both.map((r) => [r.ok, /** @type {any} */ (r).issueAction]).sort(), [[true, 'commented'], [true, 'created']]);
  assert.equal(gh.created.length, 1);
  assert.deepEqual(gh.calls.map((c) => c.argv.slice(1, 3).join(' ')).filter((c) => c === 'issue create' || c === 'issue comment'), ['issue create', 'issue comment']);
  assert.deepEqual(run.rows.filter((r) => r.event === 'review.waived').map((r) => r.issue), [ISSUE, ISSUE]);
});

test('no gh, not signed in, a failed create, a PATH lookup that throws: the waiver stands (issue null) and one autopilot.issue_failed row with a fixed code, never gh output', async () => {
  const SENTINEL = 'SENTINEL-gh-output-4242';
  const cases = [
    ['a-nogh', { onPath: () => false }, 'gh-missing', 0],
    ['a-noauth', fakeGh({ auth: false }).deps, 'gh-not-logged-in', 1],
    ['a-fail', fakeGh({ create: { result: 'failed', code: 4, signal: null, stdout: SENTINEL, stderr: `HTTP 502 ${SENTINEL}`, timedOut: false } }).deps, 'create-failed', 4],
    ['a-throw', { onPath: () => { throw new Error(SENTINEL); } }, 'gh-failed', 0],
  ];
  for (const [runId, ghDeps, reason, calls] of /** @type {Array<[string, any, string, number]>} */ (cases)) {
    const run = await newRun(runId);
    run.review({ event: 'review.triage', finding: 'N1', severity: 'nit', verdict: 'nit' });
    const decisionId = run.decide('waive:nit', { ...W1, finding: 'N1' });
    /** @type {string[][]} */
    const seen = [];
    const exec = ghDeps.exec ? async (/** @type {string[]} */ a, /** @type {any} */ o) => (seen.push(a), ghDeps.exec(a, o)) : undefined;
    const res = await waiveForOwner({ runId, ...WAIVE, finding: 'N1', severity: 'nit', decisionId }, { ...ghDeps, ...(exec ? { exec } : {}), writeRow: run.writeRow, readRows: run.readRows, now: () => at(10) });
    assert.deepEqual([res.ok, /** @type {any} */ (res).issue, /** @type {any} */ (res).issueFailure], [true, null, reason]);
    assert.equal(seen.length, calls);
    const waived = run.rows.filter((r) => r.event === 'review.waived');
    assert.deepEqual(waived.map((r) => [r.by, r.severity, r.issue]), [['autopilot', 'nit', null]]);
    const failed = run.rows.filter((r) => r.event === 'autopilot.issue_failed');
    assert.deepEqual(failed.map(({ mac, ...f }) => f), [{ run: runId, event: 'autopilot.issue_failed', block: 'B1', file: FILE, finding: 'N1', grant_id: `ap-${runId}`, reason, ts: at(10).toISOString() }]);
    assert.equal(JSON.stringify(run.rows).split(SENTINEL).length - 1, 0);
  }
});

test('autopilot round: needs the decision; one extra round per file per grant (signed row with decision_id), a second refused, none without the scope', async () => {
  const run = await newRun('a-round');
  const deps = { writeRow: run.writeRow, readRows: run.readRows, now: () => at(10) };
  await assert.rejects(grantExtraRound(/** @type {any} */ ({ runId: 'a-round', block: 'B1', file: FILE }), deps), { code: 'usage' });
  const other = run.decide('round:extra', { block: 'B1', file: 'src/b.mjs' });
  assert.equal(reasonOf(await grantExtraRound({ runId: 'a-round', block: 'B1', file: FILE, decisionId: other }, deps)), 'no-decision');
  const denied = run.decide('round:extra', { block: 'B1', file: FILE }, { decision: 'deny' });
  assert.deepEqual(await grantExtraRound({ runId: 'a-round', block: 'B1', file: FILE, decisionId: denied }, deps), { ok: false, reason: 'no-decision', message: `decision ${denied} decided "deny", not "allow"; the question goes to the owner (autopilot ask, then --decision <id>)` });
  const decisionId = run.decide('round:extra', { block: 'B1', file: FILE });
  const first = await grantExtraRound({ runId: 'a-round', block: 'B1', file: FILE, decisionId }, deps);
  assert.equal(first.ok, true);
  const rounds = run.rows.filter((r) => r.event === 'autopilot.extra_round');
  assert.deepEqual(rounds.map(({ mac, ...f }) => f), [{ run: 'a-round', event: 'autopilot.extra_round', block: 'B1', file: FILE, grant_id: 'ap-a-round', decision_id: decisionId, ts: at(10).toISOString() }]);
  assert.deepEqual(await grantExtraRound({ runId: 'a-round', block: 'B1', file: `./${FILE}`, decisionId }, deps), { ok: false, reason: 'already-used', message: `${FILE} already had its extra round under grant ap-a-round` });
  assert.equal(run.rows.filter((r) => r.event === 'autopilot.extra_round').length, 1);

  const scoped = await newRun('a-round2', { allow: 'waive:nit' });
  const d2 = scoped.decide('round:extra', { block: 'B1', file: FILE });
  assert.equal(reasonOf(await grantExtraRound({ runId: 'a-round2', block: 'B1', file: FILE, decisionId: d2 }, { writeRow: scoped.writeRow, readRows: scoped.readRows, now: () => at(10) })), 'not-allowed');
  assert.equal(scoped.rows.filter((r) => r.event === 'autopilot.extra_round').length, 0);
});

test('extraRoundsFor counts a row only when it verifies, its grant covered round:extra at its ts and its decision holds', async () => {
  const run = await newRun('a-count');
  const where = { runId: 'a-count', block: 'B1', file: FILE, key: run.key };
  const decisionId = run.decide('round:extra', { block: 'B1', file: FILE });
  const row = (/** @type {Record<string, any>} */ extra = {}) => signRow({ run: 'a-count', event: 'autopilot.extra_round', block: 'B1', file: FILE, grant_id: 'ap-a-count', decision_id: decisionId, ts: at(10).toISOString(), ...extra }, run.key);
  const ledger = run.rows.filter((r) => r.event === 'autopilot.grant' || r.event === 'autopilot.decision');
  assert.equal(extraRoundsFor([...ledger, row()], where), 1);
  assert.equal(extraRoundsFor([...ledger, row(), row({ ts: at(11).toISOString() })], where), 1); // one per grant
  assert.equal(extraRoundsFor([...ledger, { ...row(), mac: '0'.repeat(64) }], where), 0);
  assert.equal(extraRoundsFor([...ledger, row({ ts: 'not a time' })], where), 0);
  assert.equal(extraRoundsFor([...ledger, row({ decision_id: 'ad-none' })], where), 0);
  const denyId = run.decide('round:extra', { block: 'B1', file: FILE }, { decision: 'deny' });
  assert.equal(extraRoundsFor([...run.rows, row({ decision_id: denyId })], where), 0);
  assert.equal(extraRoundsFor([...ledger, row({ grant_id: 'ap-forged' })], where), 0);
  assert.equal(extraRoundsFor([...ledger, signRow({ run: 'a-count', event: 'autopilot.stop', grant_id: 'ap-a-count', ts: at(8).toISOString() }, run.key), row()], where), 0);
  assert.equal(extraRoundsFor(ledger.filter((r) => r.event !== 'autopilot.grant').concat(row()), where), 0);
  // a row the checks cannot read gives nothing, never a throw
  assert.equal(extraRoundsFor([...ledger, row({ ts: { toString: null } })], where), 0);
});

/**
 * A fix loop on a 60-line file with 5 critical findings; each fix closes one (S1 resolved), so the
 * open set shrinks every round (no stall) and only the cap stops it.
 * @param {number} extraRounds @param {{rungUsed?: boolean}} [o]
 */
function stuckLoop(extraRounds, o = {}) {
  const repoRoot = freshDir('loop');
  writeFile(repoRoot, FILE, lines(60));
  /** @type {Record<string, any>[]} */
  const rows = [];
  let fixes = 0;
  let patches = 0;
  const deps = {
    repoRoot,
    // the per-level ladder off (rule 2 at the L2 ceiling would act first): only the cap acts
    cfg: { ...cfgFor({ max_rounds_per_file: 2 }), escalation: { review_rounds_per_level: 10, after_rounds_with_warnings: 0 } },
    workDir: path.join(freshDir('work'), 'w'),
    writeRow: async (/** @type {Record<string, any>} */ r) => void rows.push(r),
    extraRounds,
    review: async () => ({ status: 'reviewed', engine: 'adaptive', findings: [1, 2, 3, 4, 5].map((n) => ({ id: `F${n}`, file: FILE, line_start: 10 * n, line_end: 10 * n, severity: 'critical', category: 'c', claim: 'x', evidence: 'e', fix: 'f' })), sessions: [{ role: 'reviewer', lens: 'A' }, { role: 'reviewer', lens: 'B' }, { role: 'judge', lens: 'judge' }] }),
    jev: async (/** @type {{state: Record<string, any>}} */ req) => ({ ok: true, answers: { resolved: { type: 'noul', noul: Number(req.state.finding.id.slice(1)) <= fixes + patches ? 0.95 : 0.1 } } }),
    spawn: async (/** @type {{promptPath: string}} */ s) => {
      const text = readFileSync(s.promptPath, 'utf8');
      const listed = text.slice(text.indexOf('\nhunks:\n') + 8).split('\n');
      const hunks = listed.slice(0, listed.findIndex((l) => !l.startsWith('- @@'))).map((l) => l.slice(2));
      return { status: 'ok', exit_code: 0, answer: answerFor(hunks), usage: { tokens_in: 900, tokens_out: 300 } };
    },
    fix: async () => {
      fixes += 1;
      const full = path.join(repoRoot, FILE);
      writeFileSync(full, readFileSync(full, 'utf8').replace(`v${10 + fixes} = ${10 + fixes};`, `v${10 + fixes} = ${10 + fixes} + 1;`));
    },
    patch: async () => {
      patches += 1;
      const full = path.join(repoRoot, FILE);
      writeFileSync(full, readFileSync(full, 'utf8').replace('v40 = 40;', 'v40 = 40 + 1;'));
    },
  };
  return { deps, rows, fixesDone: () => fixes, patchesDone: () => patches, state: newFileState({ file: FILE, level: 'L2', l3RungUsed: o.rungUsed ?? true }) };
}

test('the fix loop gives exactly one round past the cap with an extra round, and a stopped file reopens once', async () => {
  // cap 2, no extra: stops at round 2
  const plain = stuckLoop(0);
  const s0 = await converge(plain.state, plain.deps);
  assert.deepEqual([s0.status, s0.round, s0.next?.reason], ['stopped', 2, 'review_cap']);
  // cap 2 + 1 extra: stops at round 3 — exactly one more round, one more fix
  const extra = stuckLoop(1);
  const s1 = await converge(extra.state, extra.deps);
  assert.deepEqual([s1.status, s1.round, s1.next?.reason, extra.fixesDone() - plain.fixesDone()], ['stopped', 3, 'review_cap', 1]);
  assert.deepEqual(extra.rows.filter((r) => r.event === 'review.cap').map((r) => r.round), [3]);
  // already stopped at the old cap: the worker reopens it once, the loop runs round 3, then stops for good
  assert.equal(reopenForExtraRound(s0, 0, plain.deps.cfg), false);
  assert.equal(reopenForExtraRound(s0, 1, plain.deps.cfg), true);
  assert.deepEqual(s0.next, { action: 'fix', level: 'L2', trigger: 'autopilot_extra_round' });
  await converge(s0, { ...plain.deps, extraRounds: 1 });
  assert.deepEqual([s0.status, s0.round, s0.next?.reason], ['stopped', 3, 'review_cap']);
  assert.deepEqual(plain.rows.filter((r) => r.event === 'review.round').map((r) => r.round), [1, 2, 3]);
  assert.equal(reopenForExtraRound(s0, 1, plain.deps.cfg), false);
});

test('rule 6 uses the raised cap: with the rung unused, no patch at the old cap (round 2) — the rung comes at round 3', async () => {
  const plain = stuckLoop(0, { rungUsed: false });
  await converge(plain.state, plain.deps);
  const extra = stuckLoop(1, { rungUsed: false });
  await converge(extra.state, extra.deps);
  const kinds = (/** @type {Record<string, any>[]} */ rows) => rows.filter((r) => r.event === 'review.round').map((r) => `${r.round}:${r.kind}`);
  assert.deepEqual(kinds(plain.rows), ['1:full', '2:recheck', '2:patch_check']);
  assert.deepEqual(kinds(extra.rows), ['1:full', '2:recheck', '3:recheck', '3:patch_check']);
  assert.deepEqual([plain.patchesDone(), extra.patchesDone(), extra.fixesDone()], [1, 1, 2]);
});

test('level capping: the lane, at most plan + 1, never above L2', () => {
  const table = [
    ['L3', 'L2', 'L2'],
    ['L3', 'L3', 'L2'],
    ['L2', 'L0', 'L1'],
    ['L2', 'L1', 'L2'],
    ['L1', 'L1', 'L1'],
    ['L0', 'L2', 'L0'],
  ];
  assert.deepEqual(table.map(([lane, planLevel]) => chooseLevel({ lane, planLevel })), table.map((r) => r[2]));
});

const PLAN = [
  '# plan',
  '',
  '| id | title | level | depends_on | owned_files | acceptance |',
  '|---|---|---|---|---|---|',
  `| B1 | one | L0 | — | \`${FILE}\` | it works |`,
  `| B2 | two | L1 | — | \`src/b.mjs\` | it works |`,
  '',
].join('\n');

test('autopilot level: the recorded lane capped at plan + 1 → a signed autopilot.level row; no decision, no lane or no scope → refused', async () => {
  const run = await newRun('a-level');
  const planPath = path.join(freshDir('plan'), 'plan.md');
  writeFileSync(planPath, PLAN);
  // lane rows as `jev ask lane --block` writes them (no run, unsigned); the latest per block wins
  // no lane recorded yet for B1
  const early = run.decide('model:choose', { block: 'B1' });
  assert.deepEqual(await chooseCoderLevel({ runId: 'a-level', block: 'B1', plan: planPath, decisionId: early }, { writeRow: run.writeRow, readRows: run.readRows, now: () => at(10) }), { ok: false, reason: 'no-lane', message: 'block B1 has no recorded lane — run forge jev ask lane --block B1; the level goes to the owner' });
  run.rows.push({ event: 'decision', decision_id: 'd1', question: 'lane', answer: 'L1', source: 'jev', block: 'B1', plan: 'plan.md' });
  run.rows.push({ event: 'decision', decision_id: 'd2', question: 'lane', answer: 'L3', source: 'jev', block: 'B1', plan: 'plan.md' });
  run.rows.push({ event: 'decision', decision_id: 'd3', question: 'lane', answer: 'L2', source: 'jev', block: 'B1', plan: 'other.md' });
  const deps = { writeRow: run.writeRow, readRows: run.readRows, now: () => at(10) };
  // the block must be in the plan's block table (B5 is not); it need not be open in the run
  const forB2 = run.decide('model:choose', { block: 'B2' });
  await assert.rejects(chooseCoderLevel({ runId: 'a-level', block: 'B5', plan: planPath, decisionId: forB2 }, deps), { code: 'usage', message: "block B5 is not in the plan's block table" });
  assert.equal(reasonOf(await chooseCoderLevel({ runId: 'a-level', block: 'B1', plan: planPath, decisionId: forB2 }, deps)), 'no-decision');
  // the level set is L1 (lane L3, plan L0 + 1): a decision for L2 does not authorise it
  const l2 = run.decide('model:choose', { block: 'B1' }, { decision: 'L2' });
  assert.deepEqual(await chooseCoderLevel({ runId: 'a-level', block: 'B1', plan: planPath, decisionId: l2 }, deps), { ok: false, reason: 'no-decision', message: `decision ${l2} decided "L2", not "L1"; the question goes to the owner (autopilot ask, then --decision <id>)` });
  const decisionId = run.decide('model:choose', { block: 'B1' }, { decision: 'L1' });
  const res = await chooseCoderLevel({ runId: 'a-level', block: 'B1', plan: planPath, decisionId }, deps);
  assert.equal(res.ok, true);
  const rows = run.rows.filter((r) => r.event === 'autopilot.level');
  assert.deepEqual(rows.map(({ mac, ...f }) => f), [{ run: 'a-level', event: 'autopilot.level', block: 'B1', level: 'L1', lane: 'L3', lane_source: 'jev', lane_decision_id: 'd2', plan_level: 'L0', grant_id: 'ap-a-level', decision_id: decisionId, ts: at(10).toISOString() }]);
  assert.deepEqual(verifyRow(rows[0], run.key), { ok: true });
  const scoped = await newRun('a-level2', { allow: 'round:extra' });
  const d2 = scoped.decide('model:choose', { block: 'B1' });
  assert.equal(reasonOf(await chooseCoderLevel({ runId: 'a-level2', block: 'B1', plan: planPath, decisionId: d2 }, { writeRow: scoped.writeRow, readRows: scoped.readRows, now: () => at(10) })), 'not-allowed');
  assert.equal(run.rows.filter((r) => r.event === 'autopilot.level').length, 1);
});

test('feature: `autopilot waive` end to end (exit 0 with the issue; exit 1 refused for a critical finding; exit 2 without --decision)', async () => {
  const run = await newRun('a-cli');
  run.review({ event: 'review.triage', finding: 'W1', severity: 'warning', verdict: 'fix_now' });
  run.review({ event: 'review.triage', finding: 'C1', severity: 'critical', verdict: 'fix_now' });
  const decisionId = run.decide('waive:warning', W1);
  const gh = fakeGh();
  let out = '';
  let err = '';
  const io = { stdout: { write: (/** @type {string} */ s) => (out += s) }, stderr: { write: (/** @type {string} */ s) => (err += s) } };
  const deps = { ...io, writeRow: run.writeRow, now: () => at(10), actions: { ...gh.deps, readRows: run.readRows } };
  const argv = ['waive', '--run', 'a-cli', '--block', 'B1', '--file', FILE, '--finding', 'W1', '--severity', 'warning', '--reason', 'cosmetic', '--decision', decisionId];
  assert.equal(await runAutopilot(argv, deps), 0);
  assert.equal(out, `autopilot waive run a-cli: waived W1 (warning) in ${FILE}, block B1 (by: autopilot, grant ap-a-cli) · issue ${ISSUE} (created)\n`);
  assert.equal(await runAutopilot(argv.map((a) => (a === 'W1' ? 'C1' : a)), deps), 1);
  assert.equal(err, `autopilot waive: refused — finding C1 in ${FILE} is recorded as critical; critical findings are never delegated\n`);
  err = '';
  assert.equal(await runAutopilot(argv.slice(0, -2), deps), 2);
  assert.equal(err, 'autopilot waive: autopilot waive needs --decision\n');
  assert.equal(run.rows.filter((r) => r.event === 'review.waived').length, 1);
});

test('feature: `block open` without --level uses the VERIFIED autopilot.level row; repeated --level and no verified row are usage errors', async () => {
  const repo = await makeRepo();
  /** @type {Record<string, any>[]} */
  const sink = [];
  await startRun({ workspace: repo, project: 'proj-open', runId: 'a-open', writeRow: async (r) => void sink.push(r), now: T0 });
  const key = await loadKey('a-open');
  await appendRow(signRow({ run: 'a-open', event: 'autopilot.level', block: 'B9', level: 'L1', lane: 'L1', plan_level: 'L1', grant_id: 'ap-x', ts: at(1).toISOString() }, key), { slug: 'proj-open' });
  // an unsigned row for B7 does not count
  await appendRow({ run: 'a-open', event: 'autopilot.level', block: 'B7', level: 'L2', ts: at(1).toISOString(), mac: '0'.repeat(64) }, { slug: 'proj-open' });
  const accDir = path.join(PARENT, 'acc');
  mkdirSync(accDir, { recursive: true });
  const acc = path.join(accDir, 'acc.yml');
  writeFileSync(acc, '- clause: it works\n  tests: [t1]\n');
  let out = '';
  let err = '';
  const io = { stdout: { write: (/** @type {string} */ s) => (out += s) }, stderr: { write: (/** @type {string} */ s) => (err += s) } };
  const code = await runBlock(['open', 'B9', '--run', 'a-open', '--owned', 'src/x.mjs', '--acceptance', acc], io);
  assert.deepEqual([code, err], [0, '']);
  assert.match(out.split('\n')[0], /^block B9 open · L1 \(autopilot level\) · code · attempt 1 · base [0-9a-f]{12}$/);
  assert.equal((await readRun('a-open')).blocks.B9.level, 'L1');
  assert.equal(await runBlock(['open', 'B7', '--run', 'a-open', '--owned', 'src/y.mjs', '--acceptance', acc], io), 2);
  assert.equal(err, 'block open: no --level given and no autopilot level recorded for B7\n');
  err = '';
  assert.equal(await runBlock(['open', 'B6', '--run', 'a-open', '--level', 'L1', '--level', 'L2', '--owned', 'src/z.mjs', '--acceptance', acc], io), 2);
  assert.equal(err, 'block open: --level given more than once\n');
});

test('stop race: a grant stopped while gh creates the issue → no waiver row, one "waiver cancelled" comment, refused', async () => {
  const run = await newRun('a-race');
  run.review({ event: 'review.triage', finding: 'W1', severity: 'warning', verdict: 'fix_now' });
  const decisionId = run.decide('waive:warning', W1);
  const gh = fakeGh({ onCreate: () => stopGrant({ runId: 'a-race', now: at(11), writeRow: run.writeRow }) });
  let tick = 10;
  const res = await waiveForOwner({ runId: 'a-race', ...WAIVE, decisionId }, { ...gh.deps, writeRow: run.writeRow, readRows: run.readRows, now: () => at(tick++) });
  assert.deepEqual(res, { ok: false, reason: 'stopped', message: 'the grant refuses (stopped); the question goes to the owner' });
  assert.equal(run.rows.filter((r) => r.event === 'review.waived').length, 0);
  assert.equal(gh.created.length, 1);
  const comments = gh.calls.filter((c) => c.argv[2] === 'comment');
  assert.deepEqual(comments.map((c) => [c.argv.slice(3, 6), c.body]), [[['7', '--repo', REPO], `${CANCELLED_COMMENT}\n\n${waiverMarker('a-race', 'B1', FILE, 'W1')}\n`]]);
});

test('a reason that still looks like a secret after the scrub refuses the waive: usage, nothing written, no gh call', async () => {
  const run = await newRun('a-secret');
  run.review({ event: 'review.triage', finding: 'W1', severity: 'warning', verdict: 'fix_now' });
  const decisionId = run.decide('waive:warning', W1);
  const gh = fakeGh();
  const before = run.rows.length;
  await assert.rejects(
    waiveForOwner({ runId: 'a-secret', ...WAIVE, reason: 'token FAKEq8Zr3LmN0pWx7Yt2Vb5Kc9Hd4Js6Ga1Ue here', decisionId }, { ...gh.deps, writeRow: run.writeRow, readRows: run.readRows, now: () => at(10) }),
    { code: 'usage', message: 'the reason looks like it holds a secret; reword it' },
  );
  assert.deepEqual([run.rows.length - before, gh.calls.length], [0, 0]);
});

test('an autopilot.issue_failed row that cannot be written: the waiver stands and the result says so', async () => {
  const run = await newRun('a-rowfail');
  run.review({ event: 'review.triage', finding: 'W1', severity: 'warning', verdict: 'fix_now' });
  const decisionId = run.decide('waive:warning', W1);
  const writeRow = async (/** @type {Record<string, any>} */ row) => {
    if (row.event === 'autopilot.issue_failed') throw new Error('disk full');
    run.rows.push(row);
  };
  const res = await waiveForOwner({ runId: 'a-rowfail', ...WAIVE, decisionId }, { onPath: () => false, writeRow, readRows: run.readRows, now: () => at(10) });
  assert.deepEqual([res.ok, /** @type {any} */ (res).issueFailure, /** @type {any} */ (res).issueRowWritten], [true, 'gh-missing', false]);
  assert.deepEqual(run.rows.filter((r) => r.event.startsWith('review.waived') || r.event === 'autopilot.issue_failed').map((r) => r.event), ['review.waived']);
});

test('entropy rule: a hex-only hash using all 16 digits evenly is not a secret; a mixed-case token still is', () => {
  const hash = '0123456789abcdef'.repeat(4);
  assert.equal(hash.length, 64);
  assert.equal(findSecret(`sha256 ${hash}`), null);
  assert.equal(findSecret(`sha256 ${hash.toUpperCase()}`), null);
  assert.deepEqual(findSecret('x\nkey FAKEq8Zr3LmN0pWx7Yt2Vb5Kc9Hd4Js6Ga1Ue'), { rule: 'high-entropy string', line: 2 });
});

test('feature, the normal flow: autopilot level for a block not yet opened → signed row in the ledger → block open without --level codes at it', async () => {
  const repo = await makeRepo();
  await startRun({ workspace: repo, project: 'proj-flow', runId: 'a-flow', writeRow: (r) => appendRow(r, { slug: 'proj-flow' }), now: T0 });
  await startGrant({ runId: 'a-flow', input: { until: UNTIL, delegate: 'L2', allow: 'model:choose' }, now: T0, grantId: 'ap-a-flow' });
  assert.equal((await readRun('a-flow')).blocks?.B2, undefined); // not opened yet
  const key = await loadKey('a-flow');
  // the lane `jev ask lane --block B2 --plan plan.md` recorded, and the delegate's acted decision for L2
  await appendRow({ event: 'decision', decision_id: 'dl', question: 'lane', answer: 'L2', source: 'jev', block: 'B2', plan: 'plan.md' }, { slug: 'proj-flow' });
  await appendRow(signRow({ run: 'a-flow', event: 'autopilot.decision', decision_id: 'ad-flow', grant_id: 'ap-a-flow', scope: 'model:choose', subject: { block: 'B2' }, decision: 'L2', acted: true, ts: at(5).toISOString() }, key), { slug: 'proj-flow' });
  const planPath = path.join(freshDir('plan-flow'), 'plan.md');
  writeFileSync(planPath, PLAN); // B2 is L1 in the plan: lane L2 ≤ plan + 1
  const res = await chooseCoderLevel({ runId: 'a-flow', block: 'B2', plan: planPath, decisionId: 'ad-flow' }, { now: () => at(10) });
  assert.deepEqual([res.ok, /** @type {any} */ (res).row?.level], [true, 'L2']);
  const acc = path.join(freshDir('acc-flow'), 'acc.yml');
  writeFileSync(acc, '- clause: it works\n  tests: [t1]\n');
  let out = '';
  let err = '';
  const io = { stdout: { write: (/** @type {string} */ s) => (out += s) }, stderr: { write: (/** @type {string} */ s) => (err += s) } };
  assert.equal(await runBlock(['open', 'B2', '--run', 'a-flow', '--owned', 'src/b.mjs', '--acceptance', acc], io), 0);
  assert.equal(err, '');
  assert.match(out.split('\n')[0], /^block B2 open · L2 \(autopilot level\) · code · attempt 1 · base [0-9a-f]{12}$/);
  assert.equal((await readRun('a-flow')).blocks.B2.level, 'L2');
});
