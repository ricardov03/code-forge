/**
 * `code-forge review` (block B22) — review-only: the review engine on changes you already have,
 * with no plan, no coder, no proof and no block close.
 *
 *   review [--base <ref>] [--files <path…>] [--acceptance <file> | --intent "<text>"]
 *          [--run <id>] [--max <seconds>] [--json] [--keep-run]
 *
 * 1. Base: `git merge-base HEAD <default>`, where `<default>` is `origin/HEAD`'s branch, else
 *    `main`, else `master`; when that is HEAD itself (you are on the default branch), the base is
 *    HEAD and the uncommitted changes are reviewed. `--base <ref>` overrides. Resolved to a sha.
 * 2. Files: `git diff --name-only <base>` (committed, staged and unstaged changes against the
 *    working tree) plus untracked files git does not ignore. Deleted, binary, symlinked and
 *    secret-like files are skipped, and so is code-forge's own state (`.code-forge/`,
 *    `.code-forge.yml`). `--files` replaces the list; each path is checked before anything starts.
 * 3. Acceptance: `--acceptance <file>` as given; `--intent` becomes one clause; neither ⇒ one
 *    default clause. A generated file lives in this command's temp root, never in the tree.
 * 4. Flow, every step a `code-forge` child with a `GIT_*`-free env: `run start` (launches the
 *    worker) → `block open R-<stamp> --level L2 --base <sha> --owned <files…>` → `review-file`
 *    per file (all enqueued first) → one `review-file --wait` per ticket, all together under ONE run
 *    deadline (`--max`, default 900 s for the whole review; a ticket pending at the deadline is
 *    reported `stopped: timeout`, exit 1), results announced on stderr as they finish → summary in file order →
 *    `block stop` (a review never closes a block) → `run end` unless `--keep-run`. The cleanup runs
 *    in a `finally`, on SIGINT/SIGTERM and after a timeout, and no worker outlives the command.
 *    Each cleanup step is independent (a failed or throwing step is reported on stderr and the
 *    next one still runs), and a cleanup error never replaces the review's own exit code.
 *    `--run <id>` names the run: an active run with that id is reused (and never ended here);
 *    otherwise a new run starts with that id.
 * Exit codes: 0 every file approved (or nothing to review); 1 any file with findings, stopped,
 * unavailable or timed out, or the flow failed; 2 usage, not a git repository, a bad base, a bad
 * path, or no `.code-forge.yml`.
 *
 * `--files` paths go through `review-file`'s own path rule (`normalizeRequestPath`) before anything
 * starts, and `review-file` re-checks each one when it enqueues it.
 */

