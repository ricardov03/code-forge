import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { after, test } from 'node:test';
import { fileURLToPath } from 'node:url';

const CLI = fileURLToPath(new URL('../../bin/code-forge.mjs', import.meta.url));
const TMP = mkdtempSync(path.join(os.tmpdir(), 'cf-outcome-'));
after(() => rmSync(TMP, { recursive: true, force: true }));
const digest = (text) => createHash('sha256').update(text).digest('hex');

function project() {
  const cwd = mkdtempSync(path.join(TMP, 'project-'));
  mkdirSync(path.join(cwd, 'brand'));
  writeFileSync(path.join(cwd, 'brand/logo.svg'), '<svg/>\n');
  writeFileSync(path.join(cwd, 'brand/readme.md'), 'Brand\n');
  const obj = { skill: 'logo-designer', status: 'approved', approved_by: 'owner',
    files: [{ path: 'brand/logo.svg', sha256: '' }, { path: 'brand/readme.md', sha256: '' }],
    checks: [{ name: 'review', result: 'pass' }] };
  const file = path.join(cwd, 'outcome.json');
  writeFileSync(file, JSON.stringify(obj));
  return { cwd, obj, file };
}

function cli(cwd, ...args) {
  return spawnSync(process.execPath, [CLI, ...args], {
    cwd, encoding: 'utf8', env: { ...process.env, CODE_FORGE_NO_ERROR_LOG: '1' },
  });
}

test('outcome seal fills all hashes with the selected root and preserves JSON key order', () => {
  const { cwd, obj, file } = project();
  const res = cli(TMP, 'outcome', 'seal', file, '--root', cwd);
  assert.equal(res.status, 0, res.stderr);
  obj.files[0].sha256 = digest('<svg/>\n');
  obj.files[1].sha256 = digest('Brand\n');
  assert.equal(readFileSync(file, 'utf8'), `${JSON.stringify(obj, null, 2)}\n`);
  const defaultRoot = cli(cwd, 'outcome', 'seal', 'outcome.json');
  assert.equal(defaultRoot.status, 0, defaultRoot.stderr);
  assert.equal(readFileSync(file, 'utf8'), `${JSON.stringify(obj, null, 2)}\n`);
});

test('outcome seal refuses a missing file or unsafe path without changing the outcome', () => {
  const { cwd, obj, file } = project();
  for (const badPath of ['brand/missing.svg', '../outside.svg', path.join(cwd, 'brand/logo.svg')]) {
    obj.files[1].path = badPath;
    const before = JSON.stringify(obj);
    writeFileSync(file, before);
    const res = cli(cwd, 'outcome', 'seal', file);
    assert.equal(res.status, 1, res.stderr);
    assert.equal(res.stdout, '');
    assert.equal(res.stderr.trim().split('\n').length, 1, 'exactly one problem line');
    assert.ok(res.stderr.includes(badPath), `the problem names ${badPath}`);
    assert.equal(readFileSync(file, 'utf8'), before, 'no partial hash update is written');
  }
});

test('outcome check accepts sealed approved outcomes and warn checks in text and JSON', () => {
  const { cwd, obj, file } = project();
  obj.files[0].sha256 = digest('<svg/>\n');
  obj.files[1].sha256 = digest('Brand\n');
  obj.checks[0].result = 'warn';
  writeFileSync(file, JSON.stringify(obj));
  const res = cli(cwd, 'outcome', 'check', file);
  assert.equal(res.status, 0, res.stderr);
  assert.equal(res.stdout, 'ok\n');
  assert.equal(res.stderr, '');
  const json = cli(TMP, 'outcome', 'check', file, '--root', cwd, '--json');
  assert.equal(json.status, 0, json.stderr);
  assert.equal(json.stderr, '');
  assert.deepEqual(JSON.parse(json.stdout), { ok: true, problems: [] });
});

test('outcome check reports one line per changed file, status, failed check and unsafe path', () => {
  const { cwd, obj, file } = project();
  obj.files[0].sha256 = digest('old logo');
  obj.files[1] = { path: '../outside.md', sha256: digest('outside') };
  obj.status = 'draft';
  obj.checks[0].result = 'fail';
  writeFileSync(file, JSON.stringify(obj));
  const res = cli(cwd, 'outcome', 'check', file);
  assert.equal(res.status, 1, res.stderr);
  const lines = (res.stdout + res.stderr).trim().split('\n');
  assert.equal(lines.length, 4, 'one line for each of four independent problems');
  for (const pattern of [/brand\/logo\.svg.*sha256/i, /status.*draft.*approved/i, /review.*fail/i, /\.\.\/outside\.md/]) {
    assert.equal(lines.filter((line) => pattern.test(line)).length, 1, `${pattern} appears once`);
  }
  const json = cli(cwd, 'outcome', 'check', file, '--json');
  assert.equal(json.status, 1, json.stderr);
  assert.equal(json.stderr, '');
  const result = JSON.parse(json.stdout);
  assert.deepEqual(Object.keys(result).sort(), ['ok', 'problems']);
  assert.equal(result.ok, false);
  assert.deepEqual(result.problems, lines);
});

test('outcome help is discoverable and documentation names both commands', () => {
  const res = cli(TMP, 'help');
  assert.equal(res.status, 0, res.stderr);
  assert.equal(res.stdout.split('\n').filter((line) => line === '  outcome').length, 1);
  const doc = new URL('../../docs/outcomes.md', import.meta.url);
  assert.equal(existsSync(doc), true, 'the outcomes reference exists');
  const text = readFileSync(doc, 'utf8');
  assert.match(text, /code-forge outcome seal/);
  assert.match(text, /code-forge outcome check/);
  assert.match(readFileSync(new URL('../../docs/README.md', import.meta.url), 'utf8'), /\[outcomes\.md\]\(outcomes\.md\)/);
});

test('outcome rejects unknown subcommands, missing arguments and invalid flags with usage exit 2', () => {
  for (const args of [[], ['unknown'], ['seal'], ['check'], ['seal', 'x.json', '--json'],
    ['check', 'x.json', '--root'], ['check', 'x.json', '--other'], ['check', 'x.json', 'extra']]) {
    const res = cli(TMP, 'outcome', ...args);
    assert.equal(res.status, 2, `${args.join(' ')}: ${res.stderr}`);
    assert.equal(res.stdout, '');
    assert.match(res.stderr, /^usage: code-forge outcome /);
    assert.equal(res.stderr.trim().split('\n').length, 1);
  }
});
