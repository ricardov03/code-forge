/**
 * The gate runner (plan §4.6 row 3: "all gates green in one run — each exit status read"). Runs
 * every configured gate command (`test`, `lint`, `types`, `format`) INDEPENDENTLY — never chained
 * with `&&`, so a red gate never hides the ones after it: "a red gate in the middle of 3 is
 * reported red" means each of the three still gets its own read exit status, not that the run
 * stops early.
 */

import { exec } from '../util/exec.mjs';
import { registerSecret } from '../util/redact.mjs';
import { CHANGED_FILES_TOKEN } from '../util/gate-token.mjs';
import { NO_BLOCK_SCOPE, TEST_FILES_TOKEN, changedTestFiles, envOf, expandArgv, maskableEnvValues, scopeFields } from './expand.mjs';

/** @typedef {import('./detect.mjs').Gate} Gate */
/** @typedef {import('./detect.mjs').GateCommand} GateCommand */
/** @typedef {import('./detect.mjs').DetectedGates} DetectedGates */
/** @typedef {import('./expand.mjs').TestScope} TestScope */
/** @typedef {{n: number, files: string[], failed: boolean}} Repeats */
/**
 * Internal block context carried with the gate plan through the proof lock, so repeats run
 * inside the same slot as the initial commands.
 * @typedef {{n: number, base: string, owned: string[], kinds?: Record<string, string>, onSkip?: (line: string) => void}} RepeatNewTests
 */

/**
 * @typedef {object} GateResult
 * @property {'test'|'lint'|'types'|'format'} gate
 * @property {string[] | null} argv
 * @property {boolean} skipped - true when the gate has no configured command (argv was null).
 * @property {boolean} ok - true when skipped, or when the command exited with code 0 (and, for a
 *   `failOnStdout` gate, its stdout was empty).
 * @property {number | null} code
 * @property {string} stdout
 * @property {string} stderr
 * @property {'related'|'full'|'changed'} [scope] - only on a gate with a scope token.
 * @property {number} [test_count] - B60: with `scope: related`, how many test files it ran.
 * @property {'threshold'|'no_tests'|'no_baseline'|'unpaired'|'no_changes'|'flaky-test'} [reason] - fallback or repeat failure.
 * @property {Repeats} [repeats]
 * @property {string[]} [env_keys] - B60: only on a gate given with a non-empty `env`: the variable
 *   NAMES, sorted (the values are never in a result).
 */

/** Fixed run order — always all four, regardless of an earlier one's result. */
const GATE_ORDER = /** @type {const} */ (['test', 'lint', 'types', 'format']);

/**
 * A gate command is either a plain argv array or `{argv, failOnStdout?, env?}` (detect.mjs's
 * `GateCommand`: `failOnStdout` for `gofmt -l`, which always exits 0; B60 `env` from the config).
 * Normalized here so the run loop below has one shape to deal with.
 * @param {GateCommand} command
 * @returns {{argv: string[], failOnStdout: boolean, env: Record<string, string> | null}}
 */
function normalizeCommand(command) {
  if (Array.isArray(command)) return { argv: command, failOnStdout: false, env: null };
  return { argv: command.argv, failOnStdout: command.failOnStdout === true, env: envOf(command) }; // `env: {}` is no env
}

/**
 * B60: `opts.scope` is the block's {@link TestScope} (`testScope`, gates/expand.mjs); a gate argv
 * item that is exactly `{test_files}` expands to its files, or is dropped for a full run. Left out,
 * no test file is known (`scope: full, reason: no_tests`). A gate with `env` runs with those
 * variables added to the inherited environment; a gate with neither runs exactly as before B60.
 * @param {{test?: Gate, lint?: Gate, types?: Gate, format?: Gate, repeatNewTests?: RepeatNewTests}} gates
 * @param {{cwd: string, timeoutMs?: number, scope?: TestScope | import('./expand.mjs').ChangedScope}} opts
 * @returns {Promise<{results: GateResult[], allOk: boolean, repeats?: Repeats}>}
 */
