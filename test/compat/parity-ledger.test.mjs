import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { mkdir, mkdtemp, readFile, rm, symlink, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { after, before, test } from 'node:test';
import { fileURLToPath } from 'node:url';
import { helpPageParity, runParity } from '../helpers/parity.mjs';
import { linkGoBinary } from '../helpers/go-binary.mjs';

const root = fileURLToPath(new URL('../../', import.meta.url));
// Installation tests rename/chmod the release binary in parallel. Build directly
// into a private package layout so every native spawn and Node handover is isolated.
const pkg = await mkdtemp(path.join(os.tmpdir(), `cf-ledger-package-${process.pid}-`));
const binary = path.join(pkg, 'native/bin', `${process.platform}-${process.arch}`, 'code-forge-native');
after(() => rm(pkg, { recursive: true, force: true }));
before(async () => {
  await mkdir(path.dirname(binary), { recursive: true });
  await mkdir(path.join(pkg, 'bin'));
  await symlink(path.join(root, 'bin/code-forge.mjs'), path.join(pkg, 'bin/code-forge.mjs'));
  const { version } = JSON.parse(await readFile(path.join(root, 'package.json'), 'utf8'));
  linkGoBinary(binary, { trimpath: true, ldflags: `-s -w -X main.version=${version} -X main.reviewWaitNodeVersion=${process.version}` });
});
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
  ];
  for (const args of cases) {
    assert.deepEqual(await runParity(['ledger', ...args], { fixture: f, goCommand: binary,
      env: { CODE_FORGE_NODE: unavailable } }), { equal: true, diffs: [] }, JSON.stringify(args) + ': zero differences');
  }
  // B613: Go wraps Node's usage in the help page; exit, stderr and state stay equal.
  const ledgerCommands = [['ledger add', 'Add coder spend that code-forge did not see'],
    ['ledger calibration', 'Show how reliable the recorded answers were'], ['ledger outcome', 'Record the CI result of a pull request'],
    ['ledger outside-review', 'Record an outside review of a pull request'], ['ledger tail', 'Print the last ledger rows']];
  for (const [args, page] of [[['tail', '--help'], { summary: 'Print the last ledger rows' }],
    [['--help'], { summary: 'Read the ledger or record outcomes and spend', commands: ledgerCommands }]]) {
    const result = await runParity(['ledger', ...args], { fixture: f, goCommand: binary, env: { CODE_FORGE_NODE: unavailable } });
    assert.deepEqual(result, helpPageParity(result, page), JSON.stringify(args) + ': only stdout differs, Go prints the help page around Node usage');
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
  // B541: B500 runs calibration, outcome, add and outside-review in Go, so these forms answer
  // without Node and still match Node's bytes.
  for (const sub of ['calibration', 'outcome', 'add', 'outside-review']) {
    const args = [sub, '--slug', 'absent'];
    const result = native(f, args);
    assert.doesNotMatch(result.stderr, /node executable/, sub + ': Go answers without Node');
    assert.notEqual(result.code, 127, sub + ': no failed Node lookup');
    assert.deepEqual(await runParity(['ledger', ...args], { fixture: f, goCommand: binary,
      env: { CODE_FORGE_NODE: unavailable } }), { equal: true, diffs: [] }, sub + ': Go without Node yields zero differences');
  }
  // A form Go still hands over (a review.done timestamp that is not a string: ledger-js-value).
  await mkdir(f.ledger, { recursive: true });
  await writeFile(path.join(f.ledger, 'odd.jsonl'), JSON.stringify({ event: 'review.done', file: 'a.mjs', ts: { at: 1 } }) + '\n');
  const oddArgs = ['outcome', '--slug', 'odd', '--scan-git'];
  const handed = native(f, oddArgs);
  assert.equal(handed.code, 127, 'odd row: exactly exit 127 when Node is unavailable');
  assert.equal(handed.stdout, '', 'odd row: no stdout before handover');
  assert.match(handed.stderr, /code-forge-native: node executable .* not found:/, 'odd row: Node lookup failed');
  assert.deepEqual(await runParity(['ledger', ...oddArgs], { fixture: f, goCommand: binary,
    env: { CODE_FORGE_NODE: process.execPath } }), { equal: true, diffs: [] }, 'odd row: working Node handover yields zero differences');
  await rm(f.ledger, { recursive: true });
  // Enable router logging in both harness copies (the harness normally disables it).
  const preload = path.join(f.repo, 'logging.cjs');
  await writeFile(preload, 'delete process.env.CODE_FORGE_NO_ERROR_LOG;');
  const loggedGo = [process.execPath, '--input-type=module', '-e',
    `import { spawnSync } from 'node:child_process'; delete process.env.CODE_FORGE_NO_ERROR_LOG; const r = spawnSync(${JSON.stringify(binary)}, process.argv.slice(1), { env: process.env, stdio: 'inherit' }); if (r.error) throw r.error; process.exit(r.status ?? 1);`];
  // The unavailable override makes any Node handover fail, including invalid-slug logging.
  for (const args of [[], ['tail'], ['tail', '--slug', 'absent', '--n', 'bad'], ['tail', '--slug', 'absent'], ['tail', '--slug', 'INVALID']]) {
    assert.deepEqual(await runParity(['ledger', ...args], { fixture: f, goCommand: loggedGo,
      env: { NODE_OPTIONS: '--require=' + preload, CODE_FORGE_NODE: unavailable } }),
    { equal: true, diffs: [] }, JSON.stringify(args) + ': output, breadcrumbs and error log match Node without handover');
  }
});
