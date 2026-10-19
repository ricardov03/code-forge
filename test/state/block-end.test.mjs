import { commitAll, freshDir, git, makeRepo, writeFile } from '../review/helpers.mjs';
import { humanBlock } from '../helpers/human-confirm.mjs';
import assert from 'node:assert/strict';
import { execFile, spawn } from 'node:child_process';
import { existsSync, readFileSync, realpathSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { after, test } from 'node:test';
import { fileURLToPath } from 'node:url';

// B64b (issue #13): the block-end signal — `<runs>/<run>/blocks/<id>.done.json` after every block
// end, and the owner's `hooks.on_block_end` run once per end outside the run lock with one signed
// `hook.ran` row. Every CLI test runs the real CLI (`bin/code-forge.mjs`) in temp repositories
// (HOME is under the per-file temp parent of `../review/helpers.mjs`, removed in `after()`).

const { appendRow, readAllRows } = await import('../../src/ledger/write.mjs');
const { runsDir } = await import('../../src/state/paths.mjs');
const { loadKey, verifyRow } = await import('../../src/state/signer.mjs');
const { doneFileContent, doneFilePath, blockEndHook, hookWarning, runBlockEndHook, writeDoneFile } = await import('../../src/state/block-end.mjs');
const { checkTranscript } = await import('../../src/gates/transcript-grep.mjs');
const { initialConfigState } = await import('../../src/state/config-snapshot.mjs');
const { withRunLock } = await import('../../src/state/run.mjs');
const { runBlock } = await import('../../src/cli/block.mjs');
const { FORBIDDEN, hookForbidden, mergeForbidden, scanTranscript } = await import('../../src/util/forbidden.mjs');

const BIN = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..', 'bin', 'code-forge.mjs');

/** Sleeping stand-ins for the pinned worker; every one is SIGKILLed in `after()`. */
const children = [];
after(() => {
  for (const child of children) child.kill('SIGKILL');
});
/** @returns {string} the pid of a fresh sleeping child */
function sleeper() {
  const child = spawn(process.execPath, ['-e', 'setTimeout(() => {}, 600000)'], { stdio: 'ignore' });
  children.push(child);
  return String(child.pid);
}
const PID = sleeper();
const EXTERNAL = 'review:\n  external: {required: true, name: Outside}\n';
const ISO = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$/;

/** @param {string} slug @param {string} [extra] */
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
      resolve({ code: error ? (typeof error.code === 'number' ? error.code : -1) : 0, stdout, stderr }),
    );
  });

const ACCEPTANCE = freshDir('acceptance');
writeFile(ACCEPTANCE, 'acc.json', JSON.stringify([{ clause: 'c', tests: ['t'] }]));
const ACC = path.join(ACCEPTANCE, 'acc.json');
/** A clean coder transcript, so a close prints no "no coder transcript" WARN. */
writeFile(ACCEPTANCE, 'clean.log', '$ npm test\nok\n');
const CLEAN = ['--transcript', path.join(ACCEPTANCE, 'clean.log')];

/**
 * The hook used by the tests: it appends one JSON line per run to `out` — its four CODE_FORGE_*
 * vars, its cwd, whether stdin was closed, and whether it could take the run lock (`lock`, which
 * it releases again) — then exits with `code`.
 */
const HOOK_DIR = freshDir('hook');
const HOOK = path.join(HOOK_DIR, 'hook.mjs');
writeFileSync(
  HOOK,
  [
    "import { appendFileSync, mkdirSync, readSync, rmdirSync } from 'node:fs';",
    'const [out, lock, code] = process.argv.slice(2);',
    'let locked = false;',
    'try { mkdirSync(lock); locked = true; rmdirSync(lock); } catch {}',
    'let stdinClosed;',
    'try { stdinClosed = readSync(0, Buffer.alloc(1)) === 0; } catch { stdinClosed = true; }',
    'const env = Object.fromEntries(["CODE_FORGE_RUN", "CODE_FORGE_BLOCK", "CODE_FORGE_BLOCK_STATUS", "CODE_FORGE_DONE_FILE"].map((k) => [k, process.env[k]]));',
    'appendFileSync(out, JSON.stringify({ env, cwd: process.cwd(), locked, stdinClosed }) + "\\n");',
    'process.exit(Number(code ?? 0));',
    '',
  ].join('\n'),
);

/** @param {string[]} argv @param {number} [timeout] @returns {string} the YAML of `hooks.on_block_end` */
const hookYaml = (argv, timeout) => `hooks:\n  on_block_end:\n    argv: ${JSON.stringify(argv)}\n${timeout === undefined ? '' : `    timeout_s: ${timeout}\n`}`;

/**
 * A committed repo with `.code-forge.yml` and `files`, and a run started through the CLI.
 * @param {string} slug @param {string} runId @param {{extra?: string, files?: string[]}} [opts]
 */
async function fixture(slug, runId, { extra = EXTERNAL, files = [] } = {}) {
  const repo = realpathSync(await makeRepo());
  writeFile(repo, '.code-forge.yml', configYaml(slug, extra));
  for (const f of files) writeFile(repo, f, `export const v = '${f}';\n`);
  await commitAll(repo);
  const started = await cf(['run', 'start', '--cwd', repo, '--run', runId, '--worker-pid', PID]);
  assert.equal(started.code, 0, started.stderr);
  /** @param {string} id @param {string[]} owned */
  const open = async (id, owned) => {
    const res = await cf(['block', 'open', id, '--run', runId, '--level', 'L1', '--owned', ...owned, '--acceptance', ACC], repo);
    assert.equal(res.code, 0, res.stderr);
    return res;
  };
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
  const head = async () => (await git(['rev-parse', 'HEAD'], repo)).trim();
  const done = (/** @type {string} */ id) => {
    const raw = readFileSync(doneFilePath(runId, id), 'utf8');
    return { raw, json: JSON.parse(raw) };
  };
  const hookRows = async () => (await readAllRows(slug)).filter((r) => r.run === runId && r.event === 'hook.ran');
  return { repo, runId, slug, open, block, human, row, head, done, hookRows };
}

