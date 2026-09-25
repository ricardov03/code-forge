/**
 * The one place in the package that spawns a child process.
 *
 * Rules (see plan §8.2, §8.4):
 *  - argv only, never a shell string — `exec("rm -rf /")` is refused before anything spawns.
 *  - `shell: true` is refused outright — a shell reintroduces string parsing and injection.
 *  - `timeoutMs`, when given, must be a finite positive number; when omitted, the child can run
 *    indefinitely (callers that spawn a reviewer/coder CLI are expected to always pass one —
 *    this module does not impose a default, since a default here would silently apply to every
 *    caller, including ones with a legitimately long-running command).
 *  - On timeout the whole process GROUP is sent SIGTERM, then SIGKILL — the SIGKILL goes to the
 *    group as soon as the direct child exits (or after a grace period), so a grandchild that
 *    traps SIGTERM cannot outlive the call. The result is `failed`, never a partial success.
 *  - stdin is `ignore`d by default, so a child that reads stdin when it isn't a TTY (`claude -p`,
 *    for one) sees an immediate EOF instead of blocking forever. With `input` (a string or bytes),
 *    stdin is a pipe: the content is written verbatim and then the pipe is closed, so the child
 *    reads exactly those bytes followed by EOF (plan V3 — closed-book roles get the packet on
 *    stdin, never in argv).
 *  - Every child is recorded in the pid registry of this process's run root
 *    (`<run-root>/pids/<pid>.json`, `./tmp.mjs` + `./reaper.mjs`) and the entry is removed when
 *    the call settles. An entry that outlives its run (the parent was killed) is what the reaper
 *    uses to find and kill the orphan later. Registry I/O failing never fails the spawn: the
 *    registry is hygiene, the child is the caller's work.
 *  - This module does not know about the forbidden-command list (`./forbidden.mjs`); callers
 *    that spawn on a coder's behalf are expected to check argv against it first (`isForbidden`).
 *    `exec` only enforces the structural rules above (argv shape, no shell, valid timeout).
 *
 * ## Process groups and the parent's own death
 *
 * Every child is spawned `detached: true`, i.e. as the leader of its own process group, so a kill
 * reaches its grandchildren (npx wrappers, git hooks, the agent CLI's own subprocesses). The price
 * is that the child is no longer in the terminal's foreground group: Ctrl-C (SIGINT), a SIGTERM
 * or a SIGHUP sent to code-forge would NOT reach it, and it would run on as an orphan. So this
 * module keeps a registry of live child groups and, while any is live:
 *  - forwards SIGINT/SIGTERM/SIGHUP received by the parent to every live group; if no other
 *    listener handles that signal, it then removes its own handlers and re-raises the signal on
 *    the parent, so the parent still dies of it exactly as it would have by default;
 *  - on the parent's `exit`, sends SIGTERM to every group still live.
 * The handlers are installed with the first live child and removed with the last, so a process
 * that has no child running keeps Node's default signal behaviour.
 *
 * @typedef {object} ExecResult
 * @property {"ok"|"failed"} result
 * @property {number|null} code
 * @property {NodeJS.Signals|null} signal
 * @property {string} stdout
 * @property {string} stderr
 * @property {boolean} timedOut
 * @property {string} [error]
 */

import { spawn } from 'node:child_process';
import { registerPid, unregisterPid } from './reaper.mjs';
import { pidsDir } from './tmp.mjs';

/** Grace period after SIGTERM before escalating to SIGKILL, in milliseconds. */
const KILL_GRACE_MS = 2000;

/** Default cap per stream (stdout, stderr independently) before a runaway child is killed. */
const DEFAULT_MAX_BUFFER_BYTES = 10 * 1024 * 1024;

/** Signals the parent forwards to every live child group. */
const FORWARDED_SIGNALS = /** @type {const} */ (['SIGINT', 'SIGTERM', 'SIGHUP']);

/** Pids of live children — each is also the id of the child's process group. */
const liveGroups = new Set();

/**
 * Send `signal` to a whole process group; ignore a group that is already gone (ESRCH) or a
 * platform without process groups.
 * @param {number} pid
 * @param {NodeJS.Signals} signal
 * @returns {boolean} whether the group signal was delivered.
 */
function signalGroup(pid, signal) {
  try {
    process.kill(-pid, signal);
    return true;
  } catch {
    return false;
  }
}

