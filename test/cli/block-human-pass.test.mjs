import { cfgFor, freshDir, git, writeFile } from '../review/helpers.mjs';
import assert from 'node:assert/strict';
import { execFile } from 'node:child_process';
import { existsSync, realpathSync } from 'node:fs';
import path from 'node:path';
import { test } from 'node:test';
import { fileURLToPath } from 'node:url';
import { startGrant } from '../../src/autopilot/grant.mjs';
import { runBlock } from '../../src/cli/block.mjs';
import { runReport } from '../../src/cli/report.mjs';
import { runRun } from '../../src/cli/run.mjs';
import { appendRow, readAllRows } from '../../src/ledger/write.mjs';
import { readRun, saveRun, startRun, writeSigned } from '../../src/state/run.mjs';
import { loadKey, verifyRow } from '../../src/state/signer.mjs';
import { contentHash } from '../../src/worker/ticket.mjs';

const PROBE = async () => 'start-A';
const NOW = new Date('2026-10-16T12:00:00Z');
const CHECKS = ['layout fits on a phone', 'tap targets work on a phone'];
const BIN = fileURLToPath(new URL('../../bin/code-forge.mjs', import.meta.url));

/** @param {Function} verb @param {string[]} args @param {Record<string, any>} [deps] */
async function cli(verb, args, deps = {}) {
  let out = '';
  let err = '';
  const code = await verb(args, { stdout: { write: (s) => { out += s; } }, stderr: { write: (s) => { err += s; } }, probe: PROBE, ...deps });
  return { code, out, err };
}

/** @param {string} slug @param {boolean} [human] */
async function fixture(slug, human = true) {
  const repo = realpathSync(freshDir(slug));
  await git(['init', '-q', '-b', 'main'], repo);
  writeFile(repo, 'a.mjs', 'export const a = 1;\n');
  const config = { version: 1, project: { slug }, ...cfgFor(), gates: { test: ['node', '--test'], lint: null, types: null, format: null } };
  writeFile(repo, '.code-forge.yml', JSON.stringify(config));
  await git(['add', '-A'], repo);
  await git(['commit', '-q', '-m', 'base'], repo);
  const files = freshDir('human-files');
  writeFile(files, 'acc.json', JSON.stringify([{ clause: 'renders', tests: ['renders'] }]));
  writeFile(files, 'coder.log', '$ node --test\n');
  const runId = `r-${slug}`;
  const writeRow = (row) => appendRow(row, { slug });
  await startRun({ workspace: repo, project: slug, runId, workerPid: 4242, probe: PROBE, config, writeRow });
  const opened = await cli(runBlock, ['open', 'B1', '--run', runId, '--level', 'L1', '--owned', 'a.mjs', '--acceptance', path.join(files, 'acc.json')]);
  assert.deepEqual([opened.code, opened.err], [0, '']);
  // Seed a loaded human-check block so the transition assertion also runs against the old loader.
  if (human) {
    const record = await readRun(runId);
    record.blocks.B1.human_checks = CHECKS;
    record.blocks.B1.acceptance.push(...CHECKS.map((clause) => ({ clause, kind: 'human_check' })));
    await saveRun(record);
  }
  writeFile(repo, 'a.mjs', 'export const a = 2;\n');
  await writeSigned(runId, writeRow, { event: 'review.approved', block: 'B1', file: 'a.mjs', content_hash: contentHash(repo, 'a.mjs'), round: 1 });
  const sign = (row) => writeSigned(runId, writeRow, { block: 'B1', ...row });
  const entry = async () => (await readRun(runId)).blocks.B1;
  const rows = async () => (await readAllRows(slug)).filter((r) => r.run === runId);
  const awaiting = async () => {
    const record = await readRun(runId);
    record.blocks.B1.status = 'awaiting_human_check';
    await saveRun(record);
    await sign({ event: 'block.close', status: 'complete' });
    await sign({ event: 'block.awaiting_human_check', checks: CHECKS });
  };
  let asked = 0;
  const pass = (extra = [], deps = {}) => cli(runBlock, ['human-pass', 'B1', '--run', runId, '--note', 'checked on my phone', ...extra], {
    isTTY: true, now: () => NOW,
    ui: { confirm: async ({ initialValue }) => { asked++; assert.equal(initialValue, false); return true; }, isCancel: () => false }, ...deps,
  });
  return { repo, files, runId, slug, sign, entry, rows, awaiting, pass, asked: () => asked,
    close: (extra = []) => cli(runBlock, ['close', 'B1', '--run', runId, '--worker-pid', '4242', '--transcript', path.join(files, 'coder.log'), ...extra]),
  };
}