export async function runGates(gates, opts) {
  if (!gates || typeof gates !== 'object') {
    throw new TypeError('runGates: gates must be an object with test/lint/types/format');
  }
  const { cwd, timeoutMs = 300000 } = opts ?? {};
  const scope = opts?.scope ?? NO_BLOCK_SCOPE; // null falls back too
  if (typeof cwd !== 'string' || cwd.length === 0) {
    throw new TypeError('runGates: opts.cwd must be a non-empty string');
  }

  // B60: gate env is not a secret store. Its values are never put in a result (only `env_keys`);
  // a value of 8+ characters is also masked in the child's own output (a shorter one would mask
  // unrelated text — `MODE=test` vs `npm test`)
  for (const value of maskableEnvValues(gates)) registerSecret(value);
  // Every gate is normalized and expanded BEFORE any runs, so a bad gate throws with nothing run.
  // B60: config validation refuses a gate whose only item is {test_files}; the empty-argv check
  // is the guard for one that reaches here anyway: it names the gate, not an opaque spawn error.
  const planned = GATE_ORDER.map((gate) => {
    const command = gates[gate] ?? null;
    if (command === null) return { gate, command: null };
    const normalized = normalizeCommand(command);
    if (normalized.argv[0] === CHANGED_FILES_TOKEN) throw new TypeError(`gates.${gate}: '{changed_files}' cannot be the command (argv[0])`);
    const { argv, scoped } = expandArgv(normalized.argv, scope);
    if (argv.length === 0 && normalized.argv.length > 0) throw new TypeError(`gates.${gate}: the argv is empty once ${normalized.argv.map((item) => `'${item}'`).join(' and ')} is dropped for the full suite — put the test command before it`);
    return { gate, command: { argv, originalArgv: normalized.argv, scoped, failOnStdout: normalized.failOnStdout, env: normalized.env } };
  });
  /** @type {GateResult[]} */
  const results = [];
  for (const { gate, command } of planned) {
    if (command === null) {
      results.push({ gate, argv: null, skipped: true, ok: true, code: null, stdout: '', stderr: '' });
      continue;
    }
    const { argv, scoped, failOnStdout, env } = command;
    // Each command is run and its exit status read on its own — never `&&`-chained with the
    // next, so a red gate is reported red without swallowing the gates that follow it.
    const res = await exec(argv, env ? { cwd, timeoutMs, env: { ...process.env, ...env } } : { cwd, timeoutMs });
    const ok = res.result === 'ok' && (!failOnStdout || res.stdout.trim().length === 0);
    const result = /** @type {GateResult} */ ({
      gate,
      argv,
      skipped: false,
      ok,
      code: res.code,
      stdout: res.stdout,
      stderr: res.stderr,
      ...(scoped ? scopeFields(scope) : {}),
      ...(env ? { env_keys: Object.keys(env).sort() } : {}),
    });
    const repeat = gates.repeatNewTests;
    if (gate === 'test' && ok && repeat) {
      if (!command.originalArgv.includes(TEST_FILES_TOKEN)) {
        repeat.onSkip?.('repeat_new_tests: skipped (gates.test has no {test_files})');
      } else {
        const files = await changedTestFiles({ cwd, ...repeat });
        if (files.length > 0) {
          result.repeats = { n: repeat.n, files, failed: false };
          const repeatArgv = expandArgv(command.originalArgv, { scope: 'related', files }).argv;
          for (let i = 0; i < repeat.n; i++) {
            const repeated = await exec(repeatArgv, env ? { cwd, timeoutMs, env: { ...process.env, ...env } } : { cwd, timeoutMs });
            const passed = repeated.result === 'ok' && (!failOnStdout || repeated.stdout.trim().length === 0);
            // Keep the first failed run's output, even when a later repeat passes.
            if (!passed && !result.repeats.failed) {
              result.repeats.failed = true;
              Object.assign(result, { ok: false, reason: 'flaky-test', argv: repeatArgv, code: repeated.code, stdout: repeated.stdout, stderr: repeated.stderr });
            }
          }
        }
      }
    }
    results.push(result);
  }
  const repeats = results.find((r) => r.repeats)?.repeats;
  return { results, allOk: results.every((r) => r.ok), ...(repeats ? { repeats } : {}) };
}
