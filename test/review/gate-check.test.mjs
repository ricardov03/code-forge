import { commitAll, git, makeRepo, writeFile } from './helpers.mjs';
import assert from 'node:assert/strict';
import { randomBytes } from 'node:crypto';
import { test } from 'node:test';

const { blockFileSet, checkBlockReviews } = await import('../../src/review/gate-check.mjs');
const { signRow, loadKey } = await import('../../src/state/signer.mjs');
const { isForbidden, mergeForbidden } = await import('../../src/util/forbidden.mjs');
const { readRun, saveRun, startRun, writeSigned } = await import('../../src/state/run.mjs');
const { openBlock } = await import('../../src/state/block.mjs');
const { appendRow, readAllRows } = await import('../../src/ledger/write.mjs');
const { highPathsAt, runBlock } = await import('../../src/cli/block.mjs');
const { DELETED_HASH, contentHash } = await import('../../src/worker/ticket.mjs');

const KEY = randomBytes(32);
const HASH = 'a'.repeat(64);
const FILES = [{ file: 'src/a.mjs', content_hash: HASH }];
const signed = (/** @type {Record<string, any>} */ row) => signRow({ run: 'r1', block: 'B1', ...row }, KEY);
const APPROVED = signed({ event: 'review.approved', file: 'src/a.mjs', content_hash: HASH });
const check = (/** @type {Array<Record<string, any>>} */ rows, /** @type {string | null} */ transcript = null) =>
  checkBlockReviews({ block: 'B1', runId: 'r1', files: FILES, rows, key: KEY, transcript, proofFiles: FILES, highPaths: [] }).refusals.map((r) => [r.code, r.file ?? null, r.finding ?? null]);

test('the gate passes a signed approval for the current hash and refuses an unreviewed hash', () => {
  assert.deepEqual(check([APPROVED]), []);
  assert.deepEqual(check([signed({ event: 'review.approved', file: 'src/a.mjs', content_hash: 'b'.repeat(64) })]), [['unreviewed', 'src/a.mjs', null]]);
});

test('the gate refuses an unsigned (or forged) approval row', () => {
  const { mac, ...unsigned } = APPROVED;
  assert.equal(typeof mac, 'string');
  // B20: a row naming the file that fails its MAC also makes the file high tier (fail closed)
  assert.deepEqual(check([unsigned]), [['unsigned', 'src/a.mjs', null], ['unproven', 'src/a.mjs', null]]);
  assert.deepEqual(check([{ ...APPROVED, content_hash: HASH, mac: 'f'.repeat(64) }]), [['unsigned', 'src/a.mjs', null], ['unproven', 'src/a.mjs', null]]);
});

test('the gate refuses a transcript that reads the signer key path', () => {
  const hit = checkBlockReviews({ block: 'B1', runId: 'r1', files: FILES, rows: [APPROVED], key: KEY, transcript: '$ ls src\n$ cat ~/.code-forge/runs/r1.key\n', proofFiles: FILES, highPaths: [] });
  assert.deepEqual(hit.refusals, [{ code: 'rule_break', detail: 'transcript line 2: code-forge-runs-access' }]);
  assert.deepEqual(check([APPROVED], '$ ls src\n$ node --test\n'), []);
});

test('the gate refuses an unruled late finding; a signed ruling or a human waiver clears it', () => {
  const late = signed({ event: 'review.late_finding', file: 'src/a.mjs', finding: 'L1' });
  assert.deepEqual(check([APPROVED, late]), [['late_unruled', 'src/a.mjs', 'L1']]);
  assert.deepEqual(check([APPROVED, late, signed({ event: 'review.late_ruling', file: 'src/a.mjs', finding: 'L1', ruling: 'nit' })]), []);
  assert.deepEqual(check([APPROVED, late, signed({ event: 'review.waived', file: 'src/a.mjs', finding: 'L1', by: 'human', reason: 'r' })]), []);
  const { mac: _m, ...unsignedWaiver } = signed({ event: 'review.waived', file: 'src/a.mjs', finding: 'L1', by: 'human', reason: 'r' });
  assert.deepEqual(check([APPROVED, late, unsignedWaiver]), [['late_unruled', 'src/a.mjs', 'L1'], ['unproven', 'src/a.mjs', null]]);
});

