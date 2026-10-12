/**
 * B64b (issue #13): the block-end signal — a done file and the owner's `hooks.on_block_end` hook.
 *
 * A block ends where `src/state/block.mjs` sets its status to `closed` or `stopped`: `closeBlock`
 * closing it, `closeBlock` stopping it (a pinned worker event or `ledger.tamper`), and `stopBlock`
 * (`block stop`). After each end — its ledger rows written and the run lock released — the CLI
 * (`src/cli/block.mjs`):
 *
 * 1. writes `<runs>/<run>/blocks/<id>.done.json` ({@link doneFilePath}) atomically (a temp file in
 *    the same directory, then a rename; a second end overwrites it) with {@link doneFileContent}:
 *    the block, the run, its status, the end's event and reason, the HEAD commit of the block's
 *    tree, the block's own spend and its open findings, and the time. Never transcript text, diff
 *    text or secrets. A failed write changes nothing but one stderr warning line.
 * 2. runs `hooks.on_block_end` (`blockEndHook` in `config-snapshot.mjs`, from the run-start
 *    config snapshot only — the key is immutable) once, outside the run lock: argv only (no
 *    shell), cwd the repository top, stdin closed, stdout and stderr discarded, the parent env plus
 *    `CODE_FORGE_RUN`, `CODE_FORGE_BLOCK`, `CODE_FORGE_BLOCK_STATUS` and `CODE_FORGE_DONE_FILE`;
 *    its process group is killed at `timeout_s`. One signed `hook.ran` row records it (under the run lock). A hook
 *    that fails, is missing, times out or crashes changes nothing but one stderr warning line.
 */