const DONE_KEYS = ['block', 'run_id', 'status', 'event', 'reason', 'commit', 'spent_usd', 'unknown_usd_sessions', 'findings', 'at'];

/**
 * The done file: keys in order, `at` an ISO UTC time, the rest exactly `want`.
 * @param {{raw: string, json: Record<string, any>}} file @param {Record<string, any>} want
 */
function assertDone(file, want) {
  assert.deepEqual(Object.keys(file.json), DONE_KEYS);
  assert.match(file.json.at, ISO);
  assert.equal(file.raw.endsWith('}\n'), true);
  assert.deepEqual({ ...file.json, at: '<at>' }, { ...want, at: '<at>' });
}

/**
 * The run's one `hook.ran` row: its MAC verifies, `duration_ms` is a non-negative integer, `ts` is
 * ISO, and every other field is exactly `want`.
 * @param {Record<string, any>[]} rows @param {string} runId @param {Record<string, any>} want
 */
async function assertHookRow(rows, runId, want) {
  assert.equal(rows.length, 1, JSON.stringify(rows));
  const [row] = rows;
  assert.equal(verifyRow(row, await loadKey(runId)).ok, true);
  assert.equal(Number.isInteger(row.duration_ms) && row.duration_ms >= 0, true, String(row.duration_ms));
  assert.match(row.ts, ISO);
  const { mac: _mac, ts: _ts, duration_ms: _d, ...rest } = row;
  assert.deepEqual(rest, { run: runId, event: 'hook.ran', block: 'B1', hook: 'on_block_end', ...want, tokens_source: null, cost_source: null });
}

test('B64b (1): doneFileContent builds the done file in key order; null spend and commit stay null', () => {
  const at = new Date('2026-10-12T08:09:10.123Z');
  const built = doneFileContent({ block: 'B1', runId: 'r-1', status: 'stopped', event: 'block.stop', reason: 'later', commit: 'a'.repeat(40), spend: { usd: 0.25, unknown: 1 }, findings: 2, at });
  assert.deepEqual(Object.keys(built), DONE_KEYS);
  assert.deepEqual(built, { block: 'B1', run_id: 'r-1', status: 'stopped', event: 'block.stop', reason: 'later', commit: 'a'.repeat(40), spent_usd: 0.25, unknown_usd_sessions: 1, findings: 2, at: '2026-10-12T08:09:10.123Z' });
  const unread = doneFileContent({ block: 'B1', runId: 'r-1', status: 'closed', event: 'block.close', commit: null, spend: null, findings: 0, at });
  assert.deepEqual(unread, { block: 'B1', run_id: 'r-1', status: 'closed', event: 'block.close', reason: null, commit: null, spent_usd: null, unknown_usd_sessions: null, findings: 0, at: '2026-10-12T08:09:10.123Z' });
  // a non-finite spend or a non-integer session count is unknown (null) — the keys stay
  const odd = doneFileContent({ block: 'B1', runId: 'r-1', status: 'closed', event: 'block.close', commit: null, spend: { usd: Number.NaN, unknown: 1.5 }, findings: 0, at });
  assert.deepEqual(Object.keys(odd), DONE_KEYS);
  assert.deepEqual([odd.spent_usd, odd.unknown_usd_sessions], [null, null]);
  assert.deepEqual(JSON.parse(JSON.stringify(doneFileContent({ block: 'B1', runId: 'r-1', status: 'closed', event: 'block.close', commit: null, spend: { usd: Infinity, unknown: 0 }, findings: 0, at }))).spent_usd, null);
  // findings unknown (ruling (c)) stays null
  assert.equal(doneFileContent({ block: 'B1', runId: 'r-1', status: 'closed', event: 'block.close', commit: null, spend: null, findings: null, at }).findings, null);
});

test('B64b (1) ruling (c): findings is null when the open-findings count fails — never 0', async () => {
  const fx = await fixture('b64b-findnull', 'r-b64b-findnull');
  await fx.open('B1', ['src/a.mjs']);
  /** @type {string[]} */
  const warnings = [];
  const record = { run_id: fx.runId, project: fx.slug, workspace: fx.repo, blocks: {} };
  const failing = async () => {
    throw new Error('FAKE count failure');
  };
  const file = await writeDoneFile({ record, id: 'B1', status: 'stopped', event: 'block.stop', reason: 'x', openFindings: failing, err: (s) => warnings.push(s) });
  assert.equal(file, doneFilePath(fx.runId, 'B1'));
  assert.deepEqual(warnings, []);
  assert.equal(fx.done('B1').json.findings, null);
  // a count that is not a count is unknown too; a real count is kept
  await writeDoneFile({ record, id: 'B1', status: 'stopped', event: 'block.stop', openFindings: async () => /** @type {any} */ (Number.NaN), err: (s) => warnings.push(s) });
  assert.equal(fx.done('B1').json.findings, null);
  await writeDoneFile({ record, id: 'B1', status: 'stopped', event: 'block.stop', openFindings: async () => 2, err: (s) => warnings.push(s) });
  assert.equal(fx.done('B1').json.findings, 2);
  // the count is never asked for when the rows cannot be read: an unreadable ledger (a project
  // name no ledger can have) gives null spend and null findings
  let asked = false;
  await writeDoneFile({ record: { ...record, project: '\0FAKE' }, id: 'B1', status: 'stopped', event: 'block.stop', openFindings: async () => ((asked = true), 0), err: (s) => warnings.push(s) });
  const unread = fx.done('B1').json;
  assert.deepEqual([unread.findings, unread.spent_usd, unread.unknown_usd_sessions, asked], [null, null, null, false]);
  assert.deepEqual(warnings, []);
});

