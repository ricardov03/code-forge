import { freshDir, git, writeFile } from '../review/helpers.mjs';
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { existsSync, readFileSync, realpathSync, unlinkSync } from 'node:fs';
import path from 'node:path';
import { test } from 'node:test';
import { startGrant } from '../../src/autopilot/grant.mjs';
import { runBlock } from '../../src/cli/block.mjs';
import { appendRow, readAllRows } from '../../src/ledger/write.mjs';
import { sealOutcome } from '../../src/outcome/outcome.mjs';
import { closeBlock } from '../../src/state/block.mjs';
import { readRun, startRun } from '../../src/state/run.mjs';
import { loadKey, verifyRow } from '../../src/state/signer.mjs';

const PROBE = async () => 'start-A';
const PIPELINE = JSON.stringify({ root: 'website-kit', steps: [
  { id: 'logo', skill: 'logo-designer', outcome: 'brand/outcome.json', approval: 'human' },
  { id: 'site', skill: 'website-kit', needs: ['logo'], outcome: 'site/outcome.json', approval: 'none' },
] });

async function fixture(slug) {
  const repo = realpathSync(freshDir(slug));
  await git(['init', '-q', '-b', 'main'], repo);
  writeFile(repo, 'base.mjs', 'export const base = 1;\n');
  writeFile(repo, 'code-forge.pipeline.yml', PIPELINE);
  await git(['add', '-A'], repo);
  await git(['commit', '-q', '-m', 'base'], repo);
  const files = freshDir('outcome-files');
  const trees = freshDir('outcome-trees');
  writeFile(files, 'acc.json', JSON.stringify([{ clause: 'works', tests: ['after-outcome'] }]));
  const runId = `r-${slug}`;
  const writeRow = (row) => appendRow(row, { slug });
  await startRun({ workspace: repo, project: slug, runId, workerPid: 4242, probe: PROBE, writeRow,
    config: { proof: { isolation: 'worktree', worktree: { dir: trees } } },
  });
  const rows = () => readAllRows(slug);
  const record = () => readRun(runId);
  const open = async (id, extra = []) => {
    let out = '';
    let err = '';
    const code = await runBlock(['open', id, '--run', runId, '--level', 'L1', '--owned', `${id}.mjs`, '--acceptance', path.join(files, 'acc.json'), ...extra], {
      stdout: { write: (s) => { out += s; } }, stderr: { write: (s) => { err += s; } }, probe: PROBE,
    });
    return { code, out, err };
  };
  const outcome = (step, status = 'approved', extra = {}) => {
    const dir = step === 'logo' ? 'brand' : 'site';
    const file = path.join(repo, dir, 'outcome.json');
    writeFile(repo, `${dir}/asset.svg`, `<svg>${step}</svg>\n`);
    writeFile(repo, `${dir}/outcome.json`, JSON.stringify({ skill: step, status, files: [{ path: `${dir}/asset.svg`, sha256: '' }], ...extra }));
    assert.deepEqual(sealOutcome(file, { root: repo }), { ok: true, problems: [] });
    return file;
  };
  const snapshot = async () => ({ record: await record(), rows: await rows(), trees: await git(['worktree', 'list', '--porcelain'], repo) });
  const unchanged = async (before, id = 'CODE') => {
    assert.deepEqual(await snapshot(), before, 'refusal must preserve the run, every row and all registered worktrees');
    assert.equal(existsSync(path.join(trees, id)), false, 'refusal must create zero block worktrees');
  };
  return { repo, trees, runId, writeRow, rows, record, open, outcome, snapshot, unchanged };
}

function refused(result, code, message) {
  assert.equal(result.code, 1, `expected ${code} exit 1: ${result.err}`);
  assert.deepEqual(JSON.parse(result.out), { block: 'CODE', event: 'refused', code, message });
  assert.equal(result.err, `block open: ${message}\n`);
}

test('outcome gate refuses missing, draft and changed sealed files with the first problem and no writes', async () => {
  const f = await fixture('outcome-not-ready');
  await startGrant({ runId: f.runId, input: { until: '2026-01-01T01:00:00Z', delegate: 'L2', allow: 'waive:warning' }, now: new Date('2026-01-01T00:00:00Z'), writeRow: f.writeRow });
  for (const state of ['missing', 'draft', 'changed']) {
    if (state !== 'missing') f.outcome('logo', state === 'draft' ? 'draft' : 'approved');
    if (state === 'changed') writeFile(f.repo, 'brand/asset.svg', '<svg>changed after seal</svg>');
    const before = await f.snapshot();
    const message = `block CODE: --after-outcome step logo: ${state === 'missing' ? 'missing outcome' : state === 'draft' ? 'status "draft": must be approved' : 'file "brand/asset.svg": sha256 does not match'}`;
    refused(await f.open('CODE', ['--after-outcome', 'logo', 'site', '--json']), 'outcome-not-ready', message);
    await f.unchanged(before);
  }
});