test('fix 4: a waiver is scoped to one file — two files share finding id L1, waiving src/a.mjs leaves exactly the src/b.mjs refusal', () => {
  const HASH_B = 'c'.repeat(64);
  const files = [...FILES, { file: 'src/b.mjs', content_hash: HASH_B }];
  const rows = [
    APPROVED,
    signed({ event: 'review.approved', file: 'src/b.mjs', content_hash: HASH_B }),
    signed({ event: 'review.late_finding', file: 'src/a.mjs', finding: 'L1' }),
    signed({ event: 'review.late_finding', file: 'src/b.mjs', finding: 'L1' }),
  ];
  const refusals = (/** @type {Array<Record<string, any>>} */ extra) =>
    checkBlockReviews({ block: 'B1', runId: 'r1', files, rows: [...rows, ...extra], key: KEY, proofFiles: files, highPaths: [] }).refusals.map((r) => [r.code, r.file, r.finding]);
  assert.deepEqual(refusals([]), [['late_unruled', 'src/a.mjs', 'L1'], ['late_unruled', 'src/b.mjs', 'L1']]);
  assert.deepEqual(refusals([signed({ event: 'review.waived', file: 'src/a.mjs', finding: 'L1', by: 'human', reason: 'r' })]), [['late_unruled', 'src/b.mjs', 'L1']]);
  // a waiver with no file names no file: it clears nothing
  assert.deepEqual(refusals([signed({ event: 'review.waived', finding: 'L1', by: 'human', reason: 'r' })]), [['late_unruled', 'src/a.mjs', 'L1'], ['late_unruled', 'src/b.mjs', 'L1']]);
  // the review_cap check uses the same file-scoped match: src/b.mjs stopped at the cap, unapproved
  const cap = signed({ event: 'review.cap', file: 'src/b.mjs', round: 4, reason: 'review_cap', open: ['L1'] });
  const capRefusals = (/** @type {Array<Record<string, any>>} */ extra) =>
    checkBlockReviews({ block: 'B1', runId: 'r1', files: FILES, rows: [APPROVED, cap, ...extra], key: KEY, proofFiles: FILES, highPaths: [] }).refusals.map((r) => [r.code, r.file, r.finding]);
  assert.deepEqual(capRefusals([]), [['review_cap', 'src/b.mjs', 'L1']]);
  assert.deepEqual(capRefusals([signed({ event: 'review.waived', file: 'src/a.mjs', finding: 'L1', by: 'human', reason: 'r' })]), [['review_cap', 'src/b.mjs', 'L1']]);
  assert.deepEqual(capRefusals([signed({ event: 'review.waived', file: 'src/b.mjs', finding: 'L1', by: 'human', reason: 'r' })]), []);
});

test('`block waive` argv is forbidden for a coder through mergeForbidden itself; the orchestrator verbs are not', () => {
  const list = mergeForbidden([]);
  for (const argv of [
    ['code-forge', 'block', 'waive', 'B1', 'F1', '--reason', 'x'],
    ['npx', '@codedology/code-forge', 'block', 'waive', 'B1', 'F1'],
    ['forge', 'block', 'waive', 'B1', 'F1'],
  ]) {
    assert.equal(isForbidden(argv, list)?.id, 'code-forge-block-waive-from-coder', argv.join(' '));
  }
  assert.equal(isForbidden(['code-forge', 'block', 'close', 'B1', '--no-require-reviews'], list)?.id, 'code-forge-no-require-reviews');
  assert.equal(isForbidden(['code-forge', 'block', 'close', 'B1'], list), null);
});

test('the file set hashes a deleted owned file as DELETED_HASH (its directory gone too) and an edited one by content', async () => {
  const repo = await makeRepo();
  writeFile(repo, 'gone/x.mjs', 'x\n');
  writeFile(repo, 'src/a.mjs', 'a\n');
  await commitAll(repo);
  const base = (await git(['rev-parse', 'HEAD'], repo)).trim();
  await git(['rm', '-q', '-r', 'gone'], repo);
  writeFile(repo, 'src/a.mjs', 'a2\n');
  const set = await blockFileSet({ repoRoot: repo, base, owned: ['gone/x.mjs', 'src/a.mjs'] });
  assert.deepEqual(set, [{ file: 'gone/x.mjs', content_hash: DELETED_HASH }, { file: 'src/a.mjs', content_hash: contentHash(repo, 'src/a.mjs') }]);
});

/** @returns {{write: (s: string) => boolean, text: string}} */
function capture() {
  let text = '';
  return {
    write(s) {
      text += s;
      return true;
    },
    get text() {
      return text;
    },
  };
}

const WARN = 'WARN block B1: no coder transcript found (--transcript, run record, .code-forge/runs/r-gate/B1.log); the transcript grep did not run\n';

