// B96 (E1): `block close` builds the close report itself. After the review gate passes it prints
// `reviewed <path> ticket <id>` for every reviewed file (the id is the queue's ticket for that file
// and hash), before the `reviewed_by_waiver` lines and the `closed` line, and puts `reviewed:
// [{file, ticket, via}]` in `--json` and the done file. A `--report` with no ticket lines closes;
// a hand line that names another ticket warns (D1). Every test drives the real `block close`
// in process (temp HOME from helpers.mjs, temp repo, temp run).
import { freshDir, git, writeFile } from '../review/helpers.mjs';
import { humanSaysYes } from '../helpers/human-confirm.mjs';
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { readFileSync, realpathSync } from 'node:fs';
import path from 'node:path';
import { test } from 'node:test';

const { runRun } = await import('../../src/cli/run.mjs');
const { runBlock } = await import('../../src/cli/block.mjs');
const { appendRow } = await import('../../src/ledger/write.mjs');
const { writeSigned } = await import('../../src/state/run.mjs');
const { doneFileContent, doneFilePath } = await import('../../src/state/block-end.mjs');
const { checkScope, claimPath } = await import('../../src/state/block.mjs');
const { contentHash, ticketId } = await import('../../src/worker/ticket.mjs');
const { enqueue, resolveTicket } = await import('../../src/worker/queue.mjs');
const { reportMismatches, reviewedLines } = await import('../../src/review/reviewed-lines.mjs');

/** sha256 hex of a string or bytes, by hand (node:crypto), for pins that do not go through ticketId. @param {string | Buffer} s */
const sha256 = (s) => createHash('sha256').update(s).digest('hex');

const PROBE = async (/** @type {number} */ pid) => (pid === 4242 ? 'start-A' : null);
const A = 'src/a.mjs';
const B = 'src/b.mjs';
const C = 'src/c.mjs';

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

/** @param {string[]} args @param {Record<string, any>} [deps] @returns {Promise<[number, string, string]>} exit code, stdout, stderr */
async function cli(fn, args, deps = {}) {
  const stdout = capture();
  const stderr = capture();
  const code = await fn(args, { stdout, stderr, probe: PROBE, isTTY: false, ...deps });
  return [code, stdout.text, stderr.text];
}

/** @param {string} slug @param {string[]} extra - config lines after the levels. */
const configYaml = (slug, extra) =>
  ['version: 1', 'project:', `  slug: ${slug}`, 'provider: anthropic', 'engine: harness', 'levels:', '  L0: {model: claude-haiku-4-5-20251001}', '  L1: {model: claude-sonnet-5}', '  L2: {model: claude-opus-5-5}', '  L3: {model: claude-fable-5-1}', ...extra, ''].join('\n');

const ACC_DIR = freshDir('acceptance');
writeFile(ACC_DIR, 'acc.json', JSON.stringify([{ clause: 'the close report is built by the tool', tests: ['close-report'] }]));
const ACC = path.join(ACC_DIR, 'acc.json');

/**
 * A committed repository with `src/a.mjs`, `src/b.mjs`, `src/c.mjs` and a transcript; `run start`
 * in process with the fake worker pid 4242; block B1 open owning `owned`.
 * @param {string} slug @param {{extra?: string[], owned?: string[]}} [opts]
 */
async function fixture(slug, { extra = [], owned = [A, B] } = {}) {
  const repo = realpathSync(freshDir('repo'));
  await git(['init', '-q', '-b', 'main'], repo);
  writeFile(repo, '.code-forge.yml', configYaml(slug, extra));
  for (const f of [A, B, C]) writeFile(repo, f, `export const v = '${f} 0';\n`);
  writeFile(repo, 'coder.log', '$ node --test\n');
  await git(['add', '-A'], repo);
  await git(['commit', '-q', '-m', 'base'], repo);
  const runId = `r-${slug}`;
  const [started, , startErr] = await cli(runRun, ['start', '--cwd', repo, '--run', runId, '--worker-pid', '4242']);
  assert.equal(started, 0, startErr);
  const [opened, , openErr] = await cli(runBlock, ['open', 'B1', '--run', runId, '--level', 'L2', '--owned', ...owned, '--acceptance', ACC]);
  assert.equal(opened, 0, openErr);
  const writeRow = (/** @type {Record<string, any>} */ row) => appendRow(row, { slug });
  const where = extra.some((l) => l.includes('isolation: worktree')) ? `${repo}-worktrees/B1` : repo;
  /** @param {string} file @param {string} content */
  const edit = (file, content) => writeFile(where, file, content);
  /** A signed row of B1 for `file` at its current hash in the block's tree. @param {string} file @param {Record<string, any>} row */
  const sign = (file, row) => writeSigned(runId, writeRow, { block: 'B1', file, content_hash: contentHash(where, file), ...row });
  /** The queue's ticket for `file`'s current content (what `review-file` would answer). @param {string} file */
  const ticket = (file) => enqueue({ repoRoot: repo, run: runId, block: 'B1', file, tree: where === repo ? null : where }).ticket;
  /** @param {string} name @param {string} text @returns {string} a report file outside the repo */
  const report = (name, text) => {
    const dir = freshDir(`report-${name}`);
    writeFile(dir, 'report.md', text);
    return path.join(dir, 'report.md');
  };
  /** @param {string[]} [more] @param {Record<string, any>} [deps] */
  const close = (more = [], deps = {}) => cli(runBlock, ['close', 'B1', '--run', runId, '--worker-pid', '4242', '--transcript', `${repo}/coder.log`, ...more], deps);
  const done = () => JSON.parse(readFileSync(doneFilePath(runId, 'B1'), 'utf8'));
  return { repo, runId, where, edit, sign, ticket, report, close, done, writeRow };
}

