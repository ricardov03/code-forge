/**
 * The gate runner (plan §4.6 row 3: "all gates green in one run — each exit status read"). Runs
 * every configured gate command (`test`, `lint`, `types`, `format`) INDEPENDENTLY — never chained
 * with `&&`, so a red gate never hides the ones after it: "a red gate in the middle of 3 is
 * reported red" means each of the three still gets its own read exit status, not that the run
 * stops early.
 */

import { exec } from '../util/exec.mjs';

/** @typedef {import('./detect.mjs').Gate} Gate */
/** @typedef {import('./detect.mjs').GateCommand} GateCommand */
/** @typedef {import('./detect.mjs').DetectedGates} DetectedGates */

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
 */

/** Fixed run order — always all four, regardless of an earlier one's result. */
const GATE_ORDER = /** @type {const} */ (['test', 'lint', 'types', 'format']);

/**
 * A gate command is either a plain argv array or `{argv, failOnStdout: true}` (detect.mjs's
 * `GateCommand`, e.g. `gofmt -l` which always exits 0). Normalized here so the run loop below has
 * one shape to deal with.
 * @param {GateCommand} command
 * @returns {{argv: string[], failOnStdout: boolean}}
 */
function normalizeCommand(command) {
  if (Array.isArray(command)) return { argv: command, failOnStdout: false };
  return { argv: command.argv, failOnStdout: command.failOnStdout === true };
}

/**
 * @param {{test?: Gate, lint?: Gate, types?: Gate, format?: Gate}} gates
 * @param {{cwd: string, timeoutMs?: number}} opts
 * @returns {Promise<{results: GateResult[], allOk: boolean}>}
 */
export async function runGates(gates, opts) {
  if (!gates || typeof gates !== 'object') {
    throw new TypeError('runGates: gates must be an object with test/lint/types/format');
  }
  const { cwd, timeoutMs = 300000 } = opts ?? {};
  if (typeof cwd !== 'string' || cwd.length === 0) {
    throw new TypeError('runGates: opts.cwd must be a non-empty string');
  }

  /** @type {GateResult[]} */
  const results = [];
  for (const gate of GATE_ORDER) {
    const command = gates[gate] ?? null;
    if (command === null) {
      results.push({ gate, argv: null, skipped: true, ok: true, code: null, stdout: '', stderr: '' });
      continue;
    }
    const { argv, failOnStdout } = normalizeCommand(command);
    // Each command is run and its exit status read on its own — never `&&`-chained with the
    // next, so a red gate is reported red without swallowing the gates that follow it.
    const res = await exec(argv, { cwd, timeoutMs });
    const ok = res.result === 'ok' && (!failOnStdout || res.stdout.trim().length === 0);
    results.push({ gate, argv, skipped: false, ok, code: res.code, stdout: res.stdout, stderr: res.stderr });
  }
  return { results, allOk: results.every((r) => r.ok) };
}
