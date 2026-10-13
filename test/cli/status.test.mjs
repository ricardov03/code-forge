import { commitAll, freshDir, git, makeRepo, writeFile } from '../review/helpers.mjs';
import { humanBlock } from '../helpers/human-confirm.mjs';
import assert from 'node:assert/strict';
import { execFile, spawn } from 'node:child_process';
import { mkdirSync, readFileSync, readdirSync, realpathSync, renameSync, rmdirSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { after, test } from 'node:test';
import { fileURLToPath } from 'node:url';

// B64a (issue #13 item 6): `code-forge status [--run <id>] [--json]` — a short, read-only status of
// a run (at most 10 lines per run, or one JSON line) — and `--json` on every `block` verb. Every
// test runs the real CLI (`bin/code-forge.mjs`) in temp repositories and runs, and pins exact
// stdout, stderr, exit codes and ledger rows.

const { appendRow, readAllRows } = await import('../../src/ledger/write.mjs');
const { runBlock } = await import('../../src/cli/block.mjs');
const { ledgerDir } = await import('../../src/ledger/paths.mjs');
const { runsDir } = await import('../../src/state/paths.mjs');
const { loadKey, signRow } = await import('../../src/state/signer.mjs');
const { contentHash, ticketId } = await import('../../src/worker/ticket.mjs');

const BIN = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..', 'bin', 'code-forge.mjs');
/**
 * A live stand-in for the pinned worker (a sleeping child, so the close's worker pin holds and
 * `run end` may stop it without touching the test process).
 */
const WORKER = spawn(process.execPath, ['-e', 'setTimeout(() => {}, 600000)'], { stdio: 'ignore' });
after(() => WORKER.kill('SIGKILL'));
const PID = String(WORKER.pid);
/** @returns {string} the pid of a fresh stand-in, for a run that `run end` will stop */
function ownWorker() {
  const child = spawn(process.execPath, ['-e', 'setTimeout(() => {}, 600000)'], { stdio: 'ignore' });
  after(() => child.kill('SIGKILL'));
  return String(child.pid);
}
const EXTERNAL = 'review:\n  external: {required: true, name: Outside}\n';
const WORKTREE = 'proof:\n  isolation: worktree\n';
const BUDGET = 'budget:\n  usd: 5\n';
/** A marker that only a transcript and a review diff carry: it must never reach stdout. */
const MARKER = 'ZZ-SECRET-MARKER-7f3a9c';

/** @param {string} slug @param {string} [extra] - YAML appended to the base config. */
const configYaml = (slug, extra = '') =>
  [
    'version: 1',
    'project:',
    `  slug: ${slug}`,
    'provider: anthropic',
    'engine: harness',
    'levels:',
    '  L0: {model: claude-haiku-4-5-20251001}',
    '  L1: {model: claude-sonnet-5}',
    '  L2: {model: claude-opus-5-5}',
    '  L3: {model: claude-fable-5-1}',
    '',
  ].join('\n') + extra;

/**
 * The real CLI.
 * @param {string[]} args @param {string} [cwd]
 * @returns {Promise<{code: number, stdout: string, stderr: string}>}
 */
const cf = (args, cwd = os.tmpdir()) =>
  new Promise((resolve) => {
    execFile(process.execPath, [BIN, ...args], { cwd, env: { ...process.env, CODE_FORGE_NO_ERROR_LOG: '1' } }, (error, stdout, stderr) =>
      // a signal kill (code null) is -1, so a crash never reads as exit 0
      resolve({ code: error ? (typeof error.code === 'number' ? error.code : -1) : 0, stdout, stderr }),
    );
  });

const ACCEPTANCE = freshDir('acceptance');
writeFile(ACCEPTANCE, 'acc.json', JSON.stringify([{ clause: 'c', tests: ['t'] }]));
const ACC = path.join(ACCEPTANCE, 'acc.json');

/**
 * A committed repo with `.code-forge.yml` and `files`, and a run started through the CLI.
 * @param {string} slug @param {string} runId @param {{extra?: string, files?: string[], pid?: string}} [opts]
 */
async function fixture(slug, runId, { extra = EXTERNAL, files = [], pid = PID } = {}) {
  const repo = await makeRepo();
  writeFile(repo, '.code-forge.yml', configYaml(slug, extra));
  for (const f of files) writeFile(repo, f, `export const v = '${f}';\n`);
  writeFile(repo, 'README.md', '# demo\n');
  await commitAll(repo);
  const started = await cf(['run', 'start', '--cwd', repo, '--run', runId, '--worker-pid', pid]);
  assert.equal(started.code, 0, started.stderr);
  /** @param {string} id @param {string[]} owned @param {string[]} [more] */
  const open = (id, owned, more = []) => cf(['block', 'open', id, '--run', runId, '--level', 'L1', '--owned', ...owned, '--acceptance', ACC, ...more], repo);
  /** @param {string[]} args */
  const block = (args) => cf(['block', ...args, '--run', runId], repo);
  /**
   * B80: a human-only verb (waive, reopen) runs in process, for THIS fixture's repo, with the human
   * at a terminal saying yes (asked exactly once); a waive's signed row lands in THIS fixture's ledger.
   * @param {string[]} args
   */
  const human = async (args) => {
    const waivedRows = async () => (await readAllRows(slug)).filter((r) => r.run === runId && r.event === 'review.waived');
    const before = (await waivedRows()).length;
    const res = await humanBlock(repo, [...args, '--run', runId]);
    assert.equal(res.asked.length, 1, res.stderr);
    if (args[0] === 'waive' && res.code === 0) {
      const now = await waivedRows();
      assert.equal(now.length, before + 1);
      const last = now[now.length - 1];
      assert.deepEqual([last.block, last.finding, last.file, last.by], [args[1], args[2], args[args.indexOf('--file') + 1], 'human']);
    }
    return { code: res.code, stdout: res.stdout, stderr: res.stderr };
  };
  /** @param {Record<string, any>} row */
  const row = (row) => appendRow({ run: runId, ...row }, { slug });
  const rows = () => readAllRows(slug);
  const head = async () => (await git(['rev-parse', 'HEAD'], repo)).trim();
  return { repo, runId, slug, open, block, human, row, rows, head };
}

/** @param {{code: number, stdout: string, stderr: string}} res @param {string} stdout */
const ok = (res, stdout) => assert.deepEqual({ code: res.code, stdout: res.stdout }, { code: 0, stdout }, res.stderr);

/**
 * A snapshot of everything status could write for ONE fixture: its slug's ledger files, its run's
 * files (record, key, lock, run dir) and the repository's `.code-forge` — never another test
 * file's state, so parallel test files cannot change it.
 * @param {{repo: string, slug: string, runId: string}} fx
 */
function snapshot({ repo, slug, runId }) {
  /**
   * Every file under `dir` (directories marked `dir`), keeping only the top-level names `keep` takes.
   * @param {string} dir @param {(name: string) => boolean} [keep]
   * @returns {Record<string, string>}
   */
  const walk = (dir, keep = () => true) => {
    /** @type {Record<string, string>} */
    const out = {};
    let entries = [];
    try {
      entries = readdirSync(dir, { withFileTypes: true });
    } catch {
      return out;
    }
    for (const d of entries) {
      if (!keep(d.isDirectory() ? `${d.name}/` : d.name)) continue;
      const full = path.join(dir, d.name);
      if (d.isDirectory()) Object.assign(out, { [full]: 'dir' }, walk(full));
      else out[full] = readFileSync(full, 'utf8');
    }
    return out;
  };
  return {
    ...walk(ledgerDir(), (n) => n === `${slug}.jsonl` || new RegExp(`^${slug}\\.\\d+\\.jsonl$`).test(n)),
    ...walk(runsDir(), (n) => n.startsWith(`${runId}.`) || n === `${runId}/`),
    ...walk(path.join(repo, '.code-forge')),
  };
}

test('B64a (1)(2)(3): status prints the run line, one line per open block (state, level, model, last gate, open findings), the closed/stopped tally — and one JSON line with the same facts', async () => {
  const fx = await fixture('b64a-main', 'r-b64a-main', { extra: `${EXTERNAL}${BUDGET}`, files: ['src/b1.mjs', 'src/b2.mjs', 'src/b3.mjs', 'src/b4.mjs', 'src/b10.mjs'] });
  // B4 closes (its owned file changed, the external reviewer covers the reviews), B3 is stopped
  ok(await fx.open('B4', ['src/b4.mjs']), `block B4 open · L1 · code · attempt 1 · base ${(await fx.head()).slice(0, 12)}\nbrief: none given (--brief <file>)\n`);
  writeFile(fx.repo, 'src/b4.mjs', 'export const v = 4;\n');
  ok(await fx.block(['close', 'B4', '--worker-pid', PID]), 'block B4 closed\n');
  for (const [id, file] of [['B1', 'src/b1.mjs'], ['B2', 'src/b2.mjs'], ['B10', 'src/b10.mjs'], ['B3', 'src/b3.mjs']]) assert.equal((await fx.open(id, [file])).code, 0);
  ok(await fx.block(['stop', 'B3', '--reason', 'not needed']), 'block B3 stopped\n');
  ok(await fx.block(['attempt', 'B10']), 'block B10 attempt 2\n');
  // the ledger: a priced coder session for B1, a red lint gate then a failed gate run for B1, a
  // passing gate for B2, a review leaving F1 and F2 open on B1 (F2 then waived by the human) and
  // an unruled late finding on B2
  await fx.row({ event: 'session', role: 'coder', level: 'L1', provider: 'openai', model: 'gpt-5-codex', block: 'B1', usd: 1.5 });
  await fx.row({ event: 'session', role: 'reviewer', level: 'L2', provider: 'anthropic', model: 'claude-opus-5-5', block: 'B1', usd: 0.25 });
  await fx.row({ event: 'gate.red', block: 'B1', gate: 'lint', code: 1 });
  await fx.row({ event: 'gate.done', block: 'B1', stack: 'node', all_ok: false });
  await fx.row({ event: 'gate.red', block: 'B2', gate: 'test', code: 1 });
  await fx.row({ event: 'gate.done', block: 'B2', stack: 'node', all_ok: false });
  await fx.row({ event: 'gate.done', block: 'B2', stack: 'node', all_ok: true });
  await fx.row({ event: 'review.result', block: 'B1', file: 'src/b1.mjs', content_hash: 'h1', status: 'reviewed', approved: false, open: [{ id: 'F1', severity: 'warning' }, { id: 'F2', severity: 'nit' }] });
  // a later row that is not a review row but names the file at another hash: it never moves the
  // findings count off the last REVIEWED content (F1 stays open)
  await fx.row({ event: 'context.pack', block: 'B1', file: 'src/b1.mjs', content_hash: 'h-not-a-review' });
  await fx.row({ event: 'review.late_finding', block: 'B2', file: 'src/b2.mjs', finding: 'L1', severity: 'warning' });
  ok(await fx.human(['waive', 'B1', 'F2', '--file', 'src/b1.mjs', '--reason', 'accepted']), 'block B1 waived F2 in src/b1.mjs (by: human)\n');

  const text = 'run r-b64a-main · active · plan · spent $1.75 of $5.00\n' + 'B1 open L1 · openai/gpt-5-codex · gate fail (lint) · findings 1\n' + 'B2 open L1 · - · gate pass · findings 1\n' + 'B10 attempt-2 L1 · - · gate none · findings 0\n' + 'closed 1 · stopped 1\n';
  const before = await fx.rows();
  const files = snapshot(fx);
  // the snapshot holds this fixture's ledger and run record (so a write to either would show)
  assert.ok(Object.keys(files).some((f) => f.endsWith(`${fx.slug}.jsonl`)) && Object.keys(files).some((f) => f.endsWith(`${fx.runId}.json`)));
  // with --run, from the repository, and from a subfolder of it
  ok(await cf(['status', '--run', 'r-b64a-main']), text);
  ok(await cf(['status'], fx.repo), text);
  ok(await cf(['status'], path.join(fx.repo, 'src')), text);
  const json = await cf(['status', '--json'], fx.repo);
  assert.equal(json.code, 0);
  assert.equal(json.stdout.split('\n').length, 2, 'exactly one line');
  assert.equal(json.stdout.endsWith('\n'), true);
  assert.deepEqual(JSON.parse(json.stdout), {
    runs: [
      {
        run_id: 'r-b64a-main',
        status: 'active',
        mode: 'plan',
        spent_usd: 1.75,
        unknown_usd_sessions: 0,
        budget_usd: 5,
        blocks: [
          { id: 'B1', state: 'open', level: 'L1', model: 'openai/gpt-5-codex', gate: { result: 'fail', name: 'lint' }, findings: 1 },
          { id: 'B2', state: 'open', level: 'L1', model: null, gate: { result: 'pass', name: null }, findings: 1 },
          { id: 'B10', state: 'attempt-2', level: 'L1', model: null, gate: { result: 'none', name: null }, findings: 0 },
        ],
        more: 0,
        closed: 1,
        stopped: 1,
      },
    ],
  });
  assert.equal((await cf(['status', '--run', 'r-b64a-main', '--json'])).stdout, json.stdout);
  // read-only: no ledger row, no file written or changed
  assert.deepEqual(await fx.rows(), before);
  assert.deepEqual(snapshot(fx), files);
});

test('B64a (2): an unknown session price shows as `+?` after the spend; no budget, no ` of`; a brief-mode run says brief', async () => {
  const repo = await makeRepo();
  writeFile(repo, '.code-forge.yml', configYaml('b64a-brief', EXTERNAL));
  writeFile(repo, 'src/a.mjs', 'export const a = 1;\n');
  await commitAll(repo);
  assert.equal((await cf(['run', 'start', '--cwd', repo, '--run', 'r-b64a-brief', '--worker-pid', PID, '--mode', 'brief'])).code, 0);
  ok(await cf(['status'], repo), 'run r-b64a-brief · active · brief · spent $0.00\n');
  await appendRow({ run: 'r-b64a-brief', event: 'session', role: 'coder', provider: 'openai', model: 'gpt-5-codex', usd: 0.5 }, { slug: 'b64a-brief' });
  await appendRow({ run: 'r-b64a-brief', event: 'session', role: 'coder', provider: 'openai', model: 'gpt-5-codex', usd_unknown: true }, { slug: 'b64a-brief' });
  ok(await cf(['status'], repo), 'run r-b64a-brief · active · brief · spent $0.50+?\n');
  /** @param {Record<string, unknown>} money */
  const run = (money) => ({ runs: [{ run_id: 'r-b64a-brief', status: 'active', mode: 'brief', ...money, budget_usd: null, blocks: [], more: 0, closed: 0, stopped: 0 }] });
  ok(await cf(['status', '--json'], repo), `${JSON.stringify(run({ spent_usd: 0.5, unknown_usd_sessions: 1 }))}\n`);
  assert.deepEqual(JSON.parse((await cf(['status', '--json'], repo)).stdout), run({ spent_usd: 0.5, unknown_usd_sessions: 1 }));
  // the ledger cannot be read (a rotated ledger file that is a directory): the amount is `$?` and
  // both spend numbers are null — never a made-up $0.00
  const broken = path.join(ledgerDir(), 'b64a-brief.1.jsonl');
  mkdirSync(broken);
  try {
    ok(await cf(['status'], repo), 'run r-b64a-brief · active · brief · spent $?\n');
    assert.deepEqual(JSON.parse((await cf(['status', '--json'], repo)).stdout), run({ spent_usd: null, unknown_usd_sessions: null }));
  } finally {
    rmdirSync(broken);
  }
});

test('B64a (2)(3): every session without a known price: `$0.00+?` with a budget, and unknown_usd_sessions counts them', async () => {
  const fx = await fixture('b64a-unpriced', 'r-b64a-unpriced', { extra: `${EXTERNAL}${BUDGET}` });
  await fx.row({ event: 'session', role: 'coder', provider: 'openai', model: 'gpt-5-codex', usd_unknown: true });
  await fx.row({ event: 'session', role: 'reviewer', provider: 'anthropic', model: 'claude-opus-5-5', usd_unknown: true });
  ok(await cf(['status'], fx.repo), 'run r-b64a-unpriced · active · plan · spent $0.00+? of $5.00\n');
  assert.deepEqual(JSON.parse((await cf(['status', '--json'], fx.repo)).stdout), {
    runs: [{ run_id: 'r-b64a-unpriced', status: 'active', mode: 'plan', spent_usd: 0, unknown_usd_sessions: 2, budget_usd: 5, blocks: [], more: 0, closed: 0, stopped: 0 }],
  });
});

test('B64a (1): status never waits on the run lock — with the lock held (as a worker or a block verb holds it) it prints at once', async () => {
  const fx = await fixture('b64a-locked', 'r-b64a-locked', { files: ['src/a.mjs'] });
  assert.equal((await fx.open('B1', ['src/a.mjs'])).code, 0);
  const lock = path.join(runsDir(), `${fx.runId}.lock`);
  mkdirSync(lock); // the run lock is this directory (`withRunLock`)
  try {
    // a verb waiting on the lock gives up after 5 s (`locked`): each status call, timed on its own,
    // finishes before that
    /** @type {Array<{args: string[], cwd?: string}>} */
    const calls = [{ args: ['status'], cwd: fx.repo }, { args: ['status', '--run', fx.runId] }];
    for (const { args, cwd } of calls) {
      const started = Date.now();
      ok(await cf(args, cwd), 'run r-b64a-locked · active · plan · spent $0.00\nB1 open L1 · - · gate none · findings 0\n');
      assert.ok(Date.now() - started < 4500, `no wait on the lock: ${args.join(' ')}`);
    }
    // a block verb, by contrast, cannot get the lock while it is held
    const blocked = await fx.block(['attempt', 'B1']);
    assert.deepEqual({ code: blocked.code, stdout: blocked.stdout }, { code: 1, stdout: '' });
    assert.match(blocked.stderr, /^block attempt: run r-b64a-locked is locked /);
  } finally {
    rmdirSync(lock);
  }
});

test('B64a (2)(3): the 10-line cap — the last block line is `… +k more blocks`, the closed/stopped tally keeps its line; JSON lists every open block with more 0', async () => {
  const files = Array.from({ length: 12 }, (_, i) => `src/f${i + 1}.mjs`);
  const fx = await fixture('b64a-cap', 'r-b64a-cap', { files });
  // 9 open blocks: header + 9 = exactly 10 lines, nothing hidden
  for (let i = 1; i <= 9; i += 1) assert.equal((await fx.open(`B${i}`, [`src/f${i}.mjs`])).code, 0);
  const line = (/** @type {number} */ i) => `B${i} open L1 · - · gate none · findings 0`;
  const header = 'run r-b64a-cap · active · plan · spent $0.00';
  ok(await cf(['status'], fx.repo), `${[header, ...[1, 2, 3, 4, 5, 6, 7, 8, 9].map(line)].join('\n')}\n`);
  // 10 open blocks: 8 shown, the 10th line says the rest
  assert.equal((await fx.open('B10', ['src/f10.mjs'])).code, 0);
  const capped = (await cf(['status'], fx.repo)).stdout;
  assert.equal(capped, `${[header, ...[1, 2, 3, 4, 5, 6, 7, 8].map(line), '… +2 more blocks'].join('\n')}\n`);
  assert.equal(capped.split('\n').length - 1, 10);
  // 11 opened, one stopped: 10 open + the tally — 7 shown, `… +3 more blocks`, then the tally
  assert.equal((await fx.open('B11', ['src/f11.mjs'])).code, 0);
  ok(await fx.block(['stop', 'B11', '--reason', 'dropped']), 'block B11 stopped\n');
  const tallied = (await cf(['status'], fx.repo)).stdout;
  assert.equal(tallied, `${[header, ...[1, 2, 3, 4, 5, 6, 7].map(line), '… +3 more blocks', 'closed 0 · stopped 1'].join('\n')}\n`);
  assert.equal(tallied.split('\n').length - 1, 10);
  const json = JSON.parse((await cf(['status', '--json'], fx.repo)).stdout);
  assert.deepEqual(
    json.runs[0].blocks.map((/** @type {{id: string}} */ b) => b.id),
    ['B1', 'B2', 'B3', 'B4', 'B5', 'B6', 'B7', 'B8', 'B9', 'B10'],
  );
  assert.deepEqual([json.runs[0].more, json.runs[0].closed, json.runs[0].stopped], [0, 0, 1]);
});

test('B64a (1): no run, an ended run, another repository\'s run: `no active run` (exit 0); two runs: newest first, one blank line between them', async () => {
  const pid = ownWorker();
  const fx = await fixture('b64a-many', 'r-b64a-many-a', { pid });
  const other = await fixture('b64a-other', 'r-b64a-other');
  const lone = await makeRepo();
  ok(await cf(['status'], lone), 'no active run\n');
  ok(await cf(['status', '--json'], lone), '{"runs":[]}\n');
  ok(await cf(['status'], other.repo), 'run r-b64a-other · active · plan · spent $0.00\n');
  // a second run of the same repository, started later
  assert.equal((await cf(['run', 'start', '--cwd', fx.repo, '--run', 'r-b64a-many-b', '--worker-pid', ownWorker()])).code, 0);
  ok(await cf(['status'], fx.repo), 'run r-b64a-many-b · active · plan · spent $0.00\n\nrun r-b64a-many-a · active · plan · spent $0.00\n');
  assert.deepEqual(
    JSON.parse((await cf(['status', '--json'], fx.repo)).stdout).runs.map((/** @type {{run_id: string}} */ r) => r.run_id),
    ['r-b64a-many-b', 'r-b64a-many-a'],
  );
  // `run end` stops the pinned worker: these runs are pinned to their own stand-ins
  ok(await cf(['run', 'end', '--run', 'r-b64a-many-b']), 'run r-b64a-many-b ended\n');
  ok(await cf(['run', 'end', '--run', 'r-b64a-many-a']), 'run r-b64a-many-a ended\n');
  ok(await cf(['status'], fx.repo), 'no active run\n');
  // --run still shows an ended run
  ok(await cf(['status', '--run', 'r-b64a-many-a']), 'run r-b64a-many-a · ended · plan · spent $0.00\n');
});

test('B64a (1): an unknown --run exits 1 with a clear message; a usage error exits 2; neither prints on stdout', async () => {
  assert.deepEqual(await cf(['status', '--run', 'r-b64a-nope']), { code: 1, stdout: '', stderr: 'status: no run record for r-b64a-nope\n' });
  const usage = 'usage: code-forge status [--run <id>] [--json]\n';
  assert.deepEqual(await cf(['status', '--bogus']), { code: 2, stdout: '', stderr: `status: unknown flag "--bogus"\n${usage}` });
  assert.deepEqual(await cf(['status', 'extra']), { code: 2, stdout: '', stderr: `status: unexpected argument "extra"\n${usage}` });
  assert.deepEqual(await cf(['status', '--run']), { code: 2, stdout: '', stderr: `status: --run needs a value\n${usage}` });
  assert.deepEqual(await cf(['status', '--json=1']), { code: 2, stdout: '', stderr: `status: --json takes no value\n${usage}` });
  // an unknown run under --json: the same message, still nothing on stdout
  assert.deepEqual(await cf(['status', '--run', 'r-b64a-nope', '--json']), { code: 1, stdout: '', stderr: 'status: no run record for r-b64a-nope\n' });
  // the verb is listed by help, and its usage line is exactly this
  const help = await cf(['help']);
  assert.equal(help.code, 0);
  assert.equal(help.stdout.split('\n').filter((l) => l === '  status').length, 1);
  // the block usage names --json in exactly one line
  const blockUsage = await cf(['block']);
  assert.equal(blockUsage.code, 2);
  assert.deepEqual(
    blockUsage.stderr.split('\n').filter((l) => l.includes('--json')),
    ['       every block verb takes --json: one JSON line on stdout ({"block","event",…}) instead of the text'],
  );
});

test('B64a (5): neither status nor block --json ever prints transcript or diff text', async () => {
  const fx = await fixture('b64a-marker', 'r-b64a-marker', { files: ['src/m.mjs', 'src/n.mjs', 'src/o.mjs'] });
  const opened = [await fx.open('B1', ['src/m.mjs'], ['--json']), await fx.open('B2', ['src/n.mjs'], ['--json'])];
  for (const res of opened) assert.equal(res.code, 0, res.stderr);
  // the coder transcript at its default place, a review diff and a review result in the worker's
  // directory, and ledger rows that carry the same marker in free text
  writeFile(fx.repo, `.code-forge/runs/${fx.runId}/B1.log`, `$ node --test\n${MARKER}\n`);
  writeFile(fx.repo, '.code-forge/reviews/t-1.diff', `--- a/src/m.mjs\n+++ b/src/m.mjs\n+${MARKER}\n`);
  writeFile(fx.repo, '.code-forge/reviews/t-1.json', JSON.stringify({ summary: MARKER }));
  const outside = freshDir('transcripts');
  writeFile(outside, 'coder.log', `${MARKER}\n`);
  await fx.row({ event: 'review.result', block: 'B2', file: 'src/n.mjs', content_hash: 'h1', status: 'reviewed', approved: false, open: [{ id: 'F1', severity: 'warning', text: MARKER }], summary: MARKER, diff: MARKER });
  await fx.row({ event: 'session', role: 'coder', provider: 'openai', model: 'gpt-5-codex', block: 'B2', usd: 0.1, transcript: MARKER });
  await fx.row({ event: 'gate.red', block: 'B2', gate: 'test', code: 1, output: MARKER });
  writeFile(fx.repo, 'src/m.mjs', `export const m = '${MARKER}';\n`);
  const outputs = [
    await cf(['status'], fx.repo),
    await cf(['status', '--json'], fx.repo),
    await cf(['status', '--run', fx.runId, '--json']),
    await fx.block(['claim', 'B1', 'src/o.mjs', '--json']),
    await fx.block(['close', 'B1', '--worker-pid', PID, '--transcript', path.join(outside, 'coder.log'), '--json']),
  ];
  for (const res of outputs) {
    assert.equal(res.code, 0, res.stderr);
    assert.equal(res.stdout.includes(MARKER), false, res.stdout);
  }
  for (const res of opened) assert.equal(res.stdout.includes(MARKER), false, res.stdout);
  assert.equal(outputs[0].stdout, 'run r-b64a-marker · active · plan · spent $0.10\nB1 open L1 · - · gate none · findings 0\nB2 open L1 · openai/gpt-5-codex · gate fail (test) · findings 1\n');
  assert.equal(outputs[4].stdout, `${JSON.stringify({ block: 'B1', event: 'closed', status: 'closed', reason: null, close_event: null })}\n`);
});

/**
 * Every block verb, run once with `--json` and once without, on twin fixtures: the text output is
 * pinned exactly (byte-identical to what the verb printed before B64a) and the JSON output is one
 * line whose object is pinned with deepEqual.
 */
test('B64a (4): every block verb prints one JSON line with --json, and exactly its old text without it', async () => {
  /** @param {string} tag */
  const twin = async (tag) => {
    const fx = await fixture(`b64a-verbs-${tag}`, `r-b64a-verbs-${tag}`, { files: ['src/a.mjs', 'src/b.mjs', 'src/c.mjs'] });
    return { fx, base: await fx.head() };
  };
  const t = await twin('text');
  const j = await twin('json');
  /** @param {{code: number, stdout: string, stderr: string}} res @param {Record<string, unknown>} want */
  const one = (res, want) => {
    assert.equal(res.code, 0, res.stderr);
    assert.equal(res.stdout.endsWith('\n') && res.stdout.indexOf('\n') === res.stdout.length - 1, true, `one line: ${JSON.stringify(res.stdout)}`);
    assert.deepEqual(JSON.parse(res.stdout), want);
  };
  // open
  ok(await t.fx.open('B1', ['src/a.mjs']), `block B1 open · L1 · code · attempt 1 · base ${t.base.slice(0, 12)}\nbrief: none given (--brief <file>)\n`);
  one(await j.fx.open('B1', ['src/a.mjs'], ['--json']), { block: 'B1', event: 'open', level: 'L1', asked: 'L1', source: 'flag', kind: 'code', attempt: 1, base_sha: j.base, tests: 0, worktree: null, brief: null });
  // open with a brief and a kind: the pointer and the kind are facts of the line
  for (const fx of [t.fx, j.fx]) {
    writeFile(fx.repo, 'brief.md', '# do the thing\n');
    await commitAll(fx.repo);
  }
  const [tBrief, jBrief] = [await t.fx.head(), await j.fx.head()];
  const tOpen2 = await t.fx.open('B2', ['src/b.mjs'], ['--kind', 'contract', '--brief', path.join(t.fx.repo, 'brief.md')]);
  assert.equal(tOpen2.code, 0, tOpen2.stderr);
  const [first, pointer] = tOpen2.stdout.split('\n');
  assert.equal(first, `block B2 open · L1 · contract · attempt 1 · base ${tBrief.slice(0, 12)}`);
  assert.match(pointer, /^BRIEF brief\.md lines=1 sha=[0-9a-f]{8} <<<EOM>>>$/);
  const jOpen2 = await j.fx.open('B2', ['src/b.mjs'], ['--kind', 'contract', '--brief', path.join(j.fx.repo, 'brief.md'), '--json']);
  one(jOpen2, { block: 'B2', event: 'open', level: 'L1', asked: 'L1', source: 'flag', kind: 'contract', attempt: 1, base_sha: jBrief, tests: 0, worktree: null, brief: pointer });
  // attempt
  ok(await t.fx.block(['attempt', 'B1']), 'block B1 attempt 2\n');
  one(await j.fx.block(['attempt', 'B1', '--json']), { block: 'B1', event: 'attempt', attempt: 2 });
  // rebase onto a new unrelated commit
  for (const fx of [t.fx, j.fx]) {
    writeFile(fx.repo, 'docs/note.md', 'note\n');
    await git(['add', 'docs/note.md'], fx.repo);
    await git(['commit', '-q', '-m', 'note'], fx.repo);
  }
  const tHead = await t.fx.head();
  const jHead = await j.fx.head();
  ok(await t.fx.block(['rebase', 'B1']), `block B1 rebased ${t.base.slice(0, 12)} → ${tHead.slice(0, 12)}\n`);
  one(await j.fx.block(['rebase', 'B1', '--json']), { block: 'B1', event: 'rebased', old_base: j.base, new_base: jHead });
  // claim
  ok(await t.fx.block(['claim', 'B1', 'src/c.mjs']), 'block B1 claimed src/c.mjs\n');
  one(await j.fx.block(['claim', 'B1', 'src/c.mjs', '--json']), { block: 'B1', event: 'claimed', path: 'src/c.mjs' });
  // waive (on B2, which is stopped next: its review rows never reach B1's close)
  for (const fx of [t.fx, j.fx]) await fx.row({ event: 'review.result', block: 'B2', file: 'src/b.mjs', content_hash: 'h1', status: 'reviewed', approved: false, open: [{ id: 'F1', severity: 'warning' }] });
  ok(await t.fx.human(['waive', 'B2', 'F1', '--file', 'src/b.mjs', '--reason', 'fine']), 'block B2 waived F1 in src/b.mjs (by: human)\n');
  one(await j.fx.human(['waive', 'B2', 'F1', '--file', 'src/b.mjs', '--reason', 'fine', '--json']), { block: 'B2', event: 'waived', finding: 'F1', file: 'src/b.mjs', by: 'human' });
  // stop
  ok(await t.fx.block(['stop', 'B2', '--reason', 'later']), 'block B2 stopped\n');
  one(await j.fx.block(['stop', 'B2', '--reason', 'later', '--json']), { block: 'B2', event: 'stopped' });
  // close (the external reviewer covers the reviews; the owned file changed)
  for (const fx of [t.fx, j.fx]) writeFile(fx.repo, 'src/a.mjs', 'export const a = 2;\n');
  ok(await t.fx.block(['close', 'B1', '--worker-pid', PID]), 'block B1 closed\n');
  one(await j.fx.block(['close', 'B1', '--worker-pid', PID, '--json']), { block: 'B1', event: 'closed', status: 'closed', reason: null, close_event: null });
  // the same rows were written either way: --json changes the output only
  // (the full rows, with only what differs between two runs taken out: times, the MAC, and the
  // run id, slug, repository path and commit ids put back as placeholders)
  /**
   * @param {Array<Record<string, any>>} rows
   * @param {{fx: {repo: string, runId: string, slug: string}, shas: string[]}} twinOf
   */
  const strip = (rows, { fx, shas }) => {
    const swaps = [
      [fx.repo, '<repo>'],
      [fx.runId, '<run>'],
      [fx.slug, '<slug>'],
      ...shas.flatMap((sha, i) => [
        [sha, `<sha${i}>`],
        [sha.slice(0, 12), `<sha${i}>`],
      ]),
    ];
    return rows.map((r) => {
      const { ts, mac, at, started_at, ...rest } = r;
      let text = JSON.stringify(rest);
      for (const [from, to] of swaps) text = text.split(from).join(to);
      return JSON.parse(text);
    });
  };
  assert.deepEqual(
    strip(await j.fx.rows(), { fx: j.fx, shas: [j.base, jBrief, jHead] }),
    strip(await t.fx.rows(), { fx: t.fx, shas: [t.base, tBrief, tHead] }),
  );
});

test('B64a (4): a refused close keeps its stderr and exit 1 and prints {"block","ok":false,"reason"} under --json; a thrown refusal and a usage error print nothing on stdout', async () => {
  const fx = await fixture('b64a-refused', 'r-b64a-refused', { extra: '', files: ['src/a.mjs'] });
  assert.equal((await fx.open('B1', ['src/a.mjs'])).code, 0);
  writeFile(fx.repo, 'src/a.mjs', 'export const a = 2;\n');
  const text = await fx.block(['close', 'B1', '--worker-pid', PID]);
  assert.equal(text.code, 1);
  assert.equal(text.stdout, '');
  const json = await fx.block(['close', 'B1', '--worker-pid', PID, '--json']);
  assert.deepEqual({ code: json.code, stderr: json.stderr }, { code: 1, stderr: text.stderr });
  const reason = text.stderr.split('\n').find((l) => l.startsWith('block B1 open: '))?.slice('block B1 open: '.length);
  assert.equal(reason, 'unreviewed src/a.mjs'); // pinned: no reviewer is configured and nothing reviewed the changed file
  assert.equal(json.stdout, `${JSON.stringify({ block: 'B1', ok: false, reason })}\n`);
  // a block that does not exist: the thrown refusal is unchanged and stdout stays empty
  const missing = await fx.block(['close', 'B9', '--json']);
  assert.deepEqual(missing, { code: 1, stdout: '', stderr: 'block close: block B9 is not in run r-b64a-refused\n' });
  const usage = await fx.block(['stop', 'B1', '--json']);
  assert.deepEqual(usage, { code: 2, stdout: '', stderr: 'block stop: block stop needs --reason\n' });
});

test('B64a (4): block open and close --remove-worktree under worktree isolation: the tree is a fact of the JSON line; without --json the text is unchanged', async () => {
  const t = await fixture('b64a-wt-text', 'r-b64a-wt-text', { extra: `${WORKTREE}${EXTERNAL}`, files: ['src/a.mjs'] });
  const j = await fixture('b64a-wt-json', 'r-b64a-wt-json', { extra: `${WORKTREE}${EXTERNAL}`, files: ['src/a.mjs'] });
  const tBase = await t.head();
  const jBase = await j.head();
  ok(await t.open('B1', ['src/a.mjs']), `block B1 open · L1 · code · attempt 1 · base ${tBase.slice(0, 12)}\nworktree ${t.repo}-worktrees/B1 · branch forge/r-b64a-wt-text/B1\nbrief: none given (--brief <file>)\n`);
  const opened = await j.open('B1', ['src/a.mjs'], ['--json']);
  assert.equal(opened.code, 0, opened.stderr);
  assert.deepEqual(JSON.parse(opened.stdout), { block: 'B1', event: 'open', level: 'L1', asked: 'L1', source: 'flag', kind: 'code', attempt: 1, base_sha: jBase, tests: 0, worktree: { path: `${j.repo}-worktrees/B1`, branch: 'forge/r-b64a-wt-json/B1', reused: false }, brief: null });
  // the work, committed on the block's branch in its tree (so the tree is clean and can be removed)
  for (const fx of [t, j]) {
    const tree = `${fx.repo}-worktrees/B1`;
    writeFile(tree, 'src/a.mjs', 'export const a = 2;\n');
    await git(['add', 'src/a.mjs'], tree);
    await git(['commit', '-q', '-m', 'work'], tree);
  }
  ok(await t.block(['close', 'B1', '--worker-pid', PID, '--remove-worktree']), `block B1 closed\nblock B1 worktree removed: ${t.repo}-worktrees/B1 (branch forge/r-b64a-wt-text/B1 kept)\n`);
  ok(
    await j.block(['close', 'B1', '--worker-pid', PID, '--remove-worktree', '--json']),
    `${JSON.stringify({ block: 'B1', event: 'closed', status: 'closed', reason: null, close_event: null, worktree: { removed: true, path: `${j.repo}-worktrees/B1`, branch: 'forge/r-b64a-wt-json/B1' } })}\n`,
  );
});

test('B64a (4): runBlock in process: --json is a boolean flag (a value is a usage error, exit 2, nothing on stdout)', async () => {
  let stdout = '';
  let stderr = '';
  const code = await runBlock(['stop', 'B1', '--run', 'r-b64a-none', '--reason', 'x', '--json=yes'], { stdout: { write: (s) => ((stdout += s), true) }, stderr: { write: (s) => ((stderr += s), true) } });
  assert.deepEqual({ code, stdout, stderr }, { code: 2, stdout: '', stderr: 'block stop: --json takes no value\n' });
});

/** @param {{code: number, stdout: string, stderr: string}} res @returns {Record<string, unknown>} the ONE JSON line of stdout */
const oneLine = (res) => {
  assert.equal(res.stdout.endsWith('\n') && res.stdout.indexOf('\n') === res.stdout.length - 1, true, `one line: ${JSON.stringify(res.stdout)}`);
  return JSON.parse(res.stdout);
};

test('B64a (4): close --remove-worktree --json on a block with no worktree: the usage refusal, exit 2, nothing on stdout, the block stays open', async () => {
  const fx = await fixture('b64a-nowt', 'r-b64a-nowt', { files: ['src/a.mjs'] });
  assert.equal((await fx.open('B1', ['src/a.mjs'])).code, 0);
  writeFile(fx.repo, 'src/a.mjs', 'export const a = 2;\n');
  const before = await fx.rows();
  const stderr = 'block close: block B1 has no worktree (proof.isolation is not worktree); --remove-worktree does not apply\n';
  assert.deepEqual(await fx.block(['close', 'B1', '--worker-pid', PID, '--remove-worktree', '--json']), { code: 2, stdout: '', stderr });
  assert.deepEqual(await fx.block(['close', 'B1', '--worker-pid', PID, '--remove-worktree']), { code: 2, stdout: '', stderr });
  assert.deepEqual(await fx.rows(), before);
  ok(await cf(['status', '--run', fx.runId]), 'run r-b64a-nowt · active · plan · spent $0.00\nB1 open L1 · - · gate none · findings 0\n');
});

test('B64a (4): a --json close through the review gate (a signed approval of the changed file, no external reviewer) prints exactly one JSON line', async () => {
  const fx = await fixture('b64a-gated', 'r-b64a-gated', { extra: '', files: ['src/a.mjs'] });
  assert.equal((await fx.open('B1', ['src/a.mjs'])).code, 0);
  writeFile(fx.repo, 'src/a.mjs', 'export const a = 2;\n');
  const key = await loadKey(fx.runId);
  const hash = contentHash(realpathSync(fx.repo), 'src/a.mjs');
  await appendRow(signRow({ run: fx.runId, event: 'review.approved', block: 'B1', file: 'src/a.mjs', content_hash: hash }, key), { slug: fx.slug });
  const res = await fx.block(['close', 'B1', '--worker-pid', PID, '--json']);
  assert.equal(res.code, 0, res.stderr);
  // B96: the gated close names the reviewed file and its queue ticket
  const ticket = ticketId({ run: fx.runId, block: 'B1', file: 'src/a.mjs', content_hash: hash });
  assert.deepEqual(oneLine(res), { block: 'B1', event: 'closed', status: 'closed', reason: null, close_event: null, reviewed: [{ file: 'src/a.mjs', ticket, via: 'approved' }] });
  // the gate ran: its warning (no transcript) is on stderr, never on stdout
  assert.match(res.stderr, /^WARN block B1: no coder transcript found /);
  assert.ok((await fx.rows()).some((r) => r.event === 'block.close' && r.block === 'B1'));
});

test('B64a (4): --no-require-reviews --json in process: the confirmation is asked through the ui, stdout is exactly one JSON line; cancelled, stdout stays empty', async () => {
  const fx = await fixture('b64a-waived', 'r-b64a-waived', { extra: '', files: ['src/a.mjs', 'src/b.mjs'] });
  assert.equal((await fx.open('B1', ['src/a.mjs'])).code, 0);
  assert.equal((await fx.open('B2', ['src/b.mjs'])).code, 0);
  writeFile(fx.repo, 'src/a.mjs', 'export const a = 2;\n');
  writeFile(fx.repo, 'src/b.mjs', 'export const b = 2;\n');
  /** @param {string} id @param {boolean} answer */
  const close = async (id, answer) => {
    let stdout = '';
    let stderr = '';
    const asked = [];
    const code = await runBlock(['close', id, '--run', fx.runId, '--worker-pid', PID, '--no-require-reviews', '--json'], {
      stdout: { write: (s) => ((stdout += s), true) },
      stderr: { write: (s) => ((stderr += s), true) },
      isTTY: true,
      ui: { confirm: async (o) => (asked.push(o.message), answer), isCancel: () => false },
    });
    return { code, stdout, stderr, asked };
  };
  const cancelled = await close('B2', false);
  assert.deepEqual({ code: cancelled.code, stdout: cancelled.stdout, stderr: cancelled.stderr, asked: cancelled.asked.length }, { code: 1, stdout: '', stderr: 'block B2 close: cancelled — the review checks stay on\n', asked: 1 });
  const closed = await close('B1', true);
  assert.equal(closed.code, 0, closed.stderr);
  assert.equal(closed.asked.length, 1);
  assert.deepEqual(oneLine(closed), { block: 'B1', event: 'closed', status: 'closed', reason: null, close_event: null });
  assert.ok((await fx.rows()).some((r) => r.event === 'gate.reviews_waived' && r.block === 'B1' && r.by === 'human'));
});

test('B64a (1): status on a run whose key file is missing creates no key and changes nothing in the runs directory', async () => {
  const fx = await fixture('b64a-nokey', 'r-b64a-nokey', { files: ['src/a.mjs'] });
  assert.equal((await fx.open('B1', ['src/a.mjs'])).code, 0);
  await fx.row({ event: 'review.result', block: 'B1', file: 'src/a.mjs', content_hash: 'h1', status: 'reviewed', approved: false, open: [{ id: 'F1', severity: 'warning' }] });
  const keyFile = path.join(runsDir(), `${fx.runId}.key`);
  const aside = path.join(freshDir('key-aside'), 'key');
  renameSync(keyFile, aside);
  try {
    const listing = () => readdirSync(runsDir()).filter((n) => n.startsWith(`${fx.runId}.`) || n === fx.runId).sort();
    const names = listing();
    assert.equal(names.includes(`${fx.runId}.key`), false);
    const files = snapshot(fx);
    ok(await cf(['status', '--run', fx.runId]), 'run r-b64a-nokey · active · plan · spent $0.00\nB1 open L1 · - · gate none · findings 1\n');
    assert.equal(JSON.parse((await cf(['status', '--json'], fx.repo)).stdout).runs[0].blocks[0].findings, 1);
    assert.deepEqual(listing(), names);
    assert.deepEqual(snapshot(fx), files);
  } finally {
    renameSync(aside, keyFile);
  }
});

test('B64a (2)(3): a review row with no content_hash leaves its open finding counted (the gate counts a hashless refusal for every hash)', async () => {
  const fx = await fixture('b64a-hashless', 'r-b64a-hashless', { files: ['src/a.mjs'] });
  assert.equal((await fx.open('B1', ['src/a.mjs'])).code, 0);
  await fx.row({ event: 'review.result', block: 'B1', file: 'src/a.mjs', status: 'reviewed', approved: false, open: [{ id: 'F1', severity: 'warning' }] });
  ok(await cf(['status', '--run', fx.runId]), 'run r-b64a-hashless · active · plan · spent $0.00\nB1 open L1 · - · gate none · findings 1\n');
});

test('B64a (2)(3): the model is the provider/model of the latest coder session row that names one — a real spawn-shaped row; a later `ledger add coder` row (no model) does not hide it', async () => {
  const fx = await fixture('b64a-model', 'r-b64a-model', { files: ['src/a.mjs'] });
  assert.equal((await fx.open('B1', ['src/a.mjs'])).code, 0);
  // the shape `src/session/spawn.mjs` writes for a coder session
  await fx.row({ event: 'session', role: 'coder', level: 'L1', provider: 'openai', model: 'gpt-5-codex', effort: 'high', fallback_step: 0, block: 'B1', status: 'ok', tokens_in: 1200, tokens_out: 300, tokens_source: 'reported', usd: 0.4, duration_ms: 1000 });
  // then manual spend through the real CLI: a signed coder session row with no model
  const added = await cf(['ledger', 'add', 'coder', '--run', fx.runId, '--block', 'B1', '--usd', '0.6'], fx.repo);
  assert.equal(added.code, 0, added.stderr);
  const last = (await fx.rows()).at(-1);
  assert.deepEqual([last?.event, last?.role, last?.manual, last?.model], ['session', 'coder', true, undefined]);
  ok(await cf(['status', '--run', fx.runId]), 'run r-b64a-model · active · plan · spent $1.00\nB1 open L1 · openai/gpt-5-codex · gate none · findings 0\n');
  assert.equal(JSON.parse((await cf(['status', '--run', fx.runId, '--json'])).stdout).runs[0].blocks[0].model, 'openai/gpt-5-codex');
});