test('CLI: close refuses an unknown block, an unruled late finding and an unreviewed hash; `block waive` writes a signed by: human row; a missing transcript is a signed row + WARN', async () => {
  const repo = await makeRepo();
  writeFile(repo, 'src/a.mjs', 'export const a = 1;\n');
  await commitAll(repo);
  const probe = async (/** @type {number} */ pid) => (pid === 4242 ? 'start-A' : null);
  const writeRow = (/** @type {Record<string, any>} */ row) => appendRow(row, { slug: 'gate-demo' });
  await startRun({ workspace: repo, project: 'gate-demo', runId: 'r-gate', workerPid: 4242, writeRow, probe });
  await openBlock({ runId: 'r-gate', id: 'B1', level: 'L2', owned: ['src/a.mjs'], acceptance: [{ clause: 'c', tests: ['t'] }], writeRow });
  await writeSigned('r-gate', writeRow, { event: 'review.late_finding', block: 'B1', file: 'src/a.mjs', finding: 'L1' });
  const close = async (/** @type {string[]} */ extra = [], id = 'B1') => {
    const stream = capture();
    const code = await runBlock(['close', id, '--run', 'r-gate', '--worker-pid', '4242', ...extra], { stdout: stream, stderr: stream, probe });
    return [code, stream.text];
  };

  assert.deepEqual(await close([], 'B9'), [1, 'block close: block B9 is not in run r-gate\n']);
  assert.deepEqual(await close(), [1, `${WARN}block B1 open: late_unruled src/a.mjs L1\n`]);

  const noFile = capture();
  assert.equal(await runBlock(['waive', 'B1', 'L1', '--run', 'r-gate', '--reason', 'Ricardo: accepted in chat'], { stdout: noFile, stderr: noFile }), 2);
  assert.equal(noFile.text, 'block waive: block waive needs --file <path> (a finding id is scoped to one file)\n');
  const waived = capture();
  assert.equal(await runBlock(['waive', 'B1', 'L1', '--run', 'r-gate', '--file', 'src/a.mjs', '--reason', 'Ricardo: accepted in chat'], { stdout: waived, stderr: waived }), 0);
  assert.equal(waived.text, 'block B1 waived L1 in src/a.mjs (by: human)\n');
  const rows = await readAllRows('gate-demo');
  const waivers = rows.filter((r) => r.event === 'review.waived');
  assert.deepEqual(waivers.map((r) => [r.block, r.file, r.finding, r.by, r.reason]), [['B1', 'src/a.mjs', 'L1', 'human', 'Ricardo: accepted in chat']]);
  const { verifyRow } = await import('../../src/state/signer.mjs');
  const key = await loadKey('r-gate');
  assert.equal(verifyRow(waivers[0], key).ok, true);
  const missing = rows.filter((r) => r.event === 'gate.transcript_missing');
  assert.deepEqual([missing.length, verifyRow(missing[0], key).ok, missing[0].block], [1, true, 'B1']);

  // the default: a changed owned file needs a signed approval for its current hash
  writeFile(repo, 'src/a.mjs', 'export const a = 2;\n');
  const transcript = `${repo}/coder.log`;
  writeFile(repo, 'coder.log', '$ node --test\n');
  assert.deepEqual(await close(['--transcript', transcript]), [1, 'block B1 open: unreviewed src/a.mjs\n']);
  await writeSigned('r-gate', writeRow, { event: 'review.approved', block: 'B1', file: 'src/a.mjs', content_hash: contentHash(repo, 'src/a.mjs') });
  assert.deepEqual(await close(['--transcript', transcript]), [0, 'block B1 closed\n']);
  assert.equal((await readAllRows('gate-demo')).filter((r) => r.event === 'gate.reviews_waived').length, 0);
});

test('fix 1: CLI `block close --no-require-reviews` closes an unreviewed file but leaves exactly one signed gate.reviews_waived {by: human} row; the argv is forbidden to a coder', async () => {
  const repo = await makeRepo();
  writeFile(repo, 'src/a.mjs', 'export const a = 1;\n');
  await commitAll(repo);
  const probe = async (/** @type {number} */ pid) => (pid === 4242 ? 'start-A' : null);
  const writeRow = (/** @type {Record<string, any>} */ row) => appendRow(row, { slug: 'gate-waive' });
  await startRun({ workspace: repo, project: 'gate-waive', runId: 'r-nrr', workerPid: 4242, writeRow, probe });
  await openBlock({ runId: 'r-nrr', id: 'B1', level: 'L2', owned: ['src/a.mjs'], acceptance: [{ clause: 'c', tests: ['t'] }], writeRow });
  writeFile(repo, 'src/a.mjs', 'export const a = 2;\n');
  writeFile(repo, 'coder.log', '$ node --test\n');
  const close = async (/** @type {string[]} */ extra) => {
    const stream = capture();
    const code = await runBlock(['close', 'B1', '--run', 'r-nrr', '--worker-pid', '4242', '--transcript', `${repo}/coder.log`, ...extra], { stdout: stream, stderr: stream, probe });
    return [code, stream.text];
  };
  assert.deepEqual(await close([]), [1, 'block B1 open: unreviewed src/a.mjs\n']);
  assert.equal((await readAllRows('gate-waive')).filter((r) => r.event === 'gate.reviews_waived').length, 0);

  assert.deepEqual(await close(['--no-require-reviews']), [0, 'block B1 closed\n']);
  const rows = await readAllRows('gate-waive');
  const audit = rows.filter((r) => r.event === 'gate.reviews_waived');
  assert.deepEqual(audit.map((r) => [r.run, r.block, r.by]), [['r-nrr', 'B1', 'human']]);
  const { verifyRow } = await import('../../src/state/signer.mjs');
  assert.equal(verifyRow(audit[0], await loadKey('r-nrr')).ok, true);
  assert.deepEqual(rows.map((r) => r.event).slice(-2), ['gate.reviews_waived', 'block.close']);
  assert.equal(isForbidden(['code-forge', 'block', 'close', 'B1', '--no-require-reviews'], mergeForbidden([]))?.id, 'code-forge-no-require-reviews');
});

