/**
 * The invariants gate (plan §1.3 `gates.extra.invariants`: a project-configured PATH, unlike
 * `secret_scan`/`safe_edit` which are booleans). A project may keep its own structural checks —
 * comment-only diffs, forbidden patterns, naming conventions — as one executable script; when
 * `gates.extra.invariants` names it, this gate runs it exactly like `run.mjs` runs `test`/`lint`/
 * `types`/`format`: one argv, its own exit status read on its own, never chained.
 *
 * Unset (`undefined`/empty) is a pass-through, not a failure — a project with no invariants script
 * simply has nothing extra to check.
 */

import path from 'node:path';
import { exec } from '../util/exec.mjs';

/**
 * @typedef {object} InvariantsResult
 * @property {boolean} skipped - true when no `invariantsPath` was configured.
 * @property {boolean} ok
 * @property {number | null} code
 * @property {string} stdout
 * @property {string} stderr
 */

/**
 * @param {{cwd: string, invariantsPath?: string | null, timeoutMs?: number}} opts
 * @returns {Promise<InvariantsResult>}
 */
export async function runInvariants(opts) {
  const { cwd, invariantsPath, timeoutMs = 120000 } = opts ?? {};
  if (typeof cwd !== 'string' || cwd.length === 0) {
    throw new TypeError('runInvariants: opts.cwd must be a non-empty string');
  }
  if (!invariantsPath) {
    return { skipped: true, ok: true, code: null, stdout: '', stderr: '' };
  }
  // A bare filename (no slash) would otherwise be looked up on PATH instead of the project root,
  // and a relative path would resolve against the CHILD process's cwd only by accident (it
  // happens to be the same as `cwd` here, but that's not guaranteed once this call has a caller
  // with its own cwd) — resolve against the project root explicitly either way.
  const resolvedPath = path.resolve(cwd, invariantsPath);
  const res = await exec([resolvedPath], { cwd, timeoutMs });
  return { skipped: false, ok: res.result === 'ok', code: res.code, stdout: res.stdout, stderr: res.stderr };
}
