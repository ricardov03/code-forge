/**
 * Eval 18 `recheck-reviews-fix-hunks-only` (plan §4.11, §9.3). End to end through real
 * `code-forge` subprocesses (`run start` → `block open` → `review-file` → fix → `review-file`
 * again → `block close`), the reviewer a scripted fake `claude` on PATH
 * (`test/worker/fake-scripted-reviewer.mjs`, never a real CLI or key). Round 1 (a fresh file)
 * reviews the whole 120-line file; round 2 (the recheck after one major is fixed) reviews only the
 * fix-hunk window (its exact edges derived from the real `fixHunkDiff`/`hunkWindows` in
 * `src/review/{fixloop,context}.mjs`, not guessed), never the untouched top/bottom of the file,
 * plus the still-open finding list. No real project named (R13).
 */
import assert from 'node:assert/strict';
import { mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { makeHarness, REPO } from '../lib/harness.mjs';

const { DEFAULT_CONTEXT, hunkWindows } = await import(path.join(REPO, 'src', 'review', 'context.mjs'));
const { fixHunkDiff } = await import(path.join(REPO, 'src', 'review', 'fixloop.mjs'));

const FAKE_REVIEWER = path.join(REPO, 'test', 'worker', 'fake-scripted-reviewer.mjs');
const FILE = 'src/a.mjs';
const LINES = 120;
const CONFIG = `version: 1
project:
  slug: eval18
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

export async function run() {
  const h = makeHarness('18');
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

    runId = `r-eval18-${process.pid}`;
    const records = h.freshDir('records');
    const scriptFile = path.join(h.freshDir('script'), 'script.json');
    writeFileSync(scriptFile, JSON.stringify({ full: { findings: [{ id: 'F1', file: FILE, line_start: 60, line_end: 60, severity: 'warning', category: 'correctness', claim: 'claim F1', evidence: 'e', fix: 'f' }] }, recheck: { resolve: true, findings: [] } }));
    const env = h.childEnv(FAKE_REVIEWER, { FAKE_RECORD: records, FAKE_SCRIPT: scriptFile });

    const started = await h.cli(['run', 'start', '--run', runId], repo, env);
    assert.equal(started.code, 0, started.stderr);
    workerPid = h.workerPid(repo);

    const acceptance = path.join(h.freshDir('acc'), 'acceptance.yml');
    writeFileSync(acceptance, '- clause: the file is reviewed\n  tests: [eval-18]\n');
    const opened = await h.cli(['block', 'open', 'B1', '--run', runId, '--level', 'L2', '--owned', FILE, '--acceptance', acceptance], repo, env);
    assert.equal(opened.code, 0, opened.stderr);

    /** @param {number} line @param {string} tag @returns {string} the file's new content. */
    const edit = (line, tag) => {
      const full = path.join(repo, FILE);
      const all = readFileSync(full, 'utf8').split('\n');
      all[line - 1] = `export const v${line} = ${line} + ${JSON.stringify(tag)};`;
      const content = all.join('\n');
      writeFileSync(full, content);
      return content;
    };
    const review = async () => {
      const queued = await h.cli(['review-file', FILE, '--block', 'B1'], repo, env);
      assert.equal(queued.json?.status, 'queued', queued.stdout);
      const waited = await h.cli(['review-file', '--wait', queued.json.ticket, '--max', '30s'], repo, env);
      assert.equal(waited.json?.status, 'done', waited.stdout);
      return waited.json.result;
    };
    /** @returns {string[]} the packets the fake received, oldest first. */
    const packets = () =>
      readdirSync(records)
        .filter((n) => n.startsWith('packet-'))
        .sort((a, b) => (BigInt(a.slice(7, -3)) < BigInt(b.slice(7, -3)) ? -1 : 1))
        .map((n) => readFileSync(path.join(records, n), 'utf8'));

    const reviewedContent = edit(60, 'change');
    const first = await review();
    assert.deepEqual([first.status, first.approved, first.round, first.findings.length], ['reviewed', false, 1, 1]);

    const fixedContent = edit(60, 'fixed');
    const second = await review();
    assert.deepEqual([second.status, second.approved, second.round, second.kind], ['reviewed', true, 2, 'recheck']);

    const sent = packets();
    assert.equal(sent.length, 2, 'exactly 2 packets were sent to the reviewer');
    // Round 1 carried the whole 120-line file (both ends visible).
    assert.deepEqual([sent[0].includes(original(5)), sent[0].includes(original(115)), sent[0].includes('## open findings')], [true, true, false]);

    // Round 2's fix-hunk window, computed the same way the product does (`fixHunkDiff` then
    // `hunkWindows` at the ±40 `hunk_context_lines` radius, plan §4.11) — not a guessed line.
    const workDir = mkdtempSync(path.join(os.tmpdir(), 'cf-eval18-diff-'));
    let window;
    try {
      const diff = await fixHunkDiff({ file: FILE, previous: reviewedContent, current: fixedContent, workDir });
      const windows = hunkWindows(diff.hunks, LINES, DEFAULT_CONTEXT.hunk_context_lines);
      assert.equal(windows.length, 1, 'the single-line edit produces exactly one fix hunk window');
      [window] = windows;
    } finally {
      rmSync(workDir, { recursive: true, force: true });
    }
    assert.ok(window.start > 1 && window.end < LINES, 'the window must not already touch the file edges, or the edge checks below prove nothing');
    // Inside the window (both edges) and the edited line are present; one line past each edge is
    // not — the untouched top/bottom of the file, exactly at the fix-hunk boundary, never inside it.
    assert.deepEqual(
      [sent[1].includes(original(window.start)), sent[1].includes(original(window.end)), sent[1].includes(original(window.start - 1)), sent[1].includes(original(window.end + 1)), sent[1].includes(fixedContent.split('\n')[59])],
      [true, true, false, false, true],
    );
    assert.deepEqual([sent[1].includes(original(5)), sent[1].includes(original(115))], [false, false]);
    assert.equal(sent[1].includes('## open findings\n- F1 (warning, lines 60-60): claim F1'), true);

    const closed = await h.cli(['block', 'close', 'B1', '--run', runId], repo, env);
    assert.equal(closed.code, 0, closed.stderr);

    return { pass: true, detail: 'round 1 packet carries the whole file; round 2 (recheck) carries only the fix-hunk window + open findings' };
  } finally {
    if (workerPid && runId) await h.stopPid(workerPid, runId);
    h.cleanup();
  }
}
