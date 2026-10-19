import { answerFor, cfgFor, freshDir, git, writeFile } from '../review/helpers.mjs';
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { readFileSync, realpathSync } from 'node:fs';
import path from 'node:path';
import { test } from 'node:test';
import { startGrant } from '../../src/autopilot/grant.mjs';
import { runBlock } from '../../src/cli/block.mjs';
import { runReviewFile } from '../../src/cli/review-file.mjs';
import { appendRow, readAllRows } from '../../src/ledger/write.mjs';
import { checkBlockReviews } from '../../src/review/gate-check.mjs';
import { newFileState } from '../../src/review/fixloop.mjs';
import { readRun, saveRun, startRun, writeSigned } from '../../src/state/run.mjs';
import { canonicalJSON, loadKey, signRow, verifyRow } from '../../src/state/signer.mjs';
import { CODER_ONLY_FORBIDDEN, isForbidden, mergeForbidden, scanTranscript } from '../../src/util/forbidden.mjs';
import { blockEntryFor, acceptanceExcerpt, reviewTicket } from '../../src/worker/engine.mjs';
import { saveState } from '../../src/worker/review-state.mjs';
import { announceWorker, enqueue, readTicket, retractWorker, writeResult } from '../../src/worker/queue.mjs';
import { watchTick } from '../../src/worker/watch.mjs';
import { contentHash } from '../../src/worker/ticket.mjs';

const OLD = [{ clause: 'old clause', tests: ['old-test'] }];
const NEW = [{ clause: 'correct clause', tests: ['correct-test'] }, { clause: 'check device', kind: 'human_check' }];
const NOW = new Date('2026-10-19T12:00:00Z');
const hash = (value) => createHash('sha256').update(canonicalJSON(value)).digest('hex');

async function cli(verb, args, deps = {}) {
  let out = ''; let err = '';
  const code = await verb(args, { stdout: { write: (s) => { out += s; } }, stderr: { write: (s) => { err += s; } }, ...deps });
  return { code, out, err };
}

async function fixture(slug) {
  const repo = realpathSync(freshDir(slug));
  await git(['init', '-q', '-b', 'main'], repo);
  writeFile(repo, 'a.mjs', 'export const a = 1;\n');
  await git(['add', '-A'], repo);
  await git(['commit', '-q', '-m', 'base'], repo);
  const files = freshDir('amend-input');
  writeFile(files, 'old.json', JSON.stringify(OLD));
  writeFile(files, 'new.yml', '- clause: correct clause\n  tests: [correct-test]\n- clause: check device\n  kind: human_check\n');
  const runId = `r-${slug}`;
  const config = { version: 1, project: { slug }, ...cfgFor(), review: { ...cfgFor().review, file_loop: 'watch' } };
  const writeRow = (row) => appendRow(row, { slug });
  await startRun({ workspace: repo, project: slug, runId, workerPid: process.pid, probe: async () => 'start-A', config, writeRow });
  const opened = await cli(runBlock, ['open', 'B1', '--run', runId, '--level', 'L1', '--owned', 'a.mjs', '--acceptance', path.join(files, 'old.json')]);
  assert.equal(opened.code, 0, opened.err);
  writeFile(repo, 'a.mjs', 'export const a = 2;\n');
  let questions = [];
  const args = ['amend-acceptance', 'B1', '--run', runId, '--acceptance', path.join(files, 'new.yml'), '--reason', 'correct the mistaken requirement'];
  const amend = (extra = [], deps = {}) => cli(runBlock, [...args.filter((arg, i) => !(['--reason', '--acceptance'].includes(arg) && extra.includes(arg)) && !(i > 0 && ['--reason', '--acceptance'].includes(args[i - 1]) && extra.includes(args[i - 1]))), ...extra], {
    isTTY: true, now: () => NOW,
    ui: { confirm: async (q) => { questions.push(q); return true; }, isCancel: () => false }, ...deps,
  });
  const rows = async () => (await readAllRows(slug)).filter((r) => r.run === runId);
  return { repo, runId, files, config, writeRow, args, amend, rows, questions,
    entry: async () => (await readRun(runId)).blocks.B1,
    sign: (row) => writeSigned(runId, writeRow, { block: 'B1', ...row }),
  };
}

