import { freshDir, git, writeFile } from '../review/helpers.mjs';
import assert from 'node:assert/strict';
import { existsSync, realpathSync } from 'node:fs';
import path from 'node:path';
import { test } from 'node:test';
import { startGrant } from '../../src/autopilot/grant.mjs';
import { runBlock } from '../../src/cli/block.mjs';
import { appendRow, readAllRows } from '../../src/ledger/write.mjs';
import { closeBlock, openBlock, stopBlock } from '../../src/state/block.mjs';
import { readRun, startRun } from '../../src/state/run.mjs';
import { loadKey, verifyRow } from '../../src/state/signer.mjs';

const PROBE = async () => 'start-A';

/** @param {string} slug @param {boolean} [isolated] */
async function fixture(slug, isolated = false) {
  const repo = realpathSync(freshDir(slug));
  await git(['init', '-q', '-b', 'main'], repo);
  writeFile(repo, 'base.mjs', 'export const base = 1;\n');
  await git(['add', '-A'], repo);
  await git(['commit', '-q', '-m', 'base'], repo);
  const files = freshDir('after-files');
  const trees = freshDir('after-trees');
  const acceptance = [{ clause: 'works', tests: ['after'] }];
  writeFile(files, 'acc.json', JSON.stringify(acceptance));
  const runId = `r-${slug}`;
  const writeRow = (row) => appendRow(row, { slug });
  await startRun({ workspace: repo, project: slug, runId, workerPid: 4242, probe: PROBE, writeRow,
    ...(isolated ? { config: { proof: { isolation: 'worktree', worktree: { dir: trees } } } } : {}),
  });
  const rows = () => readAllRows(slug);
  const record = () => readRun(runId);
  const cli = async (args, deps = {}) => {
    let out = '';
    let err = '';
    const code = await runBlock(args, { stdout: { write: (s) => { out += s; } }, stderr: { write: (s) => { err += s; } }, probe: PROBE, ...deps });
    return { code, out, err };
  };
  const open = (id, extra = []) => cli(['open', id, '--run', runId, '--level', 'L1', '--owned', `${id}.mjs`, '--acceptance', path.join(files, 'acc.json'), ...extra]);
  const close = async (id) => closeBlock({ runId, id, rows: await rows(), writeRow, livePid: 4242, probe: PROBE });
  return { repo, trees, runId, writeRow, acceptance, rows, record, cli, open, close };
}

test('B148: every unfinished or missing dependency is named; refusal writes no rows, entry or worktree', async () => {
  const f = await fixture('b148-refuse', true);
  for (const id of ['OPEN', 'STOP']) {
    const r = await f.open(id);
    assert.equal(r.code, 0, r.err);
  }
  // Use the state API to create a real human-check transition without running unrelated code gates.
  await openBlock({ runId: f.runId, id: 'HUMAN', level: 'L1', owned: ['human.md'], acceptance: [{ clause: 'approve design', kind: 'human_check' }], writeRow: f.writeRow });
  assert.equal((await f.close('HUMAN')).status, 'awaiting_human_check');
  await stopBlock({ runId: f.runId, id: 'STOP', reason: 'stopped', writeRow: f.writeRow });
  await startGrant({ runId: f.runId, input: { until: '2026-01-01T01:00:00Z', delegate: 'L2', allow: 'waive:warning' }, now: new Date('2026-01-01T00:00:00Z'), writeRow: f.writeRow });
  const before = await f.record();
  const rows = await f.rows();
  const trees = await git(['worktree', 'list', '--porcelain'], f.repo);
  const r = await f.open('CODE', ['--after', 'OPEN', 'HUMAN', 'STOP', 'MISSING', 'OPEN', '--json']);
  assert.equal(r.code, 1, 'unfinished dependencies must refuse with exit 1');
  const message = 'block CODE: --after dependencies not closed: OPEN (open), HUMAN (awaiting_human_check), STOP (stopped), MISSING (not in run)';
  assert.deepEqual(JSON.parse(r.out), { block: 'CODE', event: 'refused', code: 'after-not-done', message });
  assert.equal(r.err, `block open: ${message}\n`);
  assert.deepEqual(await f.record(), before, 'refusal must preserve the complete run record');
  assert.deepEqual(await f.rows(), rows, 'refusal must add zero rows');
  assert.equal(existsSync(path.join(f.trees, 'CODE')), false, 'no refused block worktree');
  assert.equal(await git(['worktree', 'list', '--porcelain'], f.repo), trees, 'no worktree registration added');
});

test('B148 refusal boundary: a closed dependency does not hide an unknown id; text refusal has no stdout', async () => {
  const f = await fixture('b148-unknown');
  assert.equal((await f.open('DONE')).code, 0);
  assert.equal((await f.close('DONE')).status, 'closed');
  const before = await f.record();
  const rows = await f.rows();
  const r = await f.open('CODE', ['--after', 'DONE', 'UNKNOWN']);
  assert.deepEqual(r, { code: 1, out: '', err: 'block open: block CODE: --after dependencies not closed: UNKNOWN (not in run)\n' });
  assert.deepEqual(await f.record(), before);
  assert.deepEqual(await f.rows(), rows);
});