test('B64b (1)(2)(4): without the hook key — a close writes the done file, no hook.ran row, and stdout, stderr and exit are exactly as before', async () => {
  const fx = await fixture('b64b-close', 'r-b64b-close', { files: ['src/a.mjs', 'src/b.mjs'] });
  await fx.open('B1', ['src/a.mjs']);
  // B1's own spend, and another block's (never counted for B1)
  await fx.row({ event: 'session', role: 'coder', provider: 'openai', model: 'gpt-5-codex', block: 'B1', usd: 0.4 });
  await fx.row({ event: 'session', role: 'coder', provider: 'openai', model: 'gpt-5-codex', block: 'B2', usd: 9 });
  writeFile(fx.repo, 'src/a.mjs', 'export const a = 2;\n');
  const closed = await fx.block(['close', 'B1', '--worker-pid', PID, ...CLEAN]);
  assert.deepEqual(closed, { code: 0, stdout: 'block B1 closed\n', stderr: '' });
  assertDone(fx.done('B1'), { block: 'B1', run_id: fx.runId, status: 'closed', event: 'block.close', reason: null, commit: await fx.head(), spent_usd: 0.4, unknown_usd_sessions: 0, findings: 0 });
  // --json: exactly the B64a object, no done_file key
  await commitAll(fx.repo);
  await fx.open('B2', ['src/b.mjs']);
  writeFile(fx.repo, 'src/b.mjs', 'export const b = 2;\n');
  const json = await fx.block(['close', 'B2', '--worker-pid', PID, ...CLEAN, '--json']);
  assert.deepEqual(json, { code: 0, stdout: `${JSON.stringify({ block: 'B2', event: 'closed', status: 'closed', reason: null, close_event: null })}\n`, stderr: '' });
  assert.deepEqual(await fx.hookRows(), []);
  // stop: the same text and JSON as before
  await fx.open('B3', ['src/c.mjs']);
  assert.deepEqual(await fx.block(['stop', 'B3', '--reason', 'later']), { code: 0, stdout: 'block B3 stopped\n', stderr: '' });
  assertDone(fx.done('B3'), { block: 'B3', run_id: fx.runId, status: 'stopped', event: 'block.stop', reason: 'later', commit: await fx.head(), spent_usd: 0, unknown_usd_sessions: 0, findings: 0 });
  await fx.open('B4', ['src/d.mjs']);
  assert.deepEqual(await fx.block(['stop', 'B4', '--reason', 'later', '--json']), { code: 0, stdout: `${JSON.stringify({ block: 'B4', event: 'stopped' })}\n`, stderr: '' });
  assert.deepEqual(await fx.hookRows(), []);
});

test('B64b (1): block stop writes the done file (block rows only, open findings as status counts them); a second end overwrites it', async () => {
  const fx = await fixture('b64b-stop', 'r-b64b-stop', { files: ['src/b.mjs'] });
  await fx.open('B2', ['src/b.mjs']);
  await fx.row({ event: 'session', role: 'coder', provider: 'openai', model: 'gpt-5-codex', block: 'B2', usd: 0.25 });
  await fx.row({ event: 'session', role: 'coder', provider: 'openai', model: 'gpt-5-codex', block: 'B2', usd_unknown: true });
  await fx.row({ event: 'session', role: 'coder', provider: 'openai', model: 'gpt-5-codex', block: 'B1', usd: 3 });
  await fx.row({ event: 'review.result', block: 'B2', file: 'src/b.mjs', content_hash: 'h1', status: 'reviewed', approved: false, open: [{ id: 'F1', severity: 'warning' }] });
  const status = await cf(['status', '--run', fx.runId, '--json']);
  assert.equal(JSON.parse(status.stdout).runs[0].blocks.find((/** @type {any} */ b) => b.id === 'B2').findings, 1);
  assert.deepEqual(await fx.block(['stop', 'B2', '--reason', 'not needed']), { code: 0, stdout: 'block B2 stopped\n', stderr: '' });
  const head = await fx.head();
  const first = fx.done('B2');
  assertDone(first, { block: 'B2', run_id: fx.runId, status: 'stopped', event: 'block.stop', reason: 'not needed', commit: head, spent_usd: 0.25, unknown_usd_sessions: 1, findings: 1 });
  // a second end of the same block (re-opened at a new commit, its finding waived, stopped again)
  // replaces the file
  writeFile(fx.repo, 'docs/n.md', 'n\n');
  await commitAll(fx.repo);
  await fx.open('B2', ['src/b.mjs']);
  assert.equal((await fx.human(['waive', 'B2', 'F1', '--file', 'src/b.mjs', '--reason', 'fine'])).code, 0);
  assert.deepEqual(await fx.block(['stop', 'B2', '--reason', 'second']), { code: 0, stdout: 'block B2 stopped\n', stderr: '' });
  const head2 = await fx.head();
  assert.notEqual(head2, head);
  assertDone(fx.done('B2'), { block: 'B2', run_id: fx.runId, status: 'stopped', event: 'block.stop', reason: 'second', commit: head2, spent_usd: 0.25, unknown_usd_sessions: 1, findings: 0 });
});

test('B64b (1)(2)(4): a close that closeBlock stops (worker.replaced) writes a stopped done file, runs the hook, and --json names done_file', async () => {
  const out = path.join(freshDir('hook-out'), 'env.jsonl');
  const fx = await fixture('b64b-pin', 'r-b64b-pin', { extra: EXTERNAL + hookYaml([process.execPath, HOOK, out, path.join(runsDir(), 'r-b64b-pin.lock')]) });
  await fx.open('B1', ['src/a.mjs']);
  const other = sleeper();
  const res = await fx.block(['close', 'B1', '--worker-pid', other, ...CLEAN, '--json']);
  const reason = `live worker pid ${other} is not the pinned pid ${PID}`;
  const file = doneFilePath(fx.runId, 'B1');
  assert.deepEqual(res, { code: 1, stdout: `${JSON.stringify({ block: 'B1', ok: false, reason, done_file: file })}\n`, stderr: `block B1 stopped (worker.replaced): ${reason}\n` });
  assert.equal(path.isAbsolute(file), true);
  assertDone(fx.done('B1'), { block: 'B1', run_id: fx.runId, status: 'stopped', event: 'worker.replaced', reason, commit: await fx.head(), spent_usd: 0, unknown_usd_sessions: 0, findings: 0 });
  const ran = readFileSync(out, 'utf8').trim().split('\n').map((l) => JSON.parse(l));
  assert.deepEqual(ran, [{ env: { CODE_FORGE_RUN: fx.runId, CODE_FORGE_BLOCK: 'B1', CODE_FORGE_BLOCK_STATUS: 'stopped', CODE_FORGE_DONE_FILE: file }, cwd: fx.repo, locked: true, stdinClosed: true }]);
  await assertHookRow(await fx.hookRows(), fx.runId, { argv0: path.basename(process.execPath), exit: 0, signal: null, timed_out: false });
});