// ── clause 1: the reproduction (written first; on the base sha the close refused) ──────────────

test('B96 (1): a and b approved, round 2 changes only b and is approved; a report with only the b line closes (the base refused `coder report FAILED: no review-file ticket id for src/a.mjs`)', async () => {
  const fx = await fixture('b96-repro');
  // round 1: both files changed and approved
  fx.edit(A, "export const v = 'a 1';\n");
  fx.edit(B, "export const v = 'b 1';\n");
  const ta = fx.ticket(A);
  await fx.sign(A, { event: 'review.approved', round: 1 });
  await fx.sign(B, { event: 'review.approved', round: 1 });
  // round 2: only b changes and is approved again
  fx.edit(B, "export const v = 'b 2';\n");
  const tb = fx.ticket(B);
  await fx.sign(B, { event: 'review.approved', round: 2 });
  // the fix-round report lists only the file of the round (the brief's old rule)
  const report = fx.report('round2', `reviewed ${B} ticket ${tb}\nTESTS npm test exit=0\n===BLOCK B1 COMPLETE===\n`);
  assert.deepEqual(await fx.close(['--report', report]), [0, `reviewed ${A} ticket ${ta}\nreviewed ${B} ticket ${tb}\nblock B1 closed\n`, '']);
  // the id pinned by hand, not through ticketId: the first 24 hex of sha256(run \0 block \0 file \0 sha256(bytes))
  assert.equal(ta, sha256([fx.runId, 'B1', A, sha256("export const v = 'a 1';\n")].join('\0')).slice(0, 24));
});

// ── clause 2: the ids are the queue's, also in a worktree block ─────────────────────────────────

test('B96 (2): in a worktree block the close prints the queue ticket of the tree (the tree is part of the id), before the closed line', async () => {
  const fx = await fixture('b96-tree', { extra: ['proof:', '  isolation: worktree'], owned: [A] });
  fx.edit(A, "export const v = 'a tree';\n");
  await fx.sign(A, { event: 'review.approved', round: 1 });
  const queued = fx.ticket(A);
  // the main-tree id for the same bytes is another id: the close must name the tree's
  const mainId = ticketId({ run: fx.runId, block: 'B1', file: A, content_hash: contentHash(fx.where, A) });
  assert.notEqual(queued, mainId);
  assert.deepEqual(await fx.close(), [0, `reviewed ${A} ticket ${queued}\nblock B1 closed\n`, '']);
});

// ── clause 3: via approved, waiver, answered — text order, --json and the done file ─────────────

/**
 * Three reviewed files: a approved; b's review left W1 (warning), waived by the human; c's review
 * left C1 (critical), answered by a reviewer through a (`review.answered`).
 * @param {string} slug
 */
async function threeWays(slug) {
  const fx = await fixture(slug, { owned: [A, B, C] });
  fx.edit(A, "export const v = 'a 1';\n");
  fx.edit(B, "export const v = 'b 1';\n");
  fx.edit(C, "export const v = 'c 1';\n");
  await fx.sign(A, { event: 'review.approved', round: 1 });
  await fx.sign(B, { event: 'review.round', round: 1, kind: 'full', open_after: 1 });
  await fx.sign(B, { event: 'review.result', ticket: fx.ticket(B), status: 'reviewed', approved: false, open: [{ id: 'W1', severity: 'warning' }] });
  await fx.sign(B, { event: 'review.waived', finding: 'W1', reason: 'owner accepted it', by: 'human' });
  await fx.sign(C, { event: 'review.round', round: 1, kind: 'full', open_after: 1 });
  await fx.sign(C, { event: 'review.result', ticket: fx.ticket(C), status: 'reviewed', approved: false, open: [{ id: 'C1', severity: 'critical' }] });
  await fx.sign(C, { event: 'review.answered', finding: 'C1', by_file: A, by_hash: contentHash(fx.where, A), ticket: 'f'.repeat(24) });
  return { fx, ta: fx.ticket(A), tb: fx.ticket(B), tc: fx.ticket(C) };
}

