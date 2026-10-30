import { cfgFor, freshDir, git, lines, writeFile } from '../review/helpers.mjs';
import assert from 'node:assert/strict';
import path from 'node:path';
import { test } from 'node:test';
import { runBlock } from '../../src/cli/block.mjs';
import { runReviewFile } from '../../src/cli/review-file.mjs';
import { appendRow, readAllRows } from '../../src/ledger/write.mjs';
import { readRun, startRun, writeSigned } from '../../src/state/run.mjs';
import { loadKey, verifyRow } from '../../src/state/signer.mjs';
import { announceWorker, blockTickets } from '../../src/worker/queue.mjs';
import { contentHash } from '../../src/worker/ticket.mjs';
import { HUMAN_SAYS_YES } from '../helpers/human-confirm.mjs';

const FILES = ['a.mjs', 'b.mjs'];
const source = (first = 'base', last = 'base') => lines(30, (i) => `// ${i}: ${i === 1 ? first : i === 30 ? last : 'base'}`);
const PROBE = async (pid) => pid === 4242 ? 'start-A' : null;

async function cli(verb, args, deps = {}) {
  let out = ''; let err = '';
  const code = await verb(args, { stdout: { write: (s) => { out += s; } }, stderr: { write: (s) => { err += s; } }, probe: PROBE, ...deps });
  return { code, out, err };
}

async function fixture(slug) {
  const repo = freshDir(slug);
  await git(['init', '-q', '-b', 'main'], repo);
  const config = { version: 1, project: { slug }, ...cfgFor(), proof: { isolation: 'worktree', worktree: { dir: freshDir('trees') } } };
  writeFile(repo, '.code-forge.yml', JSON.stringify(config));
  writeFile(repo, '.gitignore', '.code-forge/\n');
  for (const file of FILES) writeFile(repo, file, source());
  await git(['add', '-A'], repo);
  await git(['commit', '-q', '-m', 'base'], repo);
  const base = (await git(['rev-parse', 'HEAD'], repo)).trim();
  const inputs = freshDir('inputs');
  writeFile(inputs, 'acceptance.json', JSON.stringify([{ clause: 'rebase exits', tests: ['rebase-exits'] }]));
  writeFile(inputs, 'coder.log', '$ node --test\n');
  const runId = `r-${slug}`;
  const writeRow = (row) => appendRow(row, { slug });
  const sign = (row) => writeSigned(runId, writeRow, { block: 'B1', ...row });
  await startRun({ workspace: repo, project: slug, runId, workerPid: 4242, probe: PROBE, config, writeRow });
  const opened = await cli(runBlock, ['open', 'B1', '--run', runId, '--level', 'L1', '--owned', '*.mjs', '--acceptance', path.join(inputs, 'acceptance.json')]);
  assert.equal(opened.code, 0, opened.err);
  const tree = (await readRun(runId)).blocks.B1.worktree.path;
  for (const file of FILES) writeFile(tree, file, source('block'));
  await git(['add', '-A'], tree);
  await git(['commit', '-q', '-m', 'reviewed'], tree);
  for (const file of FILES) await sign({ event: 'review.approved', file, content_hash: contentHash(tree, file), round: 1 });
  const closed = await cli(runBlock, ['close', 'B1', '--run', runId, '--worker-pid', '4242', '--transcript', path.join(inputs, 'coder.log')]);
  assert.equal(closed.code, 0, closed.err);
  const oldHead = (await git(['rev-parse', 'HEAD'], tree)).trim();
  for (const file of FILES) writeFile(repo, file, source('base', 'upstream'));
  await git(['add', '-A'], repo);
  await git(['commit', '-q', '-m', 'upstream'], repo);
  const newBase = (await git(['rev-parse', 'HEAD'], repo)).trim();
  announceWorker(repo, { pid: process.pid, run: runId });
  return {
    repo, tree, runId, base, newBase, oldHead, sign, writeRow,
    rows: () => readAllRows(slug),
    entry: async () => (await readRun(runId)).blocks.B1,
    rebase: (extra = [], deps = {}) => cli(runBlock, ['rebase', 'B1', '--run', runId, ...extra], deps),
    check: () => cli(runReviewFile, ['--rebase-check', '--block', 'B1', '--run', runId], { cwd: repo }),
    tickets: () => blockTickets({ repoRoot: repo, run: runId, block: 'B1', tree }),
  };
}

test('B170 clause 1: changed reviewed refusal names both exact exits and restores the old head', async () => {
  const f = await fixture('rebase-exits-help');
  const before = await f.entry();
  const refused = await f.rebase();
  assert.equal(refused.code, 1);
  assert.equal(refused.out, '');
  assert.equal(refused.err, `block rebase: rebase-changed-reviewed a.mjs, b.mjs\n` +
    `Re-review: code-forge review-file --rebase-check --block B1 --run ${f.runId}\n` +
    `Human accept (repeat --accept-changed for each file): code-forge block rebase B1 --run ${f.runId} --accept-changed <file> --reason <text>\n`);
  assert.equal((await git(['rev-parse', 'HEAD'], f.tree)).trim(), f.oldHead);
  assert.deepEqual(await f.entry(), before);
  assert.equal((await f.rows()).filter((r) => r.event === 'block.rebase').length, 0);
});