test('B143 clause 1: required arguments, both terminals, no yes, active grant, open status and cancellation', async () => {
  const f = await fixture('amend-forms');
  const before = await readRun(f.runId); const rows = await f.rows();
  for (const args of [['B2'], ['--yes'], ['--reason', ''], ['--reason', '  '], ['--reason', 'line\nbreak'], ['--reason', '\u007f'], ['--reason', 'control\u0085']]) {
    const r = await f.amend(args);
    assert.equal(r.code, 2, r.err); assert.equal(r.out, '');
  }
  for (const flag of ['--acceptance', '--reason']) {
    const at = f.args.indexOf(flag);
    const args = f.args.filter((_, i) => i !== at && i !== at + 1);
    const r = await cli(runBlock, args, { isTTY: true, ui: { confirm: async () => { assert.fail('missing flag must refuse before asking'); }, isCancel: () => false } });
    assert.equal(r.code, 2, r.err); assert.equal(r.out, '');
  }
  for (const deps of [{ stdinIsTTY: false }, { stdoutIsTTY: false }]) {
    const r = await f.amend([], deps);
    assert.equal(r.code, 2); assert.match(r.err, /needs the human at a terminal/);
  }
  for (const answer of [false, Symbol('cancel')]) {
    const r = await f.amend([], { ui: { confirm: async () => answer, isCancel: (v) => typeof v === 'symbol' } });
    assert.equal(r.code, 1); assert.match(r.err, /cancelled/);
  }
  assert.deepEqual(await readRun(f.runId), before);
  assert.deepEqual(await f.rows(), rows);
  for (const status of ['closed', 'stopped', 'awaiting_human_check']) {
    await saveRun({ ...before, blocks: { B1: { ...before.blocks.B1, status } } });
    const r = await f.amend(); assert.equal(r.code, 1); assert.match(r.err, /not-open/);
  }
  await saveRun(before);
  await startGrant({ runId: f.runId, input: { until: '2026-10-19T13:00:00Z', delegate: 'L2', allow: 'waive:warning,waive:nit' }, now: NOW, writeRow: f.writeRow });
  const granted = await f.rows();
  const active = await f.amend(); assert.equal(active.code, 1); assert.match(active.err, /active/);
  assert.equal(f.questions.length, 0, 'all refusals precede the question');
  assert.deepEqual(await f.rows(), granted);
});

test('B143 clause 1 boundary: attempts remain open and prompt names block, run and both counts', async () => {
  const f = await fixture('amend-attempt');
  assert.equal((await cli(runBlock, ['attempt', 'B1', '--run', f.runId])).code, 0);
  const r = await f.amend(); assert.equal(r.code, 0, r.err);
  assert.equal(f.questions.length, 1);
  assert.equal(f.questions[0].initialValue, false);
  for (const text of ['B1', f.runId]) assert.equal(f.questions[0].message.includes(text), true, text);
  assert.match(f.questions[0].message, /from 1 clauses to 2 clauses/);
  assert.equal((await f.entry()).attempt, 2);
});

test('B143 clause 2: YAML amendments update acceptance and set, replace and remove human checks with one signed row each', async () => {
  const f = await fixture('amend-save');
  for (const [old, next, checks] of [[OLD, NEW, ['check device']], [NEW, [{ clause: 'other device', kind: 'human_check' }], ['other device']], [[{ clause: 'other device', kind: 'human_check' }], OLD, undefined]]) {
    writeFile(f.files, 'next.json', JSON.stringify(next));
    const before = await f.rows();
    const r = await f.amend(['--acceptance', path.join(f.files, 'next.json'), '--json']);
    assert.equal(r.code, 0, r.err);
    const entry = await f.entry(); assert.deepEqual(entry.acceptance, next); assert.deepEqual(entry.human_checks, checks);
    const added = (await f.rows()).slice(before.length); assert.equal(added.length, 1);
    const row = added[0];
    assert.deepEqual([row.event, row.block, row.old_sha256, row.new_sha256, row.clauses, row.reason, row.by], ['block.acceptance_amended', 'B1', hash(old), hash(next), next.length, 'correct the mistaken requirement', 'human']);
    assert.equal(verifyRow(row, await loadKey(f.runId)).ok, true);
    assert.equal(JSON.parse(r.out).new_sha256, row.new_sha256);
  }
});