test('B137 close: code gates pass, each human check and next command print, awaiting row is signed and no done marker is written', async () => {
  const f = await fixture('b137-close');
  await f.sign({ event: 'gate.done', all_ok: true });
  const r = await f.close();
  assert.deepEqual([r.code, r.err], [0, '']);
  assert.equal((await f.entry()).status, 'awaiting_human_check');
  for (const check of CHECKS) assert.equal(r.out.split(`block B1 awaiting human check: ${check}\n`).length - 1, 1);
  assert.match(r.out, new RegExp(`code-forge block human-pass B1 --run ${f.runId} --note`));
  const rows = await f.rows();
  assert.equal(rows.filter((r) => r.event === 'block.close').length, 1);
  const waiting = rows.filter((r) => r.event === 'block.awaiting_human_check');
  assert.equal(waiting.length, 1);
  assert.deepEqual(waiting[0].checks, CHECKS);
  assert.equal(verifyRow(waiting[0], await loadKey(f.runId)).ok, true);
  assert.equal(existsSync(path.join(process.env.HOME, '.code-forge', 'runs', f.runId, 'blocks', 'B1.done.json')), false);
});

test('B137 close boundary: missing green code gates keep a human-check block open with zero close rows', async () => {
  const f = await fixture('b137-close-red');
  // This new state must be retained even after a refused close.
  assert.deepEqual((await f.entry()).human_checks, CHECKS);
  const r = await f.close();
  assert.equal(r.code, 1);
  assert.match(r.err, /no green gates run/);
  assert.equal((await f.entry()).status, 'open');
  assert.deepEqual((await f.rows()).filter((r) => ['block.close', 'block.awaiting_human_check'].includes(r.event)), []);
});

test('B137 human-pass: terminal yes with optional proof writes exactly one signed human row, proof path or null, then closes', async () => {
  for (const withProof of [true, false]) {
    const f = await fixture(`b137-pass-${withProof ? 'proof' : 'no-proof'}`);
    await f.awaiting();
    const r = await f.pass(withProof ? ['--proof', f.files] : []);
    assert.deepEqual([r.code, r.err, f.asked()], [0, '', 1]);
    assert.equal((await f.entry()).status, 'closed');
    const rows = (await f.rows()).filter((r) => r.event === 'block.human_pass');
    assert.equal(rows.length, 1);
    assert.deepEqual([rows[0].block, rows[0].note, rows[0].proof, rows[0].checks, rows[0].by], ['B1', 'checked on my phone', withProof ? f.files : null, CHECKS, 'human']);
    assert.equal(verifyRow(rows[0], await loadKey(f.runId)).ok, true);
    assert.equal((await f.pass()).code, 1, 'a second pass must be refused');
  }
});

