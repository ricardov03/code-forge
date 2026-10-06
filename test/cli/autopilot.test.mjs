// B45 `code-forge autopilot start|status|stop`: each start flag (F16–F21), exact refusal texts,
// exact status output, stop, a second grant refused, the terminal rule. Fake clock via deps; the
// real ledger writer into a temp HOME. One per-file temp parent, removed in `after()`; HOME points
// into it before any `src` module loads.
import assert from 'node:assert/strict';
import { mkdirSync, mkdtempSync, readFileSync, rmSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { after, test } from 'node:test';

const PARENT = mkdtempSync(path.join(os.tmpdir(), 'cf-ap-cli-'));
process.env.HOME = path.join(PARENT, 'home');
mkdirSync(process.env.HOME, { recursive: true });
after(() => rmSync(PARENT, { recursive: true, force: true }));

const { runAutopilot } = await import('../../src/cli/autopilot.mjs');
const { appendRow, readAllRows } = await import('../../src/ledger/write.mjs');
const { endRun, readRun, startRun } = await import('../../src/state/run.mjs');
const { isForbidden, mergeForbidden, scanTranscript } = await import('../../src/util/forbidden.mjs');
const { loadKey, verifyRow } = await import('../../src/state/signer.mjs');
const { runFilesDir } = await import('../../src/autopilot/binnacle.mjs');

/** B49b: the two Markdown files of a run (the record when no link is stored). @param {string} runId */
const filesOf = (runId) => ({ binnacle: path.join(runFilesDir(runId), 'autopilot-binnacle.md'), log: path.join(runFilesDir(runId), 'autopilot-log.md') });

const T0 = new Date('2026-10-06T20:00:00.000Z');
/** @param {number} minutes */
const at = (minutes) => new Date(T0.getTime() + minutes * 60000);
const UNTIL = '2026-10-07T00:00:00+02:00'; // T0 + 2 h
const UNTIL_UTC = '2026-10-06T22:00:00.000Z';
const FIXED = 'waive:critical, waive:proof, reviews:skip, limits:change, plan:approve, design:approve, pr:merge, destructive, budget:raise';
const USAGE =
  'usage: code-forge autopilot start --run <id> --until <ISO-8601 with offset> --delegate <L2|L3> --allow <scope,…> [--deny <scope,…>] [--budget <category>=<usd>,…] [--stop-at <0..1>] [--yes] | status --run <id> [--json] | stop --run <id> | ask --run <id> --scope <scope> --question <text> [--options a,b,…] [--context-file <path>] [--block <id>] [--file <path>] [--finding <id>] [--json] (options for an action: waive,fix · allow,deny · L1,L2) | waive --run <id> --block <id> --file <path> --finding <id> --severity <warning|nit> --reason <text> --decision <id> | round --run <id> --block <id> --file <path> --decision <id> | level --run <id> --block <id> --plan <file> --decision <id> | approve --run <id> --key <dot.path> --value <json> --until <ISO-8601 with offset> | binnacle --run <id> [--json|--markdown] [--link <https url>] | log --run <id> [--json|--markdown]\n';

/** A run whose ledger slug is its own id (so each test reads only its rows). @param {string} runId */
async function newRun(runId) {
  const workspace = path.join(PARENT, `ws-${runId}`);
  mkdirSync(workspace, { recursive: true });
  await startRun({ workspace, project: runId, runId, writeRow: (row) => appendRow(row, { slug: runId }), now: T0 });
}

/** @param {string} runId @returns {Promise<string[]>} */
const events = async (runId) => (await readAllRows(runId)).map((r) => r.event);

/**
 * @param {string[]} args @param {{now?: Date, clock?: () => Date, isTTY?: boolean, ui?: any}} [o]
 */
async function cli(args, { now = T0, clock, isTTY = false, ui } = {}) {
  let out = '';
  let err = '';
  const code = await runAutopilot(args, {
    stdout: { write: (s) => ((out += s), true) },
    stderr: { write: (s) => ((err += s), true) },
    now: clock ?? (() => now),
    isTTY,
    ui,
  });
  return { code, out, err };
}

const START = ['start', '--run', 'c-full', '--until', UNTIL, '--delegate', 'L2', '--allow', 'waive:nit,waive:warning', '--deny', 'round:extra', '--budget', 'review=5,coding=20', '--stop-at', '0.75', '--yes'];

test('start with every flag stores the grant and writes one signed autopilot.grant row; status prints it exactly (text and --json)', async () => {
  await newRun('c-full');
  const res = await cli(START);
  assert.equal(res.err, '');
  assert.equal(res.code, 0);
  const grant = (await readRun('c-full')).autopilot;
  assert.match(grant.grant_id, /^ap-[0-9a-f]{8}$/);
  assert.equal(
    res.out,
    `autopilot run c-full: grant ${grant.grant_id} active until ${UNTIL_UTC} (2h 0m)\n` +
      '  delegate L2 · allow waive:nit, waive:warning · budget coding 20.00 USD, review 5.00 USD · stop at 75%\n',
  );
  assert.deepEqual(grant, {
    grant_id: grant.grant_id,
    status: 'active',
    until: UNTIL_UTC,
    delegate: 'L2',
    scopes: ['waive:nit', 'waive:warning'],
    deny: ['round:extra'],
    caps: { review: 5, coding: 20 },
    stop_at: 0.75,
    started_at: T0.toISOString(),
    stopped_at: null,
    expired_at: null,
    link: null,
  });
  const rows = await readAllRows('c-full');
  assert.deepEqual(rows.map((r) => r.event), ['run.start', 'autopilot.grant']);
  const { mac, tokens_source, cost_source, ...fields } = rows[1];
  assert.deepEqual(fields, { run: 'c-full', event: 'autopilot.grant', grant_id: grant.grant_id, scopes: ['waive:nit', 'waive:warning'], deny: ['round:extra'], delegate: 'L2', until: UNTIL_UTC, caps: { review: 5, coding: 20 }, stop_at: 0.75, ts: T0.toISOString() });
  assert.deepEqual(verifyRow(rows[1], await loadKey('c-full')), { ok: true });

  const status = await cli(['status', '--run', 'c-full'], { now: at(40) });
  assert.equal(status.code, 0);
  assert.equal(status.err, '');
  assert.equal(
    status.out,
    `autopilot run c-full: active · 1h 20m left (until ${UNTIL_UTC})\n` +
      `  grant     ${grant.grant_id}\n` +
      '  delegate  L2\n' +
      '  allow     waive:nit, waive:warning\n' +
      `  deny      round:extra, ${FIXED}\n` +
      '  budget    coding 20.00 USD, review 5.00 USD · stop at 75%\n' +
      '  spend     coding 0.00 of 20.00 USD, review 0.00 of 5.00 USD · paused none\n' +
      '  link      none\n' +
      `  binnacle  ${filesOf('c-full').binnacle}\n` +
      `  full log  ${filesOf('c-full').log}\n`,
  );
  assert.equal(readFileSync(filesOf('c-full').binnacle, 'utf8').split('\n')[0], '# Autopilot run c-full');
  assert.equal(readFileSync(filesOf('c-full').log, 'utf8').split('\n')[0], '# Autopilot full log c-full');
  const json = await cli(['status', '--run', 'c-full', '--json'], { now: at(40) });
  assert.equal(json.code, 0);
  assert.deepEqual(JSON.parse(json.out), {
    run: 'c-full',
    state: 'active',
    grant_id: grant.grant_id,
    delegate: 'L2',
    until: UNTIL_UTC,
    time_left_s: 4800,
    scopes: ['waive:nit', 'waive:warning'],
    deny: ['round:extra'],
    fixed_deny: FIXED.split(', '),
    caps: { review: 5, coding: 20 },
    stop_at: 0.75,
    spend: { coding: 0, review: 0 },
    paused: [],
    link: null,
    files: filesOf('c-full'),
    started_at: T0.toISOString(),
    stopped_at: null,
    expired_at: null,
  });
  assert.deepEqual(await events('c-full'), ['run.start', 'autopilot.grant']); // status writes nothing while active
});

test('start defaults: no --deny, no --budget, --stop-at 0.9; --delegate L3 accepted', async () => {
  await newRun('c-def');
  const res = await cli(['start', '--run', 'c-def', '--until', '2026-10-06T21:30:00Z', '--delegate', 'L3', '--allow', 'model:choose', '--yes']);
  assert.equal(res.code, 0, res.err);
  const grant = (await readRun('c-def')).autopilot;
  assert.deepEqual([grant.delegate, grant.scopes, grant.deny, grant.caps, grant.stop_at, grant.until], ['L3', ['model:choose'], [], {}, 0.9, '2026-10-06T21:30:00.000Z']);
  assert.equal(res.out.split('\n')[1], '  delegate L3 · allow model:choose · budget no caps · stop at 90%');
});

test('start refusals: exact message, exit 2, nothing stored and no row (one per flag)', async () => {
  await newRun('c-bad');
  const base = { '--until': UNTIL, '--delegate': 'L2', '--allow': 'waive:nit' };
  /** @type {Array<[Record<string, string | undefined>, string]>} */
  const cases = [
    [{ '--until': '2026-10-06T19:00:00Z' }, '--until is in the past; give a time after now, at most 24 h ahead'],
    [{ '--until': '2026-10-08T00:00:00Z' }, '--until is more than 24 h ahead; a grant lasts at most 24 h'],
    [{ '--until': '2026-10-06T23:00:00' }, '--until must be an ISO-8601 time with an offset (Z or ±hh:mm), e.g. 2026-10-07T07:00:00+02:00'],
    [{ '--delegate': 'L1' }, '--delegate must be L2 or L3'],
    [{ '--allow': 'waive:nit,waive:everything' }, 'unknown scope "waive:everything" in --allow; allowable scopes: waive:warning, waive:nit, round:extra, model:choose'],
    [{ '--allow': 'waive:nit,pr:merge' }, 'scope pr:merge is on the fixed deny list and can never be allowed (merge a pull request)'],
    [{ '--deny': 'sleep' }, 'unknown scope "sleep" in --deny; allowable scopes: waive:warning, waive:nit, round:extra, model:choose'],
    [{ '--budget': 'coding=-1' }, 'budget for coding must be a number of USD above 0, e.g. coding=5'],
    [{ '--budget': 'travel=1' }, 'unknown budget category "travel"; categories: coding, review'],
    [{ '--stop-at': '2' }, '--stop-at must be a number above 0 and at most 1, e.g. 0.9'],
    [{ '--until': undefined }, '--until is required: an ISO-8601 time with an offset, e.g. 2026-10-07T07:00:00+02:00'],
  ];
  for (const [override, message] of cases) {
    const flags = { ...base, ...override };
    const argv = ['start', '--run', 'c-bad', ...Object.entries(flags).flatMap(([k, v]) => (v === undefined ? [] : [k, v])), '--yes'];
    const res = await cli(argv);
    assert.deepEqual([res.code, res.err, res.out], [2, `autopilot start: ${message}\n`, ''], message);
  }
  assert.equal((await readRun('c-bad')).autopilot, undefined);
  assert.deepEqual(await events('c-bad'), ['run.start']);

  assert.deepEqual(await cli(['start', '--until', UNTIL, '--yes']), { code: 2, out: '', err: 'autopilot start: autopilot start needs --run <id>\n' });
  assert.deepEqual(await cli(['start', '--run', 'c-bad', '--fast']), { code: 2, out: '', err: 'autopilot start: unknown flag "--fast"\n' });
  assert.deepEqual(await cli(['pause', '--run', 'c-bad']), { code: 2, out: '', err: USAGE });
  assert.deepEqual(await cli([]), { code: 2, out: '', err: USAGE });
});

test('a second grant for the run is refused while one is active (exit 1, no row)', async () => {
  await newRun('c-two');
  assert.equal((await cli(['start', '--run', 'c-two', '--until', UNTIL, '--delegate', 'L2', '--allow', 'waive:nit', '--yes'])).code, 0);
  const res = await cli(['start', '--run', 'c-two', '--until', UNTIL, '--delegate', 'L3', '--allow', 'waive:warning', '--yes'], { now: at(1) });
  assert.deepEqual(res, { code: 1, out: '', err: `autopilot start: run c-two already has an active autopilot grant until ${UNTIL_UTC}; stop it first with code-forge autopilot stop --run c-two\n` });
  assert.deepEqual(await events('c-two'), ['run.start', 'autopilot.grant']);
  assert.equal((await readRun('c-two')).autopilot.delegate, 'L2');
});

test('the owner starts it: no terminal and no --yes is refused; on a terminal the prompt decides', async () => {
  await newRun('c-tty');
  const argv = ['start', '--run', 'c-tty', '--until', UNTIL, '--delegate', 'L2', '--allow', 'waive:nit'];
  const refused = await cli(argv, { isTTY: false });
  assert.deepEqual(refused, { code: 2, out: '', err: 'autopilot start: no terminal to confirm; the owner starts autopilot — pass --yes to start it without a prompt\n' });
  assert.equal((await readRun('c-tty')).autopilot, undefined);

  /** @type {string[]} */
  const asked = [];
  const ui = (/** @type {boolean} */ answer) => ({ confirm: async (/** @type {any} */ o) => (asked.push(o.message), answer), isCancel: () => false });
  const no = await cli(argv, { isTTY: true, ui: ui(false) });
  assert.deepEqual(no, { code: 1, out: 'cancelled — no grant\n', err: '' });
  assert.equal((await readRun('c-tty')).autopilot, undefined);
  const yes = await cli(argv, { isTTY: true, ui: ui(true) });
  assert.equal(yes.code, 0, yes.err);
  assert.deepEqual(asked, [`Start autopilot for run c-tty until ${UNTIL_UTC}?`, `Start autopilot for run c-tty until ${UNTIL_UTC}?`]);
  assert.deepEqual(await events('c-tty'), ['run.start', 'autopilot.grant']);
});

test('stop ends the grant now with one signed autopilot.stop row; status then says stopped; a second stop is refused', async () => {
  await newRun('c-stop');
  await cli(['start', '--run', 'c-stop', '--until', UNTIL, '--delegate', 'L2', '--allow', 'waive:nit', '--yes']);
  const id = (await readRun('c-stop')).autopilot.grant_id;
  assert.deepEqual(await cli(['stop', '--run', 'c-stop'], { now: at(15) }), {
    code: 0,
    out: `autopilot run c-stop: grant ${id} stopped\n  link      none\n  binnacle  ${filesOf('c-stop').binnacle}\n  full log  ${filesOf('c-stop').log}\n`,
    err: '',
  });
  // the files were rewritten after the stop row: the full log's newest entry is the stop
  assert.equal(readFileSync(filesOf('c-stop').log, 'utf8').split('\n').filter((l) => l.startsWith('| 20'))[0].split(' | ')[1], 'Grant stopped');
  const rows = await readAllRows('c-stop');
  assert.deepEqual(rows.map((r) => r.event), ['run.start', 'autopilot.grant', 'autopilot.stop']);
  assert.deepEqual([rows[2].grant_id, rows[2].ts], [id, at(15).toISOString()]);
  assert.deepEqual(verifyRow(rows[2], await loadKey('c-stop')), { ok: true });

  const status = await cli(['status', '--run', 'c-stop'], { now: at(16) });
  assert.equal(status.out.split('\n')[0], `autopilot run c-stop: stopped at ${at(15).toISOString()} (was until ${UNTIL_UTC})`);
  assert.equal(JSON.parse((await cli(['status', '--run', 'c-stop', '--json'], { now: at(16) })).out).state, 'stopped');
  assert.deepEqual(await cli(['stop', '--run', 'c-stop'], { now: at(17) }), { code: 1, out: '', err: `autopilot stop: run c-stop's autopilot grant was already stopped at ${at(15).toISOString()}\n` });
  assert.deepEqual(await events('c-stop'), ['run.start', 'autopilot.grant', 'autopilot.stop']);
});

test('B49b: with a stored link, status and stop print the link and no file paths (text and --json); no grant prints neither', async () => {
  await newRun('c-linked');
  await cli(['start', '--run', 'c-linked', '--until', UNTIL, '--delegate', 'L2', '--allow', 'waive:nit', '--yes']);
  const LINK = 'https://claude.ai/code/artifact/0123abcd';
  const stored = await cli(['binnacle', '--run', 'c-linked', '--link', LINK, '--json'], { now: at(5) });
  assert.equal(stored.code, 0, stored.err);
  const status = await cli(['status', '--run', 'c-linked'], { now: at(10) });
  assert.equal(status.code, 0);
  const tail = status.out.split('\n').slice(-2);
  assert.deepEqual(tail, [`  link      ${LINK}`, '']);
  assert.equal(status.out.includes('binnacle  '), false);
  const json = JSON.parse((await cli(['status', '--run', 'c-linked', '--json'], { now: at(10) })).out);
  assert.deepEqual([json.link, json.files], [LINK, null]);
  const id = (await readRun('c-linked')).autopilot.grant_id;
  assert.deepEqual(await cli(['stop', '--run', 'c-linked'], { now: at(20) }), { code: 0, out: `autopilot run c-linked: grant ${id} stopped\n  link      ${LINK}\n`, err: '' });
  assert.deepEqual(await events('c-linked'), ['run.start', 'autopilot.grant', 'autopilot.link', 'autopilot.stop']);

  await newRun('c-nolink');
  const none = JSON.parse((await cli(['status', '--run', 'c-nolink', '--json'])).out);
  assert.deepEqual([none.state, none.link, none.files], ['none', null, null]);
});

test('status after the window: expired, one autopilot.expire row however many times it is asked; no grant; unknown run', async () => {
  await newRun('c-exp');
  await cli(['start', '--run', 'c-exp', '--until', UNTIL, '--delegate', 'L2', '--allow', 'waive:nit', '--yes']);
  const first = await cli(['status', '--run', 'c-exp'], { now: at(130) });
  assert.equal(first.code, 0);
  assert.equal(first.out.split('\n')[0], `autopilot run c-exp: expired at ${UNTIL_UTC}`);
  await cli(['status', '--run', 'c-exp', '--json'], { now: at(140) });
  const stop = await cli(['stop', '--run', 'c-exp'], { now: at(150) });
  assert.deepEqual(stop, { code: 1, out: '', err: `autopilot stop: run c-exp's autopilot grant already expired at ${UNTIL_UTC}\n` });
  assert.deepEqual(await events('c-exp'), ['run.start', 'autopilot.grant', 'autopilot.expire']);
  // a new grant may start once the old one expired
  assert.equal((await cli(['start', '--run', 'c-exp', '--until', '2026-10-07T03:00:00Z', '--delegate', 'L2', '--allow', 'waive:nit', '--yes'], { now: at(160) })).code, 0);
  assert.deepEqual(await events('c-exp'), ['run.start', 'autopilot.grant', 'autopilot.expire', 'autopilot.grant']);

  await newRun('c-none');
  assert.deepEqual(await cli(['status', '--run', 'c-none']), { code: 0, out: 'autopilot run c-none: no grant\n', err: '' });
  assert.equal(JSON.parse((await cli(['status', '--run', 'c-none', '--json'])).out).state, 'none');
  assert.deepEqual(await cli(['stop', '--run', 'c-none']), { code: 1, out: '', err: 'autopilot stop: run c-none has no autopilot grant\n' });
  assert.deepEqual(await cli(['status', '--run', 'c-missing']), { code: 1, out: '', err: 'autopilot status: no run record for c-missing\n' });
});

test('the clock is read again after the prompt: an --until that passed while the owner waited is refused, nothing stored', async () => {
  await newRun('c-wait');
  let calls = 0;
  const clock = () => (calls++ === 0 ? T0 : at(130)); // the owner answered 2 h 10 min later
  const ui = { confirm: async () => true, isCancel: () => false };
  const res = await cli(['start', '--run', 'c-wait', '--until', UNTIL, '--delegate', 'L2', '--allow', 'waive:nit'], { clock, isTTY: true, ui });
  assert.deepEqual(res, { code: 2, out: '', err: 'autopilot start: --until is in the past; give a time after now, at most 24 h ahead\n' });
  assert.equal(calls, 2);
  assert.equal((await readRun('c-wait')).autopilot, undefined);
  assert.deepEqual(await events('c-wait'), ['run.start']);
});

test('stop on an expired grant: "already expired", exit 1, the expire row once and no stop row', async () => {
  await newRun('c-stop-exp');
  await cli(['start', '--run', 'c-stop-exp', '--until', UNTIL, '--delegate', 'L2', '--allow', 'waive:nit', '--yes']);
  const id = (await readRun('c-stop-exp')).autopilot.grant_id;
  const first = await cli(['stop', '--run', 'c-stop-exp'], { now: at(121) });
  assert.deepEqual(first, { code: 1, out: '', err: `autopilot stop: run c-stop-exp's autopilot grant already expired at ${UNTIL_UTC}\n` });
  await cli(['stop', '--run', 'c-stop-exp'], { now: at(122) });
  const rows = await readAllRows('c-stop-exp');
  assert.deepEqual(rows.map((r) => r.event), ['run.start', 'autopilot.grant', 'autopilot.expire']);
  assert.deepEqual([rows[2].grant_id, rows[2].until, rows[2].ts], [id, UNTIL_UTC, at(121).toISOString()]); // ts = the first check's time
  assert.deepEqual(verifyRow(rows[2], await loadKey('c-stop-exp')), { ok: true });
});

test('start on an ended run is refused before any prompt: exit 1, nothing written', async () => {
  await newRun('c-start-ended');
  await endRun({ runId: 'c-start-ended', writeRow: (row) => appendRow(row, { slug: 'c-start-ended' }), probe: async () => null });
  const before = await events('c-start-ended');
  let asked = 0;
  const ui = { confirm: async () => (asked++, true), isCancel: () => false };
  const res = await cli(['start', '--run', 'c-start-ended', '--until', UNTIL, '--delegate', 'L2', '--allow', 'waive:nit'], { isTTY: true, ui });
  assert.deepEqual(res, { code: 1, out: '', err: 'autopilot start: run c-start-ended has ended\n' });
  assert.equal(asked, 0);
  assert.equal((await readRun('c-start-ended')).autopilot, undefined);
  assert.deepEqual(await events('c-start-ended'), before);
});

test('status on an ended run says the grant no longer acts', async () => {
  await newRun('c-ended');
  await cli(['start', '--run', 'c-ended', '--until', UNTIL, '--delegate', 'L2', '--allow', 'waive:nit', '--yes']);
  await endRun({ runId: 'c-ended', writeRow: (row) => appendRow(row, { slug: 'c-ended' }), probe: async () => null });
  const res = await cli(['status', '--run', 'c-ended'], { now: at(10) });
  assert.equal(res.out.split('\n')[0], `autopilot run c-ended: the run has ended; the grant no longer acts (was until ${UNTIL_UTC})`);
  assert.equal(JSON.parse((await cli(['status', '--run', 'c-ended', '--json'], { now: at(10) })).out).state, 'run-ended');
});

test('a coder may not run autopilot: the coder forbidden list (mergeForbidden) refuses the argv and flags the transcript line', () => {
  const list = mergeForbidden();
  for (const argv of [START, ['status', '--run', 'r1'], ['stop', '--run', 'r1']]) {
    assert.equal(isForbidden(['code-forge', 'autopilot', ...argv], list)?.id, 'code-forge-autopilot-from-coder');
  }
  const transcript = 'ran tests\n$ code-forge autopilot start --run r1 --until 2026-10-07T00:00:00+02:00 --delegate L2 --allow waive:nit --yes\ndone\n';
  assert.deepEqual(scanTranscript(transcript, list), [{ id: 'code-forge-autopilot-from-coder', line: 2 }]);
});
