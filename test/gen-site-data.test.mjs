import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { after, test } from 'node:test';

const SCRIPT = fileURLToPath(new URL('../scripts/gen-site-data.mjs', import.meta.url));
const ROOT = mkdtempSync(path.join(os.tmpdir(), 'site-data-test-'));
const ENV = { ...process.env, GIT_CONFIG_NOSYSTEM: '1', GIT_CONFIG_GLOBAL: os.devNull,
  GIT_AUTHOR_NAME: 'Test', GIT_AUTHOR_EMAIL: 'test@example.invalid',
  GIT_COMMITTER_NAME: 'Test', GIT_COMMITTER_EMAIL: 'test@example.invalid' };
for (const key of Object.keys(ENV)) if (key.startsWith('GIT_') && !key.startsWith('GIT_CONFIG_') && !key.startsWith('GIT_AUTHOR_') && !key.startsWith('GIT_COMMITTER_')) delete ENV[key];
after(() => rmSync(ROOT, { recursive: true, force: true }));

const CHANGELOG = '# Changelog\n\n## [Unreleased]\n\n### Added\n\n- Future secret.\n\n' +
  '## [1.9.0] — 2026-10-01\n\n### Fixed\n\n- Older *fix*.\n\n' +
  '## [1.10.0] — 2026-11-06\n\n### Added\n\n- A **feature** with [docs](https://example.invalid).\n' +
  '  Continued `Markdown`.\n  - Nested item.\n\n- Second entry.\n\n### Fixed\n\n- A fix.\n\n' +
  '[Unreleased]: https://example.invalid/compare/v1.10.0...HEAD\n';
const COMMANDS = '# Command reference\n\n- [sample](#sample)\n\n## sample\n\nDo **sample** work.\n\n' +
  '```text\nusage: code-forge sample [--flag]\n```\n\n### sample list\n\n```text\nusage: code-forge sample list\n```\n';

/** @param {string} cwd @param {string[]} args */
function git(cwd, args) {
  const r = spawnSync('git', args, { cwd, env: ENV, encoding: 'utf8' });
  assert.equal(r.status, 0, r.stderr);
}

function fixture() {
  const dir = mkdtempSync(path.join(ROOT, 'case-'));
  const repo = path.join(dir, 'repo');
  const ledger = path.join(dir, 'ledger');
  mkdirSync(path.join(repo, 'docs/reference'), { recursive: true });
  mkdirSync(ledger);
  writeFileSync(path.join(repo, 'CHANGELOG.md'), CHANGELOG);
  writeFileSync(path.join(repo, 'docs/reference/commands.md'), COMMANDS);
  git(repo, ['init', '-q']);
  git(repo, ['add', '.']);
  git(repo, ['commit', '-qm', 'tagged docs']);
  git(repo, ['tag', '-a', 'v1.10.0', '-m', 'release']);
  return { repo, ledger, dir };
}

/** @param {string} cwd @param {string[]} args */
function generate(cwd, args) {
  // An absent new CLI fails an assertion here, before Node can throw an import/startup error.
  assert.equal(existsSync(SCRIPT), true, 'site-data CLI must be provided for the requested behavior');
  return spawnSync(process.execPath, [SCRIPT, ...args], { cwd, env: ENV, encoding: 'utf8', timeout: 15000 });
}

/** @param {ReturnType<typeof fixture>} f @param {string[]} [extra] */
function document(f, extra = []) {
  const r = generate(f.repo, ['--tag', 'v1.10.0', '--ledger-dir', f.ledger, ...extra]);
  assert.equal(r.status, 0, r.stderr);
  assert.equal(r.stderr, '', 'successful generation has zero diagnostics');
  return { data: JSON.parse(r.stdout), bytes: r.stdout };
}

test('tagged release docs supply version, date, all released notes newest first, and commands despite later edits', () => {
  const f = fixture();
  writeFileSync(path.join(f.repo, 'CHANGELOG.md'), '# Later working tree changelog\n');
  writeFileSync(path.join(f.repo, 'docs/reference/commands.md'), '# Later working tree commands\n');
  const { data } = document(f);
  assert.equal(data.version, '1.10.0');
  assert.equal(data.date, '2026-11-06', 'date comes from release heading, not annotated-tag clock');
  assert.deepEqual(data.changelog, [
    { version: '1.10.0', date: '2026-11-06', sections: [
      { name: 'Added', entries: ['A **feature** with [docs](https://example.invalid).\n  Continued `Markdown`.\n  - Nested item.', 'Second entry.'] },
      { name: 'Fixed', entries: ['A fix.'] },
    ] },
    { version: '1.9.0', date: '2026-10-01', sections: [{ name: 'Fixed', entries: ['Older *fix*.'] }] },
  ], 'exactly two releases, semver newest first, no Unreleased or link definitions');
  assert.deepEqual(data.commands, [{ name: 'sample', description: 'Do **sample** work.',
    usage: 'usage: code-forge sample [--flag]', subcommands: [{ name: 'list', usage: 'usage: code-forge sample list' }] }], 'exactly one tagged command and one subcommand');
});

test('boundary: an older tag selects its own version even if newer releases are in the tagged changelog', () => {
  const f = fixture();
  git(f.repo, ['tag', 'v1.9.0']);
  const r = generate(f.repo, ['--tag', 'v1.9.0', '--ledger-dir', f.ledger]);
  assert.equal(r.status, 0, r.stderr);
  const data = JSON.parse(r.stdout);
  assert.equal(data.version, '1.9.0');
  assert.equal(data.date, '2026-10-01');
  assert.deepEqual(data.changelog.map((entry) => entry.version), ['1.10.0', '1.9.0']);
});

