import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { chmod, mkdir, mkdtemp, readFile, readdir, rm, symlink, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { after, before, test } from 'node:test';
import { fileURLToPath } from 'node:url';
import { runParity } from '../helpers/parity.mjs';
import { initialConfigState } from '../../src/state/config-snapshot.mjs';

const ROOT = fileURLToPath(new URL('../../', import.meta.url));
const CLI = path.join(ROOT, 'bin/code-forge.mjs');
const scratch = await mkdtemp(path.join(os.tmpdir(), 'cf-land-'));
const BINARY = path.join(scratch, 'package/native/bin', `${process.platform}-${process.arch}`, 'code-forge-native');
const RUN = 'r-land-fixture';
const args = ['block', 'land', 'B1', '--run', RUN, '--report', 'report.txt', '--transcript', 'transcript.txt'];
after(() => rm(scratch, { recursive: true, force: true }));
before(async () => {
  await mkdir(path.dirname(BINARY), { recursive: true });
  await mkdir(path.join(scratch, 'package/bin'));
  await symlink(CLI, path.join(scratch, 'package/bin/code-forge.mjs'));
  const build = spawnSync('go', ['build', '-o', BINARY, './cmd/code-forge-native'], {
    cwd: path.join(ROOT, 'native'), encoding: 'utf8', env: { ...process.env, GOPROXY: 'off', GOTOOLCHAIN: 'local' },
  });
  assert.equal(build.status, 0, build.stderr);
});

async function fixture(status = 'closed', config = {}) {
  const root = await mkdtemp(path.join(scratch, 'f-'));
  const f = { repo: path.join(root, 'repo'), home: path.join(root, 'home') };
  await mkdir(f.repo); await mkdir(f.home);
  const runs = path.join(f.home, '.code-forge/runs');
  await mkdir(runs, { recursive: true });
  const record = { run_id: RUN, project: 'land-fixture', workspace: f.repo, status: 'active',
    config: initialConfigState(config), blocks: status === 'unknown' ? {} : { B1: { status, owned_files: ['sample.mjs'] } }, orphans: [] };
  await writeFile(path.join(runs, `${RUN}.json`), JSON.stringify(record, null, 2) + '\n', { mode: 0o600 });
  await writeFile(path.join(runs, `${RUN}.key`), Buffer.alloc(32, 27), { mode: 0o600 });
  return f;
}
function launch(f, argv, env = {}) {
  return spawnSync(BINARY, argv, { cwd: f.repo, encoding: 'utf8', env: {
    PATH: process.env.PATH, HOME: f.home, CODE_FORGE_NATIVE: '0', CODE_FORGE_NODE: '/nonexistent',
    CODE_FORGE_NO_ERROR_LOG: '1', CODE_FORGE_KEY_BACKEND: 'file', ...env,
  } });
}

test('early land refusals run in Go and preserve Node streams, exit codes and signed timing rows', async () => {
  for (const status of ['closed', 'unknown']) {
    for (const json of [false, true]) {
      const f = await fixture(status), argv = [...args, ...(json ? ['--json'] : [])];
      const result = launch(f, argv);
      assert.equal(result.status, 1, `${status}: native land refuses with Node unavailable`);
      assert.match(result.stderr, /^block B1 land stopped at join: /);
      const parity = await runParity(argv, { fixture: await fixture(status), goCommand: BINARY,
        env: { CODE_FORGE_NATIVE: '0', CODE_FORGE_NODE: '/nonexistent' } });
      assert.deepEqual(parity.diffs, [], `${status}, json=${json}: zero process and state differences`);
      const rows = (await readFile(path.join(f.home, '.code-forge/ledger/land-fixture.jsonl'), 'utf8')).trim().split('\n').map((line) => JSON.parse(line));
      assert.deepEqual(rows.map((r) => r.event), ['land.timing'], 'one timing row and no mutation rows');
    }
  }
});

test('land usage and bad max boundaries match Node without starting Node', async () => {
  for (const argv of [
    ['block', 'land'], ['block', 'land', 'B1'], args.slice(0, -2), [...args, 'extra'],
    [...args, '--max', 'wrong'], [...args, '--max', '9999999m'], [...args, '--json=true'],
    [...args, '--run', RUN], [...args, '--unknown'],
    ['block', 'land', 'B1', '--run', RUN, '--report', '--merge', 'report.txt', '--transcript', 'transcript.txt'],
    [...args, '--merge', '--merge'], [...args, '--no-merge-gate=false'],
  ]) {
    const f = await fixture(), direct = launch(f, argv);
    assert.equal(direct.status, 2, `${argv}: native usage exits 2`);
    const parity = await runParity(argv, { fixture: f, goCommand: BINARY,
      env: { CODE_FORGE_NATIVE: '0', CODE_FORGE_NODE: '/nonexistent' } });
    assert.deepEqual(parity.diffs, [], `${argv}: zero usage differences`);
  }
  const f = await fixture('open', { hooks: { on_block_end: { argv: ['/usr/bin/true'] } } });
  const runs = path.join(f.home, '.code-forge/runs');
  const before = await Promise.all((await readdir(runs)).sort().map(async (name) => [name, await readFile(path.join(runs, name))]));
  const unavailable = launch(f, [...args, '--json']);
  assert.equal(unavailable.status, 127, 'unsupported close hook hands over the entire land before Go writes');
  const probe = path.join(scratch, 'argv-node');
  await writeFile(probe, `#!${process.execPath}\nprocess.stdout.write(JSON.stringify(process.argv.slice(3)) + '\\n');\n`);
  await chmod(probe, 0o755);
  const forwarded = launch(f, [...args, '--json'], { CODE_FORGE_NODE: probe });
  assert.deepEqual([forwarded.status, forwarded.stdout, forwarded.stderr], [0, JSON.stringify([...args, '--json']) + '\n', ''], 'whole-land handover forwards every original argument exactly once');
  assert.deepEqual(await Promise.all((await readdir(runs)).sort().map(async (name) => [name, await readFile(path.join(runs, name))])), before, 'handover preserves all run bytes');
  assert.deepEqual((await readdir(path.join(f.home, '.code-forge'))).sort(), ['runs'], 'zero Go ledger rows or other state directories on handover');
});
