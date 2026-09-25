/**
 * Per-session `CODEX_HOME` for the Codex coder (block B4.1, plan §5.2 Codex row, §8.4, [A19]).
 *
 * Facts (Codex 0.155.1, read-only checks on this Mac):
 *  - `codex exec --help`: `--ignore-rules  Do not load user or project execpolicy \`.rules\` files`
 *    — `exec` loads execpolicy rules, but has NO flag naming a rules file, and `ConfigToml` has no
 *    rules-path key, so `-c` cannot name one either.
 *  - The "user" rules are `$CODEX_HOME/rules/*.rules` (`~/.codex/rules/default.rules` on this Mac;
 *    the binary logs "loaded … .rules files in …"). `CODEX_HOME` is therefore the one per-run
 *    handle: the coder runs with `CODEX_HOME=<run temp root>/…/codex-home`, whose
 *    `rules/code-forge.rules` holds the rendered forbidden list. The user's real `~/.codex` (and
 *    its own `rules/`) is never written.
 *  - The file format is execpolicy Starlark, one `prefix_rule(pattern=[…], decision="forbidden",
 *    justification="…")` per token-array prefix (`codex execpolicy check --rules <PATH> <COMMAND>…`
 *    parses the same files; the doctor/facts evidence ran it against a rendered file).
 *  - `--ignore-user-config`'s help says "auth still uses `CODEX_HOME`", so a fresh home has no
 *    login. The real home's `auth.json` is COPIED in (0600; the bytes pass through one Buffer that
 *    is never decoded, logged or kept), never symlinked: a symlink would let a write inside the
 *    session home reach the real file. Both files the session home receives (the rules file and
 *    the auth copy) are written with `writeFreshFile`: the destination is `lstat`ed and refused
 *    when ANYTHING already exists there (a planted symlink included), then created with
 *    `O_CREAT|O_EXCL|O_NOFOLLOW`, so a token can never be written through a link (L3 patch).
 *    Tradeoff: a token refresh inside the session is NOT written back to the real
 *    `auth.json`; if Codex rotates the refresh token, the user's own Codex may have to log in
 *    again. With no `auth.json` present, Codex uses its env auth (`CODEX_API_KEY` /
 *    `OPENAI_API_KEY`). The user's Codex TOML config is NOT linked: the coder argv carries every
 *    setting it needs, and the user's own allow-rules stay out of a coder session.
 *
 * INVARIANT (fix round 1): the coder's sandbox can never write the session home. The home lives
 * under the run temp root; the builder runs the coder with `-s workspace-write` PLUS
 * `-c sandbox_workspace_write.exclude_tmpdir_env_var=true` and
 * `-c sandbox_workspace_write.exclude_slash_tmp=true` (both keys are in the 0.155.1 binary), so the
 * only writable root is the project `-C <cwd>` — and a home inside that cwd is refused. On top, the
 * rules file is 0444. The directories stay 0700, not 0555: a 0555 directory makes the run-root
 * sweep's `rmSync(…, {recursive, force})` fail (ENOTEMPTY, checked on this Mac), which would leave
 * the whole run root — the auth copy included — behind. Tradeoff of the tmp exclusion: the coder's
 * own commands cannot write `$TMPDIR` or `/tmp` either.
 */