test('B96 (2)+(3): the text close prints the reviewed lines (sorted by path), then the reviewed_by_waiver lines, then closed', async () => {
  const { fx, ta, tb, tc } = await threeWays('b96-text');
  assert.deepEqual(await fx.close(), [
    0,
    `reviewed ${A} ticket ${ta}\nreviewed ${B} ticket ${tb}\nreviewed ${C} ticket ${tc}\n` +
      `block B1: ${B} reviewed_by_waiver (W1; by human)\nblock B1: ${C} reviewed_by_waiver (answered C1)\nblock B1 closed\n`,
    '',
  ]);
  assert.deepEqual(fx.done().reviewed, [
    { file: A, ticket: ta, via: 'approved' },
    { file: B, ticket: tb, via: 'waiver' },
    { file: C, ticket: tc, via: 'answered' },
  ]);
});

test('B96 (3): --json and the done file carry reviewed [{file, ticket, via}] sorted by file; the other keys are unchanged', async () => {
  const { fx, ta, tb, tc } = await threeWays('b96-json');
  const [code, out, err] = await fx.close(['--json']);
  assert.deepEqual([code, err], [0, '']);
  const reviewed = [
    { file: A, ticket: ta, via: 'approved' },
    { file: B, ticket: tb, via: 'waiver' },
    { file: C, ticket: tc, via: 'answered' },
  ];
  const b = contentHash(fx.where, B);
  const c = contentHash(fx.where, C);
  assert.deepEqual(JSON.parse(out), {
    block: 'B1',
    event: 'closed',
    status: 'closed',
    reason: null,
    close_event: null,
    reviewed_by_waiver: [
      { file: B, content_hash: b, status: 'reviewed_by_waiver', findings: ['W1'], by: 'human' },
      { file: C, content_hash: c, status: 'reviewed_by_waiver', findings: [], by: null, answered: ['C1'] },
    ],
    reviewed,
  });
  const { at, commit, ...done } = fx.done();
  assert.equal(typeof at, 'string');
  assert.equal(commit, (await git(['rev-parse', 'HEAD'], fx.repo)).trim());
  assert.deepEqual(done, { block: 'B1', run_id: fx.runId, status: 'closed', event: 'block.close', reason: null, spent_usd: 0, unknown_usd_sessions: 0, findings: 0, reviewed });
  assert.deepEqual(Object.keys(fx.done()), ['block', 'run_id', 'status', 'event', 'reason', 'commit', 'spent_usd', 'unknown_usd_sessions', 'findings', 'at', 'reviewed']);
});

// ── clauses 4 and 5: the hand lines ─────────────────────────────────────────────────────────────

test('B96 (4): a --report with a COMPLETE sentinel and no ticket line closes when the gate passes', async () => {
  const fx = await fixture('b96-nolines', { owned: [A] });
  fx.edit(A, "export const v = 'a 1';\n");
  await fx.sign(A, { event: 'review.approved', round: 1 });
  const report = fx.report('nolines', 'all done\nTESTS npm test exit=0\n===BLOCK B1 COMPLETE===\n');
  assert.deepEqual(await fx.close(['--report', report]), [0, `reviewed ${A} ticket ${fx.ticket(A)}\nblock B1 closed\n`, '']);
});

test('B96 (5): a hand line naming another 24-hex id prints `WARN report ticket mismatch <path>` on stderr and the close goes on with the tool line; the right id warns nothing', async () => {
  const fx = await fixture('b96-mismatch');
  fx.edit(A, "export const v = 'a 1';\n");
  fx.edit(B, "export const v = 'b 1';\n");
  await fx.sign(A, { event: 'review.approved', round: 1 });
  await fx.sign(B, { event: 'review.approved', round: 1 });
  const ta = fx.ticket(A);
  const tb = fx.ticket(B);
  const other = '0123456789abcdef01234567';
  assert.notEqual(other, ta);
  const report = fx.report('mismatch', `reviewed ${A} ticket ${other}\nreviewed ${B} ticket ${tb}\nTESTS npm test exit=0\n===BLOCK B1 COMPLETE===\n`);
  assert.deepEqual(await fx.close(['--report', report]), [0, `reviewed ${A} ticket ${ta}\nreviewed ${B} ticket ${tb}\nblock B1 closed\n`, `WARN report ticket mismatch ${A}\n`]);
});

test('B96 (5): an unreadable --report is still refused before anything closes', async () => {
  const fx = await fixture('b96-unreadable', { owned: [A] });
  fx.edit(A, "export const v = 'a 1';\n");
  await fx.sign(A, { event: 'review.approved', round: 1 });
  assert.deepEqual(await fx.close(['--report', path.join(freshDir('none'), 'no-such-report.md')]), [1, '', 'block B1 open: coder report FAILED: --report cannot be read\n']);
});

