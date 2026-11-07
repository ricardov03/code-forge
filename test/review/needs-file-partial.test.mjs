import { answerFor, cfgFor, commitAll, freshDir, makeRepo, writeFile } from './helpers.mjs';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import { syncBuiltinESMExports } from 'node:module';
import path from 'node:path';
import { test } from 'node:test';

const { reviewFile } = await import('../../src/review/engine.mjs');
const { reviewTicket } = await import('../../src/worker/engine.mjs');
const { createWorker } = await import('../../src/worker/loop.mjs');
const { enqueue, readResult } = await import('../../src/worker/queue.mjs');
const { createKeyStore } = await import('../../src/keys/store.mjs');
const { registerSecret, clearSecrets } = await import('../../src/util/redact.mjs');

const FILE = 'src/feature.mjs';
const SWIFT = 'Tests/FooTests.swift';
const KOTLIN = 'Tests/PaymentReportFlowTest.kt';
const ABSOLUTE = '/Users/someone/Code/iOS/reference-repo/Tests/FooTests.swift';
const SAMPLE = [ABSOLUTE, SWIFT];
const HUNKS = ['@@ -0,0 +1 @@'];
const FINDING_TEXT = 'issue33-private-review-finding-text';

async function fixture() {
  const repo = await makeRepo();
  for (const rel of [SWIFT, KOTLIN]) {
    writeFile(repo, rel, fs.readFileSync(new URL(`../fixtures/issue-33/${path.basename(rel)}`, import.meta.url), 'utf8'));
  }
  await commitAll(repo);
  writeFile(repo, FILE, 'export const total = 4600;\n');
  return repo;
}

/** @param {string[]} requested @param {(packet: string) => void} [firstCall] */
function model(requested, firstCall = () => {}) {
  /** @type {string[]} */
  const packets = [];
  const spawn = async (/** @type {Record<string, any>} */ opts) => {
    const packet = fs.readFileSync(opts.promptPath, 'utf8');
    packets.push(packet);
    if (packets.length === 1) firstCall(packet);
    return { status: 'ok', exit_code: 0, provider: 'anthropic', model: 'fake',
      usage: { tokens_in: 1000, tokens_out: 300 },
      answer: answerFor(HUNKS, {
        needs_file: packets.length === 1 ? requested : [],
        findings: [{ id: 'N1', file: FILE, line_start: 1, line_end: 1, severity: 'nit', category: 'correctness', claim: FINDING_TEXT, evidence: 'e', fix: 'f' }],
      }) };
  };
  return { spawn, packets };
}

/** @param {string} repo @param {ReturnType<typeof model>} fake */
function review(repo, fake) {
  return reviewFile({ repoRoot: repo, file: FILE, cfg: cfgFor(), risk: 0, workDir: freshDir('packets') }, fake);
}

/** Fail only actual opens of the requested attachment, after packet construction.
 * @param {import('node:test').TestContext} t @param {string} repo @param {string[]} files
 */
function failReads(t, repo, files) {
  class AttachmentReadError extends Error {}
  const open = fs.openSync;
  t.mock.method(fs, 'openSync', (/** @type {any} */ file, /** @type {any} */ flags, /** @type {any} */ mode) => {
    if (files.some((rel) => file === path.join(repo, rel))) throw new AttachmentReadError('private read details');
    return open(file, flags, mode);
  });
  syncBuiltinESMExports();
  t.after(() => { t.mock.restoreAll(); syncBuiltinESMExports(); });
}

test('reporter Swift sample attaches the good file and one anonymous refusal in exactly two calls', async () => {
  const repo = await fixture();
  const fake = model(SAMPLE);
  const out = await review(repo, fake);
  assert.deepEqual([out.status, out.approved, fake.packets.length, out.sessions[0].needs_file_round], ['reviewed', true, 2, true]);
  const extra = fake.packets[1].slice(fake.packets[0].length);
  assert.deepEqual(extra.split('\n').filter((l) => l.startsWith('### ')), ['### (refused: not a repo-relative path)', `### ${SWIFT}`]);
  assert.match(extra, /5\|         \/\/ FooTests.swift:34/);
  assert.equal(fake.packets[1].includes(ABSOLUTE), false);
  assert.deepEqual(out.sessions[0].needs_file_requested, SAMPLE);
});

