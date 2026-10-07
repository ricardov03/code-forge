import { commitAll, git, makeRepo, writeFile } from './helpers.mjs';
import assert from 'node:assert/strict';
import path from 'node:path';
import { test } from 'node:test';

// B52 item 18 (the real field run behind issue #2, sanitised): `block close B1 --run <id>` (no
// --transcript, no --report) printed the transcript WARN and `block B1 closed` although 4 of the 9
// reviewed files had findings open and NO review.approved row. Those files never reached the gate:
// its file set is the workspace diff since base filtered by owned_files, and here that set was
// empty — owned_files were two DIRECTORIES (which did not match the files inside them), and the
// coder's work can also be invisible to the diff (committed before `block open` recorded the
// base, or done in another worktree). Each shape is replayed here.

const { startRun, writeSigned } = await import('../../src/state/run.mjs');
const { openBlock } = await import('../../src/state/block.mjs');
const { appendRow, readAllRows } = await import('../../src/ledger/write.mjs');
const { runBlock } = await import('../../src/cli/block.mjs');
const { contentHash, ticketId } = await import('../../src/worker/ticket.mjs');
const { writeResult } = await import('../../src/worker/queue.mjs');
const { loadKey } = await import('../../src/state/signer.mjs');

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

/** The 5 files the review approved and the 4 it left open (5 critical and 3 warning findings). */
const PASSING = ['src/feature/Dial.swift', 'src/feature/Grid.swift', 'src/feature/Knob.swift', 'test/feature/DialTests.swift', 'test/feature/GridTests.swift'];
/** @type {Record<string, Array<[string, 'critical' | 'warning']>>} */
const FAILING = {
  'src/feature/Fan.swift': [['F1', 'critical'], ['F2', 'critical'], ['F3', 'warning']],
  'src/feature/Hub.swift': [['F1', 'critical']],
  'test/feature/FanTests.swift': [['F1', 'critical'], ['F2', 'warning']],
  'test/feature/HubTests.swift': [['F1', 'critical'], ['F2', 'warning']],
};

/**
 * The block's ledger, round 1 only, as the field run wrote it (an old worker: review.result rows
 * have no `approved`), and each ticket's signed result file.
 * @param {{repo: string, files: string, runId: string, writeRow: (row: Record<string, any>) => Promise<unknown>}} opts
 *   `files`: where the reviewed files are on disk (the workspace, or the coder's worktree).
 */
async function replayLedger({ repo, files, runId, writeRow }) {
  const key = await loadKey(runId);
  const row = (/** @type {Record<string, any>} */ r) => writeSigned(runId, writeRow, { block: 'B1', ...r });
  for (const file of [...PASSING, ...Object.keys(FAILING)].sort()) {
    const content_hash = contentHash(files, file);
    const ticket = ticketId({ run: runId, block: 'B1', file, content_hash });
    const open = FAILING[file] ?? [];
    for (const [finding, severity] of open) await row({ event: 'review.triage', file, content_hash, finding, severity, source: 'shadow', verdict: 'fix_now' });
    await row({ event: 'review.round', file, content_hash, round: 1, level: 'L2', kind: 'full', open_before: 0, closed: 0, new_in_hunks: open.length, late: 0, open_after: open.length });
    if (open.length === 0) await row({ event: 'review.approved', file, content_hash, round: 1 });
    await row({ event: 'review.result', ticket, file, content_hash, status: 'reviewed' });
    const findings = open.map(([id, severity]) => ({ id, file, line_start: 1, line_end: 1, severity, category: 'correctness', claim: 'c', evidence: 'e', fix: 'f' }));
    const result = open.length === 0 ? { approved: true, findings: [] } : { approved: false, reason: 'findings-open', next: { action: 'fix' }, findings };
    writeResult({ repoRoot: repo, runId, ticket, key, result: { event: 'review.result', block: 'B1', file, content_hash, status: 'reviewed', ...result } });
  }
}

/** @param {string} root */
function writeWork(root) {
  for (const file of [...PASSING, ...Object.keys(FAILING)]) writeFile(root, file, `// ${path.basename(file)}\n`);
}

const PROBE = async (/** @type {number} */ pid) => (pid === 4242 ? 'start-A' : null);
const WARN_TRANSCRIPT = (/** @type {string} */ runId) => `WARN block B1: no coder transcript found (--transcript, run record, .code-forge/runs/${runId}/B1.log); the transcript grep did not run\n`;
const REFUSALS =
  'unreviewed src/feature/Fan.swift; unreviewed src/feature/Hub.swift; unreviewed test/feature/FanTests.swift; unreviewed test/feature/HubTests.swift; ' +
  'not_approved src/feature/Fan.swift F1; not_approved src/feature/Fan.swift F2; not_approved src/feature/Fan.swift F3; not_approved src/feature/Hub.swift F1; ' +
  'not_approved test/feature/FanTests.swift F1; not_approved test/feature/FanTests.swift F2; not_approved test/feature/HubTests.swift F1; not_approved test/feature/HubTests.swift F2';

/** @param {string} runId */
async function close(runId) {
  const stream = capture();
  const code = await runBlock(['close', 'B1', '--run', runId], { stdout: stream, stderr: stream, probe: PROBE });
  return [code, stream.text];
}