// ── clause 6: waived or external-review closes are unchanged ────────────────────────────────────

test('B96 (6): an external-review close with --report and no lines closes as today — no reviewed line, no reviewed key in --json or the done file', async () => {
  const fx = await fixture('b96-external', { extra: ['review:', '  external: {required: true, name: Grok}'], owned: [A] });
  fx.edit(A, "export const v = 'a 1';\n");
  const report = fx.report('external', 'TESTS npm test exit=0\n===BLOCK B1 COMPLETE===\n');
  const [code, out, err] = await fx.close(['--report', report, '--json']);
  assert.deepEqual([code, JSON.parse(out), err], [0, { block: 'B1', event: 'closed', status: 'closed', reason: null, close_event: null }, '']);
  assert.equal(Object.hasOwn(fx.done(), 'reviewed'), false);
});

test('B96 (6): a human --no-require-reviews close with --report and no lines closes as today', async () => {
  const fx = await fixture('b96-waived', { owned: [A] });
  fx.edit(A, "export const v = 'a 1';\n");
  const report = fx.report('waived', 'TESTS npm test exit=0\n===BLOCK B1 COMPLETE===\n');
  assert.deepEqual(await fx.close(['--report', report, '--no-require-reviews'], humanSaysYes()), [0, 'block B1 closed\n', '']);
  assert.equal(Object.hasOwn(fx.done(), 'reviewed'), false);
});

// ── review round 1 ──────────────────────────────────────────────────────────────────────────────

test('B96 r1 (1): a reviewed-lines builder that throws never stops a closed block — exit 0, one `WARN reviewed lines unavailable: <message>`, the done file and the on_block_end hook once', async () => {
  const hookDir = freshDir('b96-hook');
  writeFile(hookDir, 'hook.mjs', "import { appendFileSync } from 'node:fs';\nappendFileSync(process.argv[2], 'ran\\n');\n");
  const out = path.join(hookDir, 'ran.txt');
  const fx = await fixture('b96-throws', { owned: [A], extra: ['hooks:', '  on_block_end:', `    argv: ${JSON.stringify([process.execPath, path.join(hookDir, 'hook.mjs'), out])}`] });
  fx.edit(A, "export const v = 'a 1';\n");
  await fx.sign(A, { event: 'review.approved', round: 1 });
  const boom = () => {
    throw new Error('boom');
  };
  assert.deepEqual(await fx.close([], { reviewedLines: boom }), [0, 'block B1 closed\n', 'WARN reviewed lines unavailable: boom\n']);
  const { at: _at, commit: _commit, ...done } = fx.done();
  assert.deepEqual(done, { block: 'B1', run_id: fx.runId, status: 'closed', event: 'block.close', reason: null, spent_usd: 0, unknown_usd_sessions: 0, findings: 0 });
  assert.equal(readFileSync(out, 'utf8'), 'ran\n');
});

test('B96 r3: a reviewed-lines builder that returns a rejected promise is caught like a throw — exit 0, one `WARN reviewed lines unavailable: late boom`, the done file once', async () => {
  const fx = await fixture('b96-rejects', { owned: [A] });
  fx.edit(A, "export const v = 'a 1';\n");
  await fx.sign(A, { event: 'review.approved', round: 1 });
  const lateBoom = async () => {
    throw new Error('late boom');
  };
  assert.deepEqual(await fx.close([], { reviewedLines: lateBoom }), [0, 'block B1 closed\n', 'WARN reviewed lines unavailable: late boom\n']);
  assert.equal(fx.done().status, 'closed');
});

test('B96 r1 (3): every passing gate gives the lines — an all-approved close (no reviewed_by_waiver file) prints one line per file', async () => {
  const fx = await fixture('b96-allapproved', { owned: [A, B, C] });
  for (const f of [A, B, C]) {
    fx.edit(f, `export const v = '${f} 1';\n`);
    await fx.sign(f, { event: 'review.approved', round: 1 });
  }
  assert.deepEqual(await fx.close(), [0, `reviewed ${A} ticket ${fx.ticket(A)}\nreviewed ${B} ticket ${fx.ticket(B)}\nreviewed ${C} ticket ${fx.ticket(C)}\nblock B1 closed\n`, '']);
});

