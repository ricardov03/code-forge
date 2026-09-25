import { commitAll, git, makeRepo, writeFile } from './helpers.mjs';
import assert from 'node:assert/strict';
import { randomBytes } from 'node:crypto';
import { test } from 'node:test';

const { blockFileSet, checkBlockReviews } = await import('../../src/review/gate-check.mjs');
const { signRow, loadKey } = await import('../../src/state/signer.mjs');
const { isForbidden, mergeForbidden } = await import('../../src/util/forbidden.mjs');
const { startRun, writeSigned } = await import('../../src/state/run.mjs');
const { openBlock } = await import('../../src/state/block.mjs');
const { appendRow, readAllRows } = await import('../../src/ledger/write.mjs');
const { runBlock } = await import('../../src/cli/block.mjs');
const { DELETED_HASH, contentHash } = await import('../../src/worker/ticket.mjs');

const KEY = randomBytes(32);
const HASH = 'a'.repeat(64);
const FILES = [{ file: 'src/a.mjs', content_hash: HASH }];
const signed = (/** @type {Record<string, any>} */ row) => signRow({ run: 'r1', block: 'B1', ...row }, KEY);
const APPROVED = signed({ event: 'review.approved', file: 'src/a.mjs', content_hash: HASH });
const check = (/** @type {Array<Record<string, any>>} */ rows, /** @type {string | null} */ transcript = null) =>
  checkBlockReviews({ block: 'B1', runId: 'r1', files: FILES, rows, key: KEY, transcript }).refusals.map((r) => [r.code, r.file ?? null, r.finding ?? null]);

test('the gate passes a signed approval for the current hash and refuses an unreviewed hash', () => {
  assert.deepEqual(check([APPROVED]), []);
  assert.deepEqual(check([signed({ event: 'review.approved', file: 'src/a.mjs', content_hash: 'b'.repeat(64) })]), [['unreviewed', 'src/a.mjs', null]]);
});

test('the gate refuses an unsigned (or forged) approval row', () => {
  const { mac, ...unsigned } = APPROVED;
  assert.equal(typeof mac, 'string');
  assert.deepEqual(check([unsigned]), [['unsigned', 'src/a.mjs', null]]);
  assert.deepEqual(check([{ ...APPROVED, content_hash: HASH, mac: 'f'.repeat(64) }]), [['unsigned', 'src/a.mjs', null]]);
});

test('the gate refuses a transcript that reads the signer key path', () => {
  const hit = checkBlockReviews({ block: 'B1', runId: 'r1', files: FILES, rows: [APPROVED], key: KEY, transcript: '$ ls src\n$ cat ~/.code-forge/runs/r1.key\n' });
  assert.deepEqual(hit.refusals, [{ code: 'rule_break', detail: 'transcript line 2: code-forge-runs-access' }]);
  assert.deepEqual(check([APPROVED], '$ ls src\n$ node --test\n'), []);
});

test('the gate refuses an unruled late finding; a signed ruling or a human waiver clears it', () => {
  const late = signed({ event: 'review.late_finding', file: 'src/a.mjs', finding: 'L1' });
  assert.deepEqual(check([APPROVED, late]), [['late_unruled', 'src/a.mjs', 'L1']]);
  assert.deepEqual(check([APPROVED, late, signed({ event: 'review.late_ruling', file: 'src/a.mjs', finding: 'L1', ruling: 'nit' })]), []);
  assert.deepEqual(check([APPROVED, late, signed({ event: 'review.waived', file: 'src/a.mjs', finding: 'L1', by: 'human', reason: 'r' })]), []);
  const { mac: _m, ...unsignedWaiver } = signed({ event: 'review.waived', file: 'src/a.mjs', finding: 'L1', by: 'human', reason: 'r' });
  assert.deepEqual(check([APPROVED, late, unsignedWaiver]), [['late_unruled', 'src/a.mjs', 'L1']]);
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
    checkBlockReviews({ block: 'B1', runId: 'r1', files, rows: [...rows, ...extra], key: KEY }).refusals.map((r) => [r.code, r.file, r.finding]);
  assert.deepEqual(refusals([]), [['late_unruled', 'src/a.mjs', 'L1'], ['late_unruled', 'src/b.mjs', 'L1']]);
  assert.deepEqual(refusals([signed({ event: 'review.waived', file: 'src/a.mjs', finding: 'L1', by: 'human', reason: 'r' })]), [['late_unruled', 'src/b.mjs', 'L1']]);
  // a waiver with no file names no file: it clears nothing
  assert.deepEqual(refusals([signed({ event: 'review.waived', finding: 'L1', by: 'human', reason: 'r' })]), [['late_unruled', 'src/a.mjs', 'L1'], ['late_unruled', 'src/b.mjs', 'L1']]);
  // the review_cap check uses the same file-scoped match: src/b.mjs stopped at the cap, unapproved
  const cap = signed({ event: 'review.cap', file: 'src/b.mjs', round: 4, reason: 'review_cap', open: ['L1'] });
  const capRefusals = (/** @type {Array<Record<string, any>>} */ extra) =>
    checkBlockReviews({ block: 'B1', runId: 'r1', files: FILES, rows: [APPROVED, cap, ...extra], key: KEY }).refusals.map((r) => [r.code, r.file, r.finding]);
  assert.deepEqual(capRefusals([]), [['review_cap', 'src/b.mjs', 'L1']]);
  assert.deepEqual(capRefusals([signed({ event: 'review.waived', file: 'src/a.mjs', finding: 'L1', by: 'human', reason: 'r' })]), [['review_cap', 'src/b.mjs', 'L1']]);
  assert.deepEqual(capRefusals([signed({ event: 'review.waived', file: 'src/b.mjs', finding: 'L1', by: 'human', reason: 'r' })]), []);
});

test('`block waive` argv is forbidden for a coder through mergeForbidden itself; the orchestrator verbs are not', () => {
  const list = mergeForbidden([]);
  for (const argv of [
    ['code-forge', 'block', 'waive', 'B1', 'F1', '--reason', 'x'],
    ['npx', '@ricardov/code-forge', 'block', 'waive', 'B1', 'F1'],
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