test('B170 clause 2: re-review or human acceptance clears every changed file and signs the complete audit', async () => {
  for (const method of ['review', 'human', 'mixed']) {
    const f = await fixture(`rebase-exits-${method}`);
    const reviewed = method === 'human' ? [] : method === 'mixed' ? ['a.mjs'] : FILES;
    if (reviewed.length) {
      const check = await f.check();
      assert.equal(check.code, 0, check.err || check.out);
      const result = JSON.parse(check.out);
      assert.deepEqual(result.files.map((v) => v.file), FILES, 'exactly both changed files are queued');
      const tickets = f.tickets();
      assert.equal(tickets.length, 2);
      assert.deepEqual(tickets.map((t) => t.file).sort(), FILES);
      assert.equal((await f.entry()).base_sha, f.base, 'preview does not complete the rebase');
      assert.equal((await f.rows()).filter((r) => r.event === 'block.rebase').length, 0);
      assert.equal((await f.check()).code, 0, 'repeated checks are idempotent');
      assert.equal(f.tickets().length, 2);
      for (const file of reviewed) {
        const ticket = tickets.find((t) => t.file === file);
        assert.equal(ticket.content_hash, contentHash(f.tree, file), 'worker can read the new content');
        await f.sign({ event: 'review.approved', file, content_hash: ticket.content_hash, ticket: ticket.ticket, round: 1 });
      }
    }
    const accepted = FILES.filter((file) => !reviewed.includes(file));
    const args = accepted.flatMap((file) => ['--accept-changed', file]);
    if (accepted.length) args.push('--reason', 'owner checked upstream changes');
    const done = await f.rebase(args, HUMAN_SAYS_YES);
    assert.equal(done.code, 0, done.err);
    const entry = await f.entry();
    assert.deepEqual([entry.status, entry.base_sha, entry.worktree.base_sha], ['closed', f.newBase, f.newBase]);
    const rows = (await f.rows()).filter((r) => r.event === 'block.rebase');
    assert.equal(rows.length, 1, 'exactly one successful rebase row');
    const row = rows[0];
    assert.deepEqual([row.old_base, row.new_base, row.changed_reviewed], [f.base, f.newBase, FILES]);
    assert.deepEqual(row.cleared, FILES.map((file) => reviewed.includes(file)
      ? { file, via: 'review', ticket: f.tickets().find((t) => t.file === file).ticket }
      : { file, via: 'human', reason: 'owner checked upstream changes' }));
    assert.deepEqual(verifyRow(row, await loadKey(f.runId)), { ok: true });
    const unchanged = await f.rebase();
    assert.equal(unchanged.code, 0, unchanged.err);
    const second = (await f.rows()).filter((r) => r.event === 'block.rebase');
    assert.equal(second.length, 2);
    assert.deepEqual([second[1].old_base, second[1].new_base, second[1].changed_reviewed, second[1].cleared], [f.newBase, f.newBase, [], []]);
    assert.deepEqual(verifyRow(second[1], await loadKey(f.runId)), { ok: true });
  }
});

test('B170 boundary: agents, missing reasons, partial clearance and stale or unsigned approvals cannot accept changed content', async () => {
  const f = await fixture('rebase-exits-boundary');
  const args = ['--accept-changed', 'a.mjs', '--accept-changed', 'b.mjs', '--reason', 'checked'];
  const agent = await f.rebase(args, { isTTY: false });
  assert.equal(agent.code, 2);
  assert.match(agent.err, /needs the human at a terminal/);
  for (const reason of ['', '   ']) {
    const missing = await f.rebase(['--accept-changed', 'a.mjs', '--reason', reason], HUMAN_SAYS_YES);
    assert.equal(missing.code, 2);
    assert.match(missing.err, /--reason/);
  }
  const no = await f.rebase(args, { ...HUMAN_SAYS_YES, ui: { confirm: async () => false, isCancel: () => false } });
  assert.equal(no.code, 1);
  assert.match(no.err, /cancelled/);
  assert.equal((await git(['rev-parse', 'HEAD'], f.tree)).trim(), f.oldHead);
  assert.equal((await f.check()).code, 0);
  const tickets = f.tickets();
  await f.sign({ event: 'review.approved', file: 'a.mjs', ticket: tickets.find((t) => t.file === 'a.mjs').ticket, content_hash: contentHash(f.tree, 'a.mjs') });
  const b = tickets.find((t) => t.file === 'b.mjs');
  await f.writeRow({ event: 'review.approved', run: f.runId, block: 'B1', file: 'b.mjs', ticket: b.ticket, content_hash: b.content_hash });
  await f.sign({ event: 'review.approved', file: 'b.mjs', ticket: b.ticket, content_hash: 'stale' });
  const partial = await f.rebase();
  assert.equal(partial.code, 1);
  assert.match(partial.err, /rebase-changed-reviewed b.mjs/);
  assert.equal((await f.entry()).base_sha, f.base);
  assert.equal((await f.rows()).filter((r) => r.event === 'block.rebase').length, 0);
});