// ── B20: high-tier files need a proven red→green row at close (Ricardo 2026-09-25) ──

const { checkBlockProof } = await import('../../src/review/gate-check.mjs');
const { recordProof } = await import('../../src/proof/export.mjs');

const RISK2 = signed({ event: 'review.plan', file: 'src/a.mjs', risk: 2 });
const PROOF_FIELDS = { event: 'proof', isolation: 'export', step: 'red-green', test: 'test/a.test.mjs', mechanism: 'revert', red_kind: 'assertion', red: 'RED', green: 'GREEN', proven: true, covers: ['src/a.mjs'] };
const PROVEN = signed(PROOF_FIELDS);
const proofCheck = (/** @type {Array<Record<string, any>>} */ rows, files = FILES, highPaths = /** @type {string[]} */ ([])) =>
  checkBlockReviews({ block: 'B1', runId: 'r1', files, rows: [...files.map((f) => signed({ event: 'review.approved', file: f.file, content_hash: f.content_hash })), ...rows], key: KEY, proofFiles: files, highPaths }).refusals.map((r) => [r.code, r.file ?? null, r.finding ?? null]);
const UNPROVEN_A = [['unproven', 'src/a.mjs', null]];

test('B20: a high-tier file (recorded risk 2, or a high path) with no proof row is unproven; a light file needs no proof', () => {
  assert.deepEqual(proofCheck([RISK2]), UNPROVEN_A);
  assert.deepEqual(proofCheck([], FILES, ['src/**']), UNPROVEN_A);
  assert.deepEqual(proofCheck([signed({ event: 'review.plan', file: 'src/a.mjs', risk: 1 })]), []);
  assert.deepEqual(proofCheck([]), []);
  // a risk row for ANOTHER file does not raise this one
  assert.deepEqual(proofCheck([signed({ event: 'review.plan', file: 'src/b.mjs', risk: 3 })]), []);
});

test('B20: the MAC is checked first — any row naming the file that fails it makes the file risk 3, even with `risk` stripped', () => {
  const { risk: _r, ...stripped } = signed({ event: 'review.plan', file: 'src/a.mjs', risk: 3 });
  assert.deepEqual(proofCheck([stripped]), UNPROVEN_A);
  assert.deepEqual(proofCheck([{ ...RISK2, risk: 0 }]), UNPROVEN_A);
});

test('B20: only a signed, proven, assertion red→green row of this run and block counts', () => {
  assert.deepEqual(proofCheck([RISK2, PROVEN]), []);
  const { mac: _m, ...unsigned } = PROVEN;
  assert.deepEqual(proofCheck([RISK2, unsigned]), UNPROVEN_A);
  assert.deepEqual(proofCheck([RISK2, { ...PROVEN, covers: ['src/a.mjs', 'src/z.mjs'] }]), UNPROVEN_A); // tampered
  assert.deepEqual(proofCheck([RISK2, signed({ ...PROOF_FIELDS, step: 'red' })]), UNPROVEN_A);
  assert.deepEqual(proofCheck([RISK2, signed({ ...PROOF_FIELDS, proven: false, green: null })]), UNPROVEN_A);
  assert.deepEqual(proofCheck([RISK2, signed({ ...PROOF_FIELDS, red_kind: 'import', red: 'RED_INVALID' })]), UNPROVEN_A);
  assert.deepEqual(proofCheck([RISK2, signRow({ ...PROOF_FIELDS, run: 'r0', block: 'B1' }, KEY)]), UNPROVEN_A);
  assert.deepEqual(proofCheck([RISK2, signRow({ ...PROOF_FIELDS, run: 'r1', block: 'B2' }, KEY)]), UNPROVEN_A);
});