import { randomBytes } from 'node:crypto';
import { closeSync, constants, existsSync, fchmodSync, lstatSync, mkdirSync, openSync, readFileSync, realpathSync, rmSync, writeSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { currentRunRoot } from '../util/tmp.mjs';

/** The rules file name inside `<codexHome>/rules/`. Codex loads every `*.rules` file there. */
export const CODEX_RULES_FILE_NAME = 'code-forge.rules';

/**
 * @typedef {{id: string, patterns: ReadonlyArray<ReadonlyArray<string>>, enforced: boolean, description?: string}} CodexRenderedEntry
 */

/**
 * The user's real Codex home: `$CODEX_HOME` when set, else `$HOME/.codex` (`os.homedir()` without `HOME`).
 * @param {NodeJS.ProcessEnv} [env]
 * @returns {string} an absolute path.
 */
export function realCodexHome(env = process.env) {
  const fromEnv = env.CODEX_HOME;
  const home = typeof env.HOME === 'string' && env.HOME.length > 0 ? env.HOME : os.homedir();
  return path.resolve(typeof fromEnv === 'string' && fromEnv.length > 0 ? fromEnv : path.join(home, '.codex'));
}

/** @param {string} p @returns {string} the realpath of the longest existing ancestor + the rest. */
function resolveExisting(p) {
  let head = path.resolve(p);
  const tail = [];
  while (!existsSync(head)) {
    const parent = path.dirname(head);
    if (parent === head) break;
    tail.unshift(path.basename(head));
    head = parent;
  }
  return path.join(realpathSync(head), ...tail);
}

/**
 * Whether `candidate` is `dir` or inside it (both sides symlink-resolved).
 * @param {string} candidate @param {string} dir @returns {boolean}
 */
export function isInside(candidate, dir) {
  const rel = path.relative(resolveExisting(dir), resolveExisting(candidate));
  return rel === '' || (!rel.startsWith('..') && !path.isAbsolute(rel));
}

/**
 * Whether `candidate` is the real Codex home or inside it (both sides symlink-resolved).
 * @param {string} candidate @param {NodeJS.ProcessEnv} [env] @returns {boolean}
 */
export function isInsideRealCodexHome(candidate, env = process.env) {
  return isInside(candidate, realCodexHome(env));
}

/**
 * Execpolicy Starlark for a `renderForCodex` render: one `prefix_rule` per pattern, in entry
 * order. `JSON.stringify` of a string is a valid Starlark string literal for these ASCII tokens.
 * @param {ReadonlyArray<CodexRenderedEntry>} rendered
 * @returns {{content: string, count: number}}
 */
export function renderCodexRules(rendered) {
  const lines = ['# code-forge forbidden list (src/util/forbidden.mjs). Generated per session; do not edit.'];
  let count = 0;
  for (const entry of rendered) {
    for (const pattern of entry.patterns) {
      const tokens = pattern.map((t) => JSON.stringify(String(t))).join(', ');
      lines.push(`prefix_rule(pattern=[${tokens}], decision="forbidden", justification=${JSON.stringify(`code-forge: ${entry.id}`)})`);
      count += 1;
    }
  }
  return { content: `${lines.join('\n')}\n`, count };
}

/** The name of the run-root directory every session home lives in. */
export const CODEX_HOMES_DIR_NAME = 'codex-homes';

/** @returns {string} `<currentRunRoot()>/codex-homes`, the one parent of every session home. */
export function sessionCodexHomesDir() {
  return path.join(currentRunRoot(), CODEX_HOMES_DIR_NAME);
}

/**
 * Whether `candidate` is STRICTLY inside `<currentRunRoot()>/codex-homes/` (both sides
 * symlink-resolved through their nearest existing ancestor). The directory itself, the run root,
 * the real Codex home, `$HOME` and any sibling are all outside.
 * @param {string} candidate @returns {boolean}
 */
export function isSessionCodexHome(candidate) {
  if (typeof candidate !== 'string' || !path.isAbsolute(candidate)) return false;
  const rel = path.relative(resolveExisting(sessionCodexHomesDir()), resolveExisting(candidate));
  return rel !== '' && !rel.startsWith('..') && !path.isAbsolute(rel);
}

/**
 * A fresh per-session Codex home path under the run temp root.
 * @returns {string}
 */
export function defaultCodexHome() {
  return path.join(sessionCodexHomesDir(), `coder-${randomBytes(6).toString('hex')}`);
}

/**
 * Create `<codexHome>/rules/code-forge.rules` (dirs 0700, file 0444) and copy the real home's
 * `auth.json` (0600) when it exists.
 * @param {string} codexHome - absolute; never the real Codex home or inside it.
 * @param {ReadonlyArray<CodexRenderedEntry>} rendered
 * @param {{env?: NodeJS.ProcessEnv}} [opts]
 * @returns {{codexHome: string, rulesPath: string, count: number, content: string}}
 * @throws {Error} when `codexHome` is relative, is (or is inside) the real Codex home, or the
 *   render yields 0 rules.
 */
export function prepareCodexHome(codexHome, rendered, opts = {}) {
  const env = opts.env ?? process.env;
  if (typeof codexHome !== 'string' || !path.isAbsolute(codexHome)) {
    throw new TypeError('prepareCodexHome: codexHome must be an absolute path');
  }
  if (isInsideRealCodexHome(codexHome, env)) {
    throw new Error('prepareCodexHome: codexHome must not be the real Codex home or inside it');
  }
  // Fix round 4: an ANCESTOR of the real home (`$HOME`, `/`) is not "inside" it, yet a home there
  // would put `rules/` and an auth copy where the user lives. The only place a session home may be
  // is strictly inside `<run root>/codex-homes/` — the same confinement `removeCodexHome` enforces.
  if (!isSessionCodexHome(codexHome)) {
    throw new Error(`prepareCodexHome: codexHome must be strictly inside <run root>/${CODEX_HOMES_DIR_NAME}/`);
  }
  const { content, count } = renderCodexRules(rendered);
  if (count === 0) throw new Error('prepareCodexHome: the forbidden-list render has 0 rules');
  const rulesDir = path.join(codexHome, 'rules');
  const rulesPath = path.join(rulesDir, CODEX_RULES_FILE_NAME);
  const auth = path.join(realCodexHome(env), 'auth.json');
  const copy = path.join(codexHome, 'auth.json');
  // L3 patch: both destinations are checked BEFORE anything is written, so a planted entry (a
  // symlink at either path) makes this throw with the home left exactly as found.
  refuseExisting(rulesPath);
  refuseExisting(copy);
  mkdirSync(rulesDir, { recursive: true, mode: 0o700 });
  if (lstatSync(rulesDir).isSymbolicLink()) throw new Error('prepareCodexHome: refusing a symlinked rules directory');
  writeFreshFile(rulesPath, Buffer.from(content, 'utf8'), 0o444);
  if (existsSync(auth)) {
    writeFreshFile(copy, readFileSync(auth), 0o600);
  }
  return { codexHome, rulesPath, count, content };
}

/**
 * Throw when anything at all (file, directory, symlink — dangling or not) already exists at `dest`.
 * @param {string} dest
 */
function refuseExisting(dest) {
  try {
    lstatSync(dest);
  } catch (err) {
    if (/** @type {NodeJS.ErrnoException} */ (err).code === 'ENOENT') return;
    throw err;
  }
  throw new Error(`prepareCodexHome: refusing to write ${path.basename(dest)}: something already exists at the destination`);
}

/**
 * Create `dest` exclusively, never following a link (`O_WRONLY|O_CREAT|O_EXCL|O_NOFOLLOW`, mode
 * 0600), write `bytes`, set the final `mode` on the open descriptor, close. A path that gained an
 * entry between `refuseExisting` and this open fails on `O_EXCL` — nothing is ever written through it.
 * @param {string} dest @param {Buffer} bytes @param {number} mode
 */
function writeFreshFile(dest, bytes, mode) {
  const { O_WRONLY, O_CREAT, O_EXCL, O_NOFOLLOW } = constants;
  const fd = openSync(dest, O_WRONLY | O_CREAT | O_EXCL | O_NOFOLLOW, 0o600);
  try {
    let offset = 0;
    while (offset < bytes.length) offset += writeSync(fd, bytes, offset, bytes.length - offset);
    fchmodSync(fd, mode);
  } finally {
    closeSync(fd);
  }
}

/**
 * Remove a session home (the auth copy with it). The ONLY recursive delete of this module, and it
 * is confined (fix round 3): `codexHome` must be strictly inside `<currentRunRoot()>/codex-homes/`
 * after realpath resolution, or this throws and deletes nothing — the real `~/.codex`, `$HOME`,
 * the run root itself and any sibling directory are refused. No-op when the (confined) path does
 * not exist.
 * @param {string} codexHome
 * @throws {Error} when `codexHome` is not strictly inside the run root's `codex-homes/`.
 */
export function removeCodexHome(codexHome) {
  if (!isSessionCodexHome(codexHome)) {
    throw new Error(`removeCodexHome: refusing to delete a path outside <run root>/${CODEX_HOMES_DIR_NAME}/`);
  }
  rmSync(resolveExisting(codexHome), { recursive: true, force: true });
}
