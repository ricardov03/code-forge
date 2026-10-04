#!/usr/bin/env node
/**
 * Maintainer script (not shipped in the npm package): how much faster is the review pool?
 *
 *   node scripts/bench-review.mjs [--review-ms <n>] [--files <n>] [--tickets <n,n,...>] [--json]
 *
 * It runs the real worker pool (`src/worker/loop.mjs`, in this process) over `--files` tickets
 * (default 6) whose review is a fake that just waits `--review-ms` (default 2000), once for each
 * `review.parallel_tickets` value in `--tickets` (default `1,3`). It prints each wall time and the
 * speed-up of the last run over the first. No real CLI, key or network. Everything it writes lives
 * under one temp directory (also HOME), removed on exit. With `--json` it prints one JSON line
 * `{reviewMs, files, runs: [{tickets, ms}], speedup}` instead.
 */

import { mkdirSync, mkdtempSync, realpathSync, rmSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const USAGE = 'usage: node scripts/bench-review.mjs [--review-ms <n>] [--files <n>] [--tickets <n,n,...>] [--json]\n';
const BLOCK = 'B1';
const CONFIG = `version: 1
project:
  slug: bench-review
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
`;

/**
 * @param {string[]} argv
 * @returns {{reviewMs: number, files: number, tickets: number[], json: boolean}}
 */
export function parseArgs(argv) {
  const out = { reviewMs: 2000, files: 6, tickets: [1, 3], json: false };
  for (let i = 0; i < argv.length; i += 1) {
    const a = argv[i];
    if (a === '--json') out.json = true;
    else if (a === '--review-ms' || a === '--files') {
      const n = Number(argv[(i += 1)]);
      if (!Number.isInteger(n) || n < 1) throw new Error(`${a} needs a whole number >= 1`);
      if (a === '--files') out.files = n;
      else out.reviewMs = n;
    } else if (a === '--tickets') {
      const list = String(argv[(i += 1)] ?? '').split(',').map(Number);
      if (list.length < 2 || list.some((n) => !Number.isInteger(n) || n < 1 || n > 16)) throw new Error('--tickets needs two or more whole numbers from 1 to 16, like 1,3');
      out.tickets = list;
    } else throw new Error(`unknown argument ${a}`);
  }
  return out;
}

/**
 * Time the worker pool over `files` fake-review tickets for each pool size.
 * Needs `process.env.HOME` to be a throwaway directory before it is called (the CLI entry sets it).
 * @param {{reviewMs: number, files: number, tickets: number[]}} opts
 * @returns {Promise<{reviewMs: number, files: number, runs: Array<{tickets: number, ms: number}>, speedup: number}>}
 */
export async function runBench({ reviewMs, files, tickets }) {
  const parent = realpathSync(mkdtempSync(path.join(os.tmpdir(), 'cf-bench-review-')));
  const { exec } = await import('../src/util/exec.mjs');
  const { createKeyStore } = await import('../src/keys/store.mjs');
  const { openBlock } = await import('../src/state/block.mjs');
  const { startRun } = await import('../src/state/run.mjs');
  const { loadKey } = await import('../src/state/signer.mjs');
  const { createWorker } = await import('../src/worker/loop.mjs');
  const { enqueue } = await import('../src/worker/queue.mjs');
  const { gitChildEnv } = await import('../src/worker/ticket.mjs');
  /** @type {Array<{tickets: number, ms: number}>} */
  const runs = [];
  try {
    for (const [i, n] of tickets.entries()) {
      const repo = path.join(parent, `repo-${i}`);
      mkdirSync(path.join(repo, 'src'), { recursive: true });
      for (const args of [['init', '-q'], ['-c', 'user.name=Bench', '-c', 'user.email=bench@example.invalid', '-c', 'commit.gpgsign=false', 'commit', '-q', '--allow-empty', '-m', 'base']]) {
        const res = await exec(['git', ...args], { cwd: repo, env: gitChildEnv(), timeoutMs: 20000 });
        if (res.result !== 'ok') throw new Error(`git ${args.at(-1)} failed`);
      }
      writeFileSync(path.join(repo, '.code-forge.yml'), CONFIG);
      const runId = `r-bench-${process.pid}-${i}`;
      const noRow = async () => {};
      await startRun({ workspace: repo, project: 'bench-review', runId, writeRow: noRow });
      await openBlock({ runId, id: BLOCK, level: 'L2', owned: ['src/**'], acceptance: [{ clause: 'bench', tests: ['bench'] }], writeRow: noRow });
      const storeDir = path.join(parent, `store-${i}`);
      mkdirSync(storeDir);
      const worker = await createWorker(
        { runId, repoRoot: repo, cfg: { review: { parallel_tickets: n } }, runRootDir: path.join(parent, `runroot-${i}`), slug: 'bench-review', key: await loadKey(runId), pollMs: 10 },
        {
          store: await createKeyStore({ backends: [], dir: storeDir }),
          env: {},
          writeRow: async () => {},
          readRows: async () => [],
          review: async () => {
            await new Promise((r) => setTimeout(r, reviewMs));
            return { status: 'reviewed', approved: true, engine: 'bench', sessions: [] };
          },
        },
      );
      for (let f = 0; f < files; f += 1) {
        writeFileSync(path.join(repo, 'src', `f${f}.mjs`), `export const f${f} = ${f};\n`);
        enqueue({ repoRoot: repo, run: runId, block: BLOCK, file: `src/f${f}.mjs`, now: new Date(Date.UTC(2026, 9, 4, 10, 0, f)) });
      }
      const started = performance.now();
      const done = await worker.drain();
      const ms = Math.round(performance.now() - started);
      if (done !== files) throw new Error(`the pool finished ${done} of ${files} tickets`);
      runs.push({ tickets: n, ms });
    }
  } finally {
    rmSync(parent, { recursive: true, force: true });
  }
  const first = /** @type {{ms: number}} */ (runs[0]);
  const last = /** @type {{ms: number}} */ (runs.at(-1));
  return { reviewMs, files, runs, speedup: Math.round((first.ms / last.ms) * 100) / 100 };
}

/** @param {string[]} argv @returns {Promise<number>} */
async function main(argv) {
  let opts;
  try {
    opts = parseArgs(argv);
  } catch (err) {
    process.stderr.write(`bench-review: ${/** @type {Error} */ (err).message}\n${USAGE}`);
    return 2;
  }
  const home = realpathSync(mkdtempSync(path.join(os.tmpdir(), 'cf-bench-home-')));
  process.env.HOME = home;
  process.env.CODE_FORGE_KEY_BACKEND = 'file';
  try {
    const res = await runBench(opts);
    if (opts.json) {
      process.stdout.write(`${JSON.stringify(res)}\n`);
      return 0;
    }
    process.stdout.write(`${res.files} tickets, fake review ${res.reviewMs} ms each\n`);
    for (const r of res.runs) process.stdout.write(`parallel_tickets ${r.tickets}: ${r.ms} ms\n`);
    process.stdout.write(`speed-up: ${res.speedup}x (parallel_tickets ${res.runs.at(-1)?.tickets} vs ${res.runs[0]?.tickets})\n`);
    return 0;
  } finally {
    rmSync(home, { recursive: true, force: true });
  }
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  process.exitCode = await main(process.argv.slice(2));
}