test('B148: close and human-pass satisfy dependencies; entry and signed dispatch record unique ids only with the flag', async () => {
  const f = await fixture('b148-done');
  assert.equal((await f.open('DESIGN')).code, 0);
  assert.equal((await f.close('DESIGN')).status, 'closed');
  await openBlock({ runId: f.runId, id: 'HUMAN', level: 'L1', owned: ['human.md'], acceptance: [{ clause: 'approve design', kind: 'human_check' }], writeRow: f.writeRow });
  assert.equal((await f.close('HUMAN')).status, 'awaiting_human_check');
  const passed = await f.cli(['human-pass', 'HUMAN', '--run', f.runId, '--note', 'approved'], { isTTY: true, ui: { confirm: async () => true, isCancel: () => false } });
  assert.deepEqual([passed.code, passed.err], [0, '']);
  assert.equal((await f.record()).blocks.HUMAN.status, 'closed');
  const r = await f.open('CODE', ['--after=DESIGN', 'HUMAN', '--after', 'DESIGN', '--json']);
  assert.deepEqual([r.code, r.err], [0, '']);
  assert.equal(JSON.parse(r.out).event, 'open');
  assert.deepEqual((await f.record()).blocks.CODE.after, ['DESIGN', 'HUMAN']);
  const dispatch = (await f.rows()).filter((row) => row.block === 'CODE' && row.event === 'dispatch');
  assert.equal(dispatch.length, 1, 'exactly one code dispatch');
  assert.deepEqual(dispatch[0].after, ['DESIGN', 'HUMAN']);
  assert.equal(verifyRow(dispatch[0], await loadKey(f.runId)).ok, true);
  assert.equal((await f.open('PLAIN')).code, 0);
  assert.equal(Object.hasOwn((await f.record()).blocks.PLAIN, 'after'), false);
  const plain = (await f.rows()).filter((row) => row.block === 'PLAIN' && row.event === 'dispatch');
  assert.equal(plain.length, 1);
  assert.equal(Object.hasOwn(plain[0], 'after'), false);
});

test('B148 success boundary: next attempts recheck only the dependencies supplied on that call', async () => {
  const f = await fixture('b148-attempt');
  assert.equal((await f.open('DESIGN')).code, 0);
  assert.equal((await f.close('DESIGN')).status, 'closed');
  const first = await f.open('CODE', ['--after', 'DESIGN']);
  assert.deepEqual([first.code, first.err], [0, '']);
  assert.deepEqual((await f.record()).blocks.CODE.after, ['DESIGN']);
  assert.equal((await f.close('CODE')).status, 'closed');
  assert.equal((await f.open('DESIGN', ['--attempt', '2'])).code, 0);
  const before = await f.record();
  const rows = await f.rows();
  const refused = await f.open('CODE', ['--attempt', '2', '--after', 'DESIGN', '--json']);
  assert.equal(refused.code, 1);
  assert.equal(JSON.parse(refused.out).code, 'after-not-done');
  assert.deepEqual(await f.record(), before);
  assert.deepEqual(await f.rows(), rows);
  const next = await f.open('CODE', ['--attempt', '2']);
  assert.deepEqual([next.code, next.err], [0, '']);
  const entry = (await f.record()).blocks.CODE;
  assert.equal(entry.attempt, 2);
  assert.equal(Object.hasOwn(entry, 'after'), false);
  const dispatch = (await f.rows()).filter((row) => row.block === 'CODE' && row.event === 'dispatch');
  assert.equal(dispatch.length, 2);
  assert.deepEqual(dispatch[0].after, ['DESIGN']);
  assert.equal(Object.hasOwn(dispatch[1], 'after'), false);
});

test('B148: self dependency refuses with bad-after and records nothing', async () => {
  const f = await fixture('b148-self', true);
  const before = await f.record();
  const rows = await f.rows();
  const r = await f.open('CODE', ['--after', 'CODE', '--json']);
  assert.equal(r.code, 1);
  assert.deepEqual(JSON.parse(r.out), { block: 'CODE', event: 'refused', code: 'bad-after', message: 'block CODE cannot name itself in --after' });
  assert.deepEqual(await f.record(), before);
  assert.deepEqual(await f.rows(), rows);
  assert.equal(existsSync(path.join(f.trees, 'CODE')), false);
});

test('B148 self boundary: state callers also reject self among missing ids before worktree planning', async () => {
  const f = await fixture('b148-self-state');
  const before = await f.record();
  const rows = await f.rows();
  let planned = 0;
  await assert.rejects(() => openBlock({ runId: f.runId, id: 'CODE', level: 'L1', owned: ['code.mjs'], acceptance: f.acceptance, after: ['MISSING', 'CODE', 'CODE'], writeRow: f.writeRow,
    worktree: async ({ baseSha }) => {
      planned++;
      return { path: path.join(f.trees, 'CODE'), make: async () => ({ worktree: { path: path.join(f.trees, 'CODE'), branch: 'code', base_sha: baseSha }, created: false, rollback: null }) };
    },
  }), { code: 'bad-after', message: 'block CODE cannot name itself in --after' });
  assert.equal(planned, 0, 'self refusal must precede worktree planning');
  assert.deepEqual(await f.record(), before);
  assert.deepEqual(await f.rows(), rows);
});
