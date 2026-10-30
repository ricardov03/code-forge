import './support.mjs';
import assert from 'node:assert/strict';
import { writeFile } from 'node:fs/promises';
import path from 'node:path';
import test from 'node:test';
import { runGates } from '../../src/gates/run.mjs';
import { runGatesVerb } from '../../src/cli/gates.mjs';
import { captureStream, withTempDir } from './support.mjs';

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
