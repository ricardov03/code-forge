// `code-forge review` (B22) end to end: every run is the real CLI as a subprocess, in a temp git
// repo, with a temp HOME/TMPDIR (the worker helpers pin both under one per-file temp parent,
// removed in `after()`), and a fake `claude` on PATH (no real CLI, no key, no network). The
// helpers are imported FIRST so no `src` module ever sees the real HOME.
import { alive, childEnv, cli, freshDir, stopPidAfter, useFake, waitFor } from '../worker/helpers.mjs';
import assert from 'node:assert/strict';
import { spawn, execFileSync } from 'node:child_process';
import { existsSync, mkdirSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, test } from 'node:test';

const { exec } = await import('../../src/util/exec.mjs');
const { currentRunRoot, setRunRoot } = await import('../../src/util/tmp.mjs');
const { gitChildEnv } = await import('../../src/worker/ticket.mjs');
const { liveWorker } = await import('../../src/worker/queue.mjs');
const { readRun } = await import('../../src/state/run.mjs');
const { DEFAULT_CLAUSE, runReview } = await import('../../src/cli/review.mjs');

const SCRIPTED = fileURLToPath(new URL('../worker/fake-scripted-reviewer.mjs', import.meta.url));
const SLEEPY = fileURLToPath(new URL('../worker/fake-reviewer.mjs', import.meta.url));
const BIN = fileURLToPath(new URL('../../bin/code-forge.mjs', import.meta.url));
useFake(SCRIPTED);