test('Kotlin sample keeps the second attachment after a read error and records its class', async (t) => {
  const repo = await fixture();
  const fake = model([KOTLIN, SWIFT], () => failReads(t, repo, [KOTLIN]));
  const out = await review(repo, fake);
  assert.deepEqual([out.status, out.approved, fake.packets.length], ['reviewed', true, 2]);
  const extra = fake.packets[1].slice(fake.packets[0].length);
  assert.deepEqual(extra.split('\n').filter((l) => l.startsWith('### ')), [`### ${KOTLIN} (error: AttachmentReadError)`, `### ${SWIFT}`]);
  assert.match(extra, /XCTAssertEqual/);
  assert.equal(extra.includes('private read details'), false);
  assert.equal(extra.includes('includesCompletedPayments'), false);
  assert.deepEqual(out.sessions[0].needs_file_errors, ['AttachmentReadError']);
  assert.deepEqual(out.sessions[0].needs_file_requested, [KOTLIN, SWIFT]);
});

test('no usable entry stays refused with one call for bad paths, unreadable files and missing files', async (t) => {
  const repo = await fixture();
  for (const requested of [[ABSOLUTE, '../etc/passwd'], [KOTLIN, SWIFT], ['Tests/missing.swift', '.env']]) {
    const unreadable = requested[0] === KOTLIN;
    const fake = model(requested, () => { if (unreadable) failReads(t, repo, requested); });
    const out = await review(repo, fake);
    assert.deepEqual([out.status, out.reason, out.approved, fake.packets.length], ['unavailable', 'needs_file-refused', false, 1], requested.join(','));
    assert.deepEqual(out.sessions[0].needs_file_requested, requested);
    if (unreadable) assert.deepEqual(out.sessions[0].needs_file_errors, ['AttachmentReadError', 'AttachmentReadError']);
    t.mock.restoreAll();
    syncBuiltinESMExports();
  }
});

/** Drive the real worker's signed result and row writer, using the ordinary engine without fix-loop triage.
 * @param {string[]} requested
 */
async function workerRow(requested) {
  const repo = await fixture();
  const fake = model(requested);
  const runId = 'r-issue33-test';
  const ticket = enqueue({ repoRoot: repo, run: runId, block: 'B1', file: FILE });
  /** @type {Record<string, any>[]} */
  const rows = [];
  const worker = await createWorker({ repoRoot: repo, runId, cfg: cfgFor(), runRootDir: freshDir('worker'), slug: 'issue33', key: Buffer.alloc(32, 1) }, {
    env: {}, store: await createKeyStore({ backends: [], dir: freshDir('store') }),
    readRun: async () => null, readRows: async () => [], expiryCheck: async () => {},
    writeRow: async (row) => { rows.push(row); }, spawn: /** @type {any} */ (fake.spawn),
    review: (ticket, ctx) => reviewTicket(ticket, { ...ctx, key: undefined }),
  });
  try {
    assert.equal(await worker.drain(), 1);
    const resultRows = rows.filter((r) => r.event === 'review.result');
    assert.equal(resultRows.length, 1);
    const result = readResult(repo, runId, ticket.ticket);
    assert.deepEqual([resultRows[0].status, resultRows[0].approved], [result.status, result.approved]);
    return { row: resultRows[0], result, calls: fake.packets.length };
  } finally {
    await worker.stop();
  }
}

test('worker review row stores requested paths in refused and second-round cases without file content', async () => {
  for (const requested of [[ABSOLUTE], SAMPLE]) {
    const { row, result, calls } = await workerRow(requested);
    assert.equal(JSON.stringify(row).includes(FINDING_TEXT), false);
    for (const output of [row, result]) {
      assert.deepEqual([output.status, output.approved, calls], requested.length === 1 ? ['unavailable', false, 1] : ['reviewed', true, 2]);
      assert.deepEqual(output.sessions[0].needs_file_requested, requested);
      assert.equal(JSON.stringify(output).includes('XCTAssertEqual'), false);
      assert.equal(JSON.stringify(output).includes('invoice-0042'), false);
    }
  }
});

test('requested path diagnostics cap ten entries at 200 characters after row redaction', async () => {
  const secret = 'issue33-private-token';
  registerSecret(secret);
  try {
    const long = `/Users/${secret}/${'x'.repeat(250)}`;
    const requested = [long, ...Array.from({ length: 10 }, (_, i) => `/outside/${i}.swift`)];
    const { row, result, calls } = await workerRow(requested);
    const expected = [`/Users/[REDACTED]/${'x'.repeat(250)}`.slice(0, 200), ...requested.slice(1, 10)];
    for (const output of [row, result]) {
      assert.deepEqual([output.status, output.reason, output.approved, calls], ['unavailable', 'needs_file-refused', false, 1]);
      assert.deepEqual(output.sessions[0].needs_file_requested, expected);
      assert.deepEqual(output.sessions[0].needs_file_requested.map((p) => p.length), [200, ...Array(9).fill(16)]);
      assert.equal(JSON.stringify(output).includes(secret), false);
    }
  } finally {
    clearSecrets();
  }
});