/** @type {Map<NodeJS.Signals, () => void>} */
const signalHandlers = new Map();

const onParentExit = () => {
  for (const pid of liveGroups) {
    signalGroup(pid, 'SIGTERM');
  }
};

function installParentHandlers() {
  if (signalHandlers.size > 0) return;
  for (const signal of FORWARDED_SIGNALS) {
    const handler = () => {
      for (const pid of liveGroups) {
        signalGroup(pid, signal);
      }
      // Ours is the only listener: Node's default action (terminate) was suppressed by our
      // listener's mere presence, so restore it and re-raise — the parent dies of the signal.
      if (process.listenerCount(signal) === 1) {
        uninstallParentHandlers();
        process.kill(process.pid, signal);
      }
    };
    signalHandlers.set(signal, handler);
    process.on(signal, handler);
  }
  process.on('exit', onParentExit);
}

function uninstallParentHandlers() {
  for (const [signal, handler] of signalHandlers) {
    process.removeListener(signal, handler);
  }
  signalHandlers.clear();
  process.removeListener('exit', onParentExit);
}

/** @param {number} pid */
function trackChild(pid) {
  liveGroups.add(pid);
  installParentHandlers();
}

/** @param {number} pid */
function untrackChild(pid) {
  liveGroups.delete(pid);
  if (liveGroups.size === 0) {
    uninstallParentHandlers();
  }
}

/**
 * Spawn `argv[0]` with `argv.slice(1)` as arguments. Never invokes a shell.
 *
 * @param {string[]} argv - The full command line, e.g. `["git", "status"]`.
 * @param {object} [opts]
 * @param {string} [opts.cwd]
 * @param {NodeJS.ProcessEnv} [opts.env]
 * @param {number} [opts.timeoutMs] - Kill the child (and its process group) if it runs longer
 *   than this. Must be a finite number > 0 when given.
 * @param {number[]} [opts.okExitCodes] - Exit codes treated as `result: "ok"` (default `[0]`).
 *   Some read-only git subcommands (e.g. `diff --no-index`, `merge-base --is-ancestor`) use a
 *   non-zero exit code to report a normal "no" answer, not a failure.
 * @param {number} [opts.maxBufferBytes] - Per-stream cap (default 10 MiB). Exceeding it on
 *   either stream kills the child and resolves `result: "failed"`; the stream's collected text is
 *   cut at exactly `maxBufferBytes` bytes.
 * @param {string | Uint8Array} [opts.input] - Content written to the child's stdin, followed by
 *   EOF. Omitted ⇒ stdin is `ignore`.
 * @param {boolean} [opts.shell] - Forbidden. Only exists so passing it throws immediately.
 * @returns {Promise<ExecResult>}
 */
