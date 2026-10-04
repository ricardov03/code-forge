// `code-forge review` (B43): every file is enqueued before any wait, the waits run together under
// ONE run deadline (`--max`), results print as they finish and the table keeps the file order.
// `runReview` runs in this process in a temp git repo; every `code-forge` child is a fake (the
// `exec` seam), so there is no worker, no CLI and no network. The helpers are imported FIRST so no
// `src` module ever sees the real HOME.
import { freshDir } from '../worker/helpers.mjs';
import assert from 'node:assert/strict';
import { writeFileSync } from 'node:fs';
import path from 'node:path';
import { describe, test } from 'node:test';

const { exec } = await import('../../src/util/exec.mjs');
const { currentRunRoot, setRunRoot } = await import('../../src/util/tmp.mjs');
const { gitChildEnv } = await import('../../src/worker/ticket.mjs');
const { runReview, MAX_WAITERS } = await import('../../src/cli/review.mjs');

const CONFIG = 'version: 1\nproject:\n  slug: review-par\nprovider: anthropic\nlevels:\n  L0:\n    model: claude-haiku-4-5-20251001\n  L1:\n    model: claude-sonnet-5\n  L2:\n    model: claude-opus-5-5\n  L3:\n    model: claude-fable-5-1\n';
const FILES = ['a.mjs', 'b.mjs', 'c.mjs'];

/** @returns {Promise<string>} a temp git repo whose base commit holds the 3 files, all then edited. */
async function makeRepo(files = FILES) {
  const repo = freshDir('rvp');
  const git = async (/** @type {string[]} */ args) => {
    const res = await exec(['git', '-c', 'init.defaultBranch=main', '-c', 'user.name=Fake Tester', '-c', 'user.email=fake@example.invalid', '-c', 'commit.gpgsign=false', ...args], { cwd: repo, env: gitChildEnv(), timeoutMs: 20000 });
    assert.equal(res.result, 'ok', res.stderr);
  };
  await git(['init', '-q']);
  writeFileSync(path.join(repo, '.code-forge.yml'), CONFIG);
  for (const f of files) writeFileSync(path.join(repo, f), 'export const v = 1;\n');
  await git(['add', '-A']);
  await git(['commit', '-q', '-m', 'base']);
  for (const f of files) writeFileSync(path.join(repo, f), 'export const v = 2;\n');
  return repo;
}

const ok = (/** @type {unknown} */ body) => ({ result: /** @type {const} */ ('ok'), code: 0, signal: null, stdout: `${JSON.stringify(body)}\n`, stderr: '', timedOut: false });
const sleep = (/** @type {number} */ ms) => new Promise((r) => setTimeout(r, ms));

/**
 * Run the review with fake children. `finishMs` maps a file to the ms its `--wait` takes to answer
 * `done`/approved (Infinity: it answers `pending` when its `--max` runs out). `events` records,
 * in order, `enqueue <file>`, `wait <file> <max>` and `done <file>`.
 * @param {string[]} args @param {Record<string, number>} finishMs
 */
async function run(args, finishMs, files = FILES) {
  const repo = await makeRepo(files);
  let active = 0;
  let maxActive = 0;
  /** @type {string[]} */
  const events = [];
  let stdout = '';
  let stderr = '';
  const root = currentRunRoot();
  const started = Date.now();
  try {
    const code = await runReview(['--files', ...files, ...args], {
      cwd: repo,
      stdout: { write: (/** @type {string} */ s) => (stdout += s) },
      stderr: { write: (/** @type {string} */ s) => (stderr += s) },
      exec: async (argv) => {
        const sub = argv.slice(2);
        if (sub[0] !== 'review-file') return ok({});
        if (sub[1] !== '--wait') {
          events.push(`enqueue ${sub[1]}`);
          await sleep(20);
          return ok({ ticket: `t-${sub[1]}` });
        }
        const file = sub[2].slice(2);
        const maxS = Number(sub[4].replace(/s$/, ''));
        events.push(`wait ${file} ${maxS}`);
        active += 1;
        maxActive = Math.max(maxActive, active);
        try {
        const ms = finishMs[file];
        if (ms > maxS * 1000) {
          await sleep(maxS * 1000);
          return ok({ status: 'pending' });
        }
        await sleep(ms);
        events.push(`done ${file}`);
        return ok({ status: 'done', result: { approved: true } });
        } finally {
          active -= 1;
        }
      },
    });
    return { code, stdout, stderr, events, maxActive, elapsed: Date.now() - started };
  } finally {
    setRunRoot(root);
  }
}

