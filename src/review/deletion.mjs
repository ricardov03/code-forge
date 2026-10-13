/**
 * The deletion note (block B74, issue #8): a packet for a pure deletion (the file is gone from the
 * reviewed tree, its diff is all `-` lines) gets a `## deleted file` section before the diff. It
 * says the file is deleted and lists the tree paths that still name the file's basename, so the
 * reviewer checks for dangling references.
 *
 * The reviewed tree is the working tree the review engine diffs (`reviewFile`'s `repoRoot`: the
 * block's worktree, or the main tree), never a commit: the paths come from ONE
 * `git grep -l -z --untracked -F -e <basename> -- .` run with that tree as cwd (argv only, the
 * review git env `reviewGitEnv`: no inherited `GIT_*` variable, `GIT_CONFIG_NOSYSTEM=1`,
 * `GIT_CONFIG_GLOBAL` the null device, `GIT_LITERAL_PATHSPECS=1`). `--untracked` adds the files a
 * block created and has not committed yet (never ignored ones). The list is sorted, never names the
 * deleted path itself or a secret-like path (`isSecretLike`), and stops at {@link MAX_REFERENCES}
 * paths; a path with a control character (it could break the packet layout) is never printed, only
 * counted with the paths past the cap (`(<n> more not listed)`). No match ⇒ `no references found`.
 * A basename shorter than {@link MIN_BASENAME} characters is not grepped, nor is a deleted path
 * with a control character (it is printed JSON-escaped), and a grep that fails (or times out) is
 * not trusted: all say `references not checked`. The note is a hint, never an exemption: a
 * deletion is reviewed and needs a signed approval like any other change (a grep cannot prove that
 * nothing depends on a file).
 */

import path from 'node:path';
import { exec } from '../util/exec.mjs';
import { isSecretLike, reviewGitEnv } from './packet.mjs';

/** At most this many referencing paths are listed. */
export const MAX_REFERENCES = 20;

/** A basename shorter than this matches too much to be a useful hint: it is not grepped. */
export const MIN_BASENAME = 3;

export const NO_REFERENCES = 'no references found';
export const NOT_CHECKED = 'references not checked';

const GREP_TIMEOUT_MS = 30000;

const CONTROL = /[\x00-\x1f\x7f]/;

/** @param {string} p @returns {boolean} whether `p` has a control character. */
const hasControl = (p) => CONTROL.test(p);

/**
 * @typedef {object} References
 * @property {'found' | 'none' | 'not_checked'} status
 * @property {string[]} paths - sorted, at most {@link MAX_REFERENCES}.
 * @property {number} more - matching paths not listed: past the cap, or with a control character.
 */

/** @type {References} */
const NOT_CHECKED_REFS = Object.freeze({ status: 'not_checked', paths: [], more: 0 });

/**
 * The paths of the reviewed tree (`repoRoot`, the grep's cwd) that still name `file`'s basename.
 * @param {{repoRoot: string, file: string, run?: typeof exec}} opts - `run`: the child runner
 *   (default `exec`; a test seam).
 * @returns {Promise<References>}
 */
export async function findReferences({ repoRoot, file, run = exec }) {
  const base = path.posix.basename(file);
  if (base.length < MIN_BASENAME || hasControl(file)) return { ...NOT_CHECKED_REFS, paths: [] };
  let res;
  try {
    res = await run(['git', 'grep', '-l', '-z', '--untracked', '-F', '-e', base, '--', '.'], { cwd: repoRoot, env: reviewGitEnv(), timeoutMs: GREP_TIMEOUT_MS, okExitCodes: [0, 1] });
  } catch {
    return { ...NOT_CHECKED_REFS, paths: [] };
  }
  if (res?.result !== 'ok' || (res.code !== 0 && res.code !== 1) || typeof res.stdout !== 'string') return { ...NOT_CHECKED_REFS, paths: [] };
  const matched = [...new Set(res.stdout.split('\0').filter((p) => p.length > 0 && p !== file && !isSecretLike(p)))];
  const unsafe = matched.filter(hasControl).length;
  const clean = matched.filter((p) => !hasControl(p)).sort();
  if (clean.length === 0 && unsafe === 0) return { status: 'none', paths: [], more: 0 };
  return { status: 'found', paths: clean.slice(0, MAX_REFERENCES), more: Math.max(0, clean.length - MAX_REFERENCES) + unsafe };
}

/**
 * The `## deleted file` section (heading included) for a deleted `file`. A name with a control
 * character is printed JSON-escaped, never raw.
 * @param {string} file @param {References} refs @returns {string}
 */
export function renderDeletionNote(file, refs) {
  const shown = hasControl(file) ? JSON.stringify(file) : file;
  const out = ['## deleted file', `this file is deleted: ${shown}`, `check that nothing still depends on it. tree paths that still name ${JSON.stringify(path.posix.basename(file))}:`];
  if (refs.status === 'not_checked') out.push(NOT_CHECKED);
  else if (refs.status === 'none') out.push(NO_REFERENCES);
  else {
    out.push(...refs.paths.map((p) => `- ${p}`));
    if (refs.more > 0) out.push(`(${refs.more} more not listed)`);
  }
  return out.join('\n');
}

/**
 * The deletion note for `file`, its references read in `repoRoot` (the reviewed tree). Never
 * rejects: anything that fails is `references not checked`.
 * @param {{repoRoot: string, file: string, run?: typeof exec}} opts @returns {Promise<string>}
 */
export async function deletionNote(opts) {
  let refs;
  try {
    refs = await findReferences(opts);
  } catch {
    refs = NOT_CHECKED_REFS;
  }
  return renderDeletionNote(opts.file, refs);
}
