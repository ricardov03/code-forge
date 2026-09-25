/**
 * Where run state lives (plan §1.2, §4.9, §8.6) and the one error type the state verbs throw.
 *
 *  - `~/.code-forge/runs/<run>.json` — the AUTHORITATIVE run record (registry + worker pin),
 *    outside every workspace; `~/.code-forge/runs/<run>.key` — the per-run HMAC key, mode 0600.
 *  - `<workspace>/.code-forge/runs/<run>.json` — an informational mirror, minus the worker pin.
 *
 * `os.homedir()` re-reads `$HOME` on every call (POSIX), so a test that points `HOME` at a temp
 * dir never touches a real `~/.code-forge/`.
 */

import { randomBytes } from 'node:crypto';
import { realpathSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';

/** A refusal with a stable machine-readable `code` (the CLI prints the message, exits 1). */
export class StateError extends Error {
  /** @param {string} code @param {string} message */
  constructor(code, message) {
    super(message);
    this.name = 'StateError';
    this.code = code;
  }
}

const RUN_ID_PATTERN = /^[a-z0-9][a-z0-9-]{0,63}$/;

/**
 * @param {unknown} runId
 * @returns {string}
 * @throws {StateError} `bad-run-id` — a run id must never resolve outside the runs directory.
 */
export function assertRunId(runId) {
  if (typeof runId !== 'string' || !RUN_ID_PATTERN.test(runId)) {
    throw new StateError('bad-run-id', `run id must match ${RUN_ID_PATTERN}`);
  }
  return runId;
}

/** @param {Date} [now] @returns {string} e.g. `r-20260924-201500-3fa9c1` */
export function newRunId(now = new Date()) {
  const stamp = now.toISOString().replace(/[-:]/g, '').replace('T', '-').slice(0, 15);
  return `r-${stamp}-${randomBytes(3).toString('hex')}`;
}

/** @returns {string} */
export function runsDir() {
  return path.join(os.homedir(), '.code-forge', 'runs');
}

/** @param {string} runId @returns {string} */
export function runRecordPath(runId) {
  return path.join(runsDir(), `${assertRunId(runId)}.json`);
}

/** @param {string} runId @returns {string} */
export function runKeyPath(runId) {
  return path.join(runsDir(), `${assertRunId(runId)}.key`);
}

/** @param {string} p @returns {string} the real path when it exists, else the resolved one */
function canonical(p) {
  try {
    return realpathSync(p);
  } catch {
    return path.resolve(p);
  }
}

/**
 * The run record must live OUTSIDE the workspace (C4). A workspace that is `$HOME` or one of its
 * ancestors would hold `~/.code-forge/runs/` inside it — and with workspace === `$HOME` the
 * mirror would overwrite the authoritative record (dropping the worker pin).
 * @param {string} workspace
 * @returns {string} the canonical workspace path
 * @throws {StateError} `workspace-contains-runs-dir`
 */
export function assertWorkspaceOutsideRuns(workspace) {
  const ws = canonical(workspace);
  const rel = path.relative(ws, path.join(canonical(os.homedir()), '.code-forge', 'runs'));
  if (!rel.startsWith('..') && !path.isAbsolute(rel)) {
    throw new StateError('workspace-contains-runs-dir', `the workspace ${ws} contains ${runsDir()} — run code-forge from a project directory, not $HOME or above`);
  }
  return ws;
}

/** @param {string} workspace @param {string} runId @returns {string} */
export function mirrorPath(workspace, runId) {
  assertWorkspaceOutsideRuns(workspace);
  return path.join(workspace, '.code-forge', 'runs', `${assertRunId(runId)}.json`);
}