test('B20: coverage is per file — two high files and a proof covering one gives exactly 1 refusal, naming the other', () => {
  const two = [{ file: 'src/a.mjs', content_hash: HASH }, { file: 'src/b.mjs', content_hash: 'e'.repeat(64) }];
  assert.deepEqual(proofCheck([PROVEN], two, ['src/**']), [['unproven', 'src/b.mjs', null]]);
  // a row with no `covers` list covers nothing; a deleted file is skipped
  const { covers: _c, ...legacy } = PROOF_FIELDS;
  assert.deepEqual(proofCheck([signed(legacy)], two, ['src/**']), [['unproven', 'src/a.mjs', null], ['unproven', 'src/b.mjs', null]]);
  const withDeleted = [...two, { file: 'src/c.mjs', content_hash: DELETED_HASH }];
  assert.deepEqual(checkBlockProof({ runId: 'r1', files: withDeleted, rows: [], key: KEY, highPaths: ['src/**'] }).map((r) => r.file), ['src/a.mjs', 'src/b.mjs']);
});

test('B20: only a signed human waiver of finding `proof` on that file clears it', () => {
  const waiver = (/** @type {Record<string, any>} */ over) => signed({ event: 'review.waived', file: 'src/a.mjs', finding: 'proof', by: 'human', reason: 'r', ...over });
  assert.deepEqual(proofCheck([RISK2, waiver({})]), []);
  assert.deepEqual(proofCheck([RISK2, waiver({ file: 'src/b.mjs' })]), UNPROVEN_A);
  assert.deepEqual(proofCheck([RISK2, waiver({ by: 'agent' })]), UNPROVEN_A);
  assert.deepEqual(proofCheck([RISK2, waiver({ finding: 'F1' })]), UNPROVEN_A);
});

const HIGH_CONFIG = [
  'version: 1',
  'project:',
  '  slug: gate-proof',
  'provider: anthropic',
  'engine: harness',
  'levels:',
  '  L0: {model: claude-haiku-4-5-20251001}',
  '  L1: {model: claude-sonnet-5}',
  '  L2: {model: claude-opus-5-5}',
  '  L3: {model: claude-fable-5-1}',
  'proof:',
  '  tiers:',
  '    high:',
  '      paths: ["src/auth/**"]',
  '',
].join('\n');
const NO_HIGH_CONFIG = HIGH_CONFIG.split('\n').slice(0, 10).join('\n') + '\n';

test('B20 CLI: close refuses `unproven <file>` for a high file; a signed covering row or a human `block waive … proof` closes it; a light file closes with no proof; high paths come from the base config too; a tampered row stops the block', async () => {
  const repo = await makeRepo();
  writeFile(repo, '.code-forge.yml', HIGH_CONFIG);
  writeFile(repo, 'src/auth/a.mjs', 'export const a = 1;\n');
  writeFile(repo, 'src/b.mjs', 'export const b = 1;\n');
  writeFile(repo, 'coder.log', '$ node --test\n');
  await commitAll(repo);
  const probe = async (/** @type {number} */ pid) => (pid === 4242 ? 'start-A' : null);
  const writeRow = (/** @type {Record<string, any>} */ row) => appendRow(row, { slug: 'gate-proof' });
  await startRun({ workspace: repo, project: 'gate-proof', runId: 'r-pf', workerPid: 4242, writeRow, probe });
  const close = async (/** @type {string} */ id) => {
    const stream = capture();
    const code = await runBlock(['close', id, '--run', 'r-pf', '--worker-pid', '4242', '--transcript', `${repo}/coder.log`], { stdout: stream, stderr: stream, probe });
    return [code, stream.text];
  };
  const open = (/** @type {string} */ id, /** @type {string[]} */ owned) => openBlock({ runId: 'r-pf', id, level: 'L2', owned, acceptance: [{ clause: 'c', tests: ['t'] }], writeRow });
  const approve = (/** @type {string} */ id, /** @type {string} */ file) => writeSigned('r-pf', writeRow, { event: 'review.approved', block: id, file, content_hash: contentHash(repo, file) });
  const proven = (/** @type {string} */ id, /** @type {string[]} */ covers) => recordProof({ runId: 'r-pf', writeRow, row: { block: id, isolation: 'export', step: 'red-green', test: 'test/a.test.mjs', mechanism: 'revert', red_kind: 'assertion', red: 'RED', green: 'GREEN', proven: true, covers } });

  // light: src/b.mjs changed, reviewed, no proof ⇒ closed
  await open('B1', ['src/b.mjs']);
  writeFile(repo, 'src/b.mjs', 'export const b = 2;\n');
  await approve('B1', 'src/b.mjs');
  assert.deepEqual(await close('B1'), [0, 'block B1 closed\n']);

  // high by config path; the coder deletes the high paths in its own block — the base config still counts
  await open('B2', ['src/auth/a.mjs', '.code-forge.yml']);
  writeFile(repo, 'src/auth/a.mjs', 'export const a = 2;\n');
  writeFile(repo, '.code-forge.yml', NO_HIGH_CONFIG);
  await approve('B2', 'src/auth/a.mjs');
  await approve('B2', '.code-forge.yml');
  assert.deepEqual(await close('B2'), [1, 'block B2 open: unproven src/auth/a.mjs\n']);
  // a proof row of ANOTHER block in the same run does not count
  await proven('B1', ['src/auth/a.mjs']);
  assert.deepEqual(await close('B2'), [1, 'block B2 open: unproven src/auth/a.mjs\n']);
  const waived = capture();
  assert.equal(await runBlock(['waive', 'B2', 'proof', '--run', 'r-pf', '--file', 'src/auth/a.mjs', '--reason', 'Ricardo: no test reaches it'], { stdout: waived, stderr: waived }), 0);
  assert.deepEqual(await close('B2'), [0, 'block B2 closed\n']);

  // high by recorded risk: refused, then a signed proven row covering the file closes it
  await open('B3', ['src/c.mjs']);
  writeFile(repo, 'src/c.mjs', 'export const c = 1;\n');
  await approve('B3', 'src/c.mjs');
  await writeSigned('r-pf', writeRow, { event: 'review.plan', block: 'B3', file: 'src/c.mjs', risk: 2 });
  assert.deepEqual(await close('B3'), [1, 'block B3 open: unproven src/c.mjs\n']);
  await proven('B3', ['src/c.mjs']);
  assert.deepEqual(await close('B3'), [0, 'block B3 closed\n']);

  // a tampered proof row never counts: the MAC check stops the block before the gate reads it
  await open('B4', ['src/auth/d.mjs']);
  writeFile(repo, 'src/auth/d.mjs', 'export const d = 1;\n');
  await approve('B4', 'src/auth/d.mjs');
  /** @type {Record<string, any> | null} */
  let captured = null;
  await recordProof({ runId: 'r-pf', writeRow: async (r) => { captured = r; }, row: { block: 'B4', isolation: 'export', step: 'red-green', test: 'test/a.test.mjs', mechanism: 'revert', red_kind: 'assertion', red: 'RED_INVALID', green: null, proven: false, covers: ['src/auth/d.mjs'] } });
  await appendRow({ .../** @type {Record<string, any>} */ (captured), red: 'RED', green: 'GREEN', proven: true }, { slug: 'gate-proof' });
  const [code, text] = await close('B4');
  assert.deepEqual([code, /** @type {string} */ (text).startsWith('block B4 stopped (ledger.tamper): proof: ')], [1, true]);
});

