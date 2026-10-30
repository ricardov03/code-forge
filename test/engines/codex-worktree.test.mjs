import { alive, cfgWith, freshDir, sink, waitFor, writeIn } from '../session/helpers.mjs';
import assert from 'node:assert/strict';
import { execFileSync, spawnSync } from 'node:child_process';
import { chmodSync, existsSync, mkdirSync, readFileSync, realpathSync } from 'node:fs';
import path from 'node:path';
import { test } from 'node:test';

const { buildCodexArgv } = await import('../../src/engines/builders/codex.mjs');
const { CODEX_HOMES_DIR_NAME, removeCodexHome } = await import('../../src/engines/codex-home.mjs');
const { spawnSession } = await import('../../src/session/spawn.mjs');
const SPAWN_URL = new URL('../../src/cli/spawn.mjs', import.meta.url).href;
const CFG = cfgWith({ provider: 'openai', model: 'fake-gpt' });

test('background Codex home lives under the session run root and survives the spawning CLI process', async () => {
  for (const namedRun of [true, false]) {
    const cwd = freshDir('background-codex');
    const base = freshDir('background-runs');
    const home = freshDir('background-user');
    const ready = path.join(cwd, 'ready.json');
    const release = path.join(cwd, 'release');
    const checked = path.join(cwd, 'checked.json');
    const brief = writeIn(cwd, 'brief.md', 'fixture brief');
    writeIn(cwd, '.code-forge.yml', JSON.stringify({ ...CFG, tmp: { root: base } }));
    const fake = writeIn(cwd, 'codex.cjs', `#!${process.execPath}
const fs = require('node:fs');
const path = require('node:path');
const home = process.env.CODEX_HOME;
fs.writeFileSync(${JSON.stringify(ready)}, JSON.stringify({ home }));
const timer = setInterval(() => {
  if (!fs.existsSync(${JSON.stringify(release)})) return;
  clearInterval(timer);
  fs.writeFileSync(${JSON.stringify(checked)}, JSON.stringify({
    home, rules: fs.existsSync(path.join(home, 'rules', 'code-forge.rules'))
  }));
}, 25);
setTimeout(() => process.exit(0), 15000).unref();
`);
    chmodSync(fake, 0o700);
    const args = ['--level', 'L1', '--role', 'coder', '--brief', brief, '--cwd', cwd, '--background', ...(namedRun ? ['--run', 'fixture-run'] : [])];
    // Exercise the CLI handler in its own process; inject only the fake executable and row sink.
    const parent = spawnSync(process.execPath, ['--input-type=module', '-e', `
      const { runSpawn } = await import(${JSON.stringify(SPAWN_URL)});
      process.exitCode = await runSpawn(${JSON.stringify(args)}, {
        bins: { codex: ${JSON.stringify(fake)} }, writeRow: async () => {}
      });
    `], { cwd, encoding: 'utf8', timeout: 10000, env: { PATH: process.env.PATH, HOME: home, TMPDIR: process.env.TMPDIR, CODE_FORGE_TMP_ROOT: base } });
    assert.equal(parent.status, 0, parent.stderr);
    const result = JSON.parse(parent.stdout);
    try {
      assert.equal(result.status, 'started');
      assert.equal(await waitFor(() => existsSync(ready)), true, 'one fake coder started');
      const recorded = JSON.parse(readFileSync(ready, 'utf8'));
      const runRoot = path.dirname(path.dirname(result.sessionDir));
      writeIn(cwd, 'release', 'parent has exited');
      assert.equal(await waitFor(() => existsSync(checked)), true, 'coder checked its home after parent exit');
      assert.deepEqual({
        homesDir: path.dirname(recorded.home),
        homeExists: existsSync(recorded.home),
        child: JSON.parse(readFileSync(checked, 'utf8')),
      }, {
        homesDir: path.join(runRoot, CODEX_HOMES_DIR_NAME),
        homeExists: true,
        child: { home: recorded.home, rules: true },
      }, `named run: ${namedRun}`);
    } finally {
      if (alive(result.pid)) process.kill(-result.pid, 'SIGKILL');
    }
  }
});

test('foreground Codex uses an explicit session run root and removes its home after completion', async () => {
  const runRoot = freshDir('foreground-run');
  const cwd = freshDir('foreground-cwd');
  const brief = writeIn(cwd, 'brief.md', 'fixture brief');
  let home = '';
  let rulesPresent = false;
  const result = await spawnSession({ cfg: CFG, level: 'L1', role: 'coder', promptPath: brief, cwd, runRoot }, {
    stderr: sink(),
    exec: async (_argv, opts) => {
      home = opts.env.CODEX_HOME;
      rulesPresent = existsSync(path.join(home, 'rules', 'code-forge.rules'));
      return { result: 'ok', code: 0, signal: null, stdout: '', stderr: '', timedOut: false };
    },
  });
  assert.deepEqual([result.status, path.dirname(home), rulesPresent, existsSync(home)], [
    'ok', path.join(runRoot, CODEX_HOMES_DIR_NAME), true, false,
  ]);
});

test('Codex linked-worktree argv grants exactly the common git dir; normal checkouts and facts get none', () => {
  const repo = freshDir('git-main');
  const linked = path.join(freshDir('git-linked'), 'coder');
  const git = (...args) => execFileSync('git', args, { cwd: repo, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] }).trim();
  git('init', '-q');
  git('-c', 'user.name=fixture', '-c', 'user.email=fixture@example.invalid', 'commit', '--allow-empty', '-qm', 'fixture');
  git('worktree', 'add', '-qb', 'coder-fixture', linked);
  const common = realpathSync(path.join(repo, '.git'));
  const gitdir = path.join(common, 'worktrees', 'coder');
  const subdir = path.join(linked, 'nested');
  mkdirSync(subdir);
  for (const cwd of [repo, linked, subdir]) {
    const built = buildCodexArgv({ role: 'coder', model: 'fake-gpt', promptPath: '/brief.md', cwd, outPath: '/out.json' });
    try {
      const extra = cwd === repo ? [] : ['--add-dir', common];
      assert.deepEqual(built.argv, [
        'codex', 'exec', '-m', 'fake-gpt', '-s', 'workspace-write', ...extra,
        '-c', 'sandbox_workspace_write.exclude_tmpdir_env_var=true',
        '-c', 'sandbox_workspace_write.exclude_slash_tmp=true',
        '-c', 'approval_policy="never"', '-C', cwd, '--json', '-o', '/out.json', '/brief.md',
      ]);
      if (cwd !== repo) {
        assert.equal(existsSync(gitdir), true, 'common dir includes this linked worktree gitdir');
        assert.deepEqual(['objects', 'refs'].map((name) => existsSync(path.join(common, name))), [true, true]);
      }
    } finally {
      if (built.role === 'coder') removeCodexHome(built.env.CODEX_HOME);
    }
  }
  const facts = buildCodexArgv({ role: 'facts', model: 'fake-gpt', promptPath: '/brief.md', cwd: linked, outPath: '/out.json' });
  assert.equal(facts.argv.filter((arg) => arg === '--add-dir').length, 0, 'read-only facts never gain a writable git dir');
});