test('B64b (1): a block with its own worktree — commit is the HEAD of that tree, read before --remove-worktree removes it', async () => {
  const fx = await fixture('b64b-tree', 'r-b64b-tree', { extra: EXTERNAL + 'proof:\n  isolation: worktree\n', files: ['src/a.mjs'] });
  const opened = await fx.open('B1', ['src/a.mjs']);
  const tree = /^worktree (\S+) · branch /m.exec(opened.stdout)?.[1];
  assert.equal(typeof tree, 'string', opened.stdout);
  writeFile(/** @type {string} */ (tree), 'src/a.mjs', 'export const a = 9;\n');
  await commitAll(/** @type {string} */ (tree));
  const treeHead = (await git(['rev-parse', 'HEAD'], /** @type {string} */ (tree))).trim();
  assert.notEqual(treeHead, await fx.head());
  const res = await fx.block(['close', 'B1', '--worker-pid', PID, ...CLEAN, '--remove-worktree']);
  assert.equal(res.code, 0, res.stderr);
  assert.equal(existsSync(/** @type {string} */ (tree)), false);
  assertDone(fx.done('B1'), { block: 'B1', run_id: fx.runId, status: 'closed', event: 'block.close', reason: null, commit: treeHead, spent_usd: 0, unknown_usd_sessions: 0, findings: 0 });
});

test('B64b (1)(2): a ledger.tamper stop writes a stopped done file', async () => {
  const fx = await fixture('b64b-tamper', 'r-b64b-tamper');
  await fx.open('B1', ['src/a.mjs']);
  // a gate-relevant row of the block that does not verify
  await fx.row({ event: 'block.claim', block: 'B1', path: 'src/z.mjs', mac: 'f'.repeat(64) });
  const res = await fx.block(['close', 'B1', '--worker-pid', PID, ...CLEAN]);
  assert.equal(res.code, 1);
  assert.match(res.stderr, /^block B1 stopped \(ledger\.tamper\): /);
  const file = fx.done('B1');
  const reason = res.stderr.slice('block B1 stopped (ledger.tamper): '.length, -1);
  assertDone(file, { block: 'B1', run_id: fx.runId, status: 'stopped', event: 'ledger.tamper', reason, commit: await fx.head(), spent_usd: 0, unknown_usd_sessions: 0, findings: 0 });
  // the stored reason is the one the ledger.tamper row records, not only the stderr text
  const tamper = (await readAllRows(fx.slug)).filter((r) => r.run === fx.runId && r.event === 'ledger.tamper' && r.block === 'B1');
  assert.equal(tamper.length, 1);
  assert.equal(typeof tamper[0].reason, 'string');
  assert.equal(file.json.reason, tamper[0].reason);
});

test('B64b (2)(4): the hook runs once per end with the four vars, outside the run lock, from the repository top; close --json and stop --json add done_file', async () => {
  const out = path.join(freshDir('hook-out'), 'env.jsonl');
  const fx = await fixture('b64b-hook', 'r-b64b-hook', { extra: EXTERNAL + hookYaml([process.execPath, HOOK, out, path.join(runsDir(), 'r-b64b-hook.lock')]), files: ['src/a.mjs', 'sub/keep.txt'] });
  await fx.open('B1', ['src/a.mjs']);
  writeFile(fx.repo, 'src/a.mjs', 'export const a = 2;\n');
  // run from a subdirectory: the hook's cwd is still the repository top
  const res = await cf(['block', 'close', 'B1', '--run', fx.runId, '--worker-pid', PID, ...CLEAN, '--json'], path.join(fx.repo, 'sub'));
  const file = doneFilePath(fx.runId, 'B1');
  assert.deepEqual(res, { code: 0, stdout: `${JSON.stringify({ block: 'B1', event: 'closed', status: 'closed', reason: null, close_event: null, done_file: file })}\n`, stderr: '' });
  const ran = readFileSync(out, 'utf8').trim().split('\n').map((l) => JSON.parse(l));
  assert.deepEqual(ran, [{ env: { CODE_FORGE_RUN: fx.runId, CODE_FORGE_BLOCK: 'B1', CODE_FORGE_BLOCK_STATUS: 'closed', CODE_FORGE_DONE_FILE: file }, cwd: fx.repo, locked: true, stdinClosed: true }]);
  await assertHookRow(await fx.hookRows(), fx.runId, { argv0: path.basename(process.execPath), exit: 0, signal: null, timed_out: false });
  // stop, with and without --json: the text is unchanged, the JSON adds done_file
  await fx.open('B2', ['src/b.mjs']);
  assert.deepEqual(await fx.block(['stop', 'B2', '--reason', 'later']), { code: 0, stdout: 'block B2 stopped\n', stderr: '' });
  await fx.open('B3', ['src/c.mjs']);
  assert.deepEqual(await fx.block(['stop', 'B3', '--reason', 'later', '--json']), { code: 0, stdout: `${JSON.stringify({ block: 'B3', event: 'stopped', done_file: doneFilePath(fx.runId, 'B3') })}\n`, stderr: '' });
  const all = readFileSync(out, 'utf8').trim().split('\n').map((l) => JSON.parse(l));
  assert.deepEqual(
    all.map((r) => [r.env.CODE_FORGE_BLOCK, r.env.CODE_FORGE_BLOCK_STATUS, r.locked]),
    [
      ['B1', 'closed', true],
      ['B2', 'stopped', true],
      ['B3', 'stopped', true],
    ],
  );
  assert.equal((await fx.hookRows()).length, 3);
  // positive control: the hook's lock probe names the real run lock — while `withRunLock` holds it,
  // the same probe reports locked: false (so the `locked: true` above could fail)
  const control = path.join(freshDir('hook-out'), 'control.jsonl');
  await withRunLock(fx.runId, async () => {
    const probe = await new Promise((resolve) => {
      const child = execFile(process.execPath, [HOOK, control, path.join(runsDir(), 'r-b64b-hook.lock')], (error) => resolve(error ? 1 : 0));
      child.stdin?.end(); // the probe reads stdin once
    });
    assert.equal(probe, 0);
  });
  assert.equal(JSON.parse(readFileSync(control, 'utf8').trim()).locked, false);
  // and once released, a real lock-taking verb run as the hook succeeds
  const viaVerb = await fixture('b64b-hookverb', 'r-b64b-hookverb', {
    extra: EXTERNAL + hookYaml([process.execPath, BIN, 'block', 'claim', 'B9', 'src/q.mjs', '--run', 'r-b64b-hookverb']),
    files: ['src/a.mjs'],
  });
  await viaVerb.open('B9', ['src/z.mjs']);
  await viaVerb.open('B1', ['src/a.mjs']);
  // the hook (`block claim`, which takes the run lock) exits 0: no warning
  assert.deepEqual(await viaVerb.block(['stop', 'B1', '--reason', 'later']), { code: 0, stdout: 'block B1 stopped\n', stderr: '' });
  await assertHookRow(await viaVerb.hookRows(), viaVerb.runId, { argv0: path.basename(process.execPath), exit: 0, signal: null, timed_out: false });
});

