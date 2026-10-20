import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { copyFileSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { after, test } from 'node:test';
import { fileURLToPath } from 'node:url';
import { stringify } from 'yaml';

const CLI = fileURLToPath(new URL('../../bin/code-forge.mjs', import.meta.url));
const TMP = mkdtempSync(path.join(os.tmpdir(), 'cf-pipeline-cli-'));
after(() => rmSync(TMP, { recursive: true, force: true }));

function cli(cwd, ...args) {
  return spawnSync(process.execPath, [CLI, ...args], {
    cwd, encoding: 'utf8', env: { ...process.env, PATH: '', CODE_FORGE_NO_ERROR_LOG: '1' },
  });
}

function project(withPipeline = true) {
  const cwd = mkdtempSync(path.join(TMP, 'project-'));
  copyFileSync(new URL('../fixtures/config/valid-full.code-forge.yml', import.meta.url), path.join(cwd, '.code-forge.yml'));
  const pipeline = { root: 'website-kit', steps: [
    { id: 'logo', skill: 'logo-designer', outcome: 'brand/logo/outcome.json', approval: 'human' },
    { id: 'design', skill: 'website-kit', needs: ['logo'], outcome: 'design/outcome.json', approval: 'human' },
    { id: 'docs', skill: 'docs-designer', needs: ['design'], outcome: 'docs-site/outcome.json', approval: 'none' },
  ] };
  const file = path.join(cwd, 'code-forge.pipeline.yml');
  if (withPipeline) writeFileSync(file, stringify(pipeline));
  return { cwd, file, pipeline };
}

function outcome(cwd, step, status, extra = {}) {
  const file = path.join(cwd, step.outcome);
  mkdirSync(path.dirname(file), { recursive: true });
  const asset = `${step.id}.txt`;
  writeFileSync(path.join(cwd, asset), `${step.id} result\n`);
  writeFileSync(file, JSON.stringify({ skill: step.skill, status, files: [{ path: asset, sha256: '' }], ...extra }));
  if (status === 'approved') {
    const sealed = cli(TMP, 'outcome', 'seal', file, '--root', cwd);
    assert.equal(sealed.status, 0, sealed.stderr);
  }
  return file;
}

function json(cwd, command) {
  const result = cli(TMP, 'pipeline', command, '--root', cwd, '--json');
  assert.equal(result.status, 0, result.stderr);
  assert.equal(result.stderr, '');
  return JSON.parse(result.stdout);
}

test('pipeline status reports the chain through ready, waiting, draft, blocked, failed, approved and changed-file invalid', () => {
  const { cwd, file, pipeline } = project();
  const [logo, design, docs] = pipeline.steps;
  /** @returns {import('../../src/pipeline/pipeline.mjs').PipelineRow[]} */
  const rows = (states) => pipeline.steps.map((s, i) => ({ id: s.id, skill: s.skill, state: states[i] }));
  const initial = cli(cwd, 'pipeline', 'status');
  assert.equal(initial.status, 0, initial.stderr);
  assert.equal(initial.stderr, '');
  assert.equal(initial.stdout, 'logo  ready  logo-designer\ndesign  waiting  website-kit\ndocs  waiting  docs-designer\n');
  assert.deepEqual(json(cwd, 'status'), { exists: true, steps: rows(['ready', 'waiting', 'waiting']) });
  for (const state of ['draft', 'blocked', 'failed']) {
    outcome(cwd, logo, state);
    assert.deepEqual(json(cwd, 'status'), { exists: true, steps: rows([state, 'waiting', 'waiting']) });
  }
  const logoFile = outcome(cwd, logo, 'approved');
  const sealed = readFileSync(logoFile, 'utf8');
  assert.deepEqual(json(cwd, 'status'), { exists: true, steps: rows(['approved', 'ready', 'waiting']) });
  outcome(cwd, design, 'approved');
  assert.deepEqual(json(cwd, 'status'), { exists: true, steps: rows(['approved', 'approved', 'ready']) });
  outcome(cwd, docs, 'approved');
  assert.deepEqual(json(cwd, 'status'), { exists: true, steps: rows(['approved', 'approved', 'approved']) });
  writeFileSync(path.join(cwd, 'logo.txt'), 'changed after seal\n');
  const expected = rows(['invalid', 'waiting', 'waiting']);
  expected[0].problem = 'file "logo.txt": sha256 does not match';
  assert.deepEqual(json(cwd, 'status'), { exists: true, steps: expected });
  const invalid = cli(cwd, 'pipeline', 'status');
  assert.equal(invalid.status, 0, invalid.stderr);
  assert.equal(invalid.stdout, 'logo  invalid  logo-designer  — file "logo.txt": sha256 does not match\ndesign  waiting  website-kit\ndocs  waiting  docs-designer\n');
  assert.equal(readFileSync(logoFile, 'utf8'), sealed, 'status never reseals the outcome');
  assert.equal(readFileSync(file, 'utf8'), stringify(pipeline), 'status leaves the pipeline unchanged');
});

test('pipeline status marks malformed and schema-invalid outcomes invalid, checks approval, and applies waiting first', () => {
  const { cwd, pipeline } = project();
  const [logo, design] = pipeline.steps;
  const logoFile = outcome(cwd, logo, 'draft');
  const designFile = outcome(cwd, design, 'draft');
  writeFileSync(logoFile, '{broken');
  writeFileSync(designFile, '{broken');
  const malformed = json(cwd, 'status').steps;
  assert.deepEqual(malformed, [
    { id: 'logo', skill: 'logo-designer', state: 'invalid', problem: `Outcome ${JSON.stringify(logoFile)} is not valid JSON` },
    { id: 'design', skill: 'website-kit', state: 'waiting' },
    { id: 'docs', skill: 'docs-designer', state: 'waiting' },
  ]);
  writeFileSync(logoFile, JSON.stringify({ status: 'draft', files: [] }));
  const invalid = json(cwd, 'status').steps;
  assert.deepEqual(invalid, [{ ...malformed[0], problem: "/ must have required property 'skill'" }, ...malformed.slice(1)]);
  outcome(cwd, logo, 'approved', { checks: [{ name: 'review', result: 'fail' }] });
  assert.deepEqual(json(cwd, 'status').steps, [
    { ...malformed[0], problem: 'check "review": result is fail' }, ...malformed.slice(1),
  ]);
});

test('pipeline next lists ready ids, nothing ready with unmet needs, and all steps approved with exact JSON shapes', () => {
  const { cwd, pipeline } = project();
  const [logo, design, docs] = pipeline.steps;
  const waiting = [{ id: 'design', needs: ['logo'] }, { id: 'docs', needs: ['design'] }];
  const initial = cli(cwd, 'pipeline', 'next');
  assert.equal(initial.status, 0, initial.stderr);
  assert.equal(initial.stdout, 'logo\n');
  assert.deepEqual(json(cwd, 'next'), { exists: true, ready: ['logo'], waiting, done: false });
  outcome(cwd, logo, 'draft');
  const nothing = cli(cwd, 'pipeline', 'next');
  assert.equal(nothing.status, 0, nothing.stderr);
  assert.equal(nothing.stdout, 'nothing ready\ndesign  waiting for logo\ndocs  waiting for design\n');
  assert.deepEqual(json(cwd, 'next'), { exists: true, ready: [], waiting, done: false });
  for (const step of [logo, design, docs]) outcome(cwd, step, 'approved');
  const done = cli(cwd, 'pipeline', 'next');
  assert.equal(done.status, 0, done.stderr);
  assert.equal(done.stdout, 'all steps approved\n');
  assert.deepEqual(json(cwd, 'next'), { exists: true, ready: [], waiting: [], done: true });
});

test('pipeline next preserves file order, handles forward needs and reports only not-approved needs', () => {
  const { cwd, file, pipeline } = project();
  pipeline.steps.reverse();
  pipeline.steps[0].needs = ['logo', 'design'];
  writeFileSync(file, stringify(pipeline));
  assert.deepEqual(json(cwd, 'next'), {
    exists: true, ready: ['logo'], waiting: [{ id: 'docs', needs: ['logo', 'design'] }, { id: 'design', needs: ['logo'] }], done: false,
  });
  outcome(cwd, pipeline.steps[2], 'approved');
  assert.deepEqual(json(cwd, 'next'), {
    exists: true, ready: ['design'], waiting: [{ id: 'docs', needs: ['design'] }], done: false,
  });
  outcome(cwd, pipeline.steps[1], 'approved');
  assert.deepEqual(json(cwd, 'status').steps.map(({ id, state }) => ({ id, state })), [
    { id: 'docs', state: 'ready' }, { id: 'design', state: 'approved' }, { id: 'logo', state: 'approved' },
  ]);
  rmSync(path.join(cwd, pipeline.steps[2].outcome));
  pipeline.steps[0].needs = [];
  writeFileSync(file, stringify(pipeline));
  assert.deepEqual(json(cwd, 'next'), {
    exists: true, ready: ['docs', 'logo'], waiting: [{ id: 'design', needs: ['logo'] }], done: false,
  });
  const ready = cli(cwd, 'pipeline', 'next');
  assert.equal(ready.status, 0, ready.stderr);
  assert.equal(ready.stdout, 'docs\nlogo\n');
});

test('no pipeline gives one line and exit zero for both commands, JSON exists false, and unchanged validate output', () => {
  const { cwd } = project(false);
  const valid = cli(cwd, 'validate');
  assert.equal(valid.status, 0, valid.stderr);
  assert.equal(valid.stderr, '');
  assert.equal(valid.stdout, `[code-forge:info] ${path.join(cwd, '.code-forge.yml')}: valid (0 warnings)\n`);
  for (const command of ['status', 'next']) {
    const result = cli(cwd, 'pipeline', command);
    assert.equal(result.status, 0, result.stderr);
    assert.equal(result.stderr, '');
    assert.equal(result.stdout, `no code-forge.pipeline.yml in ${cwd}\n`);
    assert.deepEqual(json(cwd, command), { exists: false });
  }
});

test('pipeline is in help, rejects usage errors, and reports each invalid pipeline problem; validate uses the config folder', () => {
  const { cwd, file } = project();
  const help = cli(cwd, 'help');
  assert.equal(help.status, 0, help.stderr);
  assert.equal(help.stdout.split('\n').filter((line) => line === '  pipeline').length, 1);
  for (const args of [[], ['unknown'], ['status', '--root'], ['next', '--other'], ['status', 'extra'],
    ['next', '--json', '--json'], ['status', '--root', cwd, '--root', cwd]]) {
    const result = cli(cwd, 'pipeline', ...args);
    assert.equal(result.status, 2, `${args.join(' ')}: ${result.stderr}`);
    assert.equal(result.stdout, '');
    assert.equal(result.stderr, 'usage: code-forge pipeline status|next [--json] [--root <dir>]\n');
  }
  writeFileSync(file, 'root: website-kit\nsteps: []\nunexpected: true\n');
  for (const command of ['status', 'next']) {
    for (const flags of [[], ['--json']]) {
      const result = cli(cwd, 'pipeline', command, ...flags);
      assert.equal(result.status, 1, result.stderr);
      assert.equal(result.stdout, '');
      assert.equal(result.stderr.trim().split('\n').length, 2);
      assert.match(result.stderr, /unexpected/);
      assert.match(result.stderr, /steps/);
    }
  }
  const invalid = cli(TMP, 'validate', '--file', path.join(cwd, '.code-forge.yml'));
  assert.equal(invalid.status, 1, invalid.stderr);
  assert.equal(invalid.stdout, '');
  assert.equal(invalid.stderr.trim().split('\n').length, 2);
  assert.equal(invalid.stderr.split('\n').filter((line) => line.startsWith('[code-forge:error] ')).length, 2);
  writeFileSync(file, 'root: [broken');
  const malformed = cli(cwd, 'pipeline', 'status');
  assert.equal(malformed.status, 1, malformed.stderr);
  assert.equal(malformed.stdout, '');
  assert.equal(malformed.stderr.trim().split('\n').length, 1);
  assert.match(malformed.stderr, /YAML/);
});
