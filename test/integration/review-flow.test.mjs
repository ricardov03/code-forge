// The live review path end to end (B12c): `run start` → `block open` → edit → `review-file --wait`
// → `block close`, every verb a real CLI subprocess, the worker the one `run start` launched, the
// reviewer a scripted fake `claude` on PATH (no real CLI, no key, no network; Jev absent ⇒ the
// §3.5 fallback). The worker helpers come FIRST: they pin $HOME and TMPDIR under one per-file
// temp parent (removed in `after()`) and stop every worker this file starts.
import { cli, freshDir, startWorker, stopPid, stopPidAfter, stopWorker, useFake } from '../worker/helpers.mjs';
import assert from 'node:assert/strict';
import { mkdirSync, readdirSync, readFileSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, test } from 'node:test';

const { exec } = await import('../../src/util/exec.mjs');
const { gitChildEnv } = await import('../../src/worker/ticket.mjs');
const { liveWorker } = await import('../../src/worker/queue.mjs');
const { readRun } = await import('../../src/state/run.mjs');
const { loadKey, verifyRow } = await import('../../src/state/signer.mjs');
const { readAllRows } = await import('../../src/ledger/write.mjs');
const { ledgerPath } = await import('../../src/ledger/paths.mjs');

useFake(fileURLToPath(new URL('../worker/fake-scripted-reviewer.mjs', import.meta.url)));

const FILE = 'src/a.mjs';
const LINES = 120;
const CONFIG = `version: 1
project:
  slug: flow-test
provider: anthropic
levels:
  L0:
    model: claude-haiku-4-5-20251001
  L1:
    model: claude-sonnet-5
  L2:
    model: claude-opus-5-5
  L3:
    model: claude-fable-5-1
review:
  session_timeout_s: 60
  min_tokens_out: 40
`;

/** @param {string} id @param {number} line */
const finding = (id, line) => ({ id, file: FILE, line_start: line, line_end: line, severity: 'warning', category: 'correctness', claim: `claim ${id}`, evidence: 'e', fix: 'f' });
/** @param {number} i */
const original = (i) => `export const v${i} = ${i};`;

let seq = 0;

/**
 * A repo whose base commit holds a 120-line `src/a.mjs`, a started run (worker launched by
 * `run start`), and block B1 open on that file at L2.
 */
async function flow() {
  const repo = freshDir('flow');
  const git = async (/** @type {string[]} */ args) => {
    const res = await exec(['git', '-c', 'user.name=Fake Tester', '-c', 'user.email=fake@example.invalid', '-c', 'commit.gpgsign=false', ...args], { cwd: repo, env: gitChildEnv(), timeoutMs: 20000 });
    assert.equal(res.result, 'ok', res.stderr);
  };
  await git(['init', '-q']);
  writeFileSync(path.join(repo, '.code-forge.yml'), CONFIG);
  mkdirSync(path.join(repo, 'src'));
  writeFileSync(path.join(repo, FILE), Array.from({ length: LINES }, (_, k) => `${original(k + 1)}\n`).join(''));
  await git(['add', '-A']);
  await git(['commit', '-q', '-m', 'base']);

  seq += 1;
  const runId = `r-b12c-${process.pid}-${seq}`;
  const records = freshDir('records');
  const scriptFile = path.join(freshDir('script'), 'script.json');
  const script = (/** @type {Record<string, any>} */ s) => writeFileSync(scriptFile, JSON.stringify(s));
  script({});
  const env = { FAKE_RECORD: records, FAKE_SCRIPT: scriptFile };

  const started = await cli(['run', 'start', '--run', runId], repo, env);
  const live = liveWorker(repo);
  if (live) stopPidAfter(live.pid, runId);
  assert.equal(started.code, 0, started.stderr);
  const acceptance = path.join(freshDir('acc'), 'acceptance.yml');
  writeFileSync(acceptance, '- clause: the file is reviewed\n  tests: [review-flow]\n');
  const opened = await cli(['block', 'open', 'B1', '--run', runId, '--level', 'L2', '--owned', FILE, '--acceptance', acceptance], repo, env);
  assert.equal(opened.code, 0, opened.stderr);

  /** @param {number} line @param {string} tag */
  const edit = (line, tag) => {
    const full = path.join(repo, FILE);
    const all = readFileSync(full, 'utf8').split('\n');
    all[line - 1] = `export const v${line} = ${line} + ${JSON.stringify(tag)};`;
    writeFileSync(full, all.join('\n'));
  };
  /** `review-file <FILE>` then `review-file --wait`: the coder's two calls (`queued` = the first one's answer). */
  const review = async () => {
    const queued = await cli(['review-file', FILE, '--block', 'B1'], repo, env);
    assert.equal(queued.json?.status, 'queued', queued.stdout);
    const waited = await cli(['review-file', '--wait', queued.json.ticket, '--max', '30s'], repo, env);
    assert.equal(waited.json?.status, 'done', waited.stdout);
    return { ...waited.json, queued: queued.json };
  };
  const close = () => cli(['block', 'close', 'B1', '--run', runId], repo, env);
  /** @returns {string[]} the packets the fake received, oldest first. */
  const packets = () =>
    readdirSync(records)
      .filter((n) => n.startsWith('packet-'))
      .sort((a, b) => (BigInt(a.slice(7, -3)) < BigInt(b.slice(7, -3)) ? -1 : 1))
      .map((n) => readFileSync(path.join(records, n), 'utf8'));
  const { project } = await readRun(runId);
  const rows = async () => (await readAllRows(project)).filter((r) => r.run === runId);
  const stop = () => (live ? stopPid(live.pid, runId) : Promise.resolve());
  return { repo, runId, env, project, script, edit, review, close, packets, rows, stop };
}