test('B96 r1 (4): a mismatched hand line followed by a later refusal (an orphan in the registry) prints no WARN; once the orphan is claimed the close warns and closes', async () => {
  const fx = await fixture('b96-latewarn', { owned: [A] });
  fx.edit(A, "export const v = 'a 1';\n");
  await fx.sign(A, { event: 'review.approved', round: 1 });
  await checkScope({ runId: fx.runId, id: 'B1', files: [A, 'zz.txt'], writeRow: fx.writeRow });
  const report = fx.report('latewarn', `reviewed ${A} ticket ${'0'.repeat(24)}\nTESTS npm test exit=0\n===BLOCK B1 COMPLETE===\n`);
  assert.deepEqual(await fx.close(['--report', report]), [1, '', 'block B1 open: orphans: zz.txt\n']);
  await claimPath({ runId: fx.runId, id: 'B1', file: 'zz.txt', writeRow: fx.writeRow });
  assert.deepEqual(await fx.close(['--report', report]), [0, `reviewed ${A} ticket ${fx.ticket(A)}\nblock B1 closed\n`, `WARN report ticket mismatch ${A}\n`]);
});

test('B96 r1 (5): via is the gate record — a file with one finding waived and one answered is `waiver` (the waiver is named); all answered is `answered`, all waived is `waiver`', async () => {
  const fx = await fixture('b96-mixed', { owned: [A, B] });
  fx.edit(A, "export const v = 'a 1';\n");
  fx.edit(B, "export const v = 'b 1';\n");
  await fx.sign(A, { event: 'review.approved', round: 1 });
  await fx.sign(B, { event: 'review.round', round: 1, kind: 'full', open_after: 2 });
  await fx.sign(B, { event: 'review.result', ticket: fx.ticket(B), status: 'reviewed', approved: false, open: [{ id: 'W1', severity: 'warning' }, { id: 'C1', severity: 'critical' }] });
  await fx.sign(B, { event: 'review.waived', finding: 'W1', reason: 'owner accepted it', by: 'human' });
  await fx.sign(B, { event: 'review.answered', finding: 'C1', by_file: A, by_hash: contentHash(fx.where, A), ticket: 'f'.repeat(24) });
  const [code, out, err] = await fx.close(['--json']);
  assert.deepEqual([code, err], [0, '']);
  const json = JSON.parse(out);
  assert.deepEqual(json.reviewed_by_waiver.map((/** @type {any} */ w) => [w.file, w.findings, w.by, w.answered]), [[B, ['W1'], 'human', ['C1']]]);
  assert.deepEqual(json.reviewed, [
    { file: A, ticket: fx.ticket(A), via: 'approved' },
    { file: B, ticket: fx.ticket(B), via: 'waiver' },
  ]);
});

test('B96 r1 (6): a gate-accepted file whose ticket cannot be given has no line and prints one `WARN no ticket for <path>`; with a --report lacking its hand line the old refusal fires and nothing warns', async () => {
  const noA = (/** @type {any} */ t) => {
    if (t.file === A) throw new Error('no ticket');
    return enqueue(t);
  };
  const fx = await fixture('b96-noticket');
  fx.edit(A, "export const v = 'a 1';\n");
  fx.edit(B, "export const v = 'b 1';\n");
  await fx.sign(A, { event: 'review.approved', round: 1 });
  await fx.sign(B, { event: 'review.approved', round: 1 });
  const report = fx.report('noticket', 'TESTS npm test exit=0\n===BLOCK B1 COMPLETE===\n');
  assert.deepEqual(await fx.close(['--report', report], { resolveTicket: noA }), [1, '', `block B1 open: coder report FAILED: no review-file ticket id for ${A}\n`]);
  assert.deepEqual(await fx.close([], { resolveTicket: noA }), [0, `reviewed ${B} ticket ${fx.ticket(B)}\nblock B1 closed\n`, `WARN no ticket for ${A}\n`]);
  assert.deepEqual(fx.done().reviewed, [{ file: B, ticket: fx.ticket(B), via: 'approved' }]);
});

test('B96 r1 (7): only a `reviewed <path> ticket <id>` line is a hand line — a 40- or 64-hex value, or a 24-hex id in another shape, on a line naming the file does not warn', async () => {
  const fx = await fixture('b96-shapes', { owned: [A] });
  fx.edit(A, "export const v = 'a 1';\n");
  await fx.sign(A, { event: 'review.approved', round: 1 });
  const report = fx.report('shapes', `${A} commit ${'a'.repeat(40)}\n${A} sha256 ${'b'.repeat(64)}\n${A} ticket ${'c'.repeat(24)}\nTESTS npm test exit=0\n===BLOCK B1 COMPLETE===\n`);
  assert.deepEqual(await fx.close(['--report', report]), [0, `reviewed ${A} ticket ${fx.ticket(A)}\nblock B1 closed\n`, '']);
  // the parser: a list marker and backticks are allowed around the one shape that counts
  const lines = [{ file: A, ticket: '1'.repeat(24), via: /** @type {const} */ ('approved') }];
  assert.deepEqual(reportMismatches(`- \`reviewed ${A} ticket ${'2'.repeat(24)}\`\n`, lines), [A]);
  assert.deepEqual(reportMismatches(`reviewed ${A} ticket ${'2'.repeat(40)}\nreviewed ${A} ticket ${'1'.repeat(24)}\n`, lines), []);
});

