import './support.mjs';
import assert from 'node:assert/strict';
import { chmod, mkdir, rm, writeFile } from 'node:fs/promises';
import path from 'node:path';
import test from 'node:test';
import { runGates } from '../../src/gates/run.mjs';
import { detectGates } from '../../src/gates/detect.mjs';
import { runGatesVerb } from '../../src/cli/gates.mjs';
import { captureStream, git, withTempDir } from './support.mjs';

async function cli(args) {
  const stdout = captureStream(), stderr = captureStream();
  const code = await runGatesVerb(args, { stdout, stderr });
  return { code, stdout: stdout.text, stderr: stderr.text };
}

test('null exit gates explain signals and timeouts with both output tails, and the CLI prints them', async () => {
  await withTempDir(async (dir) => {
    for (const [reason, ending] of [
      ['SIGTERM', "process.kill(process.pid, 'SIGTERM')"],
      ['timeout', 'setInterval(() => {}, 1000)'],
    ]) {
      const script = `console.log('stdout tail'); console.error('stderr tail'); ${ending}`;
      await writeFile(path.join(dir, '.code-forge.yml'), [
        'version: 1', 'project: {slug: format-exits}', 'provider: anthropic',
        'gates:', `  format: ${JSON.stringify([process.execPath, '-e', script])}`, '',
      ].join('\n'));
      const ran = await cli(['run', '--cwd', dir, '--timeout-ms', '1000']);
      assert.equal(ran.code, 1, ran.stderr);
      const results = JSON.parse(ran.stdout).results.filter((r) => !r.skipped);
      assert.equal(results.length, 1, 'exactly the format command ran');
      const [result] = results;
      assert.equal(result.code, null, `${reason} produces a null exit code`);
      assert.equal(result.reason, reason, `${reason} is retained`);
      assert.match(result.message, new RegExp(reason));
      assert.match(result.message, /stdout tail/);
      assert.match(result.message, /stderr tail/);
      assert.match(ran.stderr, new RegExp(`format:.*${reason}`));
      assert.match(ran.stderr, /stdout tail/);
      assert.match(ran.stderr, /stderr tail/);
    }
  });
});

test('a null exit without signal or output explains a missing executable; numeric failures stay numeric', async () => {
  await withTempDir(async (dir) => {
    const ran = await runGates({
      format: [path.join(dir, 'missing-formatter')],
      lint: [process.execPath, '-e', 'process.exit(2)'],
    }, { cwd: dir });
    const format = ran.results.find((r) => r.gate === 'format');
    assert.equal(format.ok, false);
    assert.equal(format.code, null);
    assert.equal(format.reason, 'spawn-error');
    assert.match(format.message, /missing-formatter.*ENOENT/);
    const lint = ran.results.find((r) => r.gate === 'lint');
    assert.equal(lint.code, 2);
    assert.equal(lint.reason, undefined);
    assert.equal(ran.allOk, false);
  });
});

async function formatterFixture(dir, tool) {
  git(['init', '-q'], dir);
  await mkdir(path.join(dir, 'vendor/bin'), { recursive: true });
  await mkdir(path.join(dir, 'fake-bin'));
  const binary = path.join(dir, tool === 'pint' ? 'vendor/bin/pint' : 'fake-bin/npx');
  await writeFile(binary, `#!${process.execPath}\nconsole.log(JSON.stringify(process.argv.slice(2)));\n`);
  await chmod(binary, 0o755);
  await writeFile(path.join(dir, 'vendor/bin/pest'), 'fixture');
  await writeFile(path.join(dir, tool === 'pint' ? 'composer.json' : 'package.json'),
    JSON.stringify({ scripts: { test: 'exit 0' }, devDependencies: { prettier: '*' } }));
  await writeFile(path.join(dir, '.gitignore'), 'vendor/\nfake-bin/\nignored.*\n');
  const ext = tool === 'pint' ? 'php' : 'js';
  const tracked = [`-option.${ext}`, `space name.${ext}`, `unchanged.${ext}`];
  for (const file of [...tracked, `deleted.${ext}`, 'image.bin']) await writeFile(path.join(dir, file), 'fixture');
  git(['add', '.'], dir);
  git(['-c', 'commit.gpgsign=false', 'commit', '-qm', 'base'], dir);
  await rm(path.join(dir, `deleted.${ext}`));
  await writeFile(path.join(dir, `new.${ext}`), 'new fixture');
  await writeFile(path.join(dir, `ignored.${ext}`), 'must not check');
  return { ext, tracked, binary };
}

test('detected Pint and Prettier check tracked files, or only supported changed files, excluding ignored and deleted files', async () => {
  for (const tool of ['pint', 'prettier']) await withTempDir(async (dir) => {
    const { ext, tracked } = await formatterFixture(dir, tool);
    const oldPath = process.env.PATH;
    process.env.PATH = `${path.join(dir, 'fake-bin')}${path.delimiter}${oldPath}`;
    try {
      const format = detectGates(dir).format;
      const prefix = tool === 'pint' ? ['--test'] : ['prettier', '--check', '--ignore-unknown'];
      const full = await runGates({ format }, { cwd: dir });
      const fullResult = full.results.find((r) => r.gate === 'format');
      assert.equal(full.allOk, true, fullResult.stderr);
      const supported = tool === 'pint' ? tracked : [...tracked, 'package.json'].sort();
      assert.deepEqual(JSON.parse(fullResult.stdout), [...prefix, ...supported.map((f) => `./${f}`)], `${tool}: exact tracked paths, never a tree scan`);
      const changed = await runGates({ format }, { cwd: dir, scope: {
        scope: 'changed', files: [],
        changedFiles: [`space name.${ext}`, `new.${ext}`, `ignored.${ext}`, `deleted.${ext}`, 'image.bin'],
      } });
      assert.deepEqual(JSON.parse(changed.results.find((r) => r.gate === 'format').stdout),
        [...prefix, `./new.${ext}`, `./space name.${ext}`], `${tool}: exactly two existing supported changed paths`);
    } finally { process.env.PATH = oldPath; }
  });
});

test('an empty detected format scope skips the command while configured commands retain their exact argv', async () => {
  await withTempDir(async (dir) => {
    await formatterFixture(dir, 'pint');
    const format = detectGates(dir).format;
    const empty = await runGates({ format }, { cwd: dir, scope: { scope: 'changed', files: [], changedFiles: ['image.bin', 'ignored.php', 'deleted.php'] } });
    const result = empty.results.find((r) => r.gate === 'format');
    assert.equal(result.skipped, true, 'no supported changed files must never invoke bare pint --test');
    assert.equal(result.reason, 'no_changes');
    assert.equal(result.stdout, '', 'the fake formatter was not executed');
    await writeFile(path.join(dir, '.code-forge.yml'), [
      'version: 1', 'project: {slug: format-configured}', 'provider: anthropic', 'gates:',
      '  test: null', '  lint: null', '  types: null', '  format: [vendor/bin/pint, --test]', '',
    ].join('\n'));
    const configured = await cli(['run', '--cwd', dir]);
    assert.equal(configured.code, 0, configured.stderr);
    const configuredResult = JSON.parse(configured.stdout).results.find((r) => r.gate === 'format');
    assert.equal(configuredResult.skipped, false);
    assert.deepEqual(JSON.parse(configuredResult.stdout), ['--test'], 'user-configured Pint argv is unchanged');
  });
});
