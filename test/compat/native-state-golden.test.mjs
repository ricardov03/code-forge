import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { chmodSync, copyFileSync, existsSync, mkdtempSync, rmSync } from 'node:fs';
import { chmod, mkdir, mkdtemp, readFile, rm, stat, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { test } from 'node:test';
import { fileURLToPath } from 'node:url';
import { appendRow } from '../../src/ledger/write.mjs';
import { readRun, saveRun, withRunLock, writeSigned } from '../../src/state/run.mjs';
import { generateKey, verifyRow } from '../../src/state/signer.mjs';

const release = fileURLToPath(new URL(`../../native/bin/${process.platform}-${process.arch}/code-forge-native`, import.meta.url));
// Installation tests rename/chmod the release binary in parallel. Run a private, executable copy.
const binary = path.join(mkdtempSync(path.join(os.tmpdir(), 'native-state-bin-')), 'code-forge-native');
if (existsSync(release)) { copyFileSync(release, binary); chmodSync(binary, 0o755); }
process.on('exit', () => rmSync(path.dirname(binary), { recursive: true, force: true }));
const writer = fileURLToPath(new URL('../../src/ledger/write.mjs', import.meta.url));
const run = 'native-state-fixture';
const slug = 'state-golden';
const ts = '2026-10-31T01:02:03.456Z';

async function fixture(t) {
  if (!existsSync(binary)) { t.skip(`native state golden skipped: no binary for ${process.platform}-${process.arch}`); return null; }
  const oldHome = process.env.HOME;
  const root = await mkdtemp(path.join(os.tmpdir(), 'native-state-'));
  const home = path.join(root, 'home');
  const workspace = path.join(root, 'workspace');
  await mkdir(home);
  await mkdir(workspace);
  process.env.HOME = home;
  t.after(async () => { process.env.HOME = oldHome; await rm(root, { recursive: true, force: true }); });
  return {
    home, workspace,
    record: path.join(home, '.code-forge', 'runs', `${run}.json`),
    mirror: path.join(workspace, '.code-forge', 'runs', `${run}.json`),
    ledger: path.join(home, '.code-forge', 'ledger', `${slug}.jsonl`),
  };
}

function start(t, home, op) {
  const child = spawn(binary, ['internal', 'state-golden', '--home', home, '--run', run, '--op', op], { stdio: ['pipe', 'pipe', 'pipe'] });
  t.after(() => { if (child.exitCode === null) child.kill(); });
  let stdout = '', stderr = '';
  child.stdout.on('data', (chunk) => { stdout += chunk; });
  child.stderr.on('data', (chunk) => { stderr += chunk; });
  const done = once(child, 'close').then(([code]) => ({ code, stdout, stderr }));
  return { child, done };
}

async function op(t, home, name, input) {
  const p = start(t, home, name);
  p.child.stdin.on('error', () => {});
  p.child.stdin.end(JSON.stringify(input));
  return p.done;
}

function success(result, label) {
  assert.equal(result.code, 0, `${label}: native operation must succeed: ${result.stderr}`);
}

test('Go ledger, run and mirror bytes equal Node and Go signatures verify in Node', async (t) => {
  const f = await fixture(t); if (!f) return;
  const record = { z: { b: 1e-7, a: '<&>\u2028😀', empty: {}, array: [1e20, -0, null] }, run_id: run, workspace: f.workspace, worker: { pid: 123, started_at: ts }, project: slug, blocks: {} };
  await saveRun(record);
  const expectedRecord = await readFile(f.record);
  const expectedMirror = await readFile(f.mirror);
  // Save performs a read-modify-write, proving that decoding retains nested order.
  success(await op(t, f.home, 'save', { status: 'ended' }), 'save');
  record.status = 'ended';
  await saveRun(record);
  const changedRecord = await readFile(f.record);
  const changedMirror = await readFile(f.mirror);
  await writeFile(f.record, expectedRecord);
  await writeFile(f.mirror, expectedMirror);
  success(await op(t, f.home, 'save', { status: 'ended' }), 'save round trip');
  assert.deepEqual(await readFile(f.record), changedRecord, 'authoritative record bytes including nested insertion order');
  assert.deepEqual(await readFile(f.mirror), changedMirror, 'mirror bytes omit worker and retain order');
  assert.equal((await stat(f.record)).mode & 0o777, 0o600, 'authoritative record mode is exactly 0600');
  assert.equal(Object.hasOwn(JSON.parse(await readFile(f.mirror, 'utf8')), 'worker'), false, 'mirror has zero worker pins');
  assert.deepEqual(await readRun(run), JSON.parse(JSON.stringify(record)), 'Node reads all JSON fields saved by Go');

  const rows = [
    { z: { q: 'é😀\u2029', a: ['<&>', 1e-6, 1e21] }, event: 'fixture', ts, tokens_in: 0, cost_usd: 0 },
    { event: 'explicit', tokens_source: 'measured', ts, cost_source: null, tokens_out: 2, cost_usd: 1e-7 },
    { '10': 'ten', '2': 'two', event: 'empty', ts, tokens_in: '2', cost_usd: '0' },
  ];
  for (const row of rows) await appendRow(row, { slug });
  const expectedRows = await readFile(f.ledger);
  await rm(f.ledger);
  success(await op(t, f.home, 'append', { slug, rows }), 'append');
  assert.deepEqual(await readFile(f.ledger), expectedRows, 'all three normalized ledger lines are byte equal');

  const key = await generateKey(run);
  const row = { event: 'signed', z: { b: 1, a: 2 }, ts, mac: 'replace-me' };
  await rm(f.ledger);
  await writeSigned(run, (signed) => appendRow(signed, { slug }), row);
  const expectedSigned = await readFile(f.ledger);
  await rm(f.ledger);
  success(await op(t, f.home, 'append', { signed: true, rows: [row] }), 'signed append');
  const signed = await readFile(f.ledger);
  assert.deepEqual(signed, expectedSigned, 'signed ledger bytes equal Node including mac position');
  assert.deepEqual(verifyRow(JSON.parse(signed.toString()), key), { ok: true }, 'Go row MAC verifies in Node');
});

test('Node and Go share run locks and four concurrent appenders preserve 200 whole rows', async (t) => {
  const f = await fixture(t); if (!f) return;
  let blocked;
  await withRunLock(run, async () => {
    blocked = start(t, f.home, 'lock');
    let finished = false;
    blocked.done.then(() => { finished = true; });
    blocked.child.stdin.end();
    await new Promise((resolve) => setTimeout(resolve, 150));
    assert.equal(finished, false, 'Go must still be waiting while Node owns the lock');
  });
  success(await blocked.done, 'Go proceeds after Node releases');
  const holder = start(t, f.home, 'lock');
  const acquired = await Promise.race([once(holder.child.stdout, 'data').then(([data]) => data.toString()), holder.done.then((r) => `exit ${r.code}: ${r.stderr}`)]);
  assert.equal(acquired, 'locked\n', 'Go announces lock acquisition');
  let entered = false;
  const waiting = withRunLock(run, async () => { entered = true; });
  await new Promise((resolve) => setTimeout(resolve, 100));
  assert.equal(entered, false, 'Node must wait while Go owns the lock');
  holder.child.stdin.end();
  success(await holder.done, 'Go releases');
  await waiting;
  assert.equal(entered, true, 'Node proceeds after Go releases');

  const jobs = [];
  for (let i = 0; i < 2; i++) {
    const rows = Array.from({ length: 50 }, (_, n) => ({ event: 'concurrent', ts, id: `go-${i}-${n}`, payload: '😀'.repeat(2048) }));
    jobs.push(op(t, f.home, 'append', { slug, rows }));
    const script = `import {appendRow} from ${JSON.stringify(writer)}; for(let n=0;n<50;n++) await appendRow({event:'concurrent',ts:${JSON.stringify(ts)},id:'node-${i}-'+n,payload:'😀'.repeat(2048)},{slug:${JSON.stringify(slug)}});`;
    const child = spawn(process.execPath, ['--input-type=module', '-e', script], { env: { ...process.env, HOME: f.home }, stdio: ['ignore', 'ignore', 'pipe'] });
    t.after(() => { if (child.exitCode === null) child.kill(); });
    let stderr = '';
    child.stderr.on('data', (chunk) => { stderr += chunk; });
    jobs.push(once(child, 'close').then(([code]) => ({ code, stderr })));
  }
  for (const result of await Promise.all(jobs)) success(result, 'concurrent appender');
  const text = await readFile(f.ledger, 'utf8');
  assert.equal(text.endsWith('\n'), true, 'last of 200 rows has a complete newline');
  const lines = text.trimEnd().split('\n');
  assert.equal(lines.length, 200, 'exactly 200 complete lines survive');
  const actual = lines.map((line) => JSON.parse(line));
  const expectedIDs = ['go', 'node'].flatMap((kind) => [0, 1].flatMap((i) => Array.from({ length: 50 }, (_, n) => `${kind}-${i}-${n}`))).sort();
  assert.deepEqual(actual.map((r) => r.id).sort(), expectedIDs, 'every producer row appears exactly once');
  assert.equal(actual.filter((r) => r.payload === '😀'.repeat(2048)).length, 200, 'all 200 payloads remain whole');
});

test('Go matches Node at rotation and torn-tail boundaries and refuses unsafe state inputs', async (t) => {
  const f = await fixture(t); if (!f) return;
  const row = { event: 'boundary', ts };
  await appendRow(row, { slug });
  const complete = await readFile(f.ledger);
  /** @type {Array<[string, Buffer, number]>} */
  const boundaries = [
    ['below threshold', complete, complete.length + 1],
    ['exact threshold', complete, complete.length],
    ['unicode torn tail', Buffer.concat([complete, Buffer.from('{"é":"😀')]), 100000],
    ['only torn tail', Buffer.from('{"torn":'), 100000],
  ];
  for (const [label, prefix, limit] of boundaries) {
    await writeFile(f.ledger, prefix);
    await appendRow(row, { slug, rotateAtBytes: limit });
    const expected = await readFile(f.ledger);
    const rotated = f.ledger.replace('.jsonl', '.1.jsonl');
    const expectedRotation = existsSync(rotated) ? await readFile(rotated) : null;
    await rm(rotated, { force: true });
    await writeFile(f.ledger, prefix);
    success(await op(t, f.home, 'append', { slug, rows: [row], rotateAtBytes: limit }), label);
    assert.deepEqual(await readFile(f.ledger), expected, `${label}: current bytes`);
    assert.deepEqual(existsSync(rotated) ? await readFile(rotated) : null, expectedRotation, `${label}: rotated bytes`);
    await rm(rotated, { force: true });
  }
  const before = await readFile(f.ledger);
  for (const input of [{ slug, rows: [{}] }, { slug: '../bad', rows: [row] }, { slug, rows: [row], rotateAtBytes: 0 }]) {
    const result = await op(t, f.home, 'append', input);
    assert.equal(result.code, 1, `invalid append must be refused: ${JSON.stringify(input)}`);
    assert.deepEqual(await readFile(f.ledger), before, 'refused append preserves ledger bytes');
  }
  await saveRun({ run_id: run, workspace: f.workspace, project: slug });
  await generateKey(run);
  await chmod(path.join(f.home, '.code-forge', 'runs', `${run}.key`), 0o644);
  assert.equal((await op(t, f.home, 'append', { signed: true, rows: [row] })).code, 1, 'world-readable signing key refused');
});