import { randomBytes } from 'node:crypto';
import { closeSync, lstatSync, openSync, readFileSync, readSync, rmSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { parse as parseYAML } from 'yaml';
import { loadProjectConfig } from '../config/load.mjs';
import { computeFileSet } from '../gates/scope.mjs';
import { parseFlags } from '../state/cli-args.mjs';
import { assertRunId, newRunId, StateError } from '../state/paths.mjs';
import { processStartTime, readRun } from '../state/run.mjs';
import { exec } from '../util/exec.mjs';
import { semaphore } from '../util/locks.mjs';
import { writeSafe } from '../util/redact.mjs';
import { isAlive, listEntries, readStartTime, UNKNOWN_START_TIME } from '../util/reaper.mjs';
import { pidsDir, runRoot, setRunRoot, tmpBase } from '../util/tmp.mjs';
import { readWorker } from '../worker/queue.mjs';
import { gitChildEnv, normalizeRequestPath, repoRootOf, WorkerError } from '../worker/ticket.mjs';

const BIN = fileURLToPath(new URL('../../bin/code-forge.mjs', import.meta.url));

export const USAGE =
  'usage: code-forge review [--base <ref>] [--files <path…>] [--acceptance <file> | --intent "<text>"] [--run <id>] [--max <seconds>] [--json] [--keep-run]\n';

export const DEFAULT_MAX_S = 900;
/** The most `review-file --wait` children that run at once. */
export const MAX_WAITERS = 8;
export const DEFAULT_CLAUSE = 'Review for correctness, security and test quality; no intent was stated.';
const GIT_TIMEOUT_MS = 30_000;
const STEP_TIMEOUT_MS = 120_000;

/** Never sent to a reviewer (the packet refuses them too): env files, keys, certificates, credentials. */
const SECRET_LIKE = [/(^|\/)\.env[^/]*$/i, /\.pem$/i, /\.key$/i, /\.p12$/i, /(^|\/)id_(rsa|ed25519|ecdsa)[^/]*$/i, /(^|\/)credentials[^/]*$/i];

/** A refusal that exits 2 (usage, not a repo, bad base, bad path, no config). */
class UsageError extends Error {}
/** The run was interrupted (signal). */
class Interrupted extends Error {}

/** `process.env` minus every inherited `GIT_*` (a hook's `GIT_DIR` must never redirect a child). */
export function scrubbedEnv() {
  /** @type {Record<string, string>} */
  const env = {};
  for (const [key, value] of Object.entries(process.env)) if (!key.startsWith('GIT_') && value !== undefined) env[key] = value;
  return env;
}

/**
 * @param {string[]} args @param {string} cwd @param {number[]} [ok]
 * @returns {Promise<import('../util/exec.mjs').ExecResult>}
 */
const git = (args, cwd, ok) => exec(['git', ...args], { cwd, env: gitChildEnv(), timeoutMs: GIT_TIMEOUT_MS, ...(ok ? { okExitCodes: ok } : {}) });

/** @param {string} ref @param {string} cwd @returns {Promise<string | null>} the commit sha, or null. */
async function commitOf(ref, cwd) {
  if (ref.length === 0 || ref.startsWith('-')) return null;
  const res = await git(['rev-parse', '--verify', '--quiet', `${ref}^{commit}`], cwd);
  const sha = res.stdout.trim();
  return res.result === 'ok' && /^[0-9a-f]{40,64}$/.test(sha) ? sha : null;
}

/**
 * The base sha and how it was chosen.
 * @param {string} repoRoot @param {string | undefined} given
 * @returns {Promise<{sha: string, from: string}>}
 * @throws {UsageError}
 */
export async function resolveBase(repoRoot, given) {
  if (given !== undefined) {
    const sha = await commitOf(given, repoRoot);
    if (sha === null) throw new UsageError(`--base ${JSON.stringify(given)} is not a commit of this repository`);
    return { sha, from: given };
  }
  const head = await commitOf('HEAD', repoRoot);
  if (head === null) throw new UsageError('the repository has no commit yet — commit once, or pass --base');
  const candidates = [];
  const originHead = await git(['symbolic-ref', '--quiet', '--short', 'refs/remotes/origin/HEAD'], repoRoot, [0, 1]);
  if (originHead.result === 'ok' && originHead.code === 0 && originHead.stdout.trim().length > 0) candidates.push(originHead.stdout.trim());
  candidates.push('main', 'master');
  for (const name of candidates) {
    const tip = await commitOf(name, repoRoot);
    if (tip === null) continue;
    const mb = await git(['merge-base', 'HEAD', tip], repoRoot);
    const sha = mb.stdout.trim();
    if (mb.result !== 'ok' || sha.length === 0) throw new UsageError(`HEAD and ${name} share no history — pass --base <ref>`);
    return sha === head ? { sha: head, from: 'HEAD (uncommitted changes)' } : { sha, from: `merge-base HEAD ${name}` };
  }
  return { sha: head, from: 'HEAD (uncommitted changes; no main, master or origin/HEAD)' };
}

/**
 * A `--files` path through `review-file`'s own rule (`normalizeRequestPath`: relative to `cwd`, no
 * `..` segment, not absolute, inside the repository after resolving the directory's real path,
 * never under `.git/` or `.code-forge/`, never a symlink).
 * @param {string} raw @param {string} cwd @param {string} repoRoot - realpath'd
 * @returns {string} the repo-root-relative POSIX path
 * @throws {UsageError}
 */
export function checkRequestPath(raw, cwd, repoRoot) {
  try {
    return normalizeRequestPath(raw, cwd, repoRoot);
  } catch (err) {
    if (err instanceof WorkerError) throw new UsageError(`--files ${JSON.stringify(raw)}: ${err.message}`);
    throw err;
  }
}

/**
 * Why a candidate file is not sent for review, or null when it is.
 * @param {string} repoRoot @param {string} rel @param {{discovered: boolean}} opts
 * @returns {string | null}
 */
export function skipReason(repoRoot, rel, { discovered }) {
  const top = rel.split('/')[0].toLowerCase();
  if (top === '.code-forge' || top === '.git') return 'code-forge state';
  if (discovered && rel === '.code-forge.yml') return 'code-forge config';
  if (rel.startsWith('-') || rel.includes('\\')) return 'unsupported file name';
  if (SECRET_LIKE.some((re) => re.test(rel))) return 'secret-like name, never sent to a reviewer';
  let st;
  try {
    st = lstatSync(path.join(repoRoot, rel));
  } catch {
    return 'deleted';
  }
  if (st.isSymbolicLink()) return 'symlink';
  if (!st.isFile()) return 'not a regular file';
  // git's own heuristic: a NUL byte in the first 8000 bytes means binary
  let fd;
  try {
    fd = openSync(path.join(repoRoot, rel), 'r');
    const head = Buffer.alloc(8000);
    const n = readSync(fd, head, 0, head.length, 0);
    if (head.subarray(0, n).includes(0)) return 'binary';
  } catch {
    return 'unreadable';
  } finally {
    if (fd !== undefined) closeSync(fd);
  }
  return null;
}

/**
 * The acceptance clauses from a file the user gave.
 * @param {string} file @returns {Array<{clause: string, tests: string[]}>}
 * @throws {UsageError}
 */
function readAcceptance(file) {
  let parsed;
  try {
    parsed = parseYAML(readFileSync(file, 'utf8'), { prettyErrors: false });
  } catch {
    throw new UsageError(`--acceptance ${file}: not a readable YAML or JSON file`);
  }
  const ok =
    Array.isArray(parsed) &&
    parsed.length > 0 &&
    parsed.every((c) => typeof c?.clause === 'string' && c.clause.length > 0 && Array.isArray(c.tests) && c.tests.length > 0 && c.tests.every((/** @type {unknown} */ t) => typeof t === 'string' && t.length > 0));
  if (!ok) throw new UsageError(`--acceptance ${file}: must be a non-empty list of {clause, tests: [test ids…]}`);
  return parsed;
}

/** @param {string} text @returns {any} the last line of `text` as JSON, or null. */
function lastJSON(text) {
  try {
    return JSON.parse(text.trim().split('\n').at(-1) ?? '');
  } catch {
    return null;
  }
}

/**
 * @typedef {{file: string, result: 'approved' | 'unchanged' | 'findings' | 'stopped' | 'unavailable', reason: string | null,
 *   findings: Array<{id: unknown, severity: unknown, lines: string, claim: string, fix: string}>,
 *   tokens_in?: number | null, budget?: number | null, section?: string}} FileOutcome - `tokens_in`/`budget`
 *   (null when unknown; and a Markdown `section`): a `split_required` stop's packet size, only then (B54).
 */

/**
 * One file's outcome from its `review-file --wait` answer (or null when there was none).
 * @param {string} file @param {Record<string, any> | null} waited
 * @returns {FileOutcome}
 */
export function classify(file, waited) {
  const findings = Array.isArray(waited?.fix_list) ? waited.fix_list : [];
  const out = (/** @type {FileOutcome['result']} */ result, /** @type {string | null} */ reason) => ({ file, result, reason, findings });
  if (!waited) return out('unavailable', 'no answer');
  if (waited.status === 'pending') return out('stopped', 'timeout');
  if (waited.status !== 'done') return out('unavailable', String(waited.reason ?? waited.status ?? 'no answer'));
  const r = waited.result;
  if (!r || typeof r !== 'object') return out('unavailable', 'unreadable result');
  if (r.approved === true) return out('approved', null);
  if (r.status === 'no_change') return out('unchanged', null);
  // B54: a split_required stop (only) says how large the packet was and what the budget is
  const fin = (/** @type {unknown} */ v) => (typeof v === 'number' && Number.isFinite(v) ? v : null);
  const size = { tokens_in: fin(r.tokens_in), budget: fin(r.budget), ...(typeof r.section === 'string' ? { section: r.section } : {}) };
  if (r.status === 'stopped') return { ...out('stopped', String(r.stopped ?? 'stopped')), ...(r.stopped === 'split_required' ? size : {}) };
  if (r.status === 'split_required') return { ...out('stopped', 'split_required'), ...size };
  if (r.status === 'reviewed' && findings.length > 0) return out('findings', null);
  return out('unavailable', String(r.reason ?? r.status ?? 'not approved'));
}

/** @param {FileOutcome} o @returns {string} */
function label(o) {
  if (o.result === 'findings') return `${o.findings.length} finding${o.findings.length === 1 ? '' : 's'}`;
  if (o.result === 'stopped' && o.reason === 'split_required' && Number.isFinite(o.tokens_in) && Number.isFinite(o.budget)) {
    return `${o.result}: ${o.reason} (~${o.tokens_in} tokens, budget ${o.budget}${o.section ? `; section ${JSON.stringify(o.section)}` : ''})`;
  }
  if (o.result === 'stopped' || o.result === 'unavailable') return `${o.result}: ${o.reason}`;
  return o.result;
}

/** @param {FileOutcome[]} outcomes */
export function totals(outcomes) {
  const count = (/** @type {string} */ r) => outcomes.filter((o) => o.result === r).length;
  return {
    files: outcomes.length,
    approved: count('approved'),
    unchanged: count('unchanged'),
    with_findings: count('findings'),
    findings: outcomes.reduce((n, o) => n + (o.result === 'findings' ? o.findings.length : 0), 0),
    stopped: count('stopped'),
    unavailable: count('unavailable'),
  };
}

/** @param {FileOutcome[]} outcomes @returns {string} the human summary. */
export function renderText(outcomes) {
  const width = Math.max(...outcomes.map((o) => o.file.length));
  const lines = outcomes.map((o) => `${o.file.padEnd(width)}  ${label(o)}`);
  for (const o of outcomes) {
    if (o.result === 'approved' || o.result === 'unchanged' || o.findings.length === 0) continue;
    lines.push('', `fix list: ${o.file}`);
    for (const f of o.findings) lines.push(`  ${f.id} · ${f.severity} · lines ${f.lines}`, `    claim: ${f.claim}`, `    fix:   ${f.fix}`);
  }
  const t = totals(outcomes);
  lines.push(
    '',
    `totals: ${t.files} file${t.files === 1 ? '' : 's'} · ${t.approved} approved · ${t.with_findings} with findings (${t.findings} finding${t.findings === 1 ? '' : 's'}) · ${t.stopped} stopped · ${t.unavailable} unavailable${t.unchanged > 0 ? ` · ${t.unchanged} unchanged` : ''}`,
  );
  return `${lines.join('\n')}\n`;
}

/**
 * The worker pids this run left, each with the start time that proves it is still the same
 * process: the pin in the run record and the queue's announcement for this run.
 * @param {string} repoRoot @param {string} runId
 * @returns {Promise<Array<{pid: number, same: () => Promise<boolean>}>>}
 */
async function runWorkers(repoRoot, runId) {
  const found = [];
  try {
    const pin = (await readRun(runId)).worker;
    if (Number.isInteger(pin?.pid) && pin.pid > 1) found.push({ pid: pin.pid, same: async () => (await processStartTime(pin.pid)) === pin.started_at });
  } catch {
    // no record: nothing pinned
  }
  const w = readWorker(repoRoot);
  if (w?.run === runId && !found.some((f) => f.pid === w.pid)) {
    found.push({ pid: w.pid, same: async () => w.start_time === UNKNOWN_START_TIME || readStartTime(w.pid) === w.start_time });
  }
  return found;
}

/**
 * After `run end` (which sends SIGTERM and returns): wait up to 5 s for this run's worker to be
 * gone, then SIGKILL its process group. Also catches a worker a `run start` launched but never
 * pinned because it was interrupted.
 * @param {string} repoRoot @param {string} runId @returns {Promise<number>} workers killed here
 */
async function reapWorkers(repoRoot, runId) {
  let killed = 0;
  for (const w of await runWorkers(repoRoot, runId)) {
    if (!isAlive(w.pid) || !(await w.same())) continue;
    try {
      process.kill(w.pid, 'SIGTERM');
    } catch {
      continue;
    }
    const until = Date.now() + 5000;
    while (isAlive(w.pid) && Date.now() < until) await new Promise((r) => setTimeout(r, 100));
    if (isAlive(w.pid) && (await w.same())) {
      try {
        process.kill(-w.pid, 'SIGKILL');
      } catch {
        try {
          process.kill(w.pid, 'SIGKILL');
        } catch {
          // gone
        }
      }
      killed += 1;
    }
  }
  return killed;
}

/** @param {unknown} err @returns {string} */
const messageOf = (err) => (err instanceof Error ? err.message : String(err));

/**
 * @param {string[]} args
 * @param {{stdout?: {write: (s: string) => unknown}, stderr?: {write: (s: string) => unknown}, cwd?: string, exec?: typeof exec}} [deps] -
 *   `exec` runs every `code-forge` child of this command (the flow steps, the cleanup and the
 *   second-signal exit); a test seam, the real `exec` otherwise.
 * @returns {Promise<number>}
 */
export async function runReview(args, deps = {}) {
  const stdout = deps.stdout ?? process.stdout;
  const stderr = deps.stderr ?? process.stderr;
  const out = (/** @type {string} */ s) => writeSafe(stdout, s);
  const note = (/** @type {string} */ s) => writeSafe(stderr, `review: ${s}\n`);
  const cwd = deps.cwd ?? process.cwd();
  const execChild = deps.exec ?? exec;

  /** @type {Record<string, any>} */
  let flags;
  try {
    ({ flags } = parseReviewArgs(args));
  } catch (err) {
    writeSafe(stderr, `review: ${err instanceof Error ? err.message : String(err)}\n${USAGE}`);
    return 2;
  }
  if (flags.help === true) {
    out(USAGE);
    return 0;
  }
  const json = flags.json === true;
  const maxS = flags.max === undefined ? DEFAULT_MAX_S : Number(String(flags.max).replace(/s$/, ''));

  // ---- everything below until `run start` only reads: a refusal here exits 2 with no child started
  let repoRoot;
  let base;
  /** @type {string[]} */
  let files = [];
  /** @type {Array<{file: string, reason: string}>} */
  const skipped = [];
  let cfg;
  let acceptanceFile = null;
  try {
    repoRoot = await repoRootOf(cwd).catch(() => {
      throw new UsageError('not inside a git repository');
    });
    base = await resolveBase(repoRoot, typeof flags.base === 'string' ? flags.base : undefined);

    const given = Array.isArray(flags.files) ? flags.files.map((raw) => checkRequestPath(raw, cwd, repoRoot)) : null;
    const candidates = given ? [...new Set(given)] : (await computeFileSet({ cwd: repoRoot, base: base.sha })).all;
    for (const file of candidates) {
      const why = skipReason(repoRoot, file, { discovered: given === null });
      if (why === null) files.push(file);
      else skipped.push({ file, reason: why });
    }
    files = files.sort();

    if (files.length > 0) {
      const loaded = await loadProjectConfig(repoRoot);
      if (!loaded.ok) throw new UsageError(loaded.error === 'not-found' ? 'no .code-forge.yml at the repository root — run `code-forge init` first' : `.code-forge.yml does not load (${loaded.error}) — run \`code-forge validate\``);
      cfg = loaded.config;
      if (typeof flags.acceptance === 'string') {
        acceptanceFile = path.resolve(cwd, flags.acceptance);
        readAcceptance(acceptanceFile);
      }
    }
  } catch (err) {
    if (!(err instanceof UsageError)) throw err;
    note(err.message);
    return 2;
  }
  for (const s of skipped) note(`skipped ${s.file} (${s.reason})`);

  if (files.length === 0) {
    if (json) out(`${JSON.stringify({ ok: true, base: base.sha, base_from: base.from, run: null, block: null, files: [], skipped, totals: totals([]), nothing_to_review: true })}\n`);
    else out('nothing to review\n');
    return 0;
  }

  // ---- the run
  const runId = typeof flags.run === 'string' ? flags.run : newRunId();
  let reuse = false;
  if (typeof flags.run === 'string') {
    try {
      const record = await readRun(runId);
      if (record.status !== 'active') {
        note(`run ${runId} has ended — give another --run or none`);
        return 2;
      }
      reuse = true;
    } catch (err) {
      if (/** @type {any} */ (err)?.code !== 'no-run') throw err;
    }
  }
  const blockId = `R-${new Date().toISOString().replace(/[-:]/g, '').replace('T', '-').slice(0, 15)}-${randomBytes(2).toString('hex')}`;
  const tmpRootCfg = typeof cfg?.tmp?.root === 'string' ? path.resolve(repoRoot, cfg.tmp.root) : undefined;
  const tmp = runRoot(`review-${runId}`, { root: tmpBase(tmpRootCfg) });
  setRunRoot(tmp);

  let started = false;
  let blockOpen = false;
  const keepRun = flags['keep-run'] === true;

  // Signals. The FIRST SIGINT/SIGTERM kills the in-flight `code-forge` child (its process group,
  // found in this command's pid registry, start time re-checked) and rejects the step's promise at
  // once, so control reaches the `finally` cleanup without waiting for a `review-file --wait` to
  // run out its `--max`. Once the cleanup has started (`inCleanup`), a first signal only counts:
  // it kills nothing and aborts nothing, so the block stop and `run end` it would otherwise cut
  // short complete. A SECOND signal stops waiting for that cleanup: a best-effort block stop,
  // `run end` and worker reap (10 s each at most), then a hard exit.
  const abort = new AbortController();
  /** @type {Promise<never>} */
  const aborted = new Promise((_, reject) => {
    abort.signal.addEventListener('abort', () => reject(new Interrupted(String(abort.signal.reason))), { once: true });
  });
  aborted.catch(() => {});
  let signals = 0;
  let inCleanup = false;
  const killInFlight = () => {
    for (const entry of listEntries(pidsDir(tmp))) {
      if (entry.start_time !== UNKNOWN_START_TIME && readStartTime(entry.pid) !== entry.start_time) continue;
      try {
        process.kill(-entry.pid, 'SIGTERM');
      } catch {
        try {
          process.kill(entry.pid, 'SIGTERM');
        } catch {
          // already gone
        }
      }
    }
  };
  const hardExit = async (/** @type {NodeJS.Signals} */ signal) => {
    note(`second ${signal}: ending the run and exiting now`);
    const quick = (/** @type {string[]} */ a) => Promise.resolve().then(() => execChild([process.execPath, BIN, ...a], { cwd: repoRoot, env: scrubbedEnv(), timeoutMs: 10_000 })).catch(() => null);
    if (blockOpen) await quick(['block', 'stop', blockId, '--run', runId, '--reason', 'review only: interrupted']);
    if (started && !keepRun) {
      await quick(['run', 'end', '--run', runId]);
      await reapWorkers(repoRoot, runId).catch(() => 0);
    }
    rmSync(tmp, { recursive: true, force: true });
    process.exit(1);
  };
  const onSignal = (/** @type {NodeJS.Signals} */ signal) => {
    signals += 1;
    if (signals === 1) {
      if (inCleanup) {
        note(`${signal} during cleanup: finishing it (a second ${signal} exits at once)`);
        return;
      }
      killInFlight();
      abort.abort(signal);
    } else if (signals === 2) {
      void hardExit(signal);
    }
  };
  process.on('SIGINT', onSignal);
  process.on('SIGTERM', onSignal);

  /** @param {string[]} cfArgs @param {number} [timeoutMs] */
  const cf = async (cfArgs, timeoutMs = STEP_TIMEOUT_MS) => {
    if (abort.signal.aborted) throw new Interrupted(String(abort.signal.reason));
    const res = await Promise.race([execChild([process.execPath, BIN, ...cfArgs], { cwd: repoRoot, env: scrubbedEnv(), timeoutMs }), aborted]);
    if (abort.signal.aborted) throw new Interrupted(String(abort.signal.reason));
    return res;
  };
  /** @param {import('../util/exec.mjs').ExecResult} res @param {string} what */
  const must = (res, what) => {
    if (res.code !== 0) throw new Error(`${what} exited ${res.code ?? res.signal}: ${(res.stderr || res.stdout).trim().split('\n').at(-1) ?? ''}`);
    return res;
  };

  let code = 1;
  /** @type {FileOutcome[]} */
  const outcomes = [];
  try {
    if (!acceptanceFile) {
      const clause = typeof flags.intent === 'string' ? flags.intent : DEFAULT_CLAUSE;
      acceptanceFile = path.join(tmp, 'acceptance.json');
      writeFileSync(acceptanceFile, JSON.stringify([{ clause, tests: ['review-only'] }]), { mode: 0o600 });
    }
    note(`base ${base.sha.slice(0, 12)} (${base.from}) · ${files.length} file${files.length === 1 ? '' : 's'} · run ${runId}${reuse ? ' (reused)' : ''}`);
    if (!reuse) {
      started = true;
      must(await cf(['run', 'start', '--cwd', repoRoot, '--run', runId]), 'run start');
    }
    blockOpen = true; // before the call: an interrupted `block open` may still have opened it (a stop of an unopened block only fails)
    must(await cf(['block', 'open', blockId, '--run', runId, '--level', 'L2', '--base', base.sha, '--acceptance', acceptanceFile, '--owned', ...files]), 'block open');

    /** @type {Array<{file: string, ticket: string | null, answer: any}>} */
    const queued = [];
    for (const file of files) {
      const res = await cf(['review-file', file, '--block', blockId, '--run', runId]);
      const answer = lastJSON(res.stdout);
      queued.push({ file, ticket: typeof answer?.ticket === 'string' ? answer.ticket : null, answer });
    }
    // Every file is enqueued above before any wait starts. One run deadline (`--max`, measured from
    // here) covers all the waits, which run together: each `review-file --wait` gets the time still
    // left, and a ticket still pending at the deadline is reported `stopped: timeout`. A result is
    // announced on stderr as its file finishes; the table below keeps the file order.
    // At most MAX_WAITERS `review-file --wait` children at once (a review of many files must not
    // start one Node process per file); a wait that starts late still counts down to the same
    // deadline, and one that starts after it is reported pending without a child.
    const deadline = Date.now() + maxS * 1000;
    const waiters = semaphore(MAX_WAITERS);
    /** @type {Array<FileOutcome | undefined>} */
    const byIndex = new Array(queued.length);
    const finish = (/** @type {number} */ i, /** @type {FileOutcome} */ o) => {
      byIndex[i] = o;
      note(`${o.file}  ${label(o)}`);
    };
    const settled = await Promise.allSettled(
      queued.map(async (q, i) => {
        if (q.ticket === null) {
          finish(i, classify(q.file, { status: q.answer?.status ?? 'refused', reason: q.answer?.reason ?? q.answer?.status ?? 'not queued' }));
          return;
        }
        await waiters.run(async () => {
          const left = deadline - Date.now();
          if (left <= 0) {
            finish(i, classify(q.file, { status: 'pending' }));
            return;
          }
          const res = await cf(['review-file', '--wait', q.ticket, '--max', `${Math.ceil(left / 1000)}s`], left + 60_000);
          finish(i, classify(q.file, lastJSON(res.stdout)));
        });
      }),
    );
    for (const s of settled) if (s.status === 'rejected') throw s.reason;
    for (const o of byIndex) outcomes.push(/** @type {FileOutcome} */ (o));
    const allOk = outcomes.every((o) => o.result === 'approved' || o.result === 'unchanged');
    code = allOk ? 0 : 1;
    if (json) out(`${JSON.stringify({ ok: allOk, base: base.sha, base_from: base.from, run: runId, block: blockId, files: outcomes, skipped, totals: totals(outcomes) })}\n`);
    else out(renderText(outcomes));
  } catch (err) {
    note(err instanceof Interrupted ? `interrupted by ${err.message}; stopping the review` : `failed: ${err instanceof Error ? err.message : String(err)}`);
    code = 1;
  } finally {
    // Every step below is independent: a step that fails or throws is reported and the next one
    // still runs; the nested `finally` always detaches the handlers and removes the temp root. The
    // exit code is the review's own whatever the cleanup does.
    inCleanup = true;
    try {
      /**
       * One cleanup child. `exec` forwards every signal it receives to its live children, so a
       * first signal during the cleanup still cuts the running step short; a step that died of a
       * signal (not of its timeout) is run once more, so the block is stopped and the run ends.
       * @param {string[]} a
       */
      const cleanup = async (a) => {
        const run = () => execChild([process.execPath, BIN, ...a], { cwd: repoRoot, env: scrubbedEnv(), timeoutMs: 60_000 });
        const res = await run();
        return res.code === null && res.signal !== null && !res.timedOut && signals > 0 ? run() : res;
      };
      if (blockOpen) {
        try {
          const stopped = await cleanup(['block', 'stop', blockId, '--run', runId, '--reason', 'review only: never closed, no proof']);
          if (stopped.code !== 0) note(`block stop exited ${stopped.code ?? stopped.signal}`);
        } catch (err) {
          note(`block stop failed: ${messageOf(err)}`);
        }
      }
      if (started && !keepRun) {
        try {
          const ended = await cleanup(['run', 'end', '--run', runId]);
          if (ended.code !== 0) note(`run end exited ${ended.code ?? ended.signal}: ${ended.stderr.trim()}`);
        } catch (err) {
          note(`run end failed: ${messageOf(err)}`);
        }
        try {
          await reapWorkers(repoRoot, runId);
        } catch (err) {
          note(`worker reap failed: ${messageOf(err)}`);
        }
      } else if (started) {
        note(`run ${runId} kept (--keep-run): reuse it with --run ${runId}; end it with \`code-forge run end --run ${runId}\``);
      }
    } finally {
      process.off('SIGINT', onSignal);
      process.off('SIGTERM', onSignal);
      setRunRoot(null);
      try {
        rmSync(tmp, { recursive: true, force: true });
      } catch (err) {
        note(`temp root not removed: ${messageOf(err)}`);
      }
    }
  }
  return code;
}

/**
 * @param {string[]} args
 * @returns {{flags: Record<string, any>}}
 * @throws {StateError} `usage`
 */
function parseReviewArgs(args) {
  const { flags, positionals } = parseFlags(args, { values: ['base', 'acceptance', 'intent', 'run', 'max'], multi: ['files'], booleans: ['json', 'keep-run', 'help'] });
  if (positionals.length > 0) throw new StateError('usage', `unexpected argument ${JSON.stringify(positionals[0])}`);
  if (flags.acceptance !== undefined && flags.intent !== undefined) throw new StateError('usage', 'give --acceptance or --intent, not both');
  if (typeof flags.intent === 'string' && flags.intent.trim().length === 0) throw new StateError('usage', '--intent needs text');
  if (flags.max !== undefined && !/^[1-9]\d{0,5}s?$/.test(String(flags.max))) throw new StateError('usage', '--max must be whole seconds, like 900 or 900s');
  if (typeof flags.run === 'string') assertRunIdUsage(flags.run);
  return { flags };
}

/** @param {string} id */
function assertRunIdUsage(id) {
  try {
    assertRunId(id);
  } catch {
    throw new StateError('usage', '--run must match ^[a-z0-9][a-z0-9-]{0,63}$');
  }
}

/** @param {string[]} args @returns {Promise<number>} */
export default async function review(args) {
  return runReview(args);
}
