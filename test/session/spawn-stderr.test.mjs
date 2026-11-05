import { alive, cfgWith, fakeDeps, freshDir, waitFor, writeIn } from './helpers.mjs';
import assert from 'node:assert/strict';
import { chmodSync, existsSync, readFileSync, statSync } from 'node:fs';
import path from 'node:path';
import { test } from 'node:test';

const { spawnSession } = await import('../../src/session/spawn.mjs');
const { scanTranscript } = await import('../../src/util/forbidden.mjs');
const { checkTranscript } = await import('../../src/gates/transcript-grep.mjs');
const { currentRunRoot } = await import('../../src/util/tmp.mjs');

const banner = 'Reading additional input from stdin...\n';
const events = [
  { type: 'thread.started', thread_id: 'stderr-test' },
  { type: 'item.completed', item: { type: 'command_execution', command: 'cat brief.md', aggregated_output: 'git stash', exit_code: 0 } },
];

/** @param {string} body */
function fakeCli(body) {
  const fake = writeIn(freshDir('stderr-cli'), 'codex.mjs', `#!/usr/bin/env node\n${body}\n`);
  chmodSync(fake, 0o755);
  const { deps } = fakeDeps();
  return { ...deps, bins: { ...deps.bins, codex: fake } };
}

/** @param {boolean} background @returns {import('../../src/session/spawn.mjs').SessionOpts} */
function options(background) {
  return {
    cfg: cfgWith({ provider: 'openai', model: 'fake-gpt' }), level: 'L2', role: 'coder',
    promptPath: writeIn(freshDir('stderr-brief'), 'brief.md', 'do nothing'),
    cwd: freshDir('stderr-cwd'), background, runRoot: currentRunRoot(),
  };
}

test('Background Codex keeps stdout JSONL and stderr in a private diagnostic log', async () => {
  const stdout = events.map((event) => JSON.stringify(event)).join('\n') + '\n';
  const deps = fakeCli(`process.stderr.write(${JSON.stringify(banner)});\nprocess.stdout.write(${JSON.stringify(stdout)});`);
  const result = await spawnSession(options(true), deps);
  try {
    assert.equal(await waitFor(() => !alive(result.pid)), true, 'fake CLI exits');
    const stderrPath = path.join(result.sessionDir, 'session.err.log');
    assert.deepEqual(readFileSync(result.logPath, 'utf8'), stdout, 'stdout contains exactly the two JSONL events');
    assert.equal(existsSync(stderrPath), true, 'separate stderr log exists');
    assert.equal(readFileSync(stderrPath, 'utf8'), banner);
    assert.equal(statSync(stderrPath).mode & 0o777, 0o600);
    const pidRecord = JSON.parse(readFileSync(result.pidFile, 'utf8'));
    assert.deepEqual([pidRecord.log, pidRecord.errLog, result.errLogPath], [result.logPath, stderrPath, stderrPath]);
    assert.deepEqual(scanTranscript(readFileSync(result.logPath, 'utf8')), []);
    assert.deepEqual(checkTranscript(readFileSync(result.logPath, 'utf8')), { ok: true, hits: [] });
  } finally {
    if (alive(result.pid)) process.kill(-result.pid, 'SIGKILL');
  }
});

test('An immediately failing CLI keeps its stderr message visible', async () => {
  const message = 'fake codex: authentication failed\n';
  const deps = fakeCli(`process.stderr.write(${JSON.stringify(message)});\nprocess.exitCode = 1;`);
  const result = await spawnSession(options(false), deps);
  assert.deepEqual([result.status, result.stderr], ['failed', message]);
  const background = await spawnSession(options(true), deps);
  try {
    assert.equal(await waitFor(() => !alive(background.pid)), true, 'failing fake CLI exits');
    const pidRecord = JSON.parse(readFileSync(background.pidFile, 'utf8'));
    assert.equal(typeof pidRecord.errLog, 'string', 'pid metadata points to the diagnostic log');
    assert.equal(readFileSync(pidRecord.errLog, 'utf8'), message, 'background failure remains readable');
    assert.equal(readFileSync(background.logPath, 'utf8'), '', 'stderr does not contaminate stdout');
  } finally {
    if (alive(background.pid)) process.kill(-background.pid, 'SIGKILL');
  }
});
