/**
 * B80: the human at a terminal, for tests. `block waive` and `block reopen` (and `block close
 * --no-require-reviews`) ask the human at a terminal; a test that only needs the verb to run
 * passes `HUMAN_SAYS_YES` (both sides a TTY, a prompt that answers yes) in `runBlock`'s deps, or
 * runs the verb with `humanBlock` (in process, so the test controls the TTY flags).
 *
 * `src` is imported lazily, so the caller's HOME isolation is in place before it loads.
 */

/**
 * A prompt that answers `answer` and records every question it was asked.
 * @param {unknown} [answer] @returns {{ui: {confirm: (opts: {message: string, initialValue?: boolean}) => Promise<unknown>, isCancel: (v: unknown) => boolean}, calls: Array<{message: string, initialValue?: boolean}>}}
 */
export function countingUi(answer = true) {
  /** @type {Array<{message: string, initialValue?: boolean}>} */
  const calls = [];
  return {
    ui: {
      confirm: async (opts) => {
        calls.push(opts);
        return answer;
      },
      isCancel: () => false,
    },
    calls,
  };
}

/** Both sides a TTY and a prompt that answers yes — merge into `runBlock`'s deps. */
export const HUMAN_SAYS_YES = Object.freeze({
  isTTY: true,
  stdinIsTTY: true,
  stdoutIsTTY: true,
  ui: Object.freeze({ confirm: async () => true, isCancel: () => false }),
});

/**
 * `deps` with the human saying yes at a terminal; the caller's own TTY flags or `ui` win.
 * @template {Record<string, any>} T @param {T} [deps] @returns {typeof HUMAN_SAYS_YES & T}
 */
export function humanSaysYes(deps = /** @type {T} */ ({})) {
  return { ...HUMAN_SAYS_YES, ...deps };
}

/**
 * Run `code-forge block <args…>` in process for the fixture repo `repo`, with the human saying yes;
 * the result has the shape of a CLI run (`{code, stdout, stderr}`) plus the prompt's questions
 * (`asked`). `block` finds its workspace through `--run` (it reads no cwd), so the helper first
 * checks that the run's workspace IS `repo` — a test can never act on another fixture's run.
 * The prompt is always the helper's counting ui and the output always its own capture: a `deps`
 * that carries `ui`, `stdout` or `stderr` throws a TypeError (so `asked` is always the truth).
 * @param {string} repo @param {string[]} args @param {Record<string, any>} [deps] other `runBlock` deps (e.g. `now`)
 * @returns {Promise<{code: number, stdout: string, stderr: string, asked: Array<{message: string, initialValue?: boolean}>}>}
 */
export async function humanBlock(repo, args, deps = {}) {
  for (const own of ['ui', 'stdout', 'stderr']) if (Object.hasOwn(deps, own)) throw new TypeError(`humanBlock owns ${own}; do not pass it in deps`);
  const at = args.indexOf('--run');
  if (typeof repo !== 'string' || at < 0 || typeof args[at + 1] !== 'string') throw new TypeError('humanBlock needs the fixture repo and a --run <id> argument');
  const { realpathSync } = await import('node:fs');
  const { readRun } = await import('../../src/state/run.mjs');
  const record = await readRun(args[at + 1]);
  if (realpathSync(record.workspace) !== realpathSync(repo)) throw new Error(`humanBlock: run ${args[at + 1]} works in ${record.workspace}, not in the fixture repo ${repo}`);
  const { runBlock } = await import('../../src/cli/block.mjs');
  const { ui, calls } = countingUi(true);
  let stdout = '';
  let stderr = '';
  const code = await runBlock(args, humanSaysYes({ ui, ...deps, stdout: { write: (/** @type {string} */ s) => ((stdout += s), true) }, stderr: { write: (/** @type {string} */ s) => ((stderr += s), true) } }));
  return { code, stdout, stderr, asked: calls };
}