test('B64b (2): a hook that exits 3 — the exact hook.ran row, one stderr warning, exit code and stdout unchanged', async () => {
  const out = path.join(freshDir('hook-out'), 'env.jsonl');
  const fx = await fixture('b64b-exit3', 'r-b64b-exit3', { extra: EXTERNAL + hookYaml([process.execPath, HOOK, out, path.join(runsDir(), 'r-b64b-exit3.lock'), '3']) });
  await fx.open('B1', ['src/a.mjs']);
  assert.deepEqual(await fx.block(['stop', 'B1', '--reason', 'later']), { code: 0, stdout: 'block B1 stopped\n', stderr: 'warning: on_block_end hook exit 3\n' });
  await assertHookRow(await fx.hookRows(), fx.runId, { argv0: path.basename(process.execPath), exit: 3, signal: null, timed_out: false });
});

test('B64b (2)(4): a hook that exits 3 under stop --json and close --json — the exact stdout with done_file, exit 0, one warning line', async () => {
  const out = path.join(freshDir('hook-out'), 'env.jsonl');
  const fx = await fixture('b64b-exit3j', 'r-b64b-exit3j', { extra: EXTERNAL + hookYaml([process.execPath, HOOK, out, path.join(runsDir(), 'r-b64b-exit3j.lock'), '3']), files: ['src/a.mjs'] });
  await fx.open('B2', ['src/b.mjs']);
  assert.deepEqual(await fx.block(['stop', 'B2', '--reason', 'later', '--json']), {
    code: 0,
    stdout: `${JSON.stringify({ block: 'B2', event: 'stopped', done_file: doneFilePath(fx.runId, 'B2') })}\n`,
    stderr: 'warning: on_block_end hook exit 3\n',
  });
  await fx.open('B1', ['src/a.mjs']);
  writeFile(fx.repo, 'src/a.mjs', 'export const a = 2;\n');
  assert.deepEqual(await fx.block(['close', 'B1', '--worker-pid', PID, ...CLEAN, '--json']), {
    code: 0,
    stdout: `${JSON.stringify({ block: 'B1', event: 'closed', status: 'closed', reason: null, close_event: null, done_file: doneFilePath(fx.runId, 'B1') })}\n`,
    stderr: 'warning: on_block_end hook exit 3\n',
  });
  assert.deepEqual(fx.done('B1').json.status, 'closed');
  assert.deepEqual(fx.done('B2').json.status, 'stopped');
  const rows = await fx.hookRows();
  assert.deepEqual(rows.map((r) => [r.block, r.exit, r.signal, r.timed_out]), [
    ['B2', 3, null, false],
    ['B1', 3, null, false],
  ]);
});

test('B64b (2): a hook that does not exist — exit null, one "not found" warning, the stop unchanged', async () => {
  const missing = path.join(freshDir('nohook'), 'FAKE-no-such-hook');
  const fx = await fixture('b64b-enoent', 'r-b64b-enoent', { extra: EXTERNAL + hookYaml([missing, 'arg']) });
  await fx.open('B1', ['src/a.mjs']);
  assert.deepEqual(await fx.block(['stop', 'B1', '--reason', 'later']), { code: 0, stdout: 'block B1 stopped\n', stderr: 'warning: on_block_end hook not found\n' });
  await assertHookRow(await fx.hookRows(), fx.runId, { argv0: 'FAKE-no-such-hook', exit: null, signal: null, timed_out: false });
});

/** @param {number} pid @returns {boolean} whether `pid` is still a live process (a zombie counts until reaped) */
function alive(pid) {
  try {
    process.kill(pid, 0);
    return true;
  } catch (thrown) {
    assert.equal(/** @type {any} */ (thrown).code, 'ESRCH');
    return false;
  }
}