test('B96 r1 (8): the done file sorts reviewed by file, whatever order it is given', () => {
  const content = doneFileContent({
    block: 'B1', runId: 'r', status: 'closed', event: 'block.close', commit: null, spend: null, findings: 0, at: new Date('2026-10-13T00:00:00.000Z'),
    reviewed: [{ file: C, ticket: 'c'.repeat(24), via: 'approved' }, { file: A, ticket: 'a'.repeat(24), via: 'waiver' }, { file: B, ticket: 'b'.repeat(24), via: 'answered' }],
  });
  assert.deepEqual(content.reviewed.map((/** @type {any} */ r) => r.file), [A, B, C]);
});

test('B96 r1 (10): an owned file that did not change gets no line; a close the gate refuses prints none', async () => {
  const fx = await fixture('b96-unchanged', { owned: [A, B] });
  fx.edit(A, "export const v = 'a 1';\n");
  await fx.sign(A, { event: 'review.approved', round: 1 });
  fx.edit(C, "export const v = 'c 1';\n");
  assert.deepEqual(await fx.close(), [1, '', `block B1 open: unowned_change ${C}\n`]);
  fx.edit(C, `export const v = '${C} 0';\n`);
  assert.deepEqual(await fx.close(), [0, `reviewed ${A} ticket ${fx.ticket(A)}\nblock B1 closed\n`, '']);
});

test('B96 r1 (10): under context.require_coder_tests a --report with no COMPLETE sentinel, or with no TESTS line, is refused with the text of the base (1d2d062)', async () => {
  for (const [slug, text, reason] of [
    ['b96-nosentinel', 'TESTS npm test exit=0\n', 'no ===BLOCK B1 COMPLETE=== sentinel'],
    ['b96-notests', '===BLOCK B1 COMPLETE===\n', 'no TESTS line'],
  ]) {
    const fx = await fixture(slug, { owned: [A], extra: ['context:', '  require_coder_tests: true'] });
    fx.edit(A, "export const v = 'a 1';\n");
    await fx.sign(A, { event: 'review.approved', round: 1 });
    assert.deepEqual(await fx.close(['--report', fx.report(slug, text)]), [1, '', `block B1 open: coder report FAILED: ${reason}\n`]);
  }
});

test('B96 r1 (5)(6): reviewedLines reads via from the gate record and names a file with no ticket once', () => {
  /** @type {string[]} */
  const missing = [];
  const lines = reviewedLines({
    files: [{ file: C, content_hash: 'h' }, { file: A, content_hash: 'h' }, { file: B, content_hash: 'h' }],
    waivedAll: [{ file: B, content_hash: 'h', status: 'reviewed_by_waiver', findings: [], by: null, answered: ['C1'] }, { file: C, content_hash: 'h', status: 'reviewed_by_waiver', findings: ['W1'], by: 'autopilot', answered: ['C2'] }],
    ticketOf: (f) => (f.file === A ? null : f.file === B ? 'b'.repeat(24) : 'c'.repeat(24)),
    onNoTicket: (file) => missing.push(file),
  });
  assert.deepEqual(lines, [{ file: B, ticket: 'b'.repeat(24), via: 'answered' }, { file: C, ticket: 'c'.repeat(24), via: 'waiver' }]);
  assert.deepEqual(missing, [A]);
});

// ── review round 2 ──────────────────────────────────────────────────────────────────────────────

/** A reviewed-lines builder that throws: the close cannot build the lines at all. */
const linesFail = () => {
  throw new Error('boom');
};

const SENTINEL = 'TESTS npm test exit=0\n===BLOCK B1 COMPLETE===\n';

/** @param {string} slug @param {string[]} [owned] a fixture whose owned files all changed and are approved */
async function allApproved(slug, owned = [A, B]) {
  const fx = await fixture(slug, { owned });
  for (const f of owned) {
    fx.edit(f, `export const v = '${f} 1';\n`);
    await fx.sign(f, { event: 'review.approved', round: 1 });
  }
  return fx;
}

test('B96 r2 (6): when the lines cannot be built at all, --report needs a hand line for every file the gate accepts — none, or only a\'s, is refused for the rest and prints no WARN', async () => {
  const fx = await allApproved('b96-r2-nolines');
  const deps = { reviewedLines: linesFail };
  assert.deepEqual(await fx.close(['--report', fx.report('bare', SENTINEL)], deps), [1, '', `block B1 open: coder report FAILED: no review-file ticket id for ${A}, ${B}\n`]);
  const onlyA = fx.report('only-a', `reviewed ${A} ticket ${fx.ticket(A)}\n${SENTINEL}`);
  assert.deepEqual(await fx.close(['--report', onlyA], deps), [1, '', `block B1 open: coder report FAILED: no review-file ticket id for ${B}\n`]);
});