import { spawn } from 'node:child_process';
import { mkdir, rename, rm, stat, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { runSpend } from '../ledger/spend.mjs';
import { readAllRows } from '../ledger/write.mjs';
import { exec } from '../util/exec.mjs';
import { assertRunId, runsDir } from './paths.mjs';
import { withRunLock, writeSigned } from './run.mjs';

// B64b: the hook's config reader lives next to `externalReviewer` (read by `state/block.mjs` too)
export { HOOK_TIMEOUT_DEFAULT_S, HOOK_TIMEOUT_MAX_S, HOOK_TIMEOUT_MIN_S, blockEndHook } from './config-snapshot.mjs';

/**
 * The block's done file: `<runs>/<run>/blocks/<id>.done.json` (the run's own directory under the
 * runs dir, next to the run record — never in the source tree).
 * @param {string} runId @param {string} id
 * @returns {string} an absolute path
 */
export function doneFilePath(runId, id) {
  return path.resolve(runsDir(), assertRunId(runId), 'blocks', `${safeId(id)}.done.json`);
}

/** @param {string} id @returns {string} the id, refused when it could leave the blocks dir. */
function safeId(id) {
  if (typeof id !== 'string' || id.length === 0 || id.includes('/') || id.includes('\\') || id === '.' || id === '..' || id.includes('\0')) {
    throw new TypeError(`block id ${JSON.stringify(id)} cannot name a done file`);
  }
  return id;
}

/**
 * The done file's content, keys in this order. Pure.
 * @param {{
 *   block: string, runId: string, status: 'closed' | 'stopped', event: string, reason?: string | null,
 *   commit: string | null, spend: {usd: number, unknown: number} | null, findings: number | null, at: Date,
 * }} facts
 * @returns {{block: string, run_id: string, status: string, event: string, reason: string | null, commit: string | null,
 *   spent_usd: number | null, unknown_usd_sessions: number | null, findings: number | null, at: string}}
 */
export function doneFileContent({ block, runId, status, event, reason = null, commit, spend, findings, at }) {
  return {
    block,
    run_id: runId,
    status,
    event,
    reason: typeof reason === 'string' ? reason : null,
    commit: typeof commit === 'string' && commit.length > 0 ? commit : null,
    // a non-finite spend or a non-integer count is unknown (null) — the keys are never dropped
    spent_usd: spend && Number.isFinite(spend.usd) ? spend.usd : null,
    unknown_usd_sessions: spend && Number.isInteger(spend.unknown) ? spend.unknown : null,
    findings: Number.isInteger(findings) ? findings : null,
    at: at.toISOString(),
  };
}

/** @param {string} cwd @returns {Promise<string | null>} HEAD of `cwd`'s repository, or null. */
async function headCommit(cwd) {
  try {
    const env = {};
    for (const [name, value] of Object.entries(process.env)) if (!name.startsWith('GIT_')) env[name] = value;
    const res = await exec(['git', 'rev-parse', '--verify', 'HEAD'], {
      cwd,
      env: { ...env, GIT_CONFIG_NOSYSTEM: '1', GIT_CONFIG_GLOBAL: process.platform === 'win32' ? 'NUL' : '/dev/null', GIT_TERMINAL_PROMPT: '0' },
      timeoutMs: 10000,
    });
    const sha = res.stdout.trim();
    return res.result === 'ok' && /^[0-9a-f]{40,64}$/.test(sha) ? sha : null;
  } catch {
    return null;
  }
}

/**
 * Write the block's done file after an end (its ledger rows are already written). Never throws: a
 * failure prints `warning: done file not written: <reason>` on stderr and returns null.
 * @param {{
 *   record: Record<string, any>, id: string, status: 'closed' | 'stopped', event: string, reason?: string | null,
 *   openFindings: (rows: Record<string, any>[]) => Promise<number>, err: (s: string) => unknown, now?: Date,
 * }} opts - `record` is the run record read before the end (its `workspace` and the block's
 *   `worktree` do not change at an end); `openFindings` counts the block's open findings exactly
 *   as `code-forge status` does.
 * @returns {Promise<string | null>} the done file's absolute path, or null when it was not written.
 */
export async function writeDoneFile({ record, id, status, event, reason = null, openFindings, err, now = new Date() }) {
  const runId = String(record?.run_id);
  try {
    const file = doneFilePath(runId, id);
    /** @type {Record<string, any>[] | null} */
    let rows = null;
    try {
      rows = await readAllRows(record.project);
    } catch {
      rows = null;
    }
    const spend = rows === null ? null : runSpend(rows.filter((r) => r?.block === id), runId);
    // like the spend: unknown (null) when the rows cannot be read or the count fails — never a 0
    /** @type {number | null} */
    let findings = null;
    if (rows !== null) {
      try {
        const n = await openFindings(rows);
        findings = Number.isInteger(n) && n >= 0 ? n : null;
      } catch {
        findings = null;
      }
    }
    const tree = record.blocks?.[id]?.worktree?.path;
    const commit = await headCommit(typeof tree === 'string' && tree.length > 0 ? tree : record.workspace);
    const content = doneFileContent({ block: id, runId, status, event, reason, commit, spend, findings, at: now });
    const dir = path.dirname(file);
    await mkdir(dir, { recursive: true, mode: 0o700 });
    const tmp = path.join(dir, `.${path.basename(file)}.${process.pid}.${Date.now()}.tmp`);
    try {
      await writeFile(tmp, `${JSON.stringify(content)}\n`, { mode: 0o600 });
      await rename(tmp, file);
    } catch (thrown) {
      await rm(tmp, { force: true }).catch(() => {});
      throw thrown;
    }
    return file;
  } catch (thrown) {
    err(`warning: done file not written: ${reasonOf(thrown)}\n`);
    return null;
  }
}

/** @param {unknown} thrown @returns {string} a one-line reason (an error code or message). */
function reasonOf(thrown) {
  const any = /** @type {any} */ (thrown);
  const text = typeof any?.code === 'string' ? any.code : typeof any?.message === 'string' ? any.message : String(thrown);
  return text.split('\n')[0];
}

/**
 * @typedef {{exit: number | null, signal: string | null, timedOut: boolean, notFound: boolean, error: string | null}} SpawnResult
 */

/** @param {unknown} thrown @returns {string} the error's code (`EACCES`, …), else `UNKNOWN` — never its message. */
function errorCode(thrown) {
  const code = /** @type {any} */ (thrown)?.code;
  return typeof code === 'string' && /^[A-Z0-9_]{1,32}$/.test(code) ? code : 'UNKNOWN';
}

/**
 * The warning's tail for a hook that did not exit 0 (ruling round 2 (b)), or null for a clean exit:
 * `exit N`, `timed out`, `not found`, `signal <NAME>` or `failed to start (<code>)`.
 * @param {SpawnResult} ran @returns {string | null}
 */
export function hookWarning(ran) {
  if (ran.timedOut) return 'timed out';
  if (ran.notFound) return 'not found';
  if (ran.error !== null) return `failed to start (${ran.error})`;
  if (typeof ran.exit === 'number') return ran.exit === 0 ? null : `exit ${ran.exit}`;
  if (typeof ran.signal === 'string') return `signal ${ran.signal}`;
  return 'failed to start (UNKNOWN)';
}

/**
 * Spawn the hook: no shell, stdin closed, stdout and stderr discarded, its own process group,
 * killed (the whole group) at the timeout.
 * @param {string[]} argv @param {{cwd: string, env: NodeJS.ProcessEnv, timeoutMs: number}} opts
 * `notFound` is an ENOENT for `argv[0]` itself; every other spawn error (a missing cwd included —
 * Node names `argv[0]` in that ENOENT too, so the cwd is checked first) is `error`, its code.
 * @returns {Promise<SpawnResult>}
 */
async function spawnHook(argv, { cwd, env, timeoutMs }) {
  try {
    if (!(await stat(cwd)).isDirectory()) return { exit: null, signal: null, timedOut: false, notFound: false, error: 'ENOTDIR' };
  } catch (thrown) {
    return { exit: null, signal: null, timedOut: false, notFound: false, error: errorCode(thrown) };
  }
  return new Promise((resolve) => {
    let timedOut = false;
    let settled = false;
    /** @type {NodeJS.Timeout | undefined} */
    let timer;
    const done = (/** @type {SpawnResult} */ r) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      resolve(r);
    };
    const failed = (/** @type {unknown} */ thrown) => {
      const any = /** @type {any} */ (thrown);
      const notFound = any?.code === 'ENOENT' && (any?.path === undefined || any.path === argv[0]);
      done({ exit: null, signal: null, timedOut: false, notFound, error: notFound ? null : errorCode(thrown) });
    };
    let child;
    try {
      child = spawn(argv[0], argv.slice(1), { cwd, env, shell: false, detached: true, stdio: ['ignore', 'ignore', 'ignore'] });
    } catch (thrown) {
      failed(thrown);
      return;
    }
    const pid = child.pid;
    child.on('error', failed);
    child.on('close', (code, signal) => done({ exit: code, signal: signal ?? null, timedOut, notFound: false, error: null }));
    timer = setTimeout(() => {
      timedOut = true;
      try {
        if (typeof pid === 'number' && pid > 1) process.kill(-pid, 'SIGKILL');
        else child.kill('SIGKILL');
      } catch {
        try {
          child.kill('SIGKILL');
        } catch {
          // already gone
        }
      }
    }, timeoutMs);
  });
}