describe('code-forge review waits on all files together (B43)', () => {
  test('all 3 enqueues happen before the first wait starts; the 3 waits start together', async () => {
    const r = await run([], { 'a.mjs': 10, 'b.mjs': 10, 'c.mjs': 10 });
    assert.equal(r.code, 0, r.stderr);
    assert.deepEqual(r.events.slice(0, 6).map((e) => e.split(' ').slice(0, 2).join(' ')), ['enqueue a.mjs', 'enqueue b.mjs', 'enqueue c.mjs', 'wait a.mjs', 'wait b.mjs', 'wait c.mjs']);
  });

  test('files finishing in order c, a, b: 3 progress lines in finish order, the table in file order', async () => {
    const r = await run([], { 'a.mjs': 300, 'b.mjs': 600, 'c.mjs': 50 });
    assert.equal(r.code, 0, r.stderr);
    assert.deepEqual(
      r.stderr.split('\n').filter((l) => / {2}approved$/.test(l)),
      ['review: c.mjs  approved', 'review: a.mjs  approved', 'review: b.mjs  approved'],
    );
    assert.equal(r.stdout, 'a.mjs  approved\nb.mjs  approved\nc.mjs  approved\n\ntotals: 3 files · 3 approved · 0 with findings (0 findings) · 0 stopped · 0 unavailable\n');
  });

  test('--max 2 and a file that never answers: it is reported `stopped: timeout`, the other 2 approved, exit 1, after about 2 s', async () => {
    const r = await run(['--max', '2'], { 'a.mjs': 10, 'b.mjs': Infinity, 'c.mjs': 10 });
    assert.equal(r.code, 1, r.stderr);
    assert.equal(r.stdout, 'a.mjs  approved\nb.mjs  stopped: timeout\nc.mjs  approved\n\ntotals: 3 files · 2 approved · 0 with findings (0 findings) · 1 stopped · 0 unavailable\n');
    assert.equal(r.elapsed >= 1900 && r.elapsed < 5000, true, `elapsed ${r.elapsed} ms`);
  });

  test('one deadline for the run: a slow first file gives the later files no extra time (all 3 waits get --max 2, the run ends near 2 s, not 3 x 2 s)', async () => {
    const r = await run(['--max', '2'], { 'a.mjs': 1500, 'b.mjs': Infinity, 'c.mjs': Infinity });
    assert.equal(r.code, 1, r.stderr);
    assert.deepEqual(r.events.filter((e) => e.startsWith('wait ')), ['wait a.mjs 2', 'wait b.mjs 2', 'wait c.mjs 2']);
    assert.equal(r.stdout, 'a.mjs  approved\nb.mjs  stopped: timeout\nc.mjs  stopped: timeout\n\ntotals: 3 files · 1 approved · 0 with findings (0 findings) · 2 stopped · 0 unavailable\n');
    assert.equal(r.elapsed < 4000, true, `elapsed ${r.elapsed} ms`);
  });

  test('12 files: never more than MAX_WAITERS (8) wait children at once; all 12 are approved, in file order', async () => {
    const many = Array.from({ length: 12 }, (_, i) => `f${String(i).padStart(2, '0')}.mjs`);
    const r = await run([], Object.fromEntries(many.map((f) => [f, 100])), many);
    assert.equal(r.code, 0, r.stderr);
    assert.equal(MAX_WAITERS, 8);
    assert.equal(r.maxActive, 8);
    assert.equal(r.stdout.split('\n').filter((l) => / {2}approved$/.test(l)).join(','), many.map((f) => `${f}  approved`).join(','));
  });
});
