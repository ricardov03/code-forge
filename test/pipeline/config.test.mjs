import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { copyFileSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { after, test } from 'node:test';
import { fileURLToPath } from 'node:url';
import { stringify } from 'yaml';

const CLI = fileURLToPath(new URL('../../bin/code-forge.mjs', import.meta.url));
const TMP = mkdtempSync(path.join(os.tmpdir(), 'cf-pipeline-config-'));
after(() => rmSync(TMP, { recursive: true, force: true }));

function example() {
  return { root: 'website-kit', steps: [
    { id: 'logo', skill: 'logo-designer', outcome: 'brand/logo/outcome.json', approval: 'human' },
    { id: 'design', skill: 'website-kit', needs: ['logo'], outcome: 'design/outcome.json', approval: 'human' },
    { id: 'docs', skill: 'docs-designer', needs: ['design'], outcome: 'docs-site/outcome.json', approval: 'none' },
  ] };
}

function validate(pipeline) {
  const cwd = mkdtempSync(path.join(TMP, 'project-'));
  copyFileSync(new URL('../fixtures/config/valid-full.code-forge.yml', import.meta.url), path.join(cwd, '.code-forge.yml'));
  writeFileSync(path.join(cwd, 'code-forge.pipeline.yml'), stringify(pipeline));
  return spawnSync(process.execPath, [CLI, 'validate'], {
    cwd, encoding: 'utf8', env: { ...process.env, PATH: '', CODE_FORGE_NO_ERROR_LOG: '1' },
  });
}

test('pipeline accepts the example and refuses unknown needs, cycles and duplicate ids with the step id', () => {
  const valid = validate(example());
  assert.equal(valid.status, 0, valid.stderr);
  assert.equal(valid.stderr, '');
  for (const [label, change, pattern] of /** @type {[string, (p: any) => void, RegExp][]} */ ([
    ['unknown need', (p) => { p.steps[1].needs = ['missing']; }, /design.*missing/],
    ['cycle', (p) => { p.steps[0].needs = ['design']; }, /cycle.*(?:logo|design)/i],
    ['duplicate', (p) => { p.steps.push({ ...p.steps[0] }); }, /duplicate.*logo/i],
  ])) {
    const pipeline = example();
    change(pipeline);
    const result = validate(pipeline);
    assert.equal(result.status, 1, `${label}: ${result.stderr}`);
    assert.equal(result.stderr.trim().split('\n').length, 1, `${label}: one error`);
    assert.match(result.stderr, pattern);
  }
});

test('pipeline schema refuses bad approval, unknown fields, missing fields, invalid ids and non-relative outcomes', () => {
  const valid = validate(example());
  assert.equal(valid.status, 0, valid.stderr);
  for (const [label, change, pattern] of /** @type {[string, (p: any) => void, RegExp][]} */ ([
    ['approval', (p) => { p.steps[0].approval = 'auto'; }, /approval/],
    ['step key', (p) => { p.steps[0].unexpected = true; }, /unexpected/],
    ['root key', (p) => { p.unexpected = true; }, /unexpected/],
    ['root required', (p) => { delete p.root; }, /root/],
    ['empty steps', (p) => { p.steps = []; }, /steps/],
    ['id', (p) => { p.steps[0].id = 'Logo!'; }, /id/],
    ['needs type', (p) => { p.steps[1].needs = [7]; }, /needs/],
    ['absolute outcome', (p) => { p.steps[0].outcome = '/tmp/outcome.json'; }, /outcome/],
    ['Windows outcome', (p) => { p.steps[0].outcome = 'C:\\outcome.json'; }, /outcome/],
    ['parent outcome', (p) => { p.steps[0].outcome = '../outcome.json'; }, /outcome/],
    ...['id', 'skill', 'outcome', 'approval'].map((key) => [
      `${key} required`, (p) => { delete p.steps[0][key]; }, new RegExp(key),
    ]),
  ])) {
    const pipeline = example();
    change(pipeline);
    const result = validate(pipeline);
    assert.equal(result.status, 1, `${label}: ${result.stderr}`);
    assert.match(result.stderr, pattern);
    assert.equal(result.stderr.trim().split('\n').length, 1, `${label}: one error`);
  }
});