/**
 * Run `hooks.on_block_end` once for an end, outside the run lock, then write one signed `hook.ran`
 * row under it (also when the hook failed, timed out or was not found). Never throws, never prints
 * on stdout; a hook that did not exit 0 prints one `warning: on_block_end hook <tail>` line on
 * stderr ({@link hookWarning}). `doneFile` is the empty string when the done file was not written.
 * @param {{
 *   hook: {argv: string[], timeoutS: number}, runId: string, id: string, status: string, doneFile: string,
 *   repoTop: string, writeRow: (row: Record<string, any>) => Promise<unknown>, err: (s: string) => unknown,
 * }} opts
 * @returns {Promise<Record<string, any> | null>} the row written (unsigned fields), or null when none was.
 */
export async function runBlockEndHook({ hook, runId, id, status, doneFile, repoTop, writeRow, err }) {
  const env = {
    ...process.env,
    CODE_FORGE_RUN: runId,
    CODE_FORGE_BLOCK: id,
    CODE_FORGE_BLOCK_STATUS: status,
    CODE_FORGE_DONE_FILE: doneFile,
  };
  const started = process.hrtime.bigint();
  /** @type {SpawnResult} */
  let ran;
  // "failed to start" covers the spawn only; a later failure gets neutral wording below
  try {
    ran = await spawnHook(hook.argv, { cwd: repoTop, env, timeoutMs: hook.timeoutS * 1000 });
  } catch (thrown) {
    ran = { exit: null, signal: null, timedOut: false, notFound: false, error: errorCode(thrown) };
  }
  try {
    const durationMs = Math.round(Number(process.hrtime.bigint() - started) / 1e6);
    const warning = hookWarning(ran);
    if (warning !== null) err(`warning: on_block_end hook ${warning}\n`);
    const row = {
      event: 'hook.ran',
      block: id,
      hook: 'on_block_end',
      argv0: path.basename(hook.argv[0]),
      exit: ran.exit,
      signal: ran.signal,
      timed_out: ran.timedOut,
      duration_ms: durationMs,
    };
    try {
      // clause 2: the hook.ran row is appended UNDER the run lock (writeSigned alone takes no lock)
      await withRunLock(runId, () => writeSigned(runId, writeRow, row));
    } catch (thrown) {
      // an error code only, never raw message text
      err(`warning: on_block_end hook row not written (${errorCode(thrown)})\n`);
      return null;
    }
    return row;
  } catch (thrown) {
    err(`warning: on_block_end hook not recorded (${errorCode(thrown)})\n`);
    return null;
  }
}
