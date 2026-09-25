/**
 * Shared test-only helpers for `test/ledger/**` (fix round 1 findings: HOME-restore bug repeated
 * across write/cli/outcome tests; git calls not isolated from the developer's global config).
 */

import assert from 'node:assert/strict';
import { mkdtemp, rm } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';

/**
 * Point `process.env.HOME` at a fresh temp dir for the duration of `fn`, then restore it exactly
 * — `delete`, not `= undefined`, when it was unset going in (an earlier bug: assigning the STRING
 * "undefined" pollutes every later test in the same process).
 * @param {(home: string) => Promise<void>} fn
 */
export async function withTempHome(fn) {
  const home = await mkdtemp(path.join(os.tmpdir(), 'code-forge-home-'));
  const original = process.env.HOME;
  process.env.HOME = home;
  try {
    await fn(home);
  } finally {
    if (original === undefined) delete process.env.HOME;
    else process.env.HOME = original;
    await rm(home, { recursive: true, force: true });
  }
}

/**
 * Env additions that stop a `git` child process from reading the developer's real global/system
 * config (commit signing prompts, hooks, `init.defaultBranch`, identity) — layered on top of
 * `GIT_AUTHOR_*`/`GIT_COMMITTER_*` by callers that also need a controlled commit identity/date.
 * @returns {NodeJS.ProcessEnv}
 */
export function isolatedGitEnv() {
  return { GIT_CONFIG_NOSYSTEM: '1', GIT_CONFIG_GLOBAL: os.devNull };
}

/** @returns {{write: (s: string) => boolean, text: string}} */
export function captureStream() {
  let text = '';
  return {
    write(s) {
      text += s;
      return true;
    },
    get text() {
      return text;
    },
  };
}

/**
 * Assert that `stderrText` is a real, non-empty error message with NO raw stack frame in it — not
 * `stderr.text.includes('at ')`, which is both fragile (an ordinary word like "that " or "format "
 * false-positives it) and weak (an implementation that fails silently, printing nothing, still
 * passes it, since an empty string also doesn't include "at ").
 * @param {string} stderrText
 */
export function assertCleanError(stderrText) {
  assert.ok(stderrText.length > 0, 'expected a non-empty error message on stderr, got nothing');
  assert.doesNotMatch(stderrText, /\n\s+at .+:\d+:\d+/, 'stderr must not contain a raw stack frame');
}
