import assert from 'node:assert/strict';
import { readFile, readlink, rm, symlink, writeFile } from 'node:fs/promises';
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
  import assert from 'node:assert/strict';
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
  if (mode === 'relocated') {
    const run = args[args.indexOf('--run') + 1];
    const record = JSON.parse(fs.readFileSync(path.join(home, 'runs', run + '.json'), 'utf8'));
    const tree = record.blocks.B1.worktree.path;
    assert.equal(tree, path.join(process.cwd(), '.code-forge', 'worktrees', 'B1'));
    assert.equal(fs.readFileSync(path.join(tree, '.git'), 'utf8'), 'gitdir: ' + path.join(process.cwd(), '.git', 'worktrees', 'B1') + '\\n');
    assert.equal(fs.readFileSync(path.join(process.cwd(), '.git', 'worktrees', 'B1', 'gitdir'), 'utf8'), path.join(tree, '.git') + '\\n');
    const git = spawnSync('git', ['-C', tree, 'rev-parse', '--show-toplevel'], { encoding: 'utf8' });
    assert.equal(git.status, 0, git.stderr);
    assert.equal(git.stdout.trim(), tree);
    assert.equal(fs.readFileSync('paths.txt', 'utf8'), process.cwd() + '\\n' + process.env.HOME + '\\n');
    assert.deepEqual(JSON.parse(fs.readFileSync(path.join(process.env.HOME, 'paths.data'), 'utf8')), { repo: process.cwd(), home: process.env.HOME });
    assert.equal(fs.readlinkSync('repo-link'), path.join(process.cwd(), 'paths.txt'));
    assert.equal(fs.readlinkSync('home-link'), path.join(process.env.HOME, 'paths.data'));
    assert.equal(fs.readlinkSync('home-dir-link'), process.env.HOME);
    assert.equal(fs.readlinkSync('relative-link'), 'paths.txt');
    fs.writeFileSync('repo-link', 'only the copy was changed');
    fs.writeFileSync('home-link', 'only the copy was changed');
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
    p.elapsed_ms = 234; p.wall_ms = 345; p.worker_pid = 99998;
    p.total_duration_ms = 456; p.join_ms = 567; p.gates_ms = 678; p.merge_gate_ms = 789;
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
  if (mode === 'timeout') {
    const run = args[args.indexOf('--run') + 1];
    const file = path.join(home, 'runs', run + '.json');
    const record = JSON.parse(fs.readFileSync(file, 'utf8'));
    record.blocks.B1.timeout_ms = 2000; record.blocks.B1.timeoutMs = 4000;
    fs.writeFileSync(file, JSON.stringify(record));
    out = out.replace('"timeout_ms": 1000', '"timeout_ms": 2000').replace('"timeoutMs": 3000', '"timeoutMs": 4000');
  }
  if (mode === 'temp-file') out = out.replaceAll('report.json', 'different.json');
  if (mode === 'temp-prefix') out = out.replaceAll('parity-scratch-', 'different-scratch-');
  if (mode === 'binary') fs.writeFileSync(path.join(home, 'bytes.bin'), Buffer.from([0, 255, 2]));
  if (mode === 'json-marker') fs.writeFileSync(path.join(home, 'fixture.done'), JSON.stringify({ value: 2 }));
  if (mode === 'invalid-utf8') {
    const bytes = Buffer.from(out), at = bytes.indexOf(Buffer.from('�'));
    if (at < 0) throw new Error('missing Unicode output fixture');
    out = Buffer.concat([bytes.subarray(0, at), Buffer.from([255]), bytes.subarray(at + 3)]);
  }
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

test('clause 1: worktree pointers, all text files and internal absolute symlinks stay inside each copy', async () => {
  const isolated = await makeParityFixture({ isolation: 'worktree' });
  try {
    const text = path.join(isolated.repo, 'paths.txt'), data = path.join(isolated.home, 'paths.data');
    await writeFile(text, `${isolated.repo}\n${isolated.home}\n`);
    await writeFile(data, JSON.stringify({ repo: isolated.repo, home: isolated.home }));
    const links = { 'repo-link': text, 'home-link': data, 'home-dir-link': isolated.home, 'relative-link': 'paths.txt' };
    for (const [name, target] of Object.entries(links)) await symlink(target, path.join(isolated.repo, name));
    const pointers = [text, data,
      path.join(isolated.repo, '.git', 'worktrees', 'B1', 'gitdir'),
      path.join(isolated.repo, '.code-forge', 'worktrees', 'B1', '.git')];
    const saved = await Promise.all(pointers.map((file) => readFile(file)));
    const result = await runParity(['status', '--run', isolated.runId, '--json'], {
      fixture: isolated, goCommand: FAKE, env: { PARITY_MODE: 'relocated' },
    });
    assert.deepEqual(result, { equal: true, diffs: [] }, 'both commands use a functional copied git worktree and relocated paths');
    assert.deepEqual(await Promise.all(pointers.map((file) => readFile(file))), saved, 'writes through copied symlinks leave the original text and worktree metadata byte-identical');
    for (const [name, target] of Object.entries(links)) assert.equal(await readlink(path.join(isolated.repo, name)), target);
  } finally { await isolated.cleanup(); }
});

test('clause 1 boundary: leftover original paths in binary files or external links refuse execution', async () => {
  const file = path.join(fixture.repo, 'leak.bin'), link = path.join(fixture.home, 'leak-link');
  for (const key of ['repo', 'home']) {
    await writeFile(file, Buffer.concat([Buffer.from([255]), Buffer.from(fixture[key])]));
    try {
      await assert.rejects(runParity(status(), { fixture, goCommand: 'must-not-execute' }),
        new RegExp(`retains original ${key} path in file repo/leak\\.bin`));
    } finally { await rm(file); }
    await symlink(`${fixture[key]}-outside/target`, link);
    try {
      await assert.rejects(runParity(status(), { fixture, goCommand: 'must-not-execute' }),
        new RegExp(`retains original ${key} path in symlink home/leak-link`));
    } finally { await rm(link); }
  }
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
  const record = path.join(fixture.home, '.code-forge', 'runs', `${fixture.runId}.json`);
  const saved = await readFile(record), unicode = JSON.parse(saved.toString('utf8'));
  unicode.blocks.B1.acceptance[0].clause = 'Unicode �';
  await writeFile(record, JSON.stringify(unicode));
  try {
    const changed = await check('invalid-utf8', ['run', 'status', '--run', fixture.runId]);
    assert.equal(changed.equal, false, 'a malformed byte must not compare equal to a literal Unicode replacement character');
    assert.deepEqual(locations(changed), [['process', 'stdout']], 'exactly one stdout byte-sequence difference');
    const bytes = Buffer.from(changed.diffs[0].node), at = bytes.indexOf(Buffer.from('�'));
    assert.equal(changed.diffs[0].go, Buffer.concat([bytes.subarray(0, at), Buffer.from([255]), bytes.subarray(at + 3)]).toString('hex'), 'invalid UTF-8 is reported losslessly as hex');
  } finally { await writeFile(record, saved); }
});

test('clause 2: only volatile fields normalize and otherwise equal, verified rows can have different signatures', async () => {
  const file = path.join(fixture.home, '.code-forge', 'normalization.json');
  await writeFile(file, JSON.stringify({ ts: '2026-01-02T03:04:05.000Z', duration_ms: 1, elapsedMs: 2,
    elapsed_ms: 3, wall_ms: 4, worker_pid: 1234, total_duration_ms: 5, join_ms: 6, gates_ms: 7, merge_gate_ms: 8,
    worker: { pid: 123 }, run: fixture.runId }));
  try {
    assert.deepEqual(await check('volatile'), { equal: true, diffs: [] }, 'timestamps, both duration conventions, nested pid, clock run id and verified MACs yield zero differences');
  } finally {
    await rm(file);
  }
});

test('clause 2 boundary: timeout settings remain differences in both state and CLI JSON', async () => {
  const record = path.join(fixture.home, '.code-forge', 'runs', `${fixture.runId}.json`);
  const saved = await readFile(record), changed = JSON.parse(saved.toString('utf8'));
  Object.assign(changed.blocks.B1, { timeout_ms: 1000, timeoutMs: 3000 });
  await writeFile(record, JSON.stringify(changed));
  try {
    const result = await check('timeout', ['run', 'status', '--run', fixture.runId]);
    assert.equal(result.equal, false);
    assert.deepEqual(locations(result), [['process', 'stdout'],
      ['home/.code-forge/runs/<clock-run>.json', '$.blocks.B1.timeoutMs'],
      ['home/.code-forge/runs/<clock-run>.json', '$.blocks.B1.timeout_ms']]);
    assert.equal(result.diffs[0].go, result.diffs[0].node
      .replace('"timeout_ms": 1000', '"timeout_ms": 2000').replace('"timeoutMs": 3000', '"timeoutMs": 4000'));
    assert.deepEqual(result.diffs.slice(1).map(({ node, go }) => [node, go]), [[3000, 4000], [1000, 2000]]);
  } finally { await writeFile(record, saved); }
});

test('clause 2: independently created mkdtemp paths normalize while their prefixes and child paths stay visible', async () => {
  const config = path.join(fixture.repo, '.code-forge.yml'), saved = await readFile(config);
  const script = path.join(fixture.repo, 'temp-gate.cjs'), capture = path.join(fixture.repo, '..', 'temp-paths.jsonl');
  await writeFile(script, `
    const fs = require('node:fs'), path = require('node:path'), os = require('node:os');
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'parity-scratch-'));
    const value = { dir, file: path.join(dir, 'report.json') };
    fs.appendFileSync(process.env.PARITY_CAPTURE, JSON.stringify(value) + '\\n');
    fs.writeFileSync(path.join(process.env.HOME, '.code-forge', 'temp-path.json'), JSON.stringify(value));
    console.log(JSON.stringify(value));
  `);
  await writeFile(config, saved + `\ngates:\n  test: ${JSON.stringify([process.execPath, 'temp-gate.cjs'])}\n  lint: null\n  types: null\n  format: null\n`);
  try {
    const args = ['gates', 'run', '--cwd', fixture.repo];
    const options = { fixture, goCommand: NODE, env: { PARITY_CAPTURE: capture } };
    assert.deepEqual(await runParity(args, options), { equal: true, diffs: [] }, 'two real runs print independently created temporary paths and write them to state');
    const printed = (await readFile(capture, 'utf8')).trim().split('\n').map((line) => JSON.parse(line));
    assert.equal(printed.length, 2, 'both gates ran and printed a directory');
    assert.notEqual(path.basename(printed[0].dir), path.basename(printed[1].dir), 'the mkdtemp random suffixes differ');
    for (const value of printed) assert.match(value.dir, /\/home\/tmp\/parity-scratch-[A-Za-z0-9]{6}$/);
    for (const mode of ['temp-file', 'temp-prefix']) {
      const result = await runParity(args, { ...options, goCommand: FAKE, env: { ...options.env, PARITY_MODE: mode } });
      assert.equal(result.equal, false, `${mode} is still a semantic difference`);
      assert.deepEqual(locations(result), [['process', 'stdout']]);
    }
  } finally {
    await writeFile(config, saved);
    await Promise.all([rm(script), rm(capture, { force: true })]);
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