test('B143 clause 2 boundary: malformed, invalid and canonically identical lists refuse before asking and write zero rows', async () => {
  const f = await fixture('amend-invalid');
  const before = await readRun(f.runId); const rows = await f.rows();
  for (const input of ['[broken', '[]', '[{"clause":"x","tests":[]}]', '[{"clause":"x","kind":"human_check","tests":["t"]}]', '[{"tests":["old-test"],"clause":"old clause"}]']) {
    writeFile(f.files, 'bad.yml', input);
    const r = await f.amend(['--acceptance', path.join(f.files, 'bad.yml')]);
    assert.equal(r.code, 1, r.err);
    if (input.includes('old clause')) assert.match(r.err, /acceptance-unchanged/);
    assert.equal(r.out, '');
    assert.deepEqual(await readRun(f.runId), before); assert.deepEqual(await f.rows(), rows);
  }
  assert.equal(f.questions.length, 0);
});

test('B143 clause 3: stale approvals, waiver-free answers and rounds are unreviewed after the latest signed amendment', async () => {
  const key = Buffer.alloc(32, 7);
  const row = (r) => signRow({ run: 'r-test', block: 'B1', file: 'a.mjs', content_hash: 'h', ...r }, key);
  const amend = row({ event: 'block.acceptance_amended' });
  const gate = (rows) => checkBlockReviews({ block: 'B1', runId: 'r-test', files: [{ file: 'a.mjs', content_hash: 'h' }], rows, key, proofFiles: [], highPaths: [] });
  const approval = row({ event: 'review.approved' });
  assert.equal(gate([approval]).ok, true);
  for (const unrelated of [row({ event: 'block.acceptance_amended', block: 'B2' }), row({ event: 'block.acceptance_amended', run: 'r-other' }), { ...amend, mac: 'invalid' }]) assert.equal(gate([approval, unrelated]).ok, true);
  for (const old of [[approval], [row({ event: 'review.round', open_after: 0 })], [row({ event: 'review.round', open_after: 1 }), row({ event: 'review.result', status: 'reviewed', approved: false, open: [{ id: 'F1', severity: 'warning' }] }), row({ event: 'review.answered', finding: 'F1', by_file: 'a.mjs', by_hash: 'h' })]]) {
    const r = gate([...old, amend]);
    assert.equal(r.ok, false, 'old evidence cannot pass after amendment');
    assert.deepEqual(r.refusals.map((x) => [x.code, x.file]), [['unreviewed', 'a.mjs']]);
    assert.match(r.refusals[0].detail, /\(acceptance amended\)/);
    assert.equal(gate([...old, amend, approval]).ok, true, 'fresh signed approval counts');
    assert.equal(gate([...old, amend, approval, amend]).ok, false, 'latest amendment wins');
  }
});