// ── B20 fix round 3 ──

const { runProof } = await import('../../src/cli/proof.mjs');
const { checkBlockProof: proofOnly } = await import('../../src/review/gate-check.mjs');

test('fix 4: proofFiles and highPaths are required — leaving either out is a TypeError, never a silent "no proof needed"', () => {
  const base = { block: 'B1', runId: 'r1', files: FILES, rows: [APPROVED, RISK2], key: KEY };
  assert.throws(() => checkBlockReviews(/** @type {any} */ ({ ...base, highPaths: [] })), { name: 'TypeError', message: 'checkBlockReviews: proofFiles is required (the changed owned files the proof check reads)' });
  assert.throws(() => checkBlockReviews(/** @type {any} */ ({ ...base, proofFiles: FILES })), { name: 'TypeError', message: 'checkBlockReviews: highPaths is required (proof.tiers.high.paths)' });
  assert.throws(() => proofOnly(/** @type {any} */ ({ runId: 'r1', files: FILES, rows: [RISK2], key: KEY })), { name: 'TypeError', message: 'checkBlockProof: highPaths is required (proof.tiers.high.paths)' });
  assert.deepEqual(checkBlockReviews({ ...base, proofFiles: FILES, highPaths: [] }).refusals.map((r) => r.code), ['unproven']);
});

test('fix round 4: runId is required for the proof check (TypeError), and a signed proof row of another run — or with no run — gives exactly 1 unproven', () => {
  const { runId: _r, ...noRun } = { block: 'B1', runId: 'r1', files: FILES, rows: [APPROVED, RISK2], key: KEY, proofFiles: FILES, highPaths: /** @type {string[]} */ ([]) };
  assert.throws(() => checkBlockReviews(/** @type {any} */ (noRun)), { name: 'TypeError', message: 'checkBlockReviews: runId is required (the proof check matches rows on this run only)' });
  assert.throws(() => proofOnly(/** @type {any} */ ({ files: FILES, rows: [RISK2], key: KEY, highPaths: [] })), { name: 'TypeError', message: 'checkBlockProof: runId is required (a proof row counts only for its own run)' });
  const otherRun = signRow({ ...PROOF_FIELDS, run: 'r0', block: 'B1' }, KEY);
  const noRunRow = signRow({ ...PROOF_FIELDS, block: 'B1' }, KEY); // PROOF_FIELDS carries no `run`
  const refusals = (/** @type {Array<Record<string, any>>} */ rows) => proofOnly({ runId: 'r1', files: FILES, rows: [RISK2, ...rows], key: KEY, highPaths: [] }).map((r) => [r.code, r.file]);
  assert.deepEqual(refusals([otherRun]), [['unproven', 'src/a.mjs']]);
  assert.deepEqual(refusals([noRunRow]), [['unproven', 'src/a.mjs']]);
  assert.deepEqual(refusals([PROVEN]), []);
});