test('B137 human-pass boundary: both TTYs, confirmation, awaiting status and note are required; supplied proof must exist; active grant refuses', async () => {
  const f = await fixture('b137-pass-refuse');
  const wrong = await f.pass();
  assert.equal(wrong.code, 1);
  assert.match(wrong.err, /not awaiting/);
  await f.awaiting();
  const before = await f.rows();
  for (const deps of [{ isTTY: false }, { stdinIsTTY: false }, { stdoutIsTTY: false }]) {
    const r = await f.pass([], deps);
    assert.equal(r.code, 2);
    assert.match(r.err, /terminal/);
  }
  assert.equal((await f.pass(['--yes'])).code, 2);
  assert.equal((await f.pass(['--note', ''])).code, 2);
  assert.equal((await f.pass(['--proof', path.join(f.files, 'missing')])).code, 2);
  assert.equal((await f.pass([], { ui: { confirm: async () => false, isCancel: () => false } })).code, 1);
  await startGrant({ runId: f.runId, input: { until: '2026-10-16T13:00:00Z', delegate: 'L2', allow: 'waive:warning,waive:nit' }, now: NOW, writeRow: (r) => appendRow(r, { slug: f.slug }) });
  const grantRows = await f.rows();
  const active = await f.pass();
  assert.equal(active.code, 1);
  assert.match(active.err, /active/);
  assert.equal(f.asked(), 0, 'refusals happen before the confirmation');
  assert.equal((await f.entry()).status, 'awaiting_human_check');
  assert.deepEqual(await f.rows(), grantRows, 'refused pass writes zero rows');
  assert.equal(grantRows.length, before.length + 1, 'only the grant row was added');
});

test('B137 visibility: both status commands and report list awaiting ids without counting them completed; run end refuses until pass', async () => {
  const f = await fixture('b137-visible');
  await f.awaiting();
  const runStatus = await cli(runRun, ['status', '--run', f.runId]);
  assert.deepEqual(JSON.parse(runStatus.out).awaiting_human_check, ['B1']);
  const short = await new Promise((resolve, reject) => execFile(process.execPath, [BIN, 'status', '--run', f.runId, '--json'], { env: process.env }, (error, stdout) => error ? reject(error) : resolve(JSON.parse(stdout))));
  assert.deepEqual(short.runs[0].awaiting_human_check, ['B1']);
  assert.equal(short.runs[0].closed, 0);
  const report = await cli(runReport, ['--slug', f.slug, '--json']);
  const sections = JSON.parse(report.out);
  assert.deepEqual(sections.awaiting_human_check, ['B1']);
  assert.deepEqual(sections.cost_per_block.map((b) => [b.block, b.completed]), [['B1', false]]);
  const end = () => cli(runRun, ['end', '--run', f.runId], { stopWorker: async () => ({ stopped: true }) });
  const refused = await end();
  assert.equal(refused.code, 1);
  assert.match(refused.err, /awaiting.*B1/);
  assert.equal((await readRun(f.runId)).status, 'active');
  assert.equal((await f.pass()).code, 0);
  const after = JSON.parse((await cli(runReport, ['--slug', f.slug, '--json'])).out);
  assert.deepEqual(after.awaiting_human_check, []);
  assert.deepEqual(after.cost_per_block.map((b) => [b.block, b.completed]), [['B1', true]]);
  assert.equal((await end()).code, 0);
});

test('B137 visibility boundary: stopping an awaiting block clears the waiting report and allows run end', async () => {
  const f = await fixture('b137-stop');
  await f.awaiting();
  assert.equal((await cli(runBlock, ['stop', 'B1', '--run', f.runId, '--reason', 'device check failed'])).code, 0);
  assert.equal((await f.entry()).status, 'stopped');
  assert.deepEqual(JSON.parse((await cli(runReport, ['--slug', f.slug, '--json'])).out).awaiting_human_check, []);
  assert.equal((await cli(runRun, ['end', '--run', f.runId], { stopWorker: async () => ({ stopped: true }) })).code, 0);
});

test('B137 compatibility: a block without human checks closes normally and emits no new rows', async () => {
  const f = await fixture('b137-normal', false);
  await f.sign({ event: 'gate.done', all_ok: true });
  const r = await f.close();
  assert.deepEqual([r.code, r.err], [0, '']);
  assert.match(r.out, /\nblock B1 closed\n$/);
  assert.equal((await f.entry()).status, 'closed');
  assert.deepEqual((await f.rows()).filter((r) => r.event === 'block.awaiting_human_check' || r.event === 'block.human_pass'), []);
  assert.equal((await f.rows()).filter((r) => r.event === 'block.close').length, 1);
});