test('B96 r2 (5)(6): `WARN reviewed lines unavailable` is held — a later refusal (an orphan) prints it not; once the orphan is claimed the close prints it once, and no hand line is compared with a ticket', async () => {
  const fx = await allApproved('b96-r2-held');
  const deps = { reviewedLines: linesFail };
  await checkScope({ runId: fx.runId, id: 'B1', files: [A, B, 'zz.txt'], writeRow: fx.writeRow });
  // a hand line for each file, neither naming the file's ticket
  const report = fx.report('held', `reviewed ${A} ticket ${'0'.repeat(24)}\nreviewed ${B} ticket ${'1'.repeat(24)}\n${SENTINEL}`);
  assert.notEqual(fx.ticket(A), '0'.repeat(24));
  assert.deepEqual(await fx.close(['--report', report], deps), [1, '', 'block B1 open: orphans: zz.txt\n']);
  await claimPath({ runId: fx.runId, id: 'B1', file: 'zz.txt', writeRow: fx.writeRow });
  assert.deepEqual(await fx.close(['--report', report], deps), [0, 'block B1 closed\n', 'WARN reviewed lines unavailable: boom\n']);
  assert.equal(Object.hasOwn(fx.done(), 'reviewed'), false);
});

test('B96 r2 (3)(6): a file whose content changed after the review gate read it gets no line — with --report and no hand line for it the close refuses; with one, a later refusal prints no WARN; the close that succeeds prints `WARN no ticket for <path>` once', async () => {
  const fx = await allApproved('b96-r2-moved');
  const approvedA = `export const v = '${A} 1';\n`;
  const ta = fx.ticket(A);
  const tb = fx.ticket(B);
  // the queue is asked for a's ticket only after the gate passed: a changes in between
  const moveA = (/** @type {any} */ t) => {
    if (t.file === A) fx.edit(A, "export const v = 'a moved';\n");
    return resolveTicket(t);
  };
  const deps = { resolveTicket: moveA };
  assert.deepEqual(await fx.close(['--report', fx.report('only-b', `reviewed ${B} ticket ${tb}\n${SENTINEL}`)], deps), [1, '', `block B1 open: coder report FAILED: no review-file ticket id for ${A}\n`]);
  fx.edit(A, approvedA);
  await checkScope({ runId: fx.runId, id: 'B1', files: [A, B, 'zz.txt'], writeRow: fx.writeRow });
  const both = fx.report('both', `reviewed ${A} ticket ${ta}\nreviewed ${B} ticket ${tb}\n${SENTINEL}`);
  assert.deepEqual(await fx.close(['--report', both], deps), [1, '', 'block B1 open: orphans: zz.txt\n']);
  fx.edit(A, approvedA);
  await claimPath({ runId: fx.runId, id: 'B1', file: 'zz.txt', writeRow: fx.writeRow });
  assert.deepEqual(await fx.close(['--report', both], deps), [0, `reviewed ${B} ticket ${tb}\nblock B1 closed\n`, `WARN no ticket for ${A}\n`]);
  assert.deepEqual(fx.done().reviewed, [{ file: B, ticket: tb, via: 'approved' }]);
});

test('B96 r2 (7): a ticket that is a string but not 24 lowercase hex (here in capitals) gives `WARN bad ticket for <path>` and no line, not `WARN no ticket`; a later refusal prints it not', async () => {
  const fx = await allApproved('b96-r2-badticket');
  const ta = fx.ticket(A);
  const tb = fx.ticket(B);
  assert.match(ta, /[a-f]/, 'the id has a letter, so its capitals are another string');
  const upperA = (/** @type {any} */ t) => {
    const r = resolveTicket(t);
    return t.file === A ? { ...r, ticket: r.ticket.toUpperCase() } : r;
  };
  const deps = { resolveTicket: upperA };
  // with --report the lines are built before the orphan check refuses (a has no line, so it needs its hand line)
  await checkScope({ runId: fx.runId, id: 'B1', files: [A, B, 'zz.txt'], writeRow: fx.writeRow });
  assert.deepEqual(await fx.close(['--report', fx.report('only-a', `reviewed ${A} ticket ${ta}\n${SENTINEL}`)], deps), [1, '', 'block B1 open: orphans: zz.txt\n']);
  await claimPath({ runId: fx.runId, id: 'B1', file: 'zz.txt', writeRow: fx.writeRow });
  assert.deepEqual(await fx.close([], deps), [0, `reviewed ${B} ticket ${tb}\nblock B1 closed\n`, `WARN bad ticket for ${A}\n`]);
  assert.deepEqual(fx.done().reviewed, [{ file: B, ticket: tb, via: 'approved' }]);
});