const FILE = 'src/a.mjs';
const CONFIG = `version: 1
project:
  slug: review-test
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

let seq = 0;

/**
 * A repo on `main` whose base commit holds `.code-forge.yml`, `.gitignore` (`dist/`) and a
 * 120-line `src/a.mjs`; plus a fake-reviewer script, a record dir and a run id.
 */
async function makeRepo() {
  const repo = freshDir('rv');
  const git = async (/** @type {string[]} */ args) => {
    const res = await exec(['git', '-c', 'init.defaultBranch=main', '-c', 'user.name=Fake Tester', '-c', 'user.email=fake@example.invalid', '-c', 'commit.gpgsign=false', ...args], { cwd: repo, env: gitChildEnv(), timeoutMs: 20000 });
    assert.equal(res.result, 'ok', res.stderr);
    return res.stdout;
  };
  await git(['init', '-q']);
  writeFileSync(path.join(repo, '.code-forge.yml'), CONFIG);
  writeFileSync(path.join(repo, '.gitignore'), 'dist/\n');
  mkdirSync(path.join(repo, 'src'));
  writeFileSync(path.join(repo, FILE), Array.from({ length: 120 }, (_, k) => `export const v${k + 1} = ${k + 1};\n`).join(''));
  await git(['add', '-A']);
  await git(['commit', '-q', '-m', 'base']);
  const baseSha = (await git(['rev-parse', 'HEAD'])).trim();
  seq += 1;
  const runId = `rv-${process.pid}-${seq}`;
  const records = freshDir('records');
  const scriptFile = path.join(freshDir('script'), 'script.json');
  writeFileSync(scriptFile, '{}');
  const env = { FAKE_RECORD: records, FAKE_SCRIPT: scriptFile };
  /** @param {number} line @param {string} tag */
  const edit = (line, tag) => {
    const full = path.join(repo, FILE);
    const all = readFileSync(full, 'utf8').split('\n');
    all[line - 1] = `export const v${line} = ${line} + ${JSON.stringify(tag)};`;
    writeFileSync(full, all.join('\n'));
  };
  const packetTexts = () => readdirSync(records).filter((n) => n.startsWith('packet-')).map((n) => readFileSync(path.join(records, n), 'utf8'));
  const packets = () => packetTexts().length;
  const script = (/** @type {Record<string, any>} */ s) => writeFileSync(scriptFile, JSON.stringify(s));
  return { repo, git, baseSha, runId, env, edit, packets, packetTexts, script, records };
}

/** @param {string} runId @returns {number} live `code-forge worker --run <runId>` processes. */
function workerProcesses(runId) {
  const ps = execFileSync('ps', ['-Ao', 'command'], { encoding: 'utf8' });
  return ps.split('\n').filter((l) => l.includes(BIN) && l.includes(`worker --run ${runId}`)).length;
}

/** @returns {number} run records under the temp HOME. */
function runRecords() {
  const dir = path.join(/** @type {string} */ (process.env.HOME), '.code-forge', 'runs');
  return existsSync(dir) ? readdirSync(dir).filter((n) => n.endsWith('.json')).length : 0;
}

/**
 * The run is ended, its one block stopped (never closed), and no worker of it is alive.
 * @param {string} repo @param {string} runId
 * @returns {Promise<Record<string, any>>} the block entry.
 */
async function assertCleanedUp(repo, runId) {
  const record = await readRun(runId);
  if (record.worker?.pid) stopPidAfter(record.worker.pid, runId);
  const blocks = Object.values(record.blocks);
  assert.deepEqual([record.status, blocks.length, blocks[0].status], ['ended', 1, 'stopped']);
  assert.equal(alive(record.worker.pid), false);
  assert.equal(liveWorker(repo), null);
  assert.equal(workerProcesses(runId), 0);
  return blocks[0];
}

/** @param {string} dir @returns {number[]} the pids the sleepy fake recorded (one file per reviewer session). */
const reviewerPids = (dir) => readdirSync(dir).filter((n) => n.startsWith('reviewer-')).map((n) => JSON.parse(readFileSync(path.join(dir, n), 'utf8')).pid);

/**
 * `code-forge review` IN this process, its `exec` replaced by `seam` (which gets the argv and a
 * `real` that runs that child with the fake `claude` on PATH and the temp HOME). The run root the
 * preload pinned is put back afterwards (the review forgets its own in its cleanup).
 * @param {string[]} args @param {Awaited<ReturnType<typeof makeRepo>>} r
 * @param {(argv: string[], real: () => Promise<import('../../src/util/exec.mjs').ExecResult>) => Promise<import('../../src/util/exec.mjs').ExecResult>} seam
 */
async function inProcess(args, r, seam) {
  const env = childEnv(r.env);
  const root = currentRunRoot();
  const listeners = process.listenerCount('SIGINT') + process.listenerCount('SIGTERM');
  let stdout = '';
  let stderr = '';
  try {
    const code = await runReview(args, {
      cwd: r.repo,
      stdout: { write: (/** @type {string} */ s) => (stdout += s) },
      stderr: { write: (/** @type {string} */ s) => (stderr += s) },
      exec: (argv, opts) => seam(argv, () => exec(argv, { ...opts, env })),
    });
    return { code, stdout, stderr, listenersLeft: process.listenerCount('SIGINT') + process.listenerCount('SIGTERM') - listeners };
  } finally {
    setRunRoot(root);
  }
}

/** @param {string[]} argv */
const isBlockStop = (argv) => argv[2] === 'block' && argv[3] === 'stop';

const TOTALS_OK_1 = 'totals: 1 file · 1 approved · 0 with findings (0 findings) · 0 stopped · 0 unavailable\n';
const SIGNALS = /** @type {const} */ (['SIGINT', 'SIGTERM']);

describe('code-forge review (B22)', () => {
  test('a branch with 1 changed file and a clean review: exit 0, `approved`, base = merge-base with main, default clause, run ended, 0 workers left', async () => {
    const r = await makeRepo();
    await r.git(['checkout', '-q', '-b', 'feature']);
    r.edit(60, 'change');
    await r.git(['commit', '-q', '-am', 'change']);
    const out = await cli(['review', '--run', r.runId], r.repo, r.env);
    assert.equal(out.code, 0, out.stderr);
    assert.equal(out.stdout, `${FILE}  approved\n\n${TOTALS_OK_1}`);
    const block = await assertCleanedUp(r.repo, r.runId);
    assert.deepEqual([block.base_sha, block.owned_files, block.level], [r.baseSha, [FILE], 'L2']);
    assert.deepEqual(block.acceptance, [{ clause: DEFAULT_CLAUSE, tests: ['review-only'] }]);
    assert.equal(r.packets(), 1);
    assert.equal(r.packetTexts()[0].includes(`## facts\nAcceptance clauses (what the change must do):\n- ${DEFAULT_CLAUSE}\n## context`), true);
  });

  test('one major finding: exit 1 and the fix list shows exactly that finding', async () => {
    const r = await makeRepo();
    await r.git(['checkout', '-q', '-b', 'feature']);
    r.edit(60, 'change');
    await r.git(['commit', '-q', '-am', 'change']);
    r.script({ full: { findings: [finding('F1', 60)] } });
    const out = await cli(['review', '--run', r.runId], r.repo, r.env);
    assert.equal(out.code, 1, out.stderr);
    assert.equal(
      out.stdout,
      [
        `${FILE}  1 finding`,
        '',
        `fix list: ${FILE}`,
        '  F1 · warning · lines 60-60',
        '    claim: claim F1',
        '    fix:   f',
        '',
        'totals: 1 file · 0 approved · 1 with findings (1 finding) · 0 stopped · 0 unavailable',
        '',
      ].join('\n'),
    );
    await assertCleanedUp(r.repo, r.runId);
  });

  test('on the default branch: exactly the uncommitted files are reviewed (ignored and binary skipped), against base HEAD; nothing is written into the tree but .code-forge/', async () => {
    const r = await makeRepo();
    r.edit(10, 'uncommitted');
    writeFileSync(path.join(r.repo, 'src', 'new.mjs'), 'export const n = 1;\n');
    mkdirSync(path.join(r.repo, 'dist'));
    writeFileSync(path.join(r.repo, 'dist', 'out.js'), 'ignored\n');
    writeFileSync(path.join(r.repo, 'img.bin'), Buffer.from([0x89, 0x50, 0x00, 0x01]));
    const out = await cli(['review', '--run', r.runId, '--json'], r.repo, r.env);
    assert.equal(out.code, 0, out.stderr);
    const doc = JSON.parse(out.stdout);
    assert.deepEqual([doc.base, doc.base_from], [r.baseSha, 'HEAD (uncommitted changes)']);
    assert.deepEqual(doc.files.map((/** @type {any} */ f) => [f.file, f.result]), [[FILE, 'approved'], ['src/new.mjs', 'approved']]);
    assert.deepEqual(doc.skipped, [{ file: 'img.bin', reason: 'binary' }]);
    const block = await assertCleanedUp(r.repo, r.runId);
    assert.deepEqual([block.base_sha, block.owned_files], [r.baseSha, [FILE, 'src/new.mjs']]);
    assert.equal(r.packets(), 2);
    const status = (await r.git(['status', '--porcelain', '--untracked-files=normal'])).split('\n').filter(Boolean).sort();
    assert.deepEqual(status, [' M src/a.mjs', '?? .code-forge/', '?? img.bin', '?? src/new.mjs']);
  });

  test('no changes: exit 0, `nothing to review`, and nothing started', async () => {
    const r = await makeRepo();
    const before = runRecords();
    const out = await cli(['review'], r.repo, r.env);
    assert.deepEqual([out.code, out.stdout], [0, 'nothing to review\n']);
    assert.deepEqual([runRecords(), existsSync(path.join(r.repo, '.code-forge')), r.packets()], [before, false, 0]);
  });

  test('--intent reaches the reviewer: the packet the fake received carries the exact clause; the clause file is written outside the tree', async () => {
    const r = await makeRepo();
    r.edit(60, 'change');
    const intent = 'Adds a guard so v60 never goes negative';
    const out = await cli(['review', '--run', r.runId, '--intent', intent], r.repo, r.env);
    assert.equal(out.code, 0, out.stderr);
    const block = await assertCleanedUp(r.repo, r.runId);
    assert.deepEqual(block.acceptance, [{ clause: intent, tests: ['review-only'] }]);
    const sent = r.packetTexts();
    assert.equal(sent.length, 1);
    assert.equal(sent[0].split(`\n- ${intent}\n`).length - 1, 1);
    assert.equal(sent[0].includes(DEFAULT_CLAUSE), false);
    const status = (await r.git(['status', '--porcelain', '--untracked-files=all'])).split('\n').filter((l) => l.length > 0 && !l.startsWith('?? .code-forge/'));
    assert.deepEqual(status, [' M src/a.mjs']);
  });

  test('--files ../x: exit 2 and 0 spawns (no run, no queue, no reviewer)', async () => {
    const r = await makeRepo();
    r.edit(60, 'change');
    const before = runRecords();
    const out = await cli(['review', '--files', '../x'], path.join(r.repo, 'src'), r.env);
    assert.equal(out.code, 2);
    assert.equal(out.stderr, 'review: --files "../x": the path may not contain a ".." segment\n');
    assert.deepEqual([runRecords(), existsSync(path.join(r.repo, '.code-forge')), r.packets(), out.stdout], [before, false, 0, '']);
  });

  test('outside a git repository: exit 2', async () => {
    const dir = freshDir('not-a-repo');
    const out = await cli(['review'], dir);
    assert.deepEqual([out.code, out.stderr, out.stdout], [2, 'review: not inside a git repository\n', '']);
  });

  for (const signal of SIGNALS) {
    test(`${signal} during the wait: "interrupted by ${signal}", exit 1, the block is stopped, the run is ended, and no worker or reviewer is left`, async () => {
      const r = await makeRepo();
      r.edit(60, 'change');
      useFake(SLEEPY);
      const env = childEnv({ FAKE_RECORD: r.records, FAKE_SLEEP_MS: '60000' });
      useFake(SCRIPTED);
      const child = spawn(process.execPath, [BIN, 'review', '--run', r.runId], { cwd: r.repo, env, stdio: ['ignore', 'pipe', 'pipe'] });
      let stderr = '';
      child.stderr.on('data', (d) => {
        stderr += String(d);
      });
      const exited = new Promise((resolve) => child.on('exit', (code) => resolve(code)));
      try {
        assert.equal(await waitFor(() => reviewerPids(r.records).length === 1, 30000), true, stderr);
        const [reviewerPid] = reviewerPids(r.records);
        child.kill(signal);
        // the reviewer sleeps 60 s and `--max` is 900 s: only killing the in-flight wait ends within 15 s
        const code = await Promise.race([exited, new Promise((resolve) => setTimeout(() => resolve('hung after 15 s'), 15000))]);
        assert.equal(code, 1, stderr);
        assert.match(stderr, new RegExp(`^review: interrupted by ${signal}; stopping the review$`, 'm'));
        await assertCleanedUp(r.repo, r.runId);
        assert.equal(await waitFor(() => !alive(reviewerPid), 10000), true, 'the sleeping reviewer session was left running');
      } finally {
        if (child.exitCode === null) child.kill('SIGKILL');
      }
    });
  }

  test('--max 3 with a reviewer that sleeps 60 s: exit 1, `stopped: timeout`, the block is stopped, the run is ended, the reviewer is dead within 10 s', async () => {
    const r = await makeRepo();
    r.edit(60, 'change');
    useFake(SLEEPY);
    let out;
    try {
      out = await cli(['review', '--run', r.runId, '--max', '3'], r.repo, { FAKE_RECORD: r.records, FAKE_SLEEP_MS: '60000' });
    } finally {
      useFake(SCRIPTED);
    }
    assert.equal(out.code, 1, out.stderr);
    assert.equal(out.stdout, `${FILE}  stopped: timeout\n\ntotals: 1 file · 0 approved · 0 with findings (0 findings) · 1 stopped · 0 unavailable\n`);
    await assertCleanedUp(r.repo, r.runId);
    const pids = reviewerPids(r.records);
    assert.equal(pids.length, 1);
    assert.equal(await waitFor(() => !alive(pids[0]), 10000), true, 'the sleeping reviewer session was left running');
  });

  test('a `block stop` step that throws: reported, `run end` and the worker reap still run, the temp root is removed, and the exit code stays the review\'s own 0', async () => {
    const r = await makeRepo();
    r.edit(60, 'change');
    let stops = 0;
    const res = await inProcess(['--run', r.runId], r, async (argv, real) => {
      const out = await real();
      if (!isBlockStop(argv)) return out;
      stops += 1;
      throw new Error('injected: block stop plumbing failed');
    });
    assert.equal(res.code, 0, res.stderr);
    assert.equal(res.stdout, `${FILE}  approved\n\n${TOTALS_OK_1}`);
    assert.equal(stops, 1);
    assert.equal(res.stderr.split('\n').filter((l) => l === 'review: block stop failed: injected: block stop plumbing failed').length, 1);
    assert.equal(res.listenersLeft, 0);
    assert.equal(existsSync(path.join(os.tmpdir(), 'code-forge', `review-${r.runId}`)), false);
    await assertCleanedUp(r.repo, r.runId);
  });

  for (const signal of SIGNALS) {
    test(`${signal} while the cleanup runs: it only counts, the cut \`block stop\` runs again, the block is stopped, the run is ended, exit code stays 0`, async () => {
      const r = await makeRepo();
      r.edit(60, 'change');
      let stops = 0;
      const res = await inProcess(['--run', r.runId], r, async (argv, real) => {
        if (!isBlockStop(argv)) return real();
        stops += 1;
        if (stops > 1) return real();
        // hold the first `block stop` open, deliver the signal meanwhile, then report the step the
        // way `exec` reports a child the forwarded signal killed
        process.kill(process.pid, signal);
        await new Promise((resolve) => setTimeout(resolve, 200));
        return { result: 'failed', code: null, signal, stdout: '', stderr: '', timedOut: false };
      });
      assert.equal(res.code, 0, res.stderr);
      assert.equal(res.stdout, `${FILE}  approved\n\n${TOTALS_OK_1}`);
      assert.equal(stops, 2);
      assert.equal(res.stderr.split('\n').filter((l) => l === `review: ${signal} during cleanup: finishing it (a second ${signal} exits at once)`).length, 1);
      assert.equal(res.stderr.includes('interrupted by'), false);
      assert.equal(res.listenersLeft, 0);
      await assertCleanedUp(r.repo, r.runId);
    });
  }

  test('an uncommitted secret-like file is skipped with its reason, never owned, and its content is in 0 packets', async () => {
    const r = await makeRepo();
    r.edit(10, 'change');
    const secret = 'FAKE-b22-secret-value-7c1d';
    writeFileSync(path.join(r.repo, '.env'), `TOKEN=${secret}\n`);
    writeFileSync(path.join(r.repo, 'id_rsa'), `${secret}\n`);
    const out = await cli(['review', '--run', r.runId, '--json'], r.repo, r.env);
    assert.equal(out.code, 0, out.stderr);
    const doc = JSON.parse(out.stdout);
    const why = 'secret-like name, never sent to a reviewer';
    assert.deepEqual(doc.skipped, [{ file: '.env', reason: why }, { file: 'id_rsa', reason: why }]);
    assert.deepEqual(doc.files.map((/** @type {any} */ f) => f.file), [FILE]);
    const block = await assertCleanedUp(r.repo, r.runId);
    assert.deepEqual(block.owned_files, [FILE]);
    const sent = r.packetTexts();
    assert.deepEqual([sent.length, sent.filter((t) => t.includes(secret)).length], [1, 0]);
  });

  test('a deleted tracked file is skipped (reason `deleted`) and not reviewed', async () => {
    const r = await makeRepo();
    writeFileSync(path.join(r.repo, 'src', 'old.mjs'), 'export const old = 1;\n');
    await r.git(['add', '-A']);
    await r.git(['commit', '-q', '-m', 'old']);
    rmSync(path.join(r.repo, 'src', 'old.mjs'));
    r.edit(10, 'change');
    const out = await cli(['review', '--run', r.runId, '--json'], r.repo, r.env);
    assert.equal(out.code, 0, out.stderr);
    const doc = JSON.parse(out.stdout);
    assert.deepEqual(doc.skipped, [{ file: 'src/old.mjs', reason: 'deleted' }]);
    assert.deepEqual(doc.files.map((/** @type {any} */ f) => [f.file, f.result]), [[FILE, 'approved']]);
    const block = await assertCleanedUp(r.repo, r.runId);
    assert.deepEqual(block.owned_files, [FILE]);
    assert.equal(r.packets(), 1);
    assert.equal(r.packetTexts()[0].includes('src/old.mjs'), false);
  });

  test('--base nonexistent-ref: exit 2, 0 runs started, 0 packets', async () => {
    const r = await makeRepo();
    r.edit(60, 'change');
    const before = runRecords();
    const out = await cli(['review', '--base', 'nonexistent-ref'], r.repo, r.env);
    assert.deepEqual([out.code, out.stderr, out.stdout], [2, 'review: --base "nonexistent-ref" is not a commit of this repository\n', '']);
    assert.deepEqual([runRecords(), existsSync(path.join(r.repo, '.code-forge')), r.packets()], [before, false, 0]);
  });

  test('--acceptance <file>: each of its clauses is in the packet exactly once, and the default clause is not', async () => {
    const r = await makeRepo();
    r.edit(60, 'change');
    const file = path.join(freshDir('acc'), 'acceptance.yml');
    const clauses = ['v60 stays exported', 'no other constant changes'];
    writeFileSync(file, clauses.map((c, i) => `- clause: ${c}\n  tests: [t${i}]\n`).join(''));
    const out = await cli(['review', '--run', r.runId, '--acceptance', file], r.repo, r.env);
    assert.equal(out.code, 0, out.stderr);
    const block = await assertCleanedUp(r.repo, r.runId);
    assert.deepEqual(block.acceptance, [{ clause: clauses[0], tests: ['t0'] }, { clause: clauses[1], tests: ['t1'] }]);
    const sent = r.packetTexts();
    assert.equal(sent.length, 1);
    assert.deepEqual(clauses.map((c) => sent[0].split(`\n- ${c}\n`).length - 1), [1, 1]);
    assert.equal(sent[0].includes(DEFAULT_CLAUSE), false);
  });

  test('--json prints exactly one JSON document with the per-file results and the totals', async () => {
    const r = await makeRepo();
    await r.git(['checkout', '-q', '-b', 'feature']);
    r.edit(60, 'change');
    await r.git(['commit', '-q', '-am', 'change']);
    r.script({ full: { findings: [finding('F1', 60)] } });
    const out = await cli(['review', '--run', r.runId, '--json'], r.repo, r.env);
    assert.equal(out.code, 1, out.stderr);
    assert.equal(out.stdout.endsWith('\n') && !out.stdout.trimEnd().includes('\n'), true);
    const doc = JSON.parse(out.stdout);
    assert.deepEqual(Object.keys(doc), ['ok', 'base', 'base_from', 'run', 'block', 'files', 'skipped', 'totals']);
    assert.deepEqual([doc.ok, doc.base, doc.base_from, doc.run, doc.block.startsWith('R-')], [false, r.baseSha, 'merge-base HEAD main', r.runId, true]);
    assert.deepEqual(doc.files, [{ file: FILE, result: 'findings', reason: null, findings: [{ id: 'F1', severity: 'warning', lines: '60-60', claim: 'claim F1', fix: 'f' }] }]);
    assert.deepEqual(doc.totals, { files: 1, approved: 0, unchanged: 0, with_findings: 1, findings: 1, stopped: 0, unavailable: 0 });
    await assertCleanedUp(r.repo, r.runId);
  });
});