test('fix 4 CLI: `block close --no-require-reviews` lifts the review rows but still refuses a high-tier file as unproven', async () => {
  const repo = await makeRepo();
  writeFile(repo, '.code-forge.yml', HIGH_CONFIG);
  writeFile(repo, 'src/auth/a.mjs', 'export const a = 1;\n');
  writeFile(repo, 'coder.log', '$ node --test\n');
  await commitAll(repo);
  const probe = async (/** @type {number} */ pid) => (pid === 4242 ? 'start-A' : null);
  const writeRow = (/** @type {Record<string, any>} */ row) => appendRow(row, { slug: 'gate-nrr-high' });
  await startRun({ workspace: repo, project: 'gate-nrr-high', runId: 'r-nrr-high', workerPid: 4242, writeRow, probe });
  await openBlock({ runId: 'r-nrr-high', id: 'B1', level: 'L2', owned: ['src/auth/a.mjs'], acceptance: [{ clause: 'c', tests: ['t'] }], writeRow });
  writeFile(repo, 'src/auth/a.mjs', 'export const a = 2;\n');
  const stream = capture();
  const code = await runBlock(['close', 'B1', '--run', 'r-nrr-high', '--worker-pid', '4242', '--transcript', `${repo}/coder.log`, '--no-require-reviews'], { stdout: stream, stderr: stream, probe });
  assert.deepEqual([code, stream.text], [1, 'block B1 open: unproven src/auth/a.mjs\n']);
  const rows = await readAllRows('gate-nrr-high');
  assert.deepEqual([rows.filter((r) => r.event === 'gate.reviews_waived').length, rows.filter((r) => r.event === 'block.close').length], [1, 0]);
});

test('fix 1: highPathsAt fails closed on the base — a missing base and an unknown base are git-failed; a known base with no config yields the current paths only', async () => {
  const repo = await makeRepo();
  await assert.rejects(highPathsAt(repo, undefined), { code: 'git-failed', message: 'the block has no base commit — the close cannot read the base config' });
  await assert.rejects(highPathsAt(repo, ''), { code: 'git-failed' });
  await assert.rejects(highPathsAt(repo, '0'.repeat(40)), { code: 'git-failed', message: 'the block base is not a commit of the workspace — the close cannot read the base config' });
  const base = (await git(['rev-parse', 'HEAD'], repo)).trim();
  assert.deepEqual(await highPathsAt(repo, base), []);
  writeFile(repo, '.code-forge.yml', HIGH_CONFIG);
  assert.deepEqual(await highPathsAt(repo, base), ['src/auth/**']);
});

test('fix 1 CLI: a run record whose block lost its base, or names a base the workspace does not know, refuses the close with exit 1', async () => {
  const repo = await makeRepo();
  writeFile(repo, 'src/a.mjs', 'export const a = 1;\n');
  writeFile(repo, 'coder.log', '$ node --test\n');
  await commitAll(repo);
  const probe = async (/** @type {number} */ pid) => (pid === 4242 ? 'start-A' : null);
  const writeRow = (/** @type {Record<string, any>} */ row) => appendRow(row, { slug: 'gate-base' });
  await startRun({ workspace: repo, project: 'gate-base', runId: 'r-base', workerPid: 4242, writeRow, probe });
  await openBlock({ runId: 'r-base', id: 'B1', level: 'L2', owned: ['src/a.mjs'], acceptance: [{ clause: 'c', tests: ['t'] }], writeRow });
  const close = async () => {
    const stream = capture();
    const code = await runBlock(['close', 'B1', '--run', 'r-base', '--worker-pid', '4242', '--transcript', `${repo}/coder.log`], { stdout: stream, stderr: stream, probe });
    return [code, stream.text];
  };
  const setBase = async (/** @type {string | undefined} */ base) => {
    const record = await readRun('r-base');
    record.blocks.B1.base_sha = base;
    await saveRun(record);
  };
  const good = (await readRun('r-base')).blocks.B1.base_sha;
  await setBase(undefined);
  assert.deepEqual(await close(), [1, 'block close: the block has no base commit — the close cannot read the base config\n']);
  await setBase('0'.repeat(40));
  assert.deepEqual(await close(), [1, 'block close: the block base is not a commit of the workspace — the close cannot read the base config\n']);
  await setBase(good);
  assert.deepEqual(await close(), [0, 'block B1 closed\n']);
});

