import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { mkdir, mkdtemp, rm, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { test } from 'node:test';
import { fileURLToPath } from 'node:url';
import { runParity } from '../helpers/parity.mjs';

const root = fileURLToPath(new URL('../../', import.meta.url));
const binary = path.join(root, 'native/bin', `${process.platform}-${process.arch}`, 'code-forge-native');
const unavailable = '/nonexistent-b248-node';

async function fixture(t) {
  const dir = await mkdtemp(path.join(os.tmpdir(), 'cf-ledger-parity-'));
  t.after(() => rm(dir, { recursive: true, force: true }));
  const f = { repo: path.join(dir, 'repo'), home: path.join(dir, 'home') };
  await mkdir(f.repo); await mkdir(f.home);
  const ledger = path.join(f.home, '.code-forge', 'ledger');
  await mkdir(ledger, { recursive: true });
  const rows = Array.from({ length: 25 }, (_, i) => ({ event: 'sample', index: i, note: '[REDACTED]', nested: { z: '<é & 🐱>', a: [null, true, 1e-7] } }));
  await writeFile(path.join(ledger, 'sample.2.jsonl'), rows.slice(0, 4).map((row) => JSON.stringify(row)).join('\n') + '\n');
  await writeFile(path.join(ledger, 'sample.10.jsonl'), rows.slice(4, 8).map((row) => JSON.stringify(row)).join('\n') + '\n');
  await writeFile(path.join(ledger, 'sample.jsonl'), rows.slice(8).map((row) => JSON.stringify(row)).join('\n') + '\n{"torn":');
  await writeFile(path.join(ledger, 'empty.jsonl'), '');
  return { ...f, ledger, rows };
}

function native(f, args, node = unavailable) {
  const result = spawnSync(binary, ['ledger', ...args], {
    cwd: f.repo, encoding: 'utf8',
    env: { ...process.env, HOME: f.home, CODE_FORGE_NATIVE: '0', CODE_FORGE_NODE: node, CODE_FORGE_NO_ERROR_LOG: '1' },
  });
  assert.equal(result.error, undefined, 'native executable starts successfully');
  assert.equal(result.signal, null, 'native executable exits normally');
  return { code: result.status, stdout: result.stdout, stderr: result.stderr };
}

test('B248 clause: ledger tail counts and pure argument checks match Node with zero state or stream diffs, without Node', async (t) => {
  const f = await fixture(t);
  const cases = [
    ['tail', '--slug', 'sample'], ['tail', '--slug', 'sample', '--n', '3'],
    ['tail', '--slug', 'sample', '-n', '3'], ['tail', '--slug', 'sample', '--json'],
    ['tail', '--slug', 'sample', '-n', '0'], ['tail', '--slug', 'sample', '--n', '100'],
    ['tail', '--slug', 'sample', '--n', '9'.repeat(400)],
    ['tail', '--slug', 'empty'], ['tail', '--slug', 'absent'],
    [], ['unknown'], ['unknown', '--slug', 'sample'],
    ['tail'], ['tail', '--slug'], ['tail', '--slug', '--json'],
    ['tail', '--slug', 'sample', '--n'], ['tail', '--slug', 'sample', '-n'],
    ['tail', '--slug', 'sample', '--n', '--json'], ['tail', '--slug', 'sample', '--n', 'bad'],
    ['tail', '--slug', 'sample', '-n', '-1'], ['tail', '--slug', 'sample', '--n', '1.5'],
    ['tail', '--slug', 'sample', '--n', '1', '-n', '2'],
    ['tail', '--slug', 'sample', '--unknown'], ['--unknown'],
    ['tail', '--help'], ['--help'],
  ];
  for (const args of cases) {
    assert.deepEqual(await runParity(['ledger', ...args], { fixture: f, goCommand: binary,
      env: { CODE_FORGE_NODE: unavailable } }), { equal: true, diffs: [] }, JSON.stringify(args) + ': zero differences');
  }
  assert.deepEqual(native(f, ['tail', '--slug', 'sample', '-n', '3']), {
    code: 0, stdout: f.rows.slice(-3).map((row) => JSON.stringify(row)).join('\n') + '\n', stderr: '',
  }, 'exactly the final three ordered, redacted rows are emitted with no Node executable');
});

test('B248 boundary: missing state and unported ledger subcommands preserve Node handover', async (t) => {
  const f = await fixture(t);
  await rm(f.ledger, { recursive: true });
  assert.deepEqual(native(f, ['tail', '--slug', 'absent']), {
    code: 1, stdout: '', stderr: 'ledger tail: no ledger for slug "absent"; known slugs: none\n',
  }, 'a missing ledger directory is a native error, without creating state');
  assert.deepEqual(await runParity(['ledger', 'tail', '--slug', 'absent'], { fixture: f, goCommand: binary,
    env: { CODE_FORGE_NODE: unavailable } }), { equal: true, diffs: [] }, 'missing directory yields zero state and process differences');
  for (const sub of ['calibration', 'outcome', 'add', 'outside-review']) {
    const args = [sub, '--slug', 'absent'];
    const result = native(f, args);
    assert.equal(result.code, 127, sub + ': exactly exit 127 when Node is unavailable');
    assert.equal(result.stdout, '', sub + ': no stdout before handover');
    assert.match(result.stderr, /code-forge-native: node executable .* not found:/, sub + ': Node lookup failed');
    assert.deepEqual(await runParity(['ledger', ...args], { fixture: f, goCommand: binary,
      env: { CODE_FORGE_NODE: process.execPath } }), { equal: true, diffs: [] }, sub + ': working Node handover yields zero differences');
  }
  // Enable router logging in both harness copies (the harness normally disables it).
  const preload = path.join(f.repo, 'logging.cjs');
  await writeFile(preload, 'delete process.env.CODE_FORGE_NO_ERROR_LOG;');
  const loggedGo = [process.execPath, '--input-type=module', '-e',
    `import { spawnSync } from 'node:child_process'; delete process.env.CODE_FORGE_NO_ERROR_LOG; const r = spawnSync(${JSON.stringify(binary)}, process.argv.slice(1), { env: process.env, stdio: 'inherit' }); process.exit(r.status);`];
  for (const args of [[], ['tail'], ['tail', '--slug', 'absent', '--n', 'bad'], ['tail', '--slug', 'absent']]) {
    assert.deepEqual(await runParity(['ledger', ...args], { fixture: f, goCommand: loggedGo,
      env: { NODE_OPTIONS: '--require=' + preload, CODE_FORGE_NODE: unavailable } }),
    { equal: true, diffs: [] }, JSON.stringify(args) + ': breadcrumbs and error log have zero differences');
  }
});