test('B52 item 18c: owned_files as two directories — the reviewed files inside them reach the gate; the 4 failing files refuse with their finding ids', async () => {
  const repo = await makeRepo();
  writeFile(repo, 'README.md', '# app\n');
  await commitAll(repo);
  const writeRow = (/** @type {Record<string, any>} */ row) => appendRow(row, { slug: 'b52-field-dirs' });
  await startRun({ workspace: repo, project: 'b52-field-dirs', runId: 'r-field-dirs', workerPid: 4242, writeRow, probe: PROBE });
  await openBlock({ runId: 'r-field-dirs', id: 'B1', level: 'L2', owned: ['src/feature', 'test/feature'], acceptance: [{ clause: 'c', tests: ['t'] }], writeRow });
  writeWork(repo);
  await replayLedger({ repo, files: repo, runId: 'r-field-dirs', writeRow });
  assert.deepEqual(await close('r-field-dirs'), [1, `${WARN_TRANSCRIPT('r-field-dirs')}block B1 open: ${REFUSALS}\n`]);
  assert.equal((await readAllRows('b52-field-dirs')).filter((r) => r.event === 'block.close').length, 0);
});

test('B52 item 18a: the work was committed before `block open` recorded the base — the diff is empty, the reviewed files are still checked, and the close WARNs why', async () => {
  const repo = await makeRepo();
  writeFile(repo, 'README.md', '# app\n');
  writeWork(repo);
  await commitAll(repo);
  const writeRow = (/** @type {Record<string, any>} */ row) => appendRow(row, { slug: 'b52-field-commit' });
  await startRun({ workspace: repo, project: 'b52-field-commit', runId: 'r-field-commit', workerPid: 4242, writeRow, probe: PROBE });
  await openBlock({ runId: 'r-field-commit', id: 'B1', level: 'L2', owned: ['src/feature/**', 'test/feature/**'], acceptance: [{ clause: 'c', tests: ['t'] }], writeRow });
  await replayLedger({ repo, files: repo, runId: 'r-field-commit', writeRow });
  const warn = 'WARN block B1: no owned file differs from the block base in the workspace, but 9 owned file(s) have review rows — the work was likely committed before block open or done in another worktree; they are checked at their content in the workspace\n';
  assert.deepEqual(await close('r-field-commit'), [1, `${WARN_TRANSCRIPT('r-field-commit')}${warn}block B1 open: ${REFUSALS}\n`]);
});

test('B52 item 18b: the coder worked in another worktree — the reviewed files are missing from the workspace and every one refuses', async () => {
  const repo = await makeRepo();
  writeFile(repo, 'README.md', '# app\n');
  await commitAll(repo);
  const wt = `${repo}-wt`;
  await git(['worktree', 'add', '-q', wt], repo);
  const writeRow = (/** @type {Record<string, any>} */ row) => appendRow(row, { slug: 'b52-field-wt' });
  await startRun({ workspace: repo, project: 'b52-field-wt', runId: 'r-field-wt', workerPid: 4242, writeRow, probe: PROBE });
  await openBlock({ runId: 'r-field-wt', id: 'B1', level: 'L2', owned: ['src/feature/**', 'test/feature/**'], acceptance: [{ clause: 'c', tests: ['t'] }], writeRow });
  writeWork(wt);
  await replayLedger({ repo, files: wt, runId: 'r-field-wt', writeRow });
  const all = [...PASSING, ...Object.keys(FAILING)].sort();
  const warn = 'WARN block B1: no owned file differs from the block base in the workspace, but 9 owned file(s) have review rows — the work was likely committed before block open or done in another worktree; they are checked at their content in the workspace\n';
  assert.deepEqual(await close('r-field-wt'), [1, `${WARN_TRANSCRIPT('r-field-wt')}${warn}block B1 open: ${all.map((f) => `unreviewed ${f}`).join('; ')}\n`]);
});

test('B52 item 20: a block that changed no owned file and has no review rows refuses no_changes; --no-require-reviews (confirmed at a terminal) is the owner\'s way out', async () => {
  const repo = await makeRepo();
  writeFile(repo, 'README.md', '# app\n');
  await commitAll(repo);
  const writeRow = (/** @type {Record<string, any>} */ row) => appendRow(row, { slug: 'b52-field-empty' });
  await startRun({ workspace: repo, project: 'b52-field-empty', runId: 'r-field-empty', workerPid: 4242, writeRow, probe: PROBE });
  await openBlock({ runId: 'r-field-empty', id: 'B1', level: 'L2', owned: ['src/feature/**'], acceptance: [{ clause: 'c', tests: ['t'] }], writeRow });
  assert.deepEqual(await close('r-field-empty'), [1, `${WARN_TRANSCRIPT('r-field-empty')}block B1 open: no_changes\n`]);
  const stream = capture();
  const yes = { confirm: async () => true, isCancel: () => false };
  const code = await runBlock(['close', 'B1', '--run', 'r-field-empty', '--no-require-reviews'], { stdout: stream, stderr: stream, probe: PROBE, isTTY: true, ui: yes });
  assert.deepEqual([code, stream.text], [0, `${WARN_TRANSCRIPT('r-field-empty')}block B1 closed\n`]);
});