test('unknown tag and missing ledger directory exit 1 and name the offending input', () => {
  const f = fixture();
  const badTag = generate(f.repo, ['--tag', 'v9.9.9', '--ledger-dir', f.ledger]);
  assert.equal(badTag.status, 1);
  assert.match(badTag.stderr, /v9\.9\.9/);
  assert.equal(badTag.stdout, '', 'zero JSON for unknown tag');
  const missing = path.join(f.dir, 'missing-ledger');
  const badDir = generate(f.repo, ['--tag', 'v1.10.0', '--ledger-dir', missing]);
  assert.equal(badDir.status, 1);
  assert.equal(badDir.stderr.includes(missing), true, 'diagnostic names missing directory');
  assert.equal(badDir.stdout, '', 'no misleading zero totals');
});

test('boundary: ledger path that is a regular file fails without creating the requested output', () => {
  const f = fixture();
  const file = path.join(f.dir, 'not-a-directory');
  const out = path.join(f.dir, 'site-data.json');
  writeFileSync(file, 'not a ledger directory');
  const r = generate(f.repo, ['--tag', 'v1.10.0', '--ledger-dir', file, '--out', out]);
  assert.equal(r.status, 1);
  assert.equal(r.stderr.includes(file), true);
  assert.equal(existsSync(out), false, 'zero output files on failure');
});

test('rotated ledger yields exact distinct runs, successful pairs and shared pricing without private fields', () => {
  const f = fixture();
  const privateFields = { slug: 'private-slug', path: '/private/customer/path', prompt: 'private-prompt', model: 'private-model' };
  const current = [
    { event: 'run.start', run: 'private-run-a', ...privateFields },
    { event: 'block.close', run: 'private-run-a', block: 'private-block', status: 'complete' },
    { event: 'block.close', run: 'private-run-a', block: 'private-block', status: 'complete' },
    { event: 'block.close', run: 'private-run-b', block: 'private-block', status: 'complete' },
    { event: 'block.close', run: 'private-run-b', block: 'failed-block', status: 'failed' },
    { event: 'block.close', run: 'private-run-b', block: 'missing-status' },
    { event: 'block.close', block: 'missing-run', status: 'complete' },
    { event: 'session', run: 'private-run-a', usd: 1.2345 },
    { event: 'session', run: 'private-run-a', usd: null, usd_unknown: true, tokens_in: 1000000 },
    { event: 'session', run: 'private-run-b', usd: 90, source: 'shadow' },
    { event: 'session.background', run: 'private-run-b', tokens_in: 1000000 },
  ];
  const rotated = [
    { event: 'run.start', run: 'private-run-a' },
    { event: 'session', run: 'private-run-b', provider: 'openai', level: 'L1', tokens_in: 1000, tokens_out: 1000 },
    { event: 'block.close', run: 'private-run-a', block: 'rotated-block', status: 'complete' },
    { event: 'session', cost_usd: 2.3456 },
  ];
  writeFileSync(path.join(f.ledger, 'code-forge.jsonl'), current.map((r) => JSON.stringify(r)).join('\n') + '\n');
  writeFileSync(path.join(f.ledger, 'code-forge.1.jsonl'), rotated.map((r) => JSON.stringify(r)).join('\n') + '\n');
  writeFileSync(path.join(f.ledger, 'code-forge.12.jsonl'), JSON.stringify({ event: 'run.start', run: 'private-run-c' }) + '\n');
  for (const name of ['other.jsonl', 'code-forge.extra.jsonl', 'code-forge.1.jsonl.bak']) {
    writeFileSync(path.join(f.ledger, name), JSON.stringify({ event: 'session', run: 'excluded-run', usd: 999 }) + '\n');
  }
  const { data, bytes } = document(f);
  assert.deepEqual(data.proof, { runs: 3, blocks: 3, spend: 3.59, date: '2026-11-06' });
  assert.deepEqual(Object.keys(data), ['version', 'date', 'changelog', 'commands', 'proof']);
  for (const value of [...Object.values(privateFields), 'private-run-a', 'private-run-b', 'private-run-c', 'private-block', 'rotated-block', f.ledger, 'openai', 'L1', 'excluded-run']) {
    assert.equal(bytes.includes(value), false, `zero copies of private value ${value}`);
  }
});

test('boundary: existing empty ledger directory produces exactly zero totals', () => {
  const { data } = document(fixture());
  assert.deepEqual(data.proof, { runs: 0, blocks: 0, spend: 0, date: '2026-11-06' });
});

test('same inputs give identical JSON bytes with stable key order and one trailing newline', () => {
  const f = fixture();
  const first = document(f);
  const second = document(f);
  assert.equal(first.bytes, second.bytes, 'both stdout documents are byte identical');
  assert.equal(first.bytes, JSON.stringify(first.data, null, 2) + '\n', 'canonical indentation and one trailing newline');
  assert.deepEqual(Object.keys(first.data.proof), ['runs', 'blocks', 'spend', 'date']);
});

test('boundary: --out writes the identical document and leaves stdout empty', () => {
  const f = fixture();
  const { bytes } = document(f);
  const out = path.join(f.dir, 'site-data.json');
  const r = generate(f.repo, ['--tag', 'v1.10.0', '--ledger-dir', f.ledger, '--out', out]);
  assert.equal(r.status, 0, r.stderr);
  assert.equal(r.stdout, '', 'zero stdout bytes with --out');
  assert.equal(readFileSync(out, 'utf8'), bytes, 'file and stdout have identical bytes');
});