test('B64b (2): a hook past timeout_s — its whole process group (a grandchild too) is killed, timed_out true, one "timed out" warning', async () => {
  const pidFile = path.join(freshDir('grandchild'), 'pid');
  // the hook starts a grandchild (in the hook's process group), records its pid, then both sleep
  const script = [
    "const { spawn } = require('node:child_process');",
    "const { writeFileSync } = require('node:fs');",
    "const g = spawn(process.execPath, ['-e', 'setTimeout(() => {}, 60000)'], { stdio: 'ignore' });",
    `writeFileSync(${JSON.stringify(pidFile)}, String(g.pid));`,
    'setTimeout(() => {}, 60000);',
  ].join('\n');
  const fx = await fixture('b64b-timeout', 'r-b64b-timeout', { extra: EXTERNAL + hookYaml([process.execPath, '-e', script], 1) });
  await fx.open('B1', ['src/a.mjs']);
  const started = Date.now();
  let grandchild = 0;
  try {
    assert.deepEqual(await fx.block(['stop', 'B1', '--reason', 'later']), { code: 0, stdout: 'block B1 stopped\n', stderr: 'warning: on_block_end hook timed out\n' });
    assert.equal(Date.now() - started < 20000, true);
    grandchild = Number(readFileSync(pidFile, 'utf8'));
    assert.equal(Number.isInteger(grandchild) && grandchild > 1, true);
    // SIGKILL is delivered at once; give the orphan's new parent a moment to reap it
    const until = Date.now() + 20000;
    while (alive(grandchild) && Date.now() < until) await new Promise((r) => setTimeout(r, 50));
    assert.equal(alive(grandchild), false, `grandchild ${grandchild} survived the group kill`);
  } finally {
    if (grandchild > 1) {
      try {
        process.kill(grandchild, 'SIGKILL');
      } catch {
        // already gone
      }
    }
  }
  const rows = await fx.hookRows();
  await assertHookRow(rows, fx.runId, { argv0: path.basename(process.execPath), exit: null, signal: 'SIGKILL', timed_out: true });
  assert.equal(rows[0].duration_ms >= 1000, true, String(rows[0].duration_ms));
});

test('B64b (2) ruling (b): a hook killed by a signal warns "signal <NAME>"; exit null, signal recorded', async () => {
  const fx = await fixture('b64b-signal', 'r-b64b-signal', { extra: EXTERNAL + hookYaml([process.execPath, '-e', "process.kill(process.pid, 'SIGTERM')"]) });
  await fx.open('B1', ['src/a.mjs']);
  assert.deepEqual(await fx.block(['stop', 'B1', '--reason', 'later']), { code: 0, stdout: 'block B1 stopped\n', stderr: 'warning: on_block_end hook signal SIGTERM\n' });
  await assertHookRow(await fx.hookRows(), fx.runId, { argv0: path.basename(process.execPath), exit: null, signal: 'SIGTERM', timed_out: false });
});

test('B64b (2) ruling (b): a missing cwd is "failed to start (ENOENT)", a non-executable argv[0] "failed to start (EACCES)" — never "not found"; a row is still written', async () => {
  const fx = await fixture('b64b-start', 'r-b64b-start');
  const writeRow = (/** @type {Record<string, any>} */ row) => appendRow(row, { slug: fx.slug });
  /** @type {string[]} */
  const warnings = [];
  const err = (/** @type {string} */ s) => warnings.push(s);
  const base = { runId: fx.runId, id: 'B1', status: 'stopped', doneFile: '', writeRow, err };
  const gone = path.join(freshDir('gone'), 'FAKE-missing-dir');
  const row = await runBlockEndHook({ ...base, hook: { argv: [process.execPath, '-e', ''], timeoutS: 5 }, repoTop: gone });
  assert.deepEqual(warnings, ['warning: on_block_end hook failed to start (ENOENT)\n']);
  assert.deepEqual({ ...row, duration_ms: 0 }, { event: 'hook.ran', block: 'B1', hook: 'on_block_end', argv0: path.basename(process.execPath), exit: null, signal: null, timed_out: false, duration_ms: 0 });
  const noExec = path.join(freshDir('noexec'), 'FAKE-hook.sh');
  writeFileSync(noExec, '#!/bin/sh\nexit 0\n', { mode: 0o644 });
  warnings.length = 0;
  await runBlockEndHook({ ...base, hook: { argv: [noExec], timeoutS: 5 }, repoTop: fx.repo });
  assert.deepEqual(warnings, ['warning: on_block_end hook failed to start (EACCES)\n']);
  const rows = await fx.hookRows();
  assert.deepEqual(rows.map((r) => [r.argv0, r.exit, r.signal, r.timed_out]), [
    [path.basename(process.execPath), null, null, false],
    ['FAKE-hook.sh', null, null, false],
  ]);
  for (const r of rows) assert.equal(verifyRow(r, await loadKey(fx.runId)).ok, true);
  // a row that cannot be written: one more line, an error code only — never the message text
  warnings.length = 0;
  const failingWrite = async () => {
    throw Object.assign(new Error('FAKE secret message text'), { code: 'EFAKEROW' });
  };
  const none = await runBlockEndHook({ ...base, writeRow: failingWrite, hook: { argv: [process.execPath, '-e', 'process.exit(3)'], timeoutS: 5 }, repoTop: fx.repo });
  assert.equal(none, null);
  assert.deepEqual(warnings, ['warning: on_block_end hook exit 3\n', 'warning: on_block_end hook row not written (EFAKEROW)\n']);
  warnings.length = 0;
  await runBlockEndHook({ ...base, writeRow: async () => { throw new Error('FAKE no code'); }, hook: { argv: [process.execPath, '-e', ''], timeoutS: 5 }, repoTop: fx.repo });
  assert.deepEqual(warnings, ['warning: on_block_end hook row not written (UNKNOWN)\n']);
  // every warning form, and never an error message
  const ran = { exit: null, signal: null, timedOut: false, notFound: false, error: null };
  assert.deepEqual(
    [
      hookWarning({ ...ran, exit: 0 }),
      hookWarning({ ...ran, exit: 3 }),
      hookWarning({ ...ran, signal: 'SIGSEGV' }),
      hookWarning({ ...ran, timedOut: true, signal: 'SIGKILL' }),
      hookWarning({ ...ran, notFound: true }),
      hookWarning({ ...ran, error: 'EACCES' }),
      hookWarning(ran),
    ],
    [null, 'exit 3', 'signal SIGSEGV', 'timed out', 'not found', 'failed to start (EACCES)', 'failed to start (UNKNOWN)'],
  );
});