describe('review-file → fix loop → signed approval → block close (B12c)', () => {
  test('1: a clean review approves with exactly 1 signed review.approved row for the current hash; block close exits 0', async () => {
    const f = await flow();
    try {
      f.edit(60, 'change');
      const out = await f.review();
      assert.deepEqual([out.result.status, out.result.approved, out.result.round, out.result.kind, Object.hasOwn(out, 'fix_list')], ['reviewed', true, 1, 'full', false]);
      const approvals = (await f.rows()).filter((r) => r.event === 'review.approved');
      assert.equal(approvals.length, 1);
      assert.deepEqual([approvals[0].block, approvals[0].file, approvals[0].content_hash], ['B1', FILE, out.result.content_hash]);
      assert.equal(verifyRow(approvals[0], await loadKey(f.runId)).ok, true);
      const closed = await f.close();
      assert.equal(closed.code, 0, closed.stderr);
      assert.equal(closed.stdout, 'block B1 closed\n');
    } finally {
      await f.stop();
    }
  });

  test('2: 1 major ⇒ a 1-item fix list and close refuses unreviewed; the fix is rechecked on the fix-hunk window only and approved; close exits 0', async () => {
    const f = await flow();
    try {
      f.script({ full: { findings: [finding('F1', 60)] }, recheck: { resolve: true, findings: [] } });
      f.edit(60, 'change');
      const first = await f.review();
      assert.deepEqual([first.result.status, first.result.approved, first.result.findings.length, first.result.next.action], ['reviewed', false, 1, 'fix']);
      assert.deepEqual(first.fix_list, [{ id: 'F1', severity: 'warning', lines: '60-60', claim: 'claim F1', fix: 'f' }]);
      const refused = await f.close();
      assert.deepEqual([refused.code, refused.stderr.split('\n').at(-2)], [1, `block B1 open: unreviewed ${FILE}`]);

      f.edit(60, 'fixed');
      const second = await f.review();
      assert.deepEqual([second.result.status, second.result.approved, second.result.round, second.result.kind], ['reviewed', true, 2, 'recheck']);
      const sent = f.packets();
      assert.equal(sent.length, 2);
      // round 1 carried the whole 120-line file; the recheck only the ±40-line window of the fix hunk
      assert.deepEqual([sent[0].includes(original(5)), sent[0].includes(original(115)), sent[0].includes('## open findings')], [true, true, false]);
      assert.deepEqual([sent[1].includes(original(5)), sent[1].includes(original(115)), sent[1].includes(original(62))], [false, false, true]);
      assert.equal(sent[1].includes('## open findings\n- F1 (warning, lines 60-60): claim F1'), true);
      const rounds = (await f.rows()).filter((r) => r.event === 'review.round');
      assert.deepEqual(rounds.map((r) => [r.round, r.kind, r.open_before, r.closed, r.open_after, r.context_mode ?? null]), [[1, 'full', 0, 0, 1, null], [2, 'recheck', 1, 1, 0, 'recheck']]);
      assert.equal((await f.rows()).filter((r) => r.event === 'review.approved').length, 1);
      const closed = await f.close();
      assert.equal(closed.code, 0, closed.stderr);
    } finally {
      await f.stop();
    }
  });

  test('3: 3 open findings that stay 3 after the fix ⇒ review_stall in the result (at L2 the +1 is the L3 patch rung, R1); no approval', async () => {
    const f = await flow();
    try {
      f.script({ full: { findings: [finding('F1', 20), finding('F2', 60), finding('F3', 100)] }, recheck: { resolve: false, findings: [] } });
      f.edit(60, 'change');
      assert.equal((await f.review()).result.findings.length, 3);
      f.edit(60, 'attempt');
      const out = await f.review();
      assert.deepEqual([out.result.status, out.result.approved, out.result.round, out.result.trigger, out.result.next, out.fix_list.length], [
        'reviewed',
        false,
        2,
        'review_stall',
        { action: 'patch', level: 'L3', trigger: 'review_stall' },
        3,
      ]);
      assert.equal((await f.rows()).filter((r) => r.event === 'review.approved').length, 0);
    } finally {
      await f.stop();
    }
  });

  test('4: a tampered or an unsigned approval row never satisfies block close (ledger.tamper stop, exit 1)', async () => {
    /** @type {Array<[string, (row: Record<string, any>) => Record<string, any>]>} */
    const cases = [
      ['mismatch', (row) => ({ ...row, round: 9 })],
      ['unsigned', ({ mac, ...row }) => row],
    ];
    for (const [reason, forge] of cases) {
      const f = await flow();
      try {
        f.edit(60, 'change');
        assert.equal((await f.review()).result.approved, true);
        const file = ledgerPath(f.project);
        const text = readFileSync(file, 'utf8');
        const lines = text.split('\n').map((l) => {
          const row = l.length > 0 ? JSON.parse(l) : null;
          return row?.event === 'review.approved' && row.run === f.runId ? JSON.stringify(forge(row)) : l;
        });
        assert.notEqual(lines.join('\n'), text);
        writeFileSync(file, lines.join('\n'));
        const closed = await f.close();
        assert.deepEqual([closed.code, closed.stderr], [1, `block B1 stopped (ledger.tamper): review.approved: ${reason}\n`]);
      } finally {
        await f.stop();
      }
    }
  });
  test('5: unavailable is never approval: a failing reviewer ⇒ unavailable, 0 approvals, close refuses unreviewed; the same content re-submitted is a fresh attempt that approves', async () => {
    const f = await flow();
    try {
      f.script({ fail: true });
      f.edit(60, 'change');
      const failed = await f.review();
      assert.deepEqual([failed.result.status, failed.result.reason, failed.result.approved, Object.hasOwn(failed, 'fix_list')], ['unavailable', 'schema', false, false]);
      assert.equal((await f.rows()).filter((r) => r.event === 'review.approved').length, 0);
      const refused = await f.close();
      assert.deepEqual([refused.code, refused.stderr.split('\n').at(-2)], [1, `block B1 open: unreviewed ${FILE}`]);

      f.script({}); // the reviewer is back; the file is unchanged
      const again = await f.review();
      assert.deepEqual([again.queued.ticket, again.queued.retry], [failed.queued.ticket, true]);
      assert.deepEqual([again.result.status, again.result.approved, again.result.round, again.result.kind], ['reviewed', true, 1, 'full']);
      assert.equal((await f.rows()).filter((r) => r.event === 'review.approved').length, 1);
      assert.equal((await f.close()).code, 0);
    } finally {
      await f.stop();
    }
  });

  test('6: the fix-loop state survives a worker restart: after round 1 (1 major) and a new worker, the fix is round 2, a recheck with F1 open', async () => {
    const f = await flow();
    try {
      f.script({ full: { findings: [finding('F1', 60)] }, recheck: { resolve: true, findings: [] } });
      f.edit(60, 'change');
      assert.deepEqual((await f.review()).result.findings.map((/** @type {any} */ x) => x.id), ['F1']);
      await f.stop();
      assert.equal(liveWorker(f.repo), null);
      const w = await startWorker(f.repo, f.runId, f.env);
      try {
        f.edit(60, 'fixed');
        const second = await f.review();
        assert.deepEqual([second.result.status, second.result.approved, second.result.round, second.result.kind], ['reviewed', true, 2, 'recheck']);
        assert.equal(f.packets()[1].includes('## open findings\n- F1 (warning, lines 60-60)'), true);
      } finally {
        await stopWorker(w, f.runId);
      }
    } finally {
      await f.stop();
    }
  });
});
