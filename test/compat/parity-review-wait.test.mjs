import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import { once } from 'node:events';
import { chmod, mkdir, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { after, before, test } from 'node:test';
import { setTimeout as delay } from 'node:timers/promises';
import { fileURLToPath } from 'node:url';
import { runParity } from '../helpers/parity.mjs';

const ROOT = fileURLToPath(new URL('../../', import.meta.url));
const BINARY = path.join(ROOT, 'native/bin', `${process.platform}-${process.arch}`, 'code-forge-native');
const TICKET = 'abcdef0123456789abcdef01';
const WAIT = ['review-file', '--wait', TICKET];
const NO_NODE = '/nonexistent-code-forge-node';
let scratch;
before(async () => { scratch = await mkdtemp(path.join(os.tmpdir(), 'review-wait-')); });
after(async () => { await rm(scratch, { recursive: true, force: true }); });

async function fixture() {
  const dir = await mkdtemp(path.join(scratch, 'case-'));
  const repo = path.join(dir, 'repo'), home = path.join(dir, 'home');
  await mkdir(repo); await mkdir(home);
  const git = spawnSync('git', ['init', '-q', '-b', 'main', repo], { encoding: 'utf8' });
  assert.equal(git.status, 0, `fixture git init: ${git.stderr}`);
  return { repo, home };
}

async function put(f, name, value) {
  const file = path.join(f.repo, '.code-forge', name);
  await mkdir(path.dirname(file), { recursive: true });
  await writeFile(file, typeof value === 'string' ? value : JSON.stringify(value));
}

/** @param {{repo: string, home: string}} f
 * @param {{registry?: boolean, start_time?: string, heartbeat_at?: string}} [options] */
async function worker(f, { registry = false, ...overrides } = {}) {
  const now = new Date().toISOString();
  const w = { pid: process.pid, start_time: 'unknown', run: 'fixture', started_at: now, heartbeat_at: now, ...overrides };
  await put(f, registry ? `workers/${w.pid}-${w.start_time}.json` : 'queue/worker.json', w);
}

async function done(f, result) {
  await put(f, `queue/${TICKET}.done`, { result: '.code-forge/reviews/answer.json' });
  await put(f, 'reviews/answer.json', result);
}

function launch(f, args, extra = {}) {
  const child = spawn(BINARY, args, { cwd: f.repo, env: {
    ...process.env, HOME: f.home, CODE_FORGE_NATIVE: '0', CODE_FORGE_NODE: NO_NODE,
    CODE_FORGE_NO_ERROR_LOG: '1', ...extra,
  }, stdio: ['ignore', 'pipe', 'pipe'] });
  let stdout = '', stderr = '';
  child.stdout.on('data', (chunk) => { stdout += chunk; });
  child.stderr.on('data', (chunk) => { stderr += chunk; });
  const ended = once(child, 'close').then(([code, signal]) => ({ code, signal, stdout, stderr }));
  return { child, ended };
}

async function parity(f, args, env = {}) {
  const result = await runParity(args, { fixture: f, goCommand: BINARY, env: {
    CODE_FORGE_NATIVE: '0', CODE_FORGE_NODE: NO_NODE, ...env,
  } });
  assert.deepEqual(result.diffs, [], `${args.join(' ')}: zero stream, exit, or state diffs`);
  assert.equal(result.equal, true, 'parity reports equality');
}

test('wait uses Go for done, worker_down and timeout; Node parity includes result summaries and state', async () => {
  const f = await fixture();
  await done(f, { status: 'reviewed', approved: true, note: '<&> café', nested: { z: 1, a: 2 } });
  const speed = await launch(f, WAIT).ended;
  assert.equal(speed.code, 0, 'done runs with an unavailable Node executable');
  assert.equal(speed.stderr, '', 'native done has zero stderr bytes');
  for (const result of [
    { status: 'reviewed', approved: true, note: '<&> café', nested: { z: 1, a: 2 } },
    { status: 'reviewed', findings: [{ id: 'f1', severity: 'high', line_start: 2, line_end: 5, claim: 'fix this', fix: 'change it' }, null, {}], queued_at: 'q', started_at: 's', finished_at: 'f', duration_ms: 1.5 },
    { status: 'stopped', stopped: 'review_cap', findings: [{ id: 'f2' }] },
    { status: 'split_required', tokens_in: 1000, budget: 900, section: 'Introduction' },
    { stopped: 'split_required', tokens_in: 'bad', budget: null },
    { status: 'answers', findings: [{ id: 'f1', verdict: 'resolved', reason: 'fixed' }, null] },
    { kind: 'answers', approved: true, findings: [{ id: 'ignored' }] },
  ]) { await done(f, result); await parity(f, WAIT); }
  for (const marker of ['bad JSON', '{}', '{"result":".code-forge/reviews/../outside"}', '{"result":"elsewhere"}']) {
    await put(f, `queue/${TICKET}.done`, marker); await parity(f, WAIT);
  }
  await done(f, '[]'); await parity(f, WAIT);
  await done(f, 'null'); await parity(f, WAIT);
  await done(f, 'bad JSON'); await parity(f, WAIT);
  await rm(path.join(f.repo, '.code-forge'), { recursive: true });
  await parity(f, [...WAIT, '--max', '0ms']); // Missing state: two down polls, not a timeout.
  await mkdir(path.join(f.repo, '.code-forge', 'queue'), { recursive: true });
  await parity(f, [...WAIT, '--max', '1ms']); // Empty queue and unknown ticket.
  for (const registry of [false, true]) {
    await worker(f, { registry });
    await parity(f, [...WAIT, '--max=0ms']);
    await parity(f, [...WAIT, '--max', '1ms', '--cwd', f.repo, '--run', 'ignored']);
  }
  await worker(f, { start_time: 'mismatched', heartbeat_at: '2000-01-01T00:00:00Z' });
  await rm(path.join(f.repo, '.code-forge', 'workers'), { recursive: true });
  await parity(f, [...WAIT, '--max', '0ms']);
  const start = spawnSync('/bin/ps', ['-o', 'lstart=', '-p', String(process.pid)], { encoding: 'utf8' });
  assert.equal(start.status, 0, 'process start-time probe succeeds');
  await worker(f, { start_time: start.stdout.trim().replace(/\s+/g, ' '), heartbeat_at: '2000-01-01T00:00:00Z' });
  await parity(f, [...WAIT, '--max', '0ms']);
  await done(f, { approved: true });
  await parity(f, WAIT, { CODE_FORGE_NODE: process.execPath, CODE_FORGE_NO_ERROR_LOG: '0' });
  const logged = await launch(f, WAIT, { CODE_FORGE_NO_ERROR_LOG: '0' }).ended;
  assert.equal(logged.code, 0, 'successful wait with logging enabled also runs without Node');

  // A linked worktree reads the main tree's completion, not a local queue.
  await writeFile(path.join(f.repo, 'sample.mjs'), 'export const value = 1;\n');
  for (const args of [
    ['add', 'sample.mjs'],
    ['-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.invalid', '-c', 'commit.gpgsign=false', 'commit', '-qm', 'fixture'],
    ['worktree', 'add', '-q', '-b', 'linked', 'linked'],
  ]) {
    const git = spawnSync('git', args, { cwd: f.repo, encoding: 'utf8' });
    assert.equal(git.status, 0, `linked fixture ${args[0]}: ${git.stderr}`);
  }
  await parity(f, [...WAIT, '--cwd', path.join(f.repo, 'linked')]);
});

test('wait stops at max and SIGTERM/SIGINT, reaping every observed child', { timeout: 15_000 }, async () => {
  const f = await fixture();
  await worker(f);
  const tools = path.join(scratch, 'tools'); await mkdir(tools);
  const timeoutPids = path.join(scratch, 'timeout-pids');
  // Record both git discovery children, including ones too short-lived for ps.
  await writeFile(path.join(tools, 'git'), `#!/bin/sh\necho $$ >> '${timeoutPids}'\nexec /usr/bin/git "$@"\n`);
  await chmod(path.join(tools, 'git'), 0o755);
  const before = performance.now();
  const timeout = launch(f, [...WAIT, '--max', '25ms'], { PATH: `${tools}:${process.env.PATH}` });
  const result = await timeout.ended;
  assert.equal(result.code, 0, 'native timeout succeeds without Node');
  assert.equal(result.stdout, `{"ticket":"${TICKET}","status":"pending","reason":"wait_timeout"}\n`, 'one exact timeout line');
  assert.equal(result.stderr, '', 'timeout has zero stderr bytes');
  assert.ok(performance.now() - before < 1500, '25ms max finishes within 1500ms including git discovery');
  assert.deepEqual(children(timeout.child.pid), [], 'zero children remain after wait_timeout');
  const observed = (await readFile(timeoutPids, 'utf8')).trim().split('\n').map(Number);
  assert.equal(observed.length, 2, 'timeout observed exactly two git discovery children');
  assert.deepEqual(observed.filter(alive), [], 'zero observed git children survive wait_timeout');

  // Hold git discovery open so the signal must cancel and reap an actual child.
  const pidFile = path.join(scratch, 'git-pid');
  await writeFile(path.join(tools, 'git'), `#!/bin/sh\necho $$ > '${pidFile}'\nexec /bin/sleep 30\n`);
  await chmod(path.join(tools, 'git'), 0o755);
  for (const signal of ['SIGTERM', 'SIGINT']) {
    await rm(pidFile, { force: true });
    const waiting = launch(f, [...WAIT, '--max', '90s'], { PATH: `${tools}:${process.env.PATH}` });
    let pid = 0;
    try {
      for (let i = 0; i < 150; i++) {
        try { pid = Number((await readFile(pidFile, 'utf8')).trim()); } catch { /* not started yet */ }
        if (pid > 1) break;
        await delay(10);
      }
      assert.ok(pid > 1, `${signal}: git child started before cancellation`);
      assert.ok(children(waiting.child.pid).includes(pid), `${signal}: tracked git belongs to Go`);
      waiting.child.kill(/** @type {NodeJS.Signals} */ (signal));
      const ended = await Promise.race([waiting.ended, delay(2000).then(() => null)]);
      assert.notEqual(ended, null, `${signal}: Go exits within two seconds`);
      assert.equal(ended.code, signal === 'SIGTERM' ? 143 : 130, `${signal}: conventional clean exit code`);
      assert.equal(ended.stdout, '', `${signal}: zero partial output bytes`);
      assert.equal(ended.stderr, '', `${signal}: zero error bytes`);
      assert.equal(alive(pid), false, `${signal}: observed git child ${pid} was reaped`);
      assert.deepEqual(children(waiting.child.pid), [], `${signal}: zero remaining children`);
    } finally {
      waiting.child.kill('SIGKILL');
      if (pid > 1 && alive(pid)) process.kill(pid, 'SIGKILL');
    }
  }
});

function children(pid) {
  const ps = spawnSync('/bin/ps', ['-axo', 'pid=,ppid='], { encoding: 'utf8' });
  assert.equal(ps.status, 0, 'child process probe succeeds');
  return ps.stdout.trim().split('\n').map((line) => line.trim().split(/\s+/).map(Number))
    .filter(([, parent]) => parent === pid).map(([child]) => child);
}
function alive(pid) { try { process.kill(pid, 0); return true; } catch { return false; } }

test('max boundaries stay native; usage, unknown flags and unported forms hand over with identical logs', async () => {
  const f = await fixture();
  await done(f, { approved: true });
  for (const max of ['0', '90', '90s', '1500ms', '9999999s']) {
    const result = await launch(f, [...WAIT, '--max', max]).ended;
    assert.equal(result.code, 0, `valid --max ${max} stays native`);
    await parity(f, [...WAIT, '--max', max]);
  }
  const cases = [
    [...WAIT, '--max', '-1'], [...WAIT, '--max', '1.5s'], [...WAIT, '--max', '10000000'],
    [...WAIT, '--max'], [...WAIT, '--max='], [...WAIT, '--wait', TICKET],
    ['review-file', '--wait', 'unknown'], ['review-file', '--wait'],
    [...WAIT, '--json'], [...WAIT, '--unknown'], [...WAIT, '--'],
    [...WAIT, '--cwd', 'missing-dir'], [...WAIT, '--cwd', 'plain-file'],
    [...WAIT, '--block', 'B1'], [...WAIT, 'sample.mjs'], [],
    ['review-file'], ['review-file', 'sample.mjs', '--block', 'B1'],
    ['review-file', '--wait-block', 'B1', '--run', 'bad'],
    ['review-file', '--rebase-check'], ['review-file', '--help'],
  ];
  await writeFile(path.join(f.repo, 'plain-file'), 'x');
  for (const args of cases) {
    await parity(f, args, { CODE_FORGE_NODE: process.execPath, CODE_FORGE_NO_ERROR_LOG: '0' });
    const result = await launch(f, args).ended;
    assert.equal(result.code, 127, `${args.join(' ')}: unported/error form requires Node`);
    assert.match(result.stderr, /node executable .* not found/, 'handover identifies missing Node');
  }
  await rm(path.join(f.repo, '.git'), { recursive: true });
  await parity(f, WAIT, { CODE_FORGE_NODE: process.execPath, CODE_FORGE_NO_ERROR_LOG: '0' });
});