test('B96 r2 (7): reviewedLines — null or undefined is no ticket; any other value that is not 24 lowercase hex is a bad ticket; each file is named once', () => {
  /** @type {Record<string, unknown>} */
  const given = {
    'n.mjs': null,
    'u.mjs': undefined,
    'caps.mjs': 'A'.repeat(24),
    'long.mjs': 'a'.repeat(40),
    'short.mjs': 'a'.repeat(23),
    'nothex.mjs': 'g'.repeat(24),
    'empty.mjs': '',
    'num.mjs': 42,
    'nl.mjs': `${'a'.repeat(24)}\n`,
    'ok.mjs': 'a'.repeat(24),
  };
  /** @type {string[]} */
  const none = [];
  /** @type {string[]} */
  const bad = [];
  const lines = reviewedLines({
    files: Object.keys(given).map((file) => ({ file, content_hash: 'h' })),
    waivedAll: [],
    ticketOf: (f) => given[f.file],
    onNoTicket: (file) => none.push(file),
    onBadTicket: (file) => bad.push(file),
  });
  assert.deepEqual(lines, [{ file: 'ok.mjs', ticket: 'a'.repeat(24), via: 'approved' }]);
  assert.deepEqual(none, ['n.mjs', 'u.mjs']);
  assert.deepEqual(bad, ['caps.mjs', 'long.mjs', 'short.mjs', 'nothex.mjs', 'empty.mjs', 'num.mjs', 'nl.mjs']);
});

test('B96 r2 (8): a hand line may carry a numbered list marker and backticks; an id in capitals — another id, or the tool\'s own — names another ticket and warns', () => {
  const tool = '0123456789abcdef01234567';
  const other = 'fedcba9876543210fedcba98';
  const lines = [{ file: A, ticket: tool, via: /** @type {const} */ ('approved') }];
  const shapes = (/** @type {string} */ id) => [
    `1. reviewed ${A} ticket ${id}`,
    `12. \`reviewed ${A} ticket ${id}\``,
    `reviewed \`${A}\` ticket \`${id}\``,
    `  3. reviewed ${A} ticket ${id}  `,
    `- reviewed ${A} ticket ${id}`,
  ];
  for (const id of [other, other.toUpperCase(), tool.toUpperCase()]) {
    for (const line of shapes(id)) assert.deepEqual(reportMismatches(`${line}\n`, lines), [A], line);
  }
  for (const line of shapes(tool)) assert.deepEqual(reportMismatches(`${line}\n`, lines), [], line);
});

test('B96 r2 (8): in a close, a numbered, backticked hand line with another id and a hand line with the tool id in capitals both warn; the close goes on', async () => {
  const fx = await allApproved('b96-r2-shapes');
  const ta = fx.ticket(A);
  const tb = fx.ticket(B);
  assert.match(tb, /[a-f]/, 'the id has a letter, so its capitals are another string');
  const report = fx.report('shapes', `1. \`reviewed ${A} ticket ${'0'.repeat(24)}\`\nreviewed ${B} ticket ${tb.toUpperCase()}\n${SENTINEL}`);
  assert.deepEqual(await fx.close(['--report', report]), [0, `reviewed ${A} ticket ${ta}\nreviewed ${B} ticket ${tb}\nblock B1 closed\n`, `WARN report ticket mismatch ${A}\nWARN report ticket mismatch ${B}\n`]);
});

test('B96 r2 (9): an owned, changed file the review gate refuses (not approved, no waiver) keeps the block open — no reviewed line and no held WARN, with --report or without', async () => {
  const fx = await fixture('b96-r2-gate', { owned: [A, B, C] });
  for (const f of [A, B, C]) fx.edit(f, `export const v = '${f} 1';\n`);
  await fx.sign(A, { event: 'review.round', round: 1, kind: 'full', open_after: 1 });
  await fx.sign(A, { event: 'review.result', ticket: fx.ticket(A), status: 'reviewed', approved: false, open: [{ id: 'W1', severity: 'warning' }] });
  await fx.sign(B, { event: 'review.approved', round: 1 });
  await fx.sign(C, { event: 'review.approved', round: 1 });
  // were the lines built, c would warn `no ticket` and b's hand line `mismatch`
  const noC = (/** @type {any} */ t) => {
    if (t.file === C) throw new Error('no ticket');
    return resolveTicket(t);
  };
  const report = fx.report('gate', `reviewed ${B} ticket ${'0'.repeat(24)}\n${SENTINEL}`);
  const refused = [1, '', `block B1 open: unreviewed ${A}; not_approved ${A} W1\n`];
  assert.deepEqual(await fx.close(['--report', report], { resolveTicket: noC }), refused);
  assert.deepEqual(await fx.close([], { resolveTicket: noC }), refused);
});
