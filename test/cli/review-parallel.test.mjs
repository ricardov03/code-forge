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
function barrier() {
  /** @type {() => void} */
  let resolve;
  const promise = new Promise((r) => { resolve = () => r(undefined); });
  return { promise, resolve };
}

/**
 * Each step waits for named children to start, advances the injected clock, then releases
 * children one at a time and awaits their progress lines. No completion depends on real time.
 * A 60 s timer only guards against a broken barrier/deadlock.
 * @param {string[]} args
 * @param {Array<{started: string[], at?: number, done?: string[], pending?: string[]}>} steps
 */
async function run(args, steps, files = FILES) {
  const repo = await makeRepo(files);
  let active = 0;
  let maxActive = 0;
  /** @type {string[]} */
  const events = [];
  let stdout = '';
  let stderr = '';
  const root = currentRunRoot();
  let clock = 0;
  const started = new Map(files.map((file) => [file, barrier()]));
  const released = new Map(files.map((file) => [file, barrier()]));
  const completed = new Map(files.map((file) => [file, barrier()]));
  const pending = new Set();
  /** @type {Array<{file: string, maxS: number, timeoutMs: number}>} */
  const waits = [];
  /** @type {NodeJS.Timeout} */
  let guard;
  const hung = new Promise((_, reject) => {
    guard = setTimeout(() => reject(new Error(`review barrier hung: ${events.join(', ')}`)), 60_000);
  });
  const review = runReview(['--files', ...files, ...args], {
    cwd: repo,
    now: () => clock,
    stdout: { write: (/** @type {string} */ s) => (stdout += s) },
    stderr: { write: (/** @type {string} */ s) => {
      stderr += s;
      for (const file of files) {
        if (s.startsWith(`review: ${file}  `)) completed.get(file).resolve();
      }
    } },
    exec: async (argv, opts) => {
      const sub = argv.slice(2);
      if (sub[0] !== 'review-file') return ok({});
      if (sub[1] !== '--wait') {
        events.push(`enqueue ${sub[1]}`);
        return ok({ ticket: `t-${sub[1]}` });
      }
      const file = sub[2].slice(2);
      const maxS = Number(sub[4].replace(/s$/, ''));
      waits.push({ file, maxS, timeoutMs: opts.timeoutMs });
      events.push(`wait ${file} ${maxS}`);
      active += 1;
      maxActive = Math.max(maxActive, active);
      started.get(file).resolve();
      try {
        await released.get(file).promise;
        if (pending.has(file)) return ok({ status: 'pending' });
        events.push(`done ${file}`);
        return ok({ status: 'done', result: { approved: true } });
      } finally {
        active -= 1;
      }
    },
  });
  try {
    const drive = async () => {
      for (const step of steps) {
        await Promise.all(step.started.map((file) => started.get(file).promise));
        if (step.at !== undefined) {
          assert.ok(step.at >= clock, 'the injected clock only advances');
          clock = step.at;
        }
        for (const file of [...(step.done ?? []), ...(step.pending ?? [])]) {
          if (step.pending?.includes(file)) pending.add(file);
          released.get(file).resolve();
          await completed.get(file).promise;
        }
      }
      assert.equal(active, 0, 'every started wait child must be completed by the scenario');
    };
    const [code] = await Promise.race([Promise.all([review, drive()]), hung]);
    return { code, stdout, stderr, events, maxActive, waits, clock };
  } finally {
    clearTimeout(guard);
    for (const release of released.values()) release.resolve();
    await review;
    setRunRoot(root);
  }
}