test('outcome refusal boundary checks all supplied steps in order and reports only the first check problem', async () => {
  const f = await fixture('outcome-first-problem');
  f.outcome('logo');
  f.outcome('site', 'approved', { checks: [{ name: 'contrast', result: 'fail' }, { name: 'layout', result: 'fail' }] });
  const before = await f.snapshot();
  refused(await f.open('CODE', ['--after-outcome', 'logo', '--after-outcome=site', '--json']), 'outcome-not-ready', 'block CODE: --after-outcome step site: check "contrast": result is fail');
  await f.unchanged(before);
});

test('outcome gate rejects unknown ids, missing pipelines and invalid pipelines without writes', async () => {
  const f = await fixture('outcome-bad-pipeline');
  for (const problem of ['unknown', 'absent', 'invalid']) {
    if (problem === 'absent') unlinkSync(path.join(f.repo, 'code-forge.pipeline.yml'));
    if (problem === 'invalid') writeFile(f.repo, 'code-forge.pipeline.yml', JSON.stringify({ root: 'website-kit', steps: [] }));
    const before = await f.snapshot();
    const result = await f.open('CODE', ['--after-outcome', 'unknown', '--json']);
    assert.equal(result.code, 1, `bad pipeline/id must exit 1: ${result.err}`);
    const refusal = JSON.parse(result.out);
    assert.deepEqual(Object.keys(refusal).sort(), ['block', 'code', 'event', 'message']);
    assert.deepEqual([refusal.block, refusal.event, refusal.code], ['CODE', 'refused', 'bad-after-outcome']);
    assert.match(refusal.message, problem === 'unknown' ? /unknown/ : problem === 'absent' ? /no code-forge\.pipeline\.yml/ : /steps.*must NOT have fewer than 1 items/);
    assert.equal(result.err, `block open: ${refusal.message}\n`);
    await f.unchanged(before);
  }
});

test('combined dependency boundary preserves after refusals before checking outcomes', async () => {
  const f = await fixture('outcome-after-order');
  assert.equal((await f.open('DESIGN')).code, 0);
  f.outcome('logo');
  const before = await f.snapshot();
  for (const step of ['logo', 'unknown']) {
    refused(await f.open('CODE', ['--after', 'DESIGN', '--after-outcome', step, '--json']), 'after-not-done', 'block CODE: --after dependencies not closed: DESIGN (open)');
    await f.unchanged(before);
  }
  refused(await f.open('CODE', ['--after', 'MISSING', 'CODE', '--after-outcome', 'logo', '--json']), 'bad-after', 'block CODE cannot name itself in --after');
  await f.unchanged(before);
});

test('accepted outcomes record unique step ids and exact outcome byte hashes on the entry and signed dispatch', async () => {
  const f = await fixture('outcome-record');
  const files = [f.outcome('logo'), f.outcome('site')];
  const expected = files.map((file, i) => ({ step: ['logo', 'site'][i], sha256: createHash('sha256').update(readFileSync(file)).digest('hex') }));
  const result = await f.open('CODE', ['--after-outcome=logo', 'site', '--after-outcome', 'logo', '--json']);
  assert.equal(result.code, 0, result.err);
  assert.equal(JSON.parse(result.out).event, 'open');
  assert.deepEqual((await f.record()).blocks.CODE.after_outcome, expected);
  const dispatch = (await f.rows()).filter((row) => row.block === 'CODE' && row.event === 'dispatch');
  assert.equal(dispatch.length, 1, 'exactly one accepted dispatch');
  assert.deepEqual(dispatch[0].after_outcome, expected);
  assert.equal(verifyRow(dispatch[0], await loadKey(f.runId)).ok, true);
  assert.equal(existsSync(path.join(f.trees, 'CODE')), true);
  assert.equal(existsSync(path.join(f.trees, 'CODE', 'brand/outcome.json')), false, 'accepted outcome lives only in the main project, not the block worktree');
});

test('recording boundary allows satisfied block dependencies and omits outcome metadata without the flag', async () => {
  const f = await fixture('outcome-optional');
  assert.equal((await f.open('DESIGN')).code, 0);
  assert.equal((await closeBlock({ runId: f.runId, id: 'DESIGN', rows: await f.rows(), writeRow: f.writeRow, livePid: 4242, probe: PROBE })).status, 'closed');
  f.outcome('logo');
  const result = await f.open('CODE', ['--after', 'DESIGN', '--after-outcome', 'logo', '--json']);
  assert.equal(result.code, 0, result.err);
  assert.deepEqual((await f.record()).blocks.CODE.after, ['DESIGN']);
  assert.equal((await f.record()).blocks.CODE.after_outcome.length, 1);
  assert.equal((await f.open('PLAIN')).code, 0);
  assert.equal(Object.hasOwn((await f.record()).blocks.PLAIN, 'after_outcome'), false);
  const dispatch = (await f.rows()).filter((row) => row.block === 'PLAIN' && row.event === 'dispatch');
  assert.equal(dispatch.length, 1);
  assert.equal(Object.hasOwn(dispatch[0], 'after_outcome'), false);
});