export function exec(argv, opts = {}) {
  if (!Array.isArray(argv) || argv.length === 0 || !argv.every((a) => typeof a === 'string')) {
    throw new TypeError('exec: argv must be a non-empty array of strings, not a shell string');
  }
  if (opts.shell) {
    throw new TypeError('exec: the "shell" option is forbidden — pass argv as an array instead');
  }
  if (opts.timeoutMs !== undefined && !(Number.isFinite(opts.timeoutMs) && opts.timeoutMs > 0)) {
    throw new TypeError('exec: timeoutMs must be a finite number greater than 0 when given');
  }
  if (opts.input !== undefined && typeof opts.input !== 'string' && !(opts.input instanceof Uint8Array)) {
    throw new TypeError('exec: input must be a string or a Uint8Array/Buffer when given');
  }

  const { cwd, env, timeoutMs, input, okExitCodes = [0], maxBufferBytes = DEFAULT_MAX_BUFFER_BYTES } = opts;
  const [command, ...args] = argv;

  return new Promise((resolve) => {
    const stdinMode = input === undefined ? 'ignore' : 'pipe';
    const child = spawn(command, args, { cwd, env, shell: false, detached: true, stdio: [stdinMode, 'pipe', 'pipe'] });
    const pid = child.pid;
    /** @type {string | null} */
    let registryDir = null;
    if (typeof pid === 'number') {
      trackChild(pid);
      try {
        registryDir = pidsDir();
        registerPid(registryDir, pid, command);
      } catch {
        registryDir = null;
      }
    }
    if (input !== undefined && child.stdin) {
      // A child that exits without reading its stdin makes the write fail with EPIPE; that is
      // the child's business (its exit code says what happened), not an unhandled error here.
      child.stdin.on('error', () => {});
      child.stdin.end(input);
    }

    /** @type {Buffer[]} */
    const stdoutChunks = [];
    /** @type {Buffer[]} */
    const stderrChunks = [];
    const byteCounts = { stdout: 0, stderr: 0 };
    let timedOut = false;
    let overBuffer = false;
    let settled = false;
    /** @type {NodeJS.Timeout | undefined} */
    let termTimer;
    /** @type {NodeJS.Timeout | undefined} */
    let killTimer;

    /** Kill the child's whole process group; falls back to the direct child (e.g. on Windows). */
    const killGroup = (/** @type {NodeJS.Signals} */ signal) => {
      if (typeof pid === 'number' && signalGroup(pid, signal)) {
        return;
      }
      child.kill(signal);
    };

    const stdoutText = () => Buffer.concat(stdoutChunks).toString('utf8');
    const stderrText = () => Buffer.concat(stderrChunks).toString('utf8');

    /** @param {ExecResult} result */
    const settle = (result) => {
      if (settled) return;
      settled = true;
      if (termTimer) clearTimeout(termTimer);
      if (killTimer) clearTimeout(killTimer);
      if (typeof pid === 'number') {
        untrackChild(pid);
        if (registryDir !== null) {
          try {
            unregisterPid(registryDir, pid);
          } catch {
            // hygiene only — see the module comment
          }
        }
      }
      child.stdin?.destroy();
      child.stdout?.destroy();
      child.stderr?.destroy();
      resolve(result);
    };

    if (timeoutMs) {
      termTimer = setTimeout(() => {
        timedOut = true;
        killGroup('SIGTERM');
        killTimer = setTimeout(() => {
          killGroup('SIGKILL');
        }, KILL_GRACE_MS);
      }, timeoutMs);
    }

    /**
     * @param {'stdout'|'stderr'} stream
     * @param {Buffer[]} chunks
     * @param {Buffer} chunk
     */
    const collect = (stream, chunks, chunk) => {
      if (overBuffer) return;
      const room = maxBufferBytes - byteCounts[stream];
      if (chunk.length > room) {
        // A pipe read can merge many child writes into one event, so cut to the exact cap
        // rather than appending the whole event and overshooting it.
        chunks.push(chunk.subarray(0, room));
        byteCounts[stream] = maxBufferBytes;
        overBuffer = true;
        killGroup('SIGKILL');
        return;
      }
      chunks.push(chunk);
      byteCounts[stream] += chunk.length;
    };

    // Not optional chaining: stdout/stderr are always 'pipe' above, so child.stdout
    // and child.stderr are always real Readable streams, never null — Node only sets a stdio
    // stream to null for 'ignore'/'inherit' on that fd, neither of which is used for fds 1/2.
    child.stdout.on('data', (/** @type {Buffer} */ chunk) => collect('stdout', stdoutChunks, chunk));
    child.stderr.on('data', (/** @type {Buffer} */ chunk) => collect('stderr', stderrChunks, chunk));

    child.on('error', (err) => {
      settle({
        result: 'failed',
        code: null,
        signal: null,
        stdout: stdoutText(),
        stderr: stderrText(),
        timedOut,
        error: err.message,
      });
    });

    // Once we've decided to kill (timeout or over-buffer), don't wait for 'close' — a
    // grandchild that inherited the pipes can keep them open indefinitely, and 'close' would
    // then never fire even though the child we care about is dead. 'exit' fires as soon as the
    // child process itself has terminated, which is what "killed after timeout_ms" means. Before
    // settling (which cancels the grace timer), SIGKILL the group: a grandchild that trapped the
    // SIGTERM must not survive just because its parent died first.
    child.on('exit', (code, signal) => {
      if (timedOut || overBuffer) {
        killGroup('SIGKILL');
        settle({
          result: 'failed',
          code,
          signal,
          stdout: stdoutText(),
          stderr: stderrText(),
          timedOut,
          ...(overBuffer ? { error: 'exec: maxBufferBytes exceeded' } : {}),
        });
      }
    });

    child.on('close', (code, signal) => {
      const result = !timedOut && !overBuffer && code !== null && okExitCodes.includes(code) ? 'ok' : 'failed';
      settle({ result, code, signal, stdout: stdoutText(), stderr: stderrText(), timedOut });
    });
  });
}
