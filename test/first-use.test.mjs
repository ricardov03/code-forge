// B17: the first real use, automated. `scripts/first-use.mjs --engine harness --coder scripted`
// runs end to end as a real subprocess on a copy of `examples/node-lib/`: every verb is the real
// CLI, the worker is the one `run start` launched, and `claude` on PATH is a dispatcher written
// into this file's temp parent that hands the facts delegate and the author to
// `test/fixtures/bin/fake-claude` (fixed answers) and every review to
// `test/worker/fake-scripted-reviewer.mjs`. No real CLI, no key, no network (Jev off ⇒ §3.5 rules).
// HOME/TMPDIR are the isolate preload's per-process root; the copy and the run roots live under
// ONE mkdtemp parent (`--tmp-root`), removed in `after()` together with any worker still alive.
import assert from 'node:assert/strict';
import { chmodSync, existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, realpathSync, rmSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { spawn } from 'node:child_process';
import { after, before, describe, test } from 'node:test';
import { snapshotGuarded } from './helpers/isolate.mjs';
import { BLOCK, OWNED, StepFailed, blockCost, degradedBanner, parseArgs, waitForSentinel } from '../scripts/first-use.mjs';

const { exec } = await import('../src/util/exec.mjs');
const { readAllRows } = await import('../src/ledger/write.mjs');
const { estimateCostUsd } = await import('../src/ledger/prices.mjs');
const { readRun, stopPinnedWorker } = await import('../src/state/run.mjs');
const { loadKey, verifyRow } = await import('../src/state/signer.mjs');
const { isAlive, listEntries, readStartTime } = await import('../src/util/reaper.mjs');
const { gitChildEnv } = await import('../src/worker/ticket.mjs');

const REPO = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const SCRIPT = path.join(REPO, 'scripts', 'first-use.mjs');
const SLUG = 'node-lib-example';
const PARENT = realpathSync(mkdtempSync(path.join(os.tmpdir(), 'cf-b17-')));
const TMP_ROOT = path.join(PARENT, 'tmp-root');

/** @type {string[]} */
const runIds = [];
after(async () => {
  for (const runId of runIds) {
    try {
      const record = await readRun(runId);
      if (record.worker) await stopPinnedWorker(record.worker);
    } catch {
      // no record, or the worker is already gone
    }
  }
  rmSync(PARENT, { recursive: true, force: true });
});

/** The facts delegate's answer for the 5 claims of `examples/first-use/clamp.brief.md`. */
const FACTS_ANSWER = {
  facts: [
    { fact_id: 'F1', claim: './src/math.mjs', kind: 'path', command: 'ls ./src/math.mjs', output_excerpt: './src/math.mjs', tag: 'VERIFIED', why: null },
    { fact_id: 'F2', claim: './test/clamp.test.mjs', kind: 'path', command: 'ls ./test/clamp.test.mjs', output_excerpt: 'ls: ./test/clamp.test.mjs: No such file or directory', tag: 'NOT-FOUND', why: 'the block creates it' },
    { fact_id: 'F3', claim: 'npm test', kind: 'command', command: 'cat package.json', output_excerpt: '"test": "node --test"', tag: 'VERIFIED', why: null },
    { fact_id: 'F4', claim: '--test', kind: 'flag', command: '', output_excerpt: '', tag: 'UNVERIFIABLE', why: 'node accepts only --version as a read-only check' },
    { fact_id: 'F5', claim: '../src/math.mjs', kind: 'path', command: '', output_excerpt: '', tag: 'UNVERIFIABLE', why: 'relative to test/; a .. path is not an allowed check' },
  ],
};
const AUTHOR_ANSWER = { draft: '# B1 plan — add clamp()\n\nOwned: src/math.mjs, test/clamp.test.mjs.\n', questions: [] };

/**
 * `claude` on PATH: route by role (`--tools Bash` = the facts delegate; the author's system prompt;
 * anything else is a reviewer) and report 400 output tokens, a real reviewer's order of magnitude
 * (the fakes' fixed 42 is below the default `review.min_tokens_out`).
 */
function writeDispatcher() {
  const bin = path.join(PARENT, 'bin');
  mkdirSync(bin);
  const file = path.join(bin, 'claude');
  const fakeClaude = path.join(REPO, 'test', 'fixtures', 'bin', 'fake-claude');
  const reviewer = path.join(REPO, 'test', 'worker', 'fake-scripted-reviewer.mjs');
  const source = [
    '#!/usr/bin/env node',
    "import { spawnSync } from 'node:child_process';",
    "import { fstatSync, readFileSync } from 'node:fs';",
    'const argv = process.argv.slice(2);',
    'const st = fstatSync(0);',
    'const input = st.isFIFO() || st.isSocket() || st.isFile() ? readFileSync(0) : Buffer.alloc(0);',
    'const at = (flag) => { const i = argv.indexOf(flag); return i >= 0 ? argv[i + 1] : undefined; };',
    'const env = { ...process.env };',
    `let target = ${JSON.stringify(reviewer)};`,
    `if (at('--tools') === 'Bash') { target = ${JSON.stringify(fakeClaude)}; env.FAKE_ANSWER = ${JSON.stringify(JSON.stringify(FACTS_ANSWER))}; }`,
    `else if ((at('--system-prompt') ?? '').includes('plan author')) { target = ${JSON.stringify(fakeClaude)}; env.FAKE_ANSWER = ${JSON.stringify(JSON.stringify(AUTHOR_ANSWER))}; }`,
    "const r = spawnSync(process.execPath, [target, ...argv], { input, env, stdio: ['pipe', 'pipe', 'inherit'] });",
    'const out = JSON.parse(String(r.stdout));',
    'out.usage.output_tokens = 400;',
    'process.stdout.write(`${JSON.stringify(out)}\\n`);',
    'process.exit(r.status ?? 1);',
    '',
  ].join('\n');
  writeFileSync(file, source);
  chmodSync(file, 0o755);
  return bin;
}

const BIN_DIR = writeDispatcher();

/**
 * Run the script as a subprocess with only the fakes on PATH.
 * @param {string[]} args @param {Record<string, string>} [extraEnv]
 */
async function firstUse(args, extraEnv = {}) {
  const env = {
    PATH: [BIN_DIR, path.dirname(process.execPath), '/usr/bin', '/bin'].join(path.delimiter),
    HOME: /** @type {string} */ (process.env.HOME),
    TMPDIR: /** @type {string} */ (process.env.TMPDIR),
    CODE_FORGE_KEY_BACKEND: 'file',
    ...extraEnv,
  };
  const res = await exec([process.execPath, SCRIPT, ...args], { cwd: PARENT, env, timeoutMs: 240000 });
  const summaryLine = res.stdout.split('\n').find((l) => l.startsWith('FIRST-USE ')) ?? '';
  return { ...res, summary: summaryLine ? JSON.parse(summaryLine.slice('FIRST-USE '.length)) : null };
}

/** @param {string} runId @returns {Promise<Array<Record<string, any>>>} the run's ledger rows, in file order */
const runRows = async (runId) => (await readAllRows(SLUG)).filter((r) => r.run === runId);

/**
 * Every pid registered in a run root under `base` whose process (same start time) is still alive.
 * @param {string} base @returns {number[]}
 */
function livePidsUnder(base) {
  const live = [];
  for (const root of existsSync(base) ? readdirSync(base) : []) {
    for (const entry of listEntries(path.join(base, root, 'pids'))) {
      if (isAlive(entry.pid) && readStartTime(entry.pid) === entry.start_time) live.push(entry.pid);
    }
  }
  return live;
}

describe('first real use: scripts/first-use.mjs --engine harness (B17)', () => {
  const RUN = `fu-b17-${process.pid}`;
  /** @type {Awaited<ReturnType<typeof firstUse>>} */
  let res;
  /** @type {Array<Record<string, any>>} */
  let rows;
  /** @type {Set<string>} */
  let guardedBefore;
  let runRootExisted = false;
  let copy = '';

  before(async () => {
    guardedBefore = snapshotGuarded();
    runIds.push(RUN);
    res = await firstUse(['--engine', 'harness', '--coder', 'scripted', '--run', RUN, '--tmp-root', TMP_ROOT]);
    runRootExisted = existsSync(path.join(TMP_ROOT, RUN, 'owner.json'));
    rows = await runRows(RUN);
    copy = path.join(TMP_ROOT, 'first-use', RUN);
  });

  test('the run exits 0 through all 11 steps and ends its run', () => {
    assert.equal(res.code, 0, `${res.stderr}\n${res.stdout}`);
    assert.deepEqual(res.summary.steps, ['copy', 'init', 'facts', 'author', 'start', 'open', 'code', 'review', 'proof', 'close', 'report']);
    assert.deepEqual([res.summary.ok, res.summary.run_ended], [true, true]);
  });

  test('the copy and the run root are under tmp.root, never the package tree; no-leak holds (real dirs + processes)', async () => {
    assert.equal(res.summary.copy, copy);
    assert.deepEqual([copy.startsWith(`${TMP_ROOT}${path.sep}`), copy.startsWith(`${REPO}${path.sep}`), runRootExisted], [true, false, true]);
    // no-leak: 0 new entries in the real ~/.code-forge and <repo>/.code-forge; 0 live processes left
    assert.deepEqual([...snapshotGuarded()].filter((p) => !guardedBefore.has(p)), []);
    const record = await readRun(RUN);
    const defaultBase = path.join(/** @type {string} */ (process.env.TMPDIR), 'code-forge'); // the CLI verbs' own lazy roots
    assert.deepEqual(
      [record.status, isAlive(record.worker.pid) && readStartTime(record.worker.pid) === record.worker.started_at, livePidsUnder(TMP_ROOT), livePidsUnder(defaultBase)],
      ['ended', false, [], []],
    );
  });

  test('the shipped gitignore becomes the copy\'s .gitignore with exactly its 3 rules (npm pack drops nested .gitignore files)', () => {
    assert.deepEqual(
      [readFileSync(path.join(copy, '.gitignore'), 'utf8'), existsSync(path.join(copy, 'gitignore')), existsSync(path.join(REPO, 'examples', 'node-lib', '.gitignore'))],
      ['.code-forge/\nnode_modules/\n.env\n', false, false],
    );
  });

  test('init --no-interaction writes .code-forge.yml in the copy (engine harness, tmp.root = the run tmp root)', () => {
    const text = readFileSync(path.join(copy, '.code-forge.yml'), 'utf8');
    assert.deepEqual(
      [/^engine: harness$/m.test(text), /^ {2}slug: node-lib-example$/m.test(text), text.includes(`root: ${TMP_ROOT}`)],
      [true, true, true],
    );
  });

  test('the ledger shows facts.built BEFORE the author session (1 of each)', () => {
    const facts = rows.filter((r) => r.event === 'facts.built');
    const author = rows.filter((r) => r.event === 'session' && r.role === 'author');
    assert.deepEqual([facts.length, author.length], [1, 1]);
    assert.equal(rows.indexOf(facts[0]) < rows.indexOf(author[0]), true);
    assert.deepEqual([facts[0].claims, facts[0].verified], [5, 2]);
  });

  test('exactly one signed review.approved per changed or new file (2 files: 1 changed, 1 new)', async () => {
    const env = gitChildEnv();
    const changed = (await exec(['git', 'diff', '--name-only', 'HEAD', '--', ...OWNED], { cwd: copy, env })).stdout.trim().split('\n');
    const added = (await exec(['git', 'ls-files', '--others', '--exclude-standard', '--', ...OWNED], { cwd: copy, env })).stdout.trim().split('\n');
    assert.deepEqual([changed, added], [['src/math.mjs'], ['test/clamp.test.mjs']]);
    const approvals = rows.filter((r) => r.event === 'review.approved');
    assert.deepEqual(approvals.map((r) => [r.block, r.file]).sort(), [[BLOCK, 'src/math.mjs'], [BLOCK, 'test/clamp.test.mjs']]);
    const key = await loadKey(RUN);
    assert.deepEqual(approvals.map((r) => verifyRow(r, key).ok), [true, true]);
  });

  test('red→green: one signed proof row per acceptance test, red_kind assertion, mechanism revert, proven', async () => {
    const proofs = rows.filter((r) => r.event === 'proof' && r.step === 'red-green');
    assert.deepEqual(
      proofs.map((r) => [r.test, r.mechanism, r.red, r.red_kind, r.green, r.proven]),
      [
        ['test/clamp.test.mjs::clamp returns the value inside the range', 'revert', 'RED', 'assertion', 'GREEN', true],
        ['test/clamp.test.mjs::clamp returns min below the range', 'revert', 'RED', 'assertion', 'GREEN', true],
        ['test/clamp.test.mjs::clamp returns max above the range', 'revert', 'RED', 'assertion', 'GREEN', true],
        ['test/clamp.test.mjs::clamp refuses min above max', 'revert', 'RED', 'assertion', 'GREEN', true],
      ],
    );
    const key = await loadKey(RUN);
    assert.deepEqual(proofs.map((r) => verifyRow(r, key).ok), [true, true, true, true]);
  });

  test('the review engine writes one review.plan row per file (depth quick, degrade step 0)', () => {
    const plans = rows.filter((r) => r.event === 'review.plan');
    assert.deepEqual(plans.map((r) => [r.file, r.depth_chosen, r.degrade_step]), [['src/math.mjs', 'quick', 0], ['test/clamp.test.mjs', 'quick', 0]]);
  });

  test('one review.round row per file: round 1, kind full, 0 open after', () => {
    const rounds = rows.filter((r) => r.event === 'review.round');
    assert.deepEqual(rounds.map((r) => [r.file, r.round, r.kind, r.open_after]), [['src/math.mjs', 1, 'full', 0], ['test/clamp.test.mjs', 1, 'full', 0]]);
  });

  test('the proof export restores .github/ and CHANGELOG.md (export-ignore) and carries the untracked .env', () => {
    const exports = rows.filter((r) => r.event === 'proof' && r.step === 'export');
    assert.equal(exports.length, 5); // `proof export` once, then one fresh export per red→green (4)
    assert.deepEqual(exports[0].restored, ['.github/workflows/test.yml', 'CHANGELOG.md']);
    assert.deepEqual(exports[0].untracked, ['.env']);
    const line = res.stdout.split('\n').find((l) => l.startsWith('export ')) ?? '';
    assert.equal(line.endsWith(' · restored .github/workflows/test.yml, CHANGELOG.md · untracked .env'), true, line);
    assert.equal(res.stdout.includes('\ngates in the export · test ok\n'), true);
  });

  test('the cost of the completed block is printed, and it is the priced sum of its 4 session rows', () => {
    const sessions = rows.filter((r) => r.event === 'session');
    assert.deepEqual(sessions.map((r) => r.role), ['facts', 'author', 'reviewer', 'reviewer']);
    const usd = Math.round(sessions.reduce((sum, r) => sum + estimateCostUsd({ provider: r.provider, level: r.level, tokensIn: r.tokens_in, tokensOut: r.tokens_out }), 0) * 10000) / 10000;
    assert.equal(blockCost(rows, BLOCK).usd, usd);
    const line = res.stdout.split('\n').filter((l) => l.startsWith('cost per completed block: '));
    assert.equal(line.length, 1);
    assert.equal(line[0].startsWith(`cost per completed block: ${BLOCK} $${usd} (estimated from 4 priced session row(s); facts $`), true, line[0]);
  });

  // 13 sections, as landed (B6's report.mjs); the plan's "14" is a logged plan/code gap — root ruling, B17 fix round 1.
  test('report --slug prints its 13 sections, in order', () => {
    const headers = res.stdout.split('\n').filter((l) => /^== [a-z0-9_]+ ==$/.test(l)).map((l) => l.slice(3, -3));
    assert.deepEqual(headers, [
      'cost_per_block',
      'lane_distribution',
      'escalations',
      's1_calls_per_block',
      's2_rate',
      'review_budget',
      'review_depth_and_findings',
      'fix_rounds',
      'review_unavailable',
      'proof_time',
      'forecast_vs_actual_lines',
      'blocks_stopped_at_l3',
      'run_stop_counts',
    ]);
  });

  test('the harness degrade banner is shown exactly once', () => {
    const banner = degradedBanner('harness').replace('<slug>', SLUG);
    assert.equal(banner.startsWith('code-forge: engine harness — no Solo on this machine.\n'), true);
    assert.equal(res.stdout.split(banner).length - 1, 1);
  });
});

describe('engines and the failure path (B17)', () => {
  test('subprocess is opt-in: the default is harness, --help documents it, --engine subprocess writes it; solo is refused', async () => {
    assert.equal(parseArgs([]).engine, 'harness');
    const help = await firstUse(['--help']);
    assert.equal(help.stdout.includes('subprocess (OPT-IN: never chosen unless given here;'), true);
    const solo = await firstUse(['--engine', 'solo']);
    assert.deepEqual([solo.code, solo.stderr.split('\n')[0]], [2, 'first-use: --engine solo: Solo is manual evidence only — run the code-forge skill under Solo, not this script']);
    const RUN = `fu-b17-sub-${process.pid}`;
    runIds.push(RUN);
    const sub = await firstUse(['--engine', 'subprocess', '--run', RUN, '--tmp-root', TMP_ROOT, '--until', 'init']);
    assert.equal(sub.code, 0, sub.stderr);
    assert.deepEqual([sub.summary.steps, sub.summary.coder], [['copy', 'init'], 'spawn']);
    const text = readFileSync(path.join(TMP_ROOT, 'first-use', RUN, '.code-forge.yml'), 'utf8');
    assert.equal(/^engine: subprocess$/m.test(text), true);
    assert.equal(sub.stdout.includes('code-forge: engine subprocess — no Solo on this machine.\n'), true);
  });

  test('a review that is not approved fails the run at "review" (exit 1): the block is stopped, the run ended, no worker left', async () => {
    const RUN = `fu-b17-fail-${process.pid}`;
    runIds.push(RUN);
    const script = path.join(PARENT, 'reviewer-script.json');
    const major = { id: 'F1', file: 'src/math.mjs', line_start: 28, line_end: 28, severity: 'warning', category: 'correctness', claim: 'fake major', evidence: 'e', fix: 'f' };
    writeFileSync(script, JSON.stringify({ full: { findings: [major] } }));
    const res = await firstUse(['--engine', 'harness', '--coder', 'scripted', '--run', RUN, '--tmp-root', TMP_ROOT], { FAKE_SCRIPT: script });
    assert.equal(res.code, 1, res.stdout);
    assert.equal(res.stderr.split('\n').at(-2), 'first-use: FAILED at review: review of src/math.mjs is not approved (done; 1 finding(s))');
    assert.deepEqual([res.stdout.includes('\nblock stop · exit 0\n'), res.summary.ok, res.summary.run_ended], [true, false, true]);
    const record = await readRun(RUN);
    assert.deepEqual([record.status, record.blocks[BLOCK].status, isAlive(record.worker.pid) && readStartTime(record.worker.pid) === record.worker.started_at], ['ended', 'stopped', false]);
    assert.equal((await runRows(RUN)).filter((r) => r.event === 'review.approved').length, 0);
  });

  /**
   * Start the script with `--coder harness` as a detached child and resolve once it waits for the sentinel.
   * @param {string} runId @param {string[]} extra
   */
  function startWaiting(runId, extra) {
    const env = { PATH: [BIN_DIR, path.dirname(process.execPath), '/usr/bin', '/bin'].join(path.delimiter), HOME: /** @type {string} */ (process.env.HOME), TMPDIR: /** @type {string} */ (process.env.TMPDIR), CODE_FORGE_KEY_BACKEND: 'file' };
    const child = spawn(process.execPath, [SCRIPT, '--engine', 'harness', '--coder', 'harness', '--run', runId, '--tmp-root', TMP_ROOT, ...extra], { cwd: PARENT, env, stdio: ['ignore', 'pipe', 'pipe'] });
    let stdout = '';
    let stderr = '';
    child.stderr.on('data', (d) => (stderr += String(d)));
    // 'close' (not 'exit'): fires only after the child's stdio streams are drained.
    const exited = new Promise((resolve) => child.on('close', (code, signal) => resolve({ code, signal })));
    const waiting = new Promise((resolve) => {
      child.stdout.on('data', (d) => {
        stdout += String(d);
        if (stdout.includes('\nwaiting up to ')) resolve(undefined);
      });
      child.on('exit', () => resolve(undefined));
    });
    return { child, exited, waiting, out: () => stdout, err: () => stderr };
  }

  test('SIGTERM while waiting for the harness coder: exit 1 at "code", the block stopped, the run ended, no worker left', async () => {
    const RUN = `fu-b17-sig-${process.pid}`;
    runIds.push(RUN);
    const w = startWaiting(RUN, ['--coder-timeout', '120']);
    try {
      await w.waiting;
      assert.equal(w.out().includes('\nwaiting up to 120s for ===BLOCK B1 COMPLETE==='), true, w.out());
      w.child.kill('SIGTERM');
      assert.deepEqual(await w.exited, { code: 1, signal: null });
      assert.equal(w.err().split('\n').at(-2), 'first-use: FAILED at code: interrupted by SIGTERM');
      assert.equal(w.out().includes('\nblock stop · exit 0\n'), true);
      const record = await readRun(RUN);
      assert.deepEqual([record.status, record.blocks[BLOCK].status, isAlive(record.worker.pid) && readStartTime(record.worker.pid) === record.worker.started_at], ['ended', 'stopped', false]);
    } finally {
      if (w.child.exitCode === null && w.child.signalCode === null) w.child.kill('SIGKILL');
    }
  });

  test('--coder-timeout bounds the wait for the harness sentinel: 1 s ⇒ exit 1 at "code", the run ended', async () => {
    const RUN = `fu-b17-wait-${process.pid}`;
    runIds.push(RUN);
    const w = startWaiting(RUN, ['--coder-timeout', '1']);
    try {
      assert.deepEqual(await w.exited, { code: 1, signal: null });
      assert.equal(w.err().split('\n').at(-2), 'first-use: FAILED at code: no ===BLOCK B1 COMPLETE=== line within 1s');
      assert.equal((await readRun(RUN)).status, 'ended');
    } finally {
      if (w.child.exitCode === null && w.child.signalCode === null) w.child.kill('SIGKILL');
    }
  });

  test('the sentinel wait polls many times without leaking abort listeners (0 warnings), and an abort ends it with StepFailed', async () => {
    /** @type {Error[]} */
    const warnings = [];
    const onWarning = (/** @type {Error} */ w) => warnings.push(w);
    process.on('warning', onWarning);
    try {
      const logFile = path.join(PARENT, 'wait.log');
      writeFileSync(logFile, 'ACK 00000000 lines=1\n');
      const ac = new AbortController();
      // ~60 polls of 5 ms on ONE signal: the old loop added one listener per poll (warning at 11)
      await assert.rejects(waitForSentinel({ logFile, signal: ac.signal, timeoutMs: 300, pollMs: 5 }), (err) => err instanceof StepFailed && err.message === 'no ===BLOCK B1 COMPLETE=== line within 0.3s');
      const aborted = waitForSentinel({ logFile, signal: ac.signal, timeoutMs: 60000, pollMs: 5 });
      setTimeout(() => ac.abort('interrupted by SIGTERM'), 150);
      await assert.rejects(aborted, (err) => err instanceof StepFailed && err.message === 'interrupted by SIGTERM');
      await new Promise((r) => setImmediate(r)); // warnings are emitted on a later tick
      assert.deepEqual(warnings.map((w) => w.name), []);
    } finally {
      process.off('warning', onWarning);
    }
  });
});