describe('code-forge review waits on all files together (B43)', () => {
  test('all 3 enqueues happen before the first wait starts; the 3 waits start together', async () => {
    const r = await run([], [{ started: FILES, done: FILES }]);
    assert.equal(r.code, 0, r.stderr);
    assert.deepEqual(r.events.slice(0, 6).map((e) => e.split(' ').slice(0, 2).join(' ')), ['enqueue a.mjs', 'enqueue b.mjs', 'enqueue c.mjs', 'wait a.mjs', 'wait b.mjs', 'wait c.mjs']);
    assert.equal(r.maxActive, 3);
  });

  test('files finishing in order c, a, b: 3 progress lines in finish order, the table in file order', async () => {
    const r = await run([], [{ started: FILES, done: ['c.mjs', 'a.mjs', 'b.mjs'] }]);
    assert.equal(r.code, 0, r.stderr);
    assert.deepEqual(
      r.stderr.split('\n').filter((l) => / {2}approved$/.test(l)),
      ['review: c.mjs  approved', 'review: a.mjs  approved', 'review: b.mjs  approved'],
    );
    assert.equal(r.stdout, 'a.mjs  approved\nb.mjs  approved\nc.mjs  approved\n\ntotals: 3 files · 3 approved · 0 with findings (0 findings) · 0 stopped · 0 unavailable\n');
  });

  test('--max 2 and a pending file: it is reported `stopped: timeout`, the other 2 approved, exit 1', async () => {
    const r = await run(['--max', '2'], [
      { started: FILES, at: 10, done: ['a.mjs', 'c.mjs'] },
      { started: [], at: 2000, pending: ['b.mjs'] },
    ]);
    assert.equal(r.code, 1, r.stderr);
    assert.equal(r.stdout, 'a.mjs  approved\nb.mjs  stopped: timeout\nc.mjs  approved\n\ntotals: 3 files · 2 approved · 0 with findings (0 findings) · 1 stopped · 0 unavailable\n');
    assert.equal(r.clock, 2000);
  });

  test('one deadline for the run: all 3 waits get --max 2 before a slow first file completes', async () => {
    const r = await run(['--max', '2'], [
      { started: FILES, at: 1500, done: ['a.mjs'] },
      { started: [], at: 2000, pending: ['b.mjs', 'c.mjs'] },
    ]);
    assert.equal(r.code, 1, r.stderr);
    assert.deepEqual(r.events.filter((e) => e.startsWith('wait ')), ['wait a.mjs 2', 'wait b.mjs 2', 'wait c.mjs 2']);
    assert.equal(r.stdout, 'a.mjs  approved\nb.mjs  stopped: timeout\nc.mjs  stopped: timeout\n\ntotals: 3 files · 1 approved · 0 with findings (0 findings) · 2 stopped · 0 unavailable\n');
    assert.deepEqual(r.waits.map((w) => w.timeoutMs), [62000, 62000, 62000]);
  });

  test('12 files: never more than MAX_WAITERS (8) wait children at once; all 12 are approved, in file order', async () => {
    const many = Array.from({ length: 12 }, (_, i) => `f${String(i).padStart(2, '0')}.mjs`);
    const r = await run([], [
      { started: many.slice(0, 8), done: many.slice(0, 4) },
      { started: many.slice(8), done: many.slice(4) },
    ], many);
    assert.equal(r.code, 0, r.stderr);
    assert.equal(MAX_WAITERS, 8);
    assert.equal(r.maxActive, 8);
    assert.equal(r.stdout.split('\n').filter((l) => / {2}approved$/.test(l)).join(','), many.map((f) => `${f}  approved`).join(','));
  });

  test('later waiters get only the time left on the shared deadline', async () => {
    const many = Array.from({ length: 12 }, (_, i) => `f${String(i).padStart(2, '0')}.mjs`);
    const r = await run(['--max', '2'], [
      { started: many.slice(0, 8), at: 1500, done: many.slice(0, 4) },
      { started: many.slice(8), at: 2000, pending: many.slice(4) },
    ], many);
    assert.equal(r.code, 1, r.stderr);
    assert.deepEqual(r.waits, many.map((file, i) => ({ file, maxS: i < 8 ? 2 : 1, timeoutMs: i < 8 ? 62000 : 60500 })));
    assert.equal(r.maxActive, MAX_WAITERS);
    assert.equal(r.stdout.split('\n').filter((l) => /stopped: timeout$/.test(l)).length, 8);
  });

  test('files reaching the semaphore after the deadline time out without another wait child', async () => {
    const many = Array.from({ length: 12 }, (_, i) => `f${String(i).padStart(2, '0')}.mjs`);
    const r = await run(['--max', '2'], [
      { started: many.slice(0, 8), at: 2000, pending: many.slice(0, 8) },
    ], many);
    assert.equal(r.code, 1, r.stderr);
    assert.deepEqual(r.waits.map((w) => w.file), many.slice(0, 8));
    assert.equal(r.stdout.split('\n').filter((l) => /stopped: timeout$/.test(l)).length, 12);
  });
});