test('B143 clause 3 boundary: direct requests, block waits and unchanged watch commits queue fresh tickets with new packet clauses', async () => {
  for (const mode of ['direct', 'wait', 'watch']) {
    const f = await fixture(`amend-queue-${mode}`);
    await git(['add', 'a.mjs'], f.repo); await git(['commit', '-q', '-m', 'change'], f.repo);
    await watchTick({ repoRoot: f.repo, runId: f.runId, cfg: f.config, writeRow: f.writeRow });
    const old = enqueue({ repoRoot: f.repo, run: f.runId, block: 'B1', file: 'a.mjs' });
    writeResult({ repoRoot: f.repo, runId: f.runId, ticket: old.ticket, key: await loadKey(f.runId), result: { status: 'stopped', stopped: 'review_cap', approved: false } });
    const workerRow = (row) => f.sign({ file: 'a.mjs', content_hash: old.content_hash, ...row });
    if (mode === 'direct') {
      const state = newFileState({ file: 'a.mjs', level: 'L1' });
      state.status = 'stopped'; state.round = 1; state.next = { action: 'stop', reason: 'review_cap' };
      await saveState({ runRootDir: f.files, runId: f.runId, block: 'B1', file: 'a.mjs', key: await loadKey(f.runId), rows: await f.rows(), state, seq: 0, writeRow: workerRow, contentRoot: f.repo });
    }
    assert.equal((await f.amend()).code, 0);
    announceWorker(f.repo, { pid: process.pid, run: f.runId });
    try {
      let ticket;
      if (mode === 'watch') {
        await watchTick({ repoRoot: f.repo, runId: f.runId, cfg: f.config, writeRow: f.writeRow });
        const { blockTickets } = await import('../../src/worker/queue.mjs');
        const pending = blockTickets({ repoRoot: f.repo, run: f.runId, block: 'B1' });
        assert.equal(pending.length, 1); ticket = pending[0].ticket;
      } else {
        const args = mode === 'direct' ? ['a.mjs', '--block', 'B1', '--run', f.runId] : ['--wait-block', 'B1', '--run', f.runId, '--max', '0ms'];
        const r = await cli(runReviewFile, args, { cwd: f.repo, pollMs: 1 }); assert.equal(r.code, 0, r.err);
        const result = JSON.parse(r.out); ticket = mode === 'direct' ? result.ticket : result.files[0].ticket;
        assert.equal(mode === 'direct' ? result.status : result.files[0].status, mode === 'direct' ? 'queued' : 'pending');
      }
      assert.notEqual(ticket, old.ticket, `${mode} must create a new ticket at identical content`);
      assert.equal(readTicket(f.repo, ticket).content_hash, contentHash(f.repo, 'a.mjs'));
      const entry = await blockEntryFor(f.runId, 'B1');
      assert.deepEqual(entry.acceptance, NEW);
      assert.equal(acceptanceExcerpt(entry.acceptance, f.config).includes('- correct clause\n- check device'), true);
      if (mode === 'direct') {
        const packets = [];
        const result = await reviewTicket(readTicket(f.repo, ticket), {
          repoRoot: f.repo, runId: f.runId, runRootDir: f.files, cfg: f.config, jevKey: null,
          key: await loadKey(f.runId), readRows: f.rows, writeRow: workerRow,
          spawn: async ({ promptPath }) => {
            const text = readFileSync(promptPath, 'utf8'); packets.push(text);
            const hunks = text.slice(text.indexOf('\nhunks:\n') + 8).split('\n').filter((line) => line.startsWith('- @@')).map((line) => line.slice(2));
            return { status: 'ok', exit_code: 0, answer: answerFor(hunks), usage: { tokens_in: 900, tokens_out: 300 } };
          },
        });
        assert.deepEqual([result.status, result.approved, result.round, packets.length], ['reviewed', true, 1, 1]);
        assert.equal(packets[0].includes('Acceptance clauses (what the change must do):\n- correct clause\n- check device'), true);
        assert.equal(packets[0].includes('- old clause'), false);
      }
    } finally { retractWorker(f.repo, process.pid); }
  }
});

test('B143 clause 4: both executable prefixes are coder-forbidden and transcript hits name the new rule', () => {
  const id = 'code-forge-block-amend-acceptance-from-coder';
  assert.equal(CODER_ONLY_FORBIDDEN.filter((e) => e.id === id).length, 1);
  for (const bin of ['code-forge', 'forge']) {
    const argv = [bin, 'block', 'amend-acceptance', 'B1', '--run', 'r-test'];
    assert.equal(isForbidden(argv, mergeForbidden())?.id, id);
    const hits = scanTranscript(`$ ${argv.join(' ')}\n`, mergeForbidden());
    assert.deepEqual(hits.map((h) => h.id), [id]);
  }
});

test('B143 clause 4 boundary: the coder-only entry does not prohibit another block verb or a prefix lookalike', () => {
  const entries = CODER_ONLY_FORBIDDEN.filter((e) => e.id === 'code-forge-block-amend-acceptance-from-coder');
  assert.equal(entries.length, 1);
  for (const verb of ['open', 'amend-acceptance-extra']) assert.equal(isForbidden(['forge', 'block', verb], entries), null);
});
