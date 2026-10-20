import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { mkdirSync, mkdtempSync, readFileSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { test } from 'node:test';
import { fileURLToPath } from 'node:url';
import { appendRow, readAllRows } from '../../src/ledger/write.mjs';
import { startRun } from '../../src/state/run.mjs';
import { loadKey, verifyRow } from '../../src/state/signer.mjs';
import { withTempHome } from '../ledger/helpers.mjs';

const CLI = fileURLToPath(new URL('../../bin/code-forge.mjs', import.meta.url));
const slug = 'specialist-project';
const runId = 'r-specialist';
const digest = (bytes) => createHash('sha256').update(bytes).digest('hex');

async function project() {
  const root = mkdtempSync(path.join(os.tmpdir(), 'cf-record-'));
  mkdirSync(path.join(root, 'brand'));
  writeFileSync(path.join(root, '.code-forge.yml'), `version: 1\nproject: {slug: ${slug}}\n`);
  writeFileSync(path.join(root, 'brand/logo.svg'), '<svg/>\n');
  const file = path.join(root, 'outcome.json');
  writeFileSync(file, JSON.stringify({ skill: 'logo-designer', status: 'approved', approved_by: 'owner',
    files: [{ path: 'brand/logo.svg', sha256: '' }], cost_usd: 1.25, minutes: 12 }));
  const cli = (...args) => spawnSync(process.execPath, [CLI, 'outcome', ...args], {
    cwd: root, encoding: 'utf8', env: { ...process.env, CODE_FORGE_NO_ERROR_LOG: '1' },
  });
  const sealed = cli('seal', file);
  assert.equal(sealed.status, 0, sealed.stderr);
  await startRun({ workspace: root, project: slug, runId, writeRow: (row) => appendRow(row, { slug }) });
  return { root, file, cli, rows: async () => (await readAllRows(slug)).filter((r) => r.event === 'outcome.recorded') };
}

test('B154 clause 1: record checks a sealed approved outcome and appends exactly one correctly signed row', async () => {
  await withTempHome(async () => {
    const { root, file, cli, rows } = await project();
    const result = cli('record', file, '--root', root, '--json');
    assert.equal(result.status, 0, result.stderr);
    assert.equal(JSON.parse(result.stdout).ok, true);
    const recorded = await rows();
    assert.equal(recorded.length, 1, 'exactly one specialist outcome');
    const { event, skill, step, status, sha256, cost_usd, minutes } = recorded[0];
    assert.deepEqual({ event, skill, step, status, sha256, cost_usd, minutes }, {
      event: 'outcome.recorded', skill: 'logo-designer', step: null, status: 'approved',
      sha256: digest(readFileSync(file)), cost_usd: 1.25, minutes: 12,
    });
    assert.equal(recorded[0].run, runId);
    assert.deepEqual(verifyRow(recorded[0], await loadKey(runId)), { ok: true });
  });
});

test('B154 clause 1 negative: a changed artifact refuses recording and prints the same check problems', async () => {
  await withTempHome(async () => {
    const { root, file, cli, rows } = await project();
    writeFileSync(path.join(root, 'brand/logo.svg'), '<svg>changed</svg>');
    const checked = cli('check', file);
    const result = cli('record', file);
    assert.equal(result.status, 1, result.stderr);
    assert.equal(result.stderr, checked.stderr);
    assert.match(result.stderr, /logo\.svg.*sha256/);
    assert.equal(result.stdout, '');
    assert.deepEqual(await rows(), [], 'no specialist outcome on failure');
    assert.equal((await readAllRows(slug)).length, 1, 'only the pre-existing run start remains');
    const json = cli('record', file, '--json');
    assert.equal(json.status, 1, json.stderr);
    assert.deepEqual(JSON.parse(json.stdout), JSON.parse(cli('check', file, '--json').stdout));
    assert.equal((await readAllRows(slug)).length, 1);
  });
});

test('B154 boundary: identical bytes and step are idempotent, different steps record separately, absent metrics stay null', async () => {
  await withTempHome(async () => {
    const { file, cli, rows } = await project();
    const obj = JSON.parse(readFileSync(file, 'utf8'));
    delete obj.cost_usd;
    delete obj.minutes;
    writeFileSync(file, JSON.stringify(obj));
    for (const step of ['design', 'publish']) {
      const result = cli('record', file, '--step', step);
      assert.equal(result.status, 0, result.stderr);
      if (step === 'design') {
        const duplicate = cli('record', file, '--step', step);
        assert.equal(duplicate.status, 0, duplicate.stderr);
        assert.equal(duplicate.stdout, 'already recorded\n');
      }
    }
    assert.deepEqual((await rows()).map((r) => [r.step, r.cost_usd, r.minutes]), [
      ['design', null, null], ['publish', null, null],
    ]);
    writeFileSync(file, `${JSON.stringify(obj)}\n`);
    assert.equal(cli('record', file, '--step', 'design').status, 0);
    assert.equal((await rows()).length, 3, 'changed file bytes are a new outcome');
  });
});

test('B154 pipeline: record infers the matching outcome step and an explicit step overrides it', async () => {
  await withTempHome(async () => {
    const { root, file, cli, rows } = await project();
    writeFileSync(path.join(root, 'code-forge.pipeline.yml'),
      'root: .\nsteps:\n  - {id: design, skill: logo-designer, outcome: ./outcome.json, approval: human}\n');
    const inferred = cli('record', file);
    assert.equal(inferred.status, 0, inferred.stderr);
    const overridden = cli('record', file, '--step', 'manual');
    assert.equal(overridden.status, 0, overridden.stderr);
    assert.deepEqual((await rows()).map((r) => r.step), ['design', 'manual']);
  });
});