const MATH_BASE = 'export const add = (a, b) => a + b;\nexport const clamp = (n) => n;\n';
const MATH_DONE = 'export const add = (a, b) => a + b;\nexport const clamp = (n, lo, hi) => Math.min(hi, Math.max(lo, n));\n';
const ADD_SPEC = ["import assert from 'node:assert/strict';", "import { test } from 'node:test';", "import { add } from '../src/math.mjs';", '', "test('add pins', () => {", '  assert.equal(add(2, 3), 5);', '});', ''].join('\n');
const CLAMP_SPEC = ["import assert from 'node:assert/strict';", "import { test } from 'node:test';", "import { clamp } from '../src/math.mjs';", '', "test('clamp caps at hi', () => {", '  assert.equal(clamp(5, 0, 3), 3);', '});', ''].join('\n');

test('fix 3 CLI: `proof red-green` with a ./-prefixed test (revert) and a ::case label (assertion-deletion) writes `covers` in the gate\'s own path form, and the close passes', async () => {
  const repo = await makeRepo();
  writeFile(repo, '.gitignore', '.code-forge/\n');
  writeFile(repo, 'src/math.mjs', MATH_BASE);
  writeFile(repo, 'test/add.spec.mjs', ADD_SPEC);
  writeFile(repo, 'coder.log', '$ node --test\n');
  await commitAll(repo);
  const probe = async (/** @type {number} */ pid) => (pid === 4242 ? 'start-A' : null);
  const writeRow = (/** @type {Record<string, any>} */ row) => appendRow(row, { slug: 'gate-covers' });
  await startRun({ workspace: repo, project: 'gate-covers', runId: 'r-cov', workerPid: 4242, writeRow, probe });
  await openBlock({ runId: 'r-cov', id: 'B1', level: 'L2', owned: ['src/math.mjs', 'test/add.spec.mjs', 'test/clamp.spec.mjs'], acceptance: [{ clause: 'c', tests: ['t'] }], writeRow });
  // the block implements clamp, adds its test and touches the characterization test; both high by recorded risk
  writeFile(repo, 'src/math.mjs', MATH_DONE);
  writeFile(repo, 'test/clamp.spec.mjs', CLAMP_SPEC);
  writeFile(repo, 'test/add.spec.mjs', `${ADD_SPEC}// touched\n`);
  for (const file of ['src/math.mjs', 'test/add.spec.mjs']) await writeSigned('r-cov', writeRow, { event: 'review.plan', block: 'B1', file, risk: 2 });
  for (const file of ['src/math.mjs', 'test/add.spec.mjs', 'test/clamp.spec.mjs']) {
    await writeSigned('r-cov', writeRow, { event: 'review.approved', block: 'B1', file, content_hash: contentHash(repo, file) });
  }
  const close = async () => {
    const stream = capture();
    const code = await runBlock(['close', 'B1', '--run', 'r-cov', '--worker-pid', '4242', '--transcript', `${repo}/coder.log`], { stdout: stream, stderr: stream, probe });
    return [code, stream.text];
  };
  const prove = async (/** @type {string[]} */ args) => {
    const stream = capture();
    const errors = capture();
    const code = await runProof(['red-green', 'B1', '--run', 'r-cov', ...args], { stdout: stream, stderr: errors });
    return [code, errors.text];
  };
  assert.deepEqual(await close(), [1, 'block B1 open: unproven src/math.mjs; unproven test/add.spec.mjs\n']);

  assert.deepEqual(await prove(['--test', './test/./clamp.spec.mjs']), [0, 'RED test/clamp.spec.mjs\nGREEN test/clamp.spec.mjs\n']);
  assert.deepEqual(await close(), [1, 'block B1 open: unproven test/add.spec.mjs\n']);

  assert.deepEqual(await prove(['--test', './test/add.spec.mjs::add pins', '--mechanism', 'assertion-deletion']), [0, 'RED test/add.spec.mjs::add pins\nGREEN test/add.spec.mjs::add pins\n']);
  const proofs = (await readAllRows('gate-covers')).filter((r) => r.event === 'proof' && r.step === 'red-green');
  assert.deepEqual(proofs.map((r) => [r.test, r.mechanism, r.proven, r.covers]), [
    ['test/clamp.spec.mjs', 'revert', true, ['src/math.mjs']],
    ['test/add.spec.mjs::add pins', 'assertion-deletion', true, ['test/add.spec.mjs']],
  ]);
  assert.deepEqual(await close(), [0, 'block B1 closed\n']);
});