test('B64b (1): a done file that cannot be written — one stderr warning, the stop exactly as before', async () => {
  const fx = await fixture('b64b-nowrite', 'r-b64b-nowrite');
  await fx.open('B1', ['src/a.mjs']);
  // `blocks` is a file: the directory cannot be made
  writeFile(path.join(runsDir(), fx.runId), 'blocks', 'not a directory\n');
  const res = await fx.block(['stop', 'B1', '--reason', 'later']);
  assert.deepEqual({ code: res.code, stdout: res.stdout }, { code: 0, stdout: 'block B1 stopped\n' });
  assert.match(res.stderr, /^warning: done file not written: \S[^\n]*\n$/);
});

test('B64b (1)(2) ruling (d): with the hook set and the done file unwritable — done_file null, one warning, the hook runs once with CODE_FORGE_DONE_FILE ""', async () => {
  const out = path.join(freshDir('hook-out'), 'env.jsonl');
  const fx = await fixture('b64b-nowrite2', 'r-b64b-nowrite2', { extra: EXTERNAL + hookYaml([process.execPath, HOOK, out, path.join(runsDir(), 'r-b64b-nowrite2.lock')]) });
  await fx.open('B1', ['src/a.mjs']);
  writeFile(path.join(runsDir(), fx.runId), 'blocks', 'not a directory\n');
  const res = await fx.block(['stop', 'B1', '--reason', 'later', '--json']);
  assert.deepEqual({ code: res.code, stdout: res.stdout }, { code: 0, stdout: '{"block":"B1","event":"stopped","done_file":null}\n' });
  assert.match(res.stderr, /^warning: done file not written: \S[^\n]*\n$/);
  const ran = readFileSync(out, 'utf8').trim().split('\n').map((l) => JSON.parse(l));
  assert.deepEqual(ran, [{ env: { CODE_FORGE_RUN: fx.runId, CODE_FORGE_BLOCK: 'B1', CODE_FORGE_BLOCK_STATUS: 'stopped', CODE_FORGE_DONE_FILE: '' }, cwd: fx.repo, locked: true, stdinClosed: true }]);
  await assertHookRow(await fx.hookRows(), fx.runId, { argv0: path.basename(process.execPath), exit: 0, signal: null, timed_out: false });
});

test('B64b ruling (e): when --remove-worktree throws after the close, the hook still runs exactly once', async () => {
  const out = path.join(freshDir('hook-out'), 'env.jsonl');
  const fx = await fixture('b64b-throw', 'r-b64b-throw', {
    extra: EXTERNAL + 'proof:\n  isolation: worktree\n' + hookYaml([process.execPath, HOOK, out, path.join(runsDir(), 'r-b64b-throw.lock')]),
    files: ['src/a.mjs'],
  });
  const opened = await fx.open('B1', ['src/a.mjs']);
  const tree = /** @type {string} */ (/^worktree (\S+) · branch /m.exec(opened.stdout)?.[1]);
  writeFile(tree, 'src/a.mjs', 'export const a = 9;\n');
  await commitAll(tree);
  let stdout = '';
  let stderr = '';
  const cwd = process.cwd();
  /** @type {number} */
  let code;
  process.chdir(fx.repo);
  try {
    code = await runBlock(['close', 'B1', '--run', fx.runId, '--worker-pid', PID, ...CLEAN, '--remove-worktree', '--json'], {
      stdout: { write: (s) => (stdout += s) },
      stderr: { write: (s) => (stderr += s) },
      removeBlockTree: async () => {
        throw new Error('FAKE removal crash');
      },
    });
  } finally {
    process.chdir(cwd);
  }
  assert.deepEqual({ code, stdout, stderr }, { code: 1, stdout: '', stderr: 'block close: FAKE removal crash\n' });
  assert.equal(fx.done('B1').json.status, 'closed');
  const ran = readFileSync(out, 'utf8').trim().split('\n').map((l) => JSON.parse(l));
  assert.deepEqual(ran.map((r) => [r.env.CODE_FORGE_BLOCK, r.env.CODE_FORGE_BLOCK_STATUS, r.env.CODE_FORGE_DONE_FILE, r.locked]), [['B1', 'closed', doneFilePath(fx.runId, 'B1'), true]]);
  await assertHookRow(await fx.hookRows(), fx.runId, { argv0: path.basename(process.execPath), exit: 0, signal: null, timed_out: false });
});

test('B64b (2): run reload refuses adding, changing or removing hooks.on_block_end', async () => {
  const fx = await fixture('b64b-reload', 'r-b64b-reload', { extra: EXTERNAL + hookYaml(['true']) });
  const why = "hooks.on_block_end cannot change mid-run (it runs a command on the owner's machine at every block end; fixed by the owner before the run starts) — put it back, or end the run and start a new one\n";
  for (const extra of [EXTERNAL + hookYaml(['false']), EXTERNAL + hookYaml(['true'], 5), EXTERNAL]) {
    writeFile(fx.repo, '.code-forge.yml', configYaml(fx.slug, extra));
    const res = await cf(['run', 'reload', '--run', fx.runId], fx.repo);
    assert.deepEqual(res, { code: 1, stdout: '', stderr: `run reload: ${why}` });
  }
  // a run started without the key cannot gain it
  const bare = await fixture('b64b-reload2', 'r-b64b-reload2');
  writeFile(bare.repo, '.code-forge.yml', configYaml(bare.slug, EXTERNAL + hookYaml(['true'])));
  assert.deepEqual(await cf(['run', 'reload', '--run', bare.runId], bare.repo), { code: 1, stdout: '', stderr: `run reload: ${why}` });
});

