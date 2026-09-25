import './support.mjs';
import assert from 'node:assert/strict';
import test from 'node:test';
import { mkdir, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { runGatesVerb } from '../../src/cli/gates.mjs';
import { readAllRows } from '../../src/ledger/write.mjs';
import { build as buildNode } from '../fixtures/repos/node/build.mjs';
import { buildTestRepo, captureStream, withTempDir } from './support.mjs';

/**
 * Built at runtime, never a literal — so this file carries no secret-shaped source literal
 * without a FAKE marker (this package's own `gates secret-scan`, or GitHub push protection,
 * scans committed source too).
 */
const plantedToken = ['sk-ant-', 'api03-', 'realLookingLeakedValue123456'].join('');

/** @param {string[]} args */
async function run(args) {
  const stdout = captureStream();
  const stderr = captureStream();
  const code = await runGatesVerb(args, { stdout, stderr });
  return { code, stdout: stdout.text, stderr: stderr.text };
}

test('gates detect --cwd <dir> prints the detected stack as JSON, exit 0', async () => {
  await withTempDir(async (dir) => {
    await buildNode(dir);
    const { code, stdout } = await run(['detect', '--cwd', dir]);
    assert.equal(code, 0);
    const parsed = JSON.parse(stdout);
    assert.equal(parsed.stack, 'node');
    assert.deepEqual(parsed.test, ['pnpm', 'test']);
  });
});

test('gates detect: a .code-forge.yml gates.test override wins over auto-detection', async () => {
  await withTempDir(async (dir) => {
    await buildNode(dir);
    await writeFile(
      path.join(dir, '.code-forge.yml'),
      ['version: 1', 'project:', '  slug: cli-test', 'provider: anthropic', 'gates:', '  test: ["custom", "runner"]', ''].join('\n'),
    );
    const { code, stdout } = await run(['detect', '--cwd', dir]);
    assert.equal(code, 0);
    const parsed = JSON.parse(stdout);
    assert.deepEqual(parsed.test, ['custom', 'runner']);
    // lint was NOT overridden in the config, so detection's own answer (the fixture's `lint`
    // script) still applies — the override is per-key, not "config present ⇒ ignore detection".
    assert.deepEqual(parsed.lint, ['pnpm', 'run', 'lint']);
  });
});

test('gates run --slug writes gate.red + gate.done ledger rows under a temp HOME, exit 1 on a failing gate', async () => {
  await withTempDir(async (dir) => {
    await writeFile(path.join(dir, 'package.json'), JSON.stringify({ name: 'x', scripts: { test: 'exit-1' } }));
    // Override the test command directly via config so it's a real, fast-failing command.
    await writeFile(
      path.join(dir, '.code-forge.yml'),
      ['version: 1', 'project:', '  slug: cli-run-test', 'provider: anthropic', 'gates:', '  test: ["node", "-e", "process.exit(1)"]', ''].join('\n'),
    );
    const { code, stdout } = await run(['run', '--cwd', dir, '--slug', 'cli-run-test']);
    assert.equal(code, 1);
    const parsed = JSON.parse(stdout);
    assert.equal(parsed.allOk, false);

    const rows = await readAllRows('cli-run-test');
    const redRows = rows.filter((r) => r.event === 'gate.red');
    const doneRows = rows.filter((r) => r.event === 'gate.done');
    // Exact counts: only `test` is configured (lint/types/format are null ⇒ skipped, never red),
    // so exactly 1 gate.red row naming the `test` gate and exactly 1 gate.done row — not "at
    // least one of each", which would also pass on a duplicate or a red row for the wrong gate.
    assert.equal(redRows.length, 1, `expected exactly 1 gate.red row, got ${redRows.length}: ${JSON.stringify(redRows)}`);
    assert.equal(redRows[0].gate, 'test');
    assert.equal(doneRows.length, 1, `expected exactly 1 gate.done row, got ${doneRows.length}`);
    assert.equal(doneRows[0].all_ok, false);
  });
});

test('gates secret-scan --file finds a planted token, exit 1, and never prints the token itself; a clean file exits 0', async () => {
  await withTempDir(async (dir) => {
    await writeFile(path.join(dir, 'leak.txt'), `token: ${plantedToken}\n`);
    await writeFile(path.join(dir, 'clean.txt'), 'nothing to see here\n');
    const dirty = await run(['secret-scan', '--cwd', dir, '--file', 'leak.txt']);
    assert.equal(dirty.code, 1);
    assert.equal(JSON.parse(dirty.stdout).count, 1);
    // The hit was found, but the token text itself must never reach stdout/stderr (plan §8.2) —
    // a secret-printing regression would otherwise go unnoticed by a test that only checks `count`.
    assert.ok(!dirty.stdout.includes(plantedToken), 'stdout must not contain the planted token');
    assert.ok(!dirty.stderr.includes(plantedToken), 'stderr must not contain the planted token');

    const clean = await run(['secret-scan', '--cwd', dir, '--file', 'clean.txt']);
    assert.equal(clean.code, 0);
    assert.equal(JSON.parse(clean.stdout).count, 0);
  });
});

test('gates safe-edit --base reports ok on an untouched repo', async () => {
  await withTempDir(async (dir) => {
    const { baseSha } = await buildTestRepo(dir);
    const { code, stdout } = await run(['safe-edit', '--cwd', dir, '--base', baseSha]);
    assert.equal(code, 0);
    assert.equal(JSON.parse(stdout).ok, true);
  });
});

test('gates scope --base reports the file set', async () => {
  await withTempDir(async (dir) => {
    const { baseSha } = await buildTestRepo(dir);
    await writeFile(path.join(dir, 'new.txt'), 'x\n');
    const { code, stdout } = await run(['scope', '--cwd', dir, '--base', baseSha]);
    assert.equal(code, 0);
    assert.deepEqual(JSON.parse(stdout).untracked, ['new.txt']);
  });
});

test('gates acceptance --clauses --tap: a test id that never RAN (missing, not failing) ⇒ exit 1, naming the clause and the missing test', async () => {
  await withTempDir(async (dir) => {
    const clausesPath = path.join(dir, 'clauses.json');
    const tapPath = path.join(dir, 'out.tap');
    await writeFile(clausesPath, JSON.stringify([{ clause: 'thing works', tests: ['t1'] }]));
    // t1 never appears anywhere in this TAP output — genuinely missing, not present-but-failing.
    await writeFile(tapPath, 'TAP version 13\nok 1 - other\n1..1\n');
    const { code, stdout } = await run(['acceptance', '--clauses', clausesPath, '--tap', tapPath]);
    assert.equal(code, 1);
    const parsed = JSON.parse(stdout);
    assert.equal(parsed.ok, false);
    assert.equal(parsed.clause, 'thing works');
    assert.equal(parsed.test, 't1');
  });
});

test('gates acceptance --clauses --tap: a test id that RAN and FAILED (not ok) ⇒ exit 1, the distinct failing-test path', async () => {
  await withTempDir(async (dir) => {
    const clausesPath = path.join(dir, 'clauses.json');
    const tapPath = path.join(dir, 'out.tap');
    await writeFile(clausesPath, JSON.stringify([{ clause: 'thing works', tests: ['t1'] }]));
    await writeFile(tapPath, 'TAP version 13\nnot ok 1 - t1\n1..1\n');
    const { code, stdout } = await run(['acceptance', '--clauses', clausesPath, '--tap', tapPath]);
    assert.equal(code, 1);
    const parsed = JSON.parse(stdout);
    assert.equal(parsed.ok, false);
    assert.equal(parsed.clause, 'thing works');
    assert.equal(parsed.test, 't1');
  });
});

test('gates transcript-grep --file: flags a forbidden line, exit 1', async () => {
  await withTempDir(async (dir) => {
    const filePath = path.join(dir, 'transcript.txt');
    await writeFile(filePath, '$ git push --force origin main\n');
    const { code, stdout } = await run(['transcript-grep', '--file', filePath]);
    assert.equal(code, 1);
    assert.equal(JSON.parse(stdout).hits.length, 1);
  });
});

test('an unknown subcommand and a missing required flag are usage errors (exit 2), not silent no-ops', async () => {
  const unknown = await run(['bogus-subcommand']);
  assert.equal(unknown.code, 2);
  assert.match(unknown.stderr, /usage:/);

  const missingBase = await run(['safe-edit', '--cwd', '/tmp']);
  assert.equal(missingBase.code, 2);
  assert.match(missingBase.stderr, /--base/);
});

test('a malformed --cwd (no value, or immediately followed by another flag) is a usage error, never a silent fallback to process.cwd()', async () => {
  // `--cwd` at the very end of argv: no value follows it at all.
  const trailing = await run(['detect', '--cwd']);
  assert.equal(trailing.code, 2);
  assert.match(trailing.stderr, /--cwd/);
  assert.equal(trailing.stdout, ''); // must NOT have silently run detect() against process.cwd()

  // `--cwd` immediately followed by another flag: no usable value either.
  const followedByFlag = await run(['safe-edit', '--cwd', '--base', 'HEAD']);
  assert.equal(followedByFlag.code, 2);
  assert.match(followedByFlag.stderr, /--cwd/);
});

test('a malformed --slug on `gates run` is a usage error, never a silent skip of the ledger rows', async () => {
  await withTempDir(async (dir) => {
    await buildNode(dir);
    const trailing = await run(['run', '--cwd', dir, '--slug']);
    assert.equal(trailing.code, 2);
    assert.match(trailing.stderr, /--slug/);
    assert.equal(trailing.stdout, ''); // must NOT have silently run the gates anyway
  });
});

test('the default export (the router\'s call shape: default(args)) dispatches to runGatesVerb', async () => {
  await withTempDir(async (dir) => {
    await buildNode(dir);
    const mod = await import('../../src/cli/gates.mjs');
    const code = await mod.default(['detect', '--cwd', dir]);
    assert.equal(code, 0);
  });
});
