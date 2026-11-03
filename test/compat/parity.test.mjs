import assert from 'node:assert/strict';
import { readFile, rm, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { after, before, test } from 'node:test';
import { fileURLToPath } from 'node:url';
import { makeParityFixture, runParity } from '../helpers/parity.mjs';

const CLI = fileURLToPath(new URL('../../bin/code-forge.mjs', import.meta.url));
const SIGNER = new URL('../../src/state/signer.mjs', import.meta.url).href;
const NODE = [process.execPath, CLI];
let fixture;
before(async () => { fixture = await makeParityFixture(); });
after(async () => { await fixture?.cleanup(); });

// A fake Go executable delegates to the real CLI, then changes exactly the chosen behavior.
// It never launches a worker or a model, and all mutations are in the disposable Go copy.
const FAKE = [process.execPath, '--input-type=module', '-e', `
  import { spawnSync } from 'node:child_process';
  import fs from 'node:fs';
  import path from 'node:path';
  import { signRow } from ${JSON.stringify(SIGNER)};
  const args = process.argv.slice(1);
  const result = spawnSync(process.execPath, [${JSON.stringify(CLI)}, ...args], { env: process.env, encoding: 'utf8' });
  if (result.error) throw result.error;
  const home = path.join(process.env.HOME, '.code-forge');
  const ledger = path.join(home, 'ledger', 'parity-fixture.jsonl');
  const mode = process.env.PARITY_MODE;
  let out = result.stdout, err = result.stderr, code = result.status;
  if (mode === 'isolation') {
    const input = fs.readFileSync(0, 'utf8');
    if (input !== 'parity input\\n' || process.env.PARITY_CUSTOM !== 'passed'
      || process.cwd() === process.env.PARITY_ORIGINAL_REPO || process.env.HOME === process.env.PARITY_ORIGINAL_HOME) code = 29;
  }
  if (mode === 'stdout') out = '!' + out.slice(1);
  if (mode === 'stderr') err += 'one stderr byte!';
  if (mode === 'exit') code = 17;
  if (mode === 'extra') {
    fs.mkdirSync(path.join(process.cwd(), '.code-forge', 'nested'), { recursive: true });
    fs.writeFileSync(path.join(process.cwd(), '.code-forge', 'nested', '.extra'), 'extra');
  }
  if (mode === 'clock-file') {
    const run = args[args.indexOf('--run') + 1];
    fs.copyFileSync(path.join(home, 'runs', run + '.json'), path.join(home, 'runs', 'r-20990101-000000-ffffff.json'));
  }
  if (['volatile', 'invalid', 'row', 'signed-row'].includes(mode)) {
    const rows = fs.readFileSync(ledger, 'utf8').trim().split('\\n').map(JSON.parse);
    const row = rows[0];
    const key = fs.readFileSync(path.join(home, 'runs', row.run + '.key'));
    if (mode === 'volatile') row.ts = '2030-01-02T03:04:05.678Z';
    if (mode === 'row' || mode === 'signed-row') row.event = 'different.event';
    if (mode === 'volatile' || mode === 'signed-row') rows[0] = signRow(row, key);
    if (mode === 'invalid') row.mac = '0'.repeat(64);
    fs.writeFileSync(ledger, rows.map(r => JSON.stringify(r)).join('\\n') + '\\n');
  }
  if (mode === 'volatile') {
    const file = path.join(home, 'normalization.json');
    const p = JSON.parse(fs.readFileSync(file, 'utf8'));
    p.ts = '2031-02-03T04:05:06.000Z'; p.duration_ms = 999; p.elapsedMs = 123;
    p.worker.pid = 99999; p.run = 'r-20300102-030405-abcdef';
    // Key order and JSON spacing have no semantic meaning in a state file.
    fs.writeFileSync(file, JSON.stringify(Object.fromEntries(Object.entries(p).reverse()), null, 4));
  }
  if (mode === 'semantic') {
    const file = path.join(home, 'boundary.json');
    const p = JSON.parse(fs.readFileSync(file, 'utf8'));
    p.count = 2; p.cost_usd = 8; p.run = 'r-manual-other'; p.hash = 'b'.repeat(64);
    p.nested[0].value = 'changed'; p.timestamp = 'invalid-date-2';
    delete p.present; p.added = true;
    fs.writeFileSync(file, JSON.stringify(p));
  }
  if (mode === 'binary') fs.writeFileSync(path.join(home, 'bytes.bin'), Buffer.from([0, 255, 2]));
  if (mode === 'json-marker') fs.writeFileSync(path.join(home, 'fixture.done'), JSON.stringify({ value: 2 }));
  process.stdout.write(out); process.stderr.write(err); process.exitCode = code;
`];

const status = () => ['status', '--run', fixture.runId, '--json'];
const check = (mode, args = status(), extra = {}) => runParity(args, { fixture, goCommand: FAKE, env: { PARITY_MODE: mode, ...extra } });
const locations = (result) => result.diffs.map(({ where, field }) => [where, field]);
const ledgerName = 'home/.code-forge/ledger/parity-fixture.jsonl';

test('clause 1: independent repo and HOME copies compare the complete CLI result without changing the fixture', async () => {
  const record = path.join(fixture.home, '.code-forge', 'runs', `${fixture.runId}.json`);
  const beforeBytes = await readFile(record);
  const result = await runParity(status(), {
    fixture, goCommand: FAKE, stdin: 'parity input\n',
    env: { PARITY_MODE: 'isolation', PARITY_CUSTOM: 'passed', PARITY_ORIGINAL_REPO: fixture.repo, PARITY_ORIGINAL_HOME: fixture.home },
  });
  assert.deepEqual(result, { equal: true, diffs: [] }, 'exit, streams and every copied state file have zero differences');
  assert.deepEqual(await readFile(record), beforeBytes, 'the authoritative fixture record is byte-identical after both commands');
});

test('clause 1 boundary: stderr, binary state and missing nested dotfiles retain exact locations and values', async () => {
  const stderr = await check('stderr');
  assert.deepEqual(stderr, { equal: false, diffs: [{ where: 'process', field: 'stderr', node: '', go: 'one stderr byte!' }] }, 'one stderr difference, including both values');
  await writeFile(path.join(fixture.home, '.code-forge', 'bytes.bin'), Buffer.from([0, 255, 1]));
  try {
    assert.deepEqual(await check('binary'), { equal: false, diffs: [{ where: 'home/.code-forge/bytes.bin', field: '$', node: '00ff01', go: '00ff02' }] }, 'binary bytes are compared without lossy UTF-8 decoding');
  } finally {
    await rm(path.join(fixture.home, '.code-forge', 'bytes.bin'));
  }
  assert.deepEqual(await check('extra'), { equal: false, diffs: [{ where: 'repo/.code-forge/nested/.extra', field: '$file', node: undefined, go: 'present' }] }, 'one extra nested dotfile');
  assert.deepEqual(await check('clock-file'), { equal: false, diffs: [{ where: 'home/.code-forge/runs/<clock-run>.json', field: '$file', node: undefined, go: 'present' }] }, 'normalizing a clock-based filename must not hide a second state file');
  const marker = path.join(fixture.home, '.code-forge', 'fixture.done');
  await writeFile(marker, JSON.stringify({ value: 1 }));
  try {
    assert.deepEqual(await check('json-marker'), { equal: false, diffs: [{ where: 'home/.code-forge/fixture.done', field: '$.value', node: 1, go: 2 }] }, 'JSON markers without a .json suffix still report individual fields');
  } finally { await rm(marker); }
});

test('clause 2: only volatile fields normalize and otherwise equal, verified rows can have different signatures', async () => {
  const file = path.join(fixture.home, '.code-forge', 'normalization.json');
  await writeFile(file, JSON.stringify({ ts: '2026-01-02T03:04:05.000Z', duration_ms: 1, elapsedMs: 2, worker: { pid: 123 }, run: fixture.runId }));
  try {
    assert.deepEqual(await check('volatile'), { equal: true, diffs: [] }, 'timestamps, both duration conventions, nested pid, clock run id and verified MACs yield zero differences');
  } finally {
    await rm(file);
  }
});

test('clause 2 boundary: semantic fields, missing fields, unsigned and semantically different signed rows remain visible', async () => {
  const file = path.join(fixture.home, '.code-forge', 'boundary.json');
  await writeFile(file, JSON.stringify({ count: 1, cost_usd: 7, run: 'r-manual', hash: 'a'.repeat(64), nested: [{ value: 'original' }], timestamp: 'invalid-date-1', present: null }));
  try {
    const changed = await check('semantic');
    assert.equal(changed.equal, false, 'semantic changes cannot compare equal');
    assert.deepEqual(changed.diffs, [
      { field: '$.added', node: undefined, go: true },
      { field: '$.cost_usd', node: 7, go: 8 },
      { field: '$.count', node: 1, go: 2 },
      { field: '$.hash', node: 'a'.repeat(64), go: 'b'.repeat(64) },
      { field: '$.nested[0].value', node: 'original', go: 'changed' },
      { field: '$.present', node: null, go: undefined },
      { field: '$.run', node: 'r-manual', go: 'r-manual-other' },
      { field: '$.timestamp', node: 'invalid-date-1', go: 'invalid-date-2' },
    ].map((diff) => ({ where: 'home/.code-forge/boundary.json', ...diff })), 'exactly eight semantic field differences, with both values');
  } finally {
    await rm(file);
  }
  const invalid = await check('invalid');
  assert.deepEqual(locations(invalid), [[ledgerName, '$[0].mac']], 'one invalid signature is never normalized away');
  assert.equal(invalid.equal, false);
  assert.equal(invalid.diffs[0].go, '0'.repeat(64));
  const signed = await check('signed-row');
  assert.deepEqual(locations(signed), [[ledgerName, '$[0].event'], [ledgerName, '$[0].mac']], 'even two valid signatures remain different when the row semantics differ');
  assert.equal(signed.equal, false);
  assert.deepEqual([signed.diffs[0].node, signed.diffs[0].go], ['run.start', 'different.event']);
});

test('clause 3: Node against Node is equal for status, block open and review-file on a real fixture run', async () => {
  for (const args of [status(),
    ['block', 'open', 'B2', '--run', fixture.runId, '--level', 'L1', '--owned', 'other.mjs', '--acceptance', 'acceptance.json'],
    ['review-file', 'sample.mjs', '--block', fixture.block, '--run', fixture.runId],
  ]) {
    assert.deepEqual(await runParity(args, { fixture, goCommand: NODE }), { equal: true, diffs: [] }, `${args.slice(0, 2).join(' ')} has zero process or state differences`);
  }
});

test('clause 3 boundary: one stdout byte, exit status, extra state file and row field each produce a precise difference', async () => {
  const stdout = await check('stdout');
  assert.equal(stdout.equal, false);
  assert.deepEqual(locations(stdout), [['process', 'stdout']], 'exactly one changed stdout stream');
  assert.equal(stdout.diffs[0].go, '!' + stdout.diffs[0].node.slice(1), 'only the first stdout byte changed');
  assert.deepEqual(await check('exit'), { equal: false, diffs: [{ where: 'process', field: 'code', node: 0, go: 17 }] }, 'exactly one exit-code difference');
  assert.deepEqual(locations(await check('extra')), [['repo/.code-forge/nested/.extra', '$file']], 'exactly one added file');
  assert.deepEqual(await check('row'), { equal: false, diffs: [{ where: ledgerName, field: '$[0].event', node: 'run.start', go: 'different.event' }] }, 'exactly one changed row field');
});