test('B64b (3): with the hook set, a block may not own .code-forge.yml', async () => {
  const fx = await fixture('b64b-owner', 'r-b64b-owner', { extra: hookYaml(['true']) });
  const res = await fx.block(['open', 'B1', '--level', 'L1', '--owned', '.code-forge.yml', '--acceptance', ACC]);
  const refused = (/** @type {string} */ verb) => ({ code: 1, stdout: '', stderr: `block ${verb}: owned entry ".code-forge.yml" covers the project's .code-forge.yml; no block may own it while hooks.on_block_end is set\n` });
  assert.deepEqual(res, refused('open'));
  // claim takes the same guard
  await fx.open('B1', ['src/a.mjs']);
  assert.deepEqual(await fx.block(['claim', 'B1', '.code-forge.yml']), refused('claim'));
  // and so does a reopen
  assert.equal((await fx.block(['stop', 'B1', '--reason', 'later'])).code, 0);
  assert.deepEqual(await fx.block(['open', 'B1', '--level', 'L1', '--owned', '.code-forge.yml', '--acceptance', ACC]), refused('open'));
  // without either key it may, as before
  const bare = await fixture('b64b-owner2', 'r-b64b-owner2', { extra: '' });
  await bare.open('B1', ['.code-forge.yml']);
});

test('B64b (3): a coder transcript that runs the hook refuses the close (no row, no done file, no hook)', async () => {
  const out = path.join(freshDir('hook-out'), 'env.jsonl');
  const argv = [process.execPath, HOOK, out, path.join(runsDir(), 'r-b64b-gate.lock')];
  const fx = await fixture('b64b-gate', 'r-b64b-gate', { extra: EXTERNAL + hookYaml(argv), files: ['src/a.mjs'] });
  await fx.open('B1', ['src/a.mjs']);
  writeFile(fx.repo, 'src/a.mjs', 'export const a = 2;\n');
  const logs = freshDir('logs');
  writeFile(logs, 'coder.log', `$ npm test\nok\n$ ${path.basename(process.execPath)} ${HOOK} /dev/null\n`);
  const res = await fx.block(['close', 'B1', '--worker-pid', PID, '--transcript', path.join(logs, 'coder.log')]);
  assert.deepEqual(res, { code: 1, stdout: '', stderr: 'block B1 open: rule_break transcript line 3: code-forge-on-block-end-hook\n' });
  assert.equal(existsSync(doneFilePath(fx.runId, 'B1')), false);
  assert.equal(existsSync(out), false);
  assert.deepEqual(await fx.hookRows(), []);
  // a clean transcript closes
  writeFile(logs, 'clean.log', '$ npm test\nok\n');
  assert.equal((await fx.block(['close', 'B1', '--worker-pid', PID, '--transcript', path.join(logs, 'clean.log')])).code, 0);
});

test('B64b (3): hookForbidden only ever adds to the list; the static lists and checkTranscript are unchanged', () => {
  assert.deepEqual(hookForbidden(undefined), []);
  assert.deepEqual(hookForbidden([]), []);
  // a blank argv[0], or one naming a directory, `.` or `..`, builds no entry
  for (const argv of [[' '], ['/'], ['dir/'], ['C:\\hooks\\'], ['\t', 'x'], ['.'], ['..'], ['..', 'x'], ['sub/..']]) assert.equal(hookForbidden(argv).length, 0, JSON.stringify(argv));
  // a blank argv[1] still builds the entry, from argv[0] alone (clause 3, "if any")
  assert.deepEqual(hookForbidden(['notify', '  ']).map((e) => e.tokens), [['notify']]);
  // tokens are stored trimmed; an argv[1] with inner whitespace stays one literal token
  assert.deepEqual(hookForbidden([' /bin/notify ', ' a b ']).map((e) => e.tokens), [['notify', 'a b']]);
  const one = hookForbidden(['/usr/local/bin/notify-done']);
  assert.deepEqual(one.map((e) => [e.id, e.kind, e.tokens]), [['code-forge-on-block-end-hook', 'prefix', ['notify-done']]]);
  const two = hookForbidden(['/opt/node', '/home/o/hooks/done.mjs', '--x']);
  assert.deepEqual(two.map((e) => [e.id, e.kind, e.tokens]), [['code-forge-on-block-end-hook', 'prefix', ['node', '/home/o/hooks/done.mjs']]]);
  const text = 'step\n$ node /home/o/hooks/done.mjs --x\n$ cat ~/.code-forge/runs/r.key\n';
  const base = scanTranscript(text, FORBIDDEN);
  const withHook = scanTranscript(text, [...FORBIDDEN, ...two]);
  assert.deepEqual(withHook.filter((h) => !base.some((b) => b.id === h.id && b.line === h.line)), [{ id: 'code-forge-on-block-end-hook', line: 2 }]);
  for (const hit of base) assert.equal(withHook.some((h) => h.id === hit.id && h.line === hit.line), true);
  // the gate wrapper never sees the hook entry (the close's own check is the one place)
  assert.equal(checkTranscript(text).hits.some((h) => h.id === 'code-forge-on-block-end-hook'), false);
  // the static lists are untouched
  assert.equal(FORBIDDEN.some((e) => e.id === 'code-forge-on-block-end-hook'), false);
  assert.equal(mergeForbidden([]).some((e) => e.id === 'code-forge-on-block-end-hook'), false);
  assert.deepEqual(scanTranscript('$ notify-done now\n', one), [{ id: 'code-forge-on-block-end-hook', line: 1 }]);
});

test('B64b (2): blockEndHook reads only the run-start snapshot, defaults timeout_s to 60', () => {
  // a record whose snapshot is `cfg`, as `run start` stores it
  const record = (/** @type {Record<string, any>} */ cfg) => ({ run_id: 'r-x', config: initialConfigState(cfg) });
  assert.equal(blockEndHook({ run_id: 'r-x' }), null);
  assert.equal(blockEndHook(record({})), null);
  assert.deepEqual(blockEndHook(record({ hooks: { on_block_end: { argv: ['notify', 'done'] } } })), { argv: ['notify', 'done'], timeoutS: 60 });
  assert.deepEqual(blockEndHook(record({ hooks: { on_block_end: { argv: ['notify'], timeout_s: 600 } } })), { argv: ['notify'], timeoutS: 600 });
  assert.equal(blockEndHook(record({ hooks: { on_block_end: { argv: [] } } })), null);
  // a snapshot that does not match its hash is no hook
  const forged = record({});
  forged.config.snapshots[forged.config.hash] = { hooks: { on_block_end: { argv: ['evil'] } } };
  assert.equal(blockEndHook(forged), null);
});
