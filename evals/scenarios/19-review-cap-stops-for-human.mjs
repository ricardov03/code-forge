/**
 * Eval 19 `review-cap-stops-for-human` (plan §4.11, §9.3; R1). End to end through real
 * `code-forge` subprocesses, same shape as eval 18. The review stalls at round 2 (the open set
 * does not shrink) ⇒ the L3 patch rung fires ONCE (never a running L3 level, R1); the patch
 * shrinks the open set; rounds 3–4 keep shrinking but never reach 0 ⇒ round 4 hits
 * `review.max_rounds_per_file` (4) with an open finding ⇒ `stopped: review_cap` — the human
 * decides next (fix by hand, `block waive`, or re-decompose), the worker does not retry on its
 * own. Reviewer: the scripted fake `claude` on PATH (`test/worker/fake-scripted-reviewer.mjs`),
 * never a real CLI or key. No real project named (R13).
 */
import assert from 'node:assert/strict';
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { makeHarness, REPO } from '../lib/harness.mjs';

const FAKE_REVIEWER = path.join(REPO, 'test', 'worker', 'fake-scripted-reviewer.mjs');
const FILE = 'src/a.mjs';
const LINES = 120;
const CONFIG = `version: 1
project:
  slug: eval19
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

/** @param {number} i */
const original = (i) => `export const v${i} = ${i};`;
/** @param {string} id @param {number} line */
const finding = (id, line) => ({ id, file: FILE, line_start: line, line_end: line, severity: 'warning', category: 'correctness', claim: `claim ${id}`, evidence: 'e', fix: 'f' });

export async function run() {
  const h = makeHarness('19');
  let workerPid = null;
  let runId = null;
  try {
    const repo = h.freshDir('repo');
    await h.git(['init', '-q'], repo);
    writeFileSync(path.join(repo, '.code-forge.yml'), CONFIG);
    mkdirSync(path.join(repo, 'src'));
    writeFileSync(path.join(repo, FILE), Array.from({ length: LINES }, (_, k) => `${original(k + 1)}\n`).join(''));
    await h.git(['add', '-A'], repo);
    await h.git(['commit', '-q', '-m', 'base'], repo);

    runId = `r-eval19-${process.pid}`;
    const records = h.freshDir('records');
    const scriptFile = path.join(h.freshDir('script'), 'script.json');
    const script = (s) => writeFileSync(scriptFile, JSON.stringify(s));
    script({ full: { findings: [finding('F1', 20), finding('F2', 50), finding('F3', 60), finding('F4', 90)] } });
    const env = h.childEnv(FAKE_REVIEWER, { FAKE_RECORD: records, FAKE_SCRIPT: scriptFile });

    const started = await h.cli(['run', 'start', '--run', runId], repo, env);
    assert.equal(started.code, 0, started.stderr);
    workerPid = h.workerPid(repo);

    const acceptance = path.join(h.freshDir('acc'), 'acceptance.yml');
    writeFileSync(acceptance, '- clause: the file is reviewed\n  tests: [eval-19]\n');
    const opened = await h.cli(['block', 'open', 'B1', '--run', runId, '--level', 'L2', '--owned', FILE, '--acceptance', acceptance], repo, env);
    assert.equal(opened.code, 0, opened.stderr);

    let editSeq = 0;
    const edit = () => {
      editSeq += 1;
      const full = path.join(repo, FILE);
      const all = readFileSync(full, 'utf8').split('\n');
      all[59] = `export const v60 = 60 + ${editSeq};`; // one line, so every round shares one fix hunk
      writeFileSync(full, all.join('\n'));
    };
    const review = async () => {
      const queued = await h.cli(['review-file', FILE, '--block', 'B1'], repo, env);
      assert.equal(queued.json?.status, 'queued', queued.stdout);
      const waited = await h.cli(['review-file', '--wait', queued.json.ticket, '--max', '30s'], repo, env);
      assert.equal(waited.json?.status, 'done', waited.stdout);
      return waited.json.result;
    };

    // Round 1 (full): 4 findings open.
    edit();
    const r1 = await review();
    assert.deepEqual([r1.status, r1.round, r1.kind, r1.findings.length], ['reviewed', 1, 'full', 4]);

    // Round 2 (recheck): nothing resolves, nothing new ⇒ stall ⇒ the L3 rung (never a running L3
    // level, R1): `next` is a patch at L3, not a fresh review round.
    script({ recheck: { resolve: false, findings: [] } });
    edit();
    const r2 = await review();
    assert.deepEqual([r2.status, r2.round, r2.kind, r2.trigger, r2.next.action, r2.next.level, r2.findings.length], ['reviewed', 2, 'recheck', 'review_stall', 'patch', 'L3', 4]);

    // The L3 patch (simulated by an edit): shrinks 4 open ⇒ 3. `patch_check` keeps round 2. New
    // findings share the exact edited line: the fix-hunk diff itself (not the wider ± context
    // shown around it) is what decides whether a new finding is "inside" the hunk (§4.11).
    script({ recheck: { resolve: true, findings: [finding('G1', 60), finding('G2', 60), finding('G3', 60)] } });
    edit();
    const r3 = await review();
    assert.deepEqual([r3.status, r3.round, r3.kind, r3.findings.length], ['reviewed', 2, 'patch_check', 3]);

    // Rounds 3 and 4 continue shrinking at L2 (the rung is spent) but never reach 0.
    script({ recheck: { resolve: true, findings: [finding('H1', 60), finding('H2', 60)] } });
    edit();
    const r4 = await review();
    assert.deepEqual([r4.status, r4.round, r4.kind, r4.findings.length], ['reviewed', 3, 'recheck', 2]);

    script({ recheck: { resolve: true, findings: [finding('I1', 60)] } });
    edit();
    const r5 = await review();
    assert.deepEqual([r5.status, r5.round, r5.kind, r5.stopped, r5.trigger, r5.findings.map((f) => f.id)], ['stopped', 4, 'recheck', 'review_cap', 'review_cap', ['I1']]);

    // The cap is terminal: re-submitting the SAME (unchanged) content does not retry the loop.
    // `enqueue()` is idempotent by content hash and the ticket already has a `.done` marker, so
    // the enqueue call itself reports `status: 'done'` straight away (`{ticket, status, file,
    // content_hash}` — no `result` yet; that needs the `--wait` call, same as every other round).
    const resubmitted = await h.cli(['review-file', FILE, '--block', 'B1'], repo, env);
    assert.deepEqual(Object.keys(resubmitted.json).sort(), ['content_hash', 'file', 'status', 'ticket']);
    assert.equal(resubmitted.json.status, 'done');
    const waitedAgain = await h.cli(['review-file', '--wait', resubmitted.json.ticket, '--max', '30s'], repo, env);
    assert.equal(waitedAgain.json?.status, 'done', waitedAgain.stdout);
    const again = waitedAgain.json.result;
    assert.deepEqual([again.status, again.stopped], ['stopped', 'review_cap']);

    const closed = await h.cli(['block', 'close', 'B1', '--run', runId], repo, env);
    assert.deepEqual([closed.code, closed.stderr.split('\n').at(-2)], [1, `block B1 open: unreviewed ${FILE}; review_cap ${FILE} I1`]);

    return { pass: true, detail: 'a stall at round 2 takes the one L3 patch rung; rounds 3-4 keep shrinking but round 4 still has an open finding ⇒ stopped: review_cap, terminal' };
  } finally {
    if (workerPid && runId) await h.stopPid(workerPid, runId);
    h.cleanup();
  }
}
