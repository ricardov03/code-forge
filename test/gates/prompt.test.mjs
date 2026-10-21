import './support.mjs';
import assert from 'node:assert/strict';
import { readFile, writeFile } from 'node:fs/promises';
import path from 'node:path';
import test from 'node:test';
import { runGates } from '../../src/gates/run.mjs';
import { buildTestRepo, withTempDir } from './support.mjs';

const MESSAGE = 'gate ended on an interactive prompt; install the tool or make the command non-interactive';

test('zero-exit fixed and custom gates ending in a prompt fail, with non-interactive defaults in every gate', async () => {
  await withTempDir(async (cwd) => {
    const lines = ['Ok to proceed? (y)', 'Continue?', 'Install tool (y/n)', 'Proceed [y/N]', 'Proceed [Y/n]', 'Choose Yes/No', 'Ready?'];
    for (const [index, line] of lines.entries()) {
      const stream = index % 2 ? 'stderr' : 'stdout';
      const argv = [process.execPath, '-e', `console.log(JSON.stringify({CI: process.env.CI, npm_config_yes: process.env.npm_config_yes})); process.${stream}.write(${JSON.stringify(`${line.toUpperCase()}  \n\n  \n`)});`];
      const ran = await runGates({ test: argv, lint: argv, types: argv, format: argv, custom: [{ name: 'build', argv }] }, { cwd });
      assert.equal(ran.allOk, false, line);
      assert.equal(ran.results.length, 5);
      for (const result of ran.results) {
        assert.equal(result.ok, false, `${result.gate}: ${line}`);
        assert.equal(result.code, 0);
        assert.equal(result.reason, 'prompt');
        assert.equal(result.stderr.split('\n').filter((entry) => entry === MESSAGE).length, 1);
        assert.deepEqual(JSON.parse(result.stdout.split('\n')[0]), { CI: '1', npm_config_yes: 'false' });
        assert.equal(result.env_keys, undefined, 'injected defaults are not user env keys');
      }
    }
  });
});

test('only the last non-empty line per stream counts; normal output stays green and gate env overrides defaults', async () => {
  await withTempDir(async (cwd) => {
    const argv = [process.execPath, '-e', 'console.log("Ok to proceed? (y)\\ninstalled"); console.error("Continue?\\ndone"); console.log(JSON.stringify({CI: process.env.CI, npm_config_yes: process.env.npm_config_yes}));'];
    const gates = { test: argv, lint: { argv, env: { CI: '0', npm_config_yes: 'true' } }, custom: [{ name: 'build', argv }] };
    const ran = await runGates(gates, { cwd });
    assert.equal(ran.allOk, true);
    const results = ran.results.filter((result) => !result.skipped);
    assert.equal(results.length, 3);
    for (const result of results) {
      assert.equal(result.ok, true);
      assert.equal(result.reason, undefined);
      assert.deepEqual(JSON.parse(result.stdout.trim().split('\n').at(-1)), result.gate === 'lint'
        ? { CI: '0', npm_config_yes: 'true' } : { CI: '1', npm_config_yes: 'false' });
      assert.deepEqual(result.env_keys, result.gate === 'lint' ? ['CI', 'npm_config_yes'] : undefined);
    }
    const failed = await runGates({ test: [process.execPath, '-e', 'console.log("Continue?"); process.exit(2);'] }, { cwd });
    assert.equal(failed.allOk, false);
    assert.equal(failed.results[0].code, 2);
    assert.equal(failed.results[0].reason, undefined, 'nonzero exit retains its ordinary failure');
  });
});

test('test repeats receive the same env and a prompt on a repeat makes the gate red', async () => {
  await withTempDir(async (cwd) => {
    const { baseSha } = await buildTestRepo(cwd);
    await writeFile(path.join(cwd, 'a.test.mjs'), '// changed test\n');
    await writeFile(path.join(cwd, 'runner.mjs'), [
      "import { existsSync, readFileSync, writeFileSync } from 'node:fs';",
      "const calls = existsSync('calls.json') ? JSON.parse(readFileSync('calls.json', 'utf8')) : [];",
      'calls.push({CI: process.env.CI, npm_config_yes: process.env.npm_config_yes});',
      "writeFileSync('calls.json', JSON.stringify(calls));",
      "console.log(calls.length === 2 ? 'Ok to proceed? (y)' : 'done');",
    ].join('\n'));
    const ran = await runGates({
      test: { argv: [process.execPath, 'runner.mjs', '{test_files}'], env: { CI: '0' } },
      repeatNewTests: { n: 2, base: baseSha, owned: ['a.test.mjs'] },
    }, { cwd });
    assert.equal(ran.allOk, false);
    assert.equal(ran.results[0].reason, 'prompt');
    assert.deepEqual(ran.results[0].repeats, { n: 2, files: ['a.test.mjs'], failed: true });
    assert.match(ran.results[0].stderr, /gate ended on an interactive prompt/);
    assert.deepEqual(ran.results[0].env_keys, ['CI']);
    assert.deepEqual(JSON.parse(await readFile(path.join(cwd, 'calls.json'), 'utf8')),
      Array.from({ length: 3 }, () => ({ CI: '0', npm_config_yes: 'false' })));
  });
});
