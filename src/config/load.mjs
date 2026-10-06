/**
 * The project config loader (plan §1.2 `load.mjs`). Reads `.code-forge.yml`, parses it, and runs
 * it through {@link migrateConfig} — nothing more. It deliberately does NOT validate (that is
 * `validate.mjs`, a separate concern so a caller can load-then-inspect a config that fails
 * validation instead of getting only a thrown error) and does NOT merge in provider defaults
 * (those are only ever written into the file by the `init` wizard, a later block — a config on
 * disk is expected to already be complete).
 *
 * **Secret safety (fix round 1, MAJOR):** the `yaml` package's default `parse()` builds its
 * `YAMLParseError.message` WITH a code-frame excerpt of the offending source line baked in — so a
 * malformed `.code-forge.yml` with a secret-looking value on or near the bad line (`api_key: sk-
 * ...`) would put that value straight into `result.message`, which every CLI caller prints
 * (`src/cli/validate.mjs`). This module parses with `{prettyErrors: false}` (message text only,
 * no excerpt) and builds its own `line, column` location from the error's raw character offset
 * (`err.pos`) and the source string — never from anything the `yaml` package formats for display.
 */

import { existsSync, realpathSync, statSync } from 'node:fs';
import { readFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { parse as parseYAML } from 'yaml';
import { migrateConfig } from './migrate.mjs';
import { maskSecretTokens } from './secret-patterns.mjs';

/** The config file's conventional name at a project's root. */
export const DEFAULT_CONFIG_FILENAME = '.code-forge.yml';

/**
 * @typedef {object} LoadResult
 * @property {boolean} ok
 * @property {string} path - the resolved file path this result is about.
 * @property {Record<string, any>} [config] - present when `ok`.
 * @property {"not-found" | "read-error" | "parse-error" | "migrate-error"} [error] - present when `!ok`.
 * @property {string} [message] - present when `!ok`.
 */

/**
 * Computes a 1-based `{line, column}` from a character offset into `source`, without ever
 * formatting or returning the source text itself.
 * @param {string} source
 * @param {number} offset
 * @returns {{line: number, column: number}}
 */
function locationAt(source, offset) {
  let line = 1;
  let column = 1;
  const end = Math.min(offset, source.length);
  for (let i = 0; i < end; i += 1) {
    if (source[i] === '\n') {
      line += 1;
      column = 1;
    } else {
      column += 1;
    }
  }
  return { line, column };
}

/**
 * Builds a safe (excerpt-free) message for a `yaml` `YAMLParseError` (or any error shaped like
 * one — `{code, message, pos}`), appending `line, column` when the error carries a position.
 * @param {string} source
 * @param {{code?: string, message: string, pos?: unknown}} err
 * @returns {string}
 */
function describeYamlError(source, err) {
  const code = err.code ?? 'YAML_PARSE_ERROR';
  // Even with `prettyErrors: false` a few `yaml` messages quote a source token (an alias or tag
  // name) — every secret-shaped token is masked as a second layer (round 3).
  const text = maskSecretTokens(String(err.message));
  const pos = Array.isArray(err.pos) && typeof err.pos[0] === 'number' ? err.pos[0] : undefined;
  if (pos === undefined) {
    return `${code}: ${text}`;
  }
  const { line, column } = locationAt(source, pos);
  return `${code}: ${text} (line ${line}, column ${column})`;
}

/**
 * @param {unknown} parsed
 * @returns {boolean} true when `parsed` is a plain mapping (what a `.code-forge.yml` root must be).
 */
function isPlainMapping(parsed) {
  return parsed !== null && typeof parsed === 'object' && !Array.isArray(parsed);
}

/**
 * @param {string} filePath - an absolute or cwd-relative path to a `.code-forge.yml`-shaped file.
 * @returns {Promise<LoadResult>}
 */
export async function loadConfigFile(filePath) {
  let raw;
  try {
    raw = await readFile(filePath, 'utf8');
  } catch (err) {
    const code = /** @type {NodeJS.ErrnoException} */ (err).code;
    if (code === 'ENOENT') {
      return { ok: false, path: filePath, error: 'not-found', message: `no config file at ${filePath}` };
    }
    // A directory, a permission error, etc. — distinct from "not found" so a caller can't
    // mistake "the path exists but can't be read as a file" for "nothing is there yet".
    return {
      ok: false,
      path: filePath,
      error: 'read-error',
      message: `could not read ${filePath}: ${code ?? /** @type {Error} */ (err).message}`,
    };
  }

  let parsed;
  try {
    parsed = parseYAML(raw, { prettyErrors: false });
  } catch (err) {
    return { ok: false, path: filePath, error: 'parse-error', message: describeYamlError(raw, /** @type {any} */ (err)) };
  }

  if (!isPlainMapping(parsed)) {
    const kind = parsed === null || parsed === undefined ? 'an empty document' : Array.isArray(parsed) ? 'a list' : typeof parsed;
    return { ok: false, path: filePath, error: 'parse-error', message: `config root must be a mapping, got ${kind}` };
  }

  try {
    const config = migrateConfig(parsed);
    return { ok: true, path: filePath, config };
  } catch (err) {
    return { ok: false, path: filePath, error: 'migrate-error', message: /** @type {Error} */ (err).message };
  }
}

/**
 * @param {string} [cwd] - defaults to `process.cwd()`.
 * @returns {Promise<LoadResult>}
 */
export async function loadProjectConfig(cwd = process.cwd()) {
  // B50: always the PROJECT ROOT's config ({@link projectRootFor}; idempotent when `cwd` is the
  // root). A symlinked `.code-forge.yml` (dotfiles) loads like a file; a directory there is a `read-error`.
  return loadConfigFile(path.join(projectRootFor(cwd), DEFAULT_CONFIG_FILENAME));
}

/**
 * The nearest ancestor of `start` (itself included) that holds `marker`, or null.
 * @param {string} start @param {string} marker
 * @returns {string | null}
 */
function nearestWith(start, marker) {
  for (let dir = start; ; dir = path.dirname(dir)) {
    if (existsSync(path.join(dir, marker))) return dir;
    if (path.dirname(dir) === dir) return null;
  }
}

/** @param {string} p @returns {boolean} true when `p` is (or links to) a regular file — a symlinked config (dotfiles) counts, a directory never. */
function isRegularFile(p) {
  try {
    return statSync(p).isFile();
  } catch {
    return false;
  }
}

/** @param {string} p @returns {string} `p` realpath'd, or resolved as given when it cannot be. */
function realOrResolved(p) {
  try {
    return realpathSync(p);
  } catch {
    return path.resolve(p);
  }
}

/**
 * The project root a verb run from `start` belongs to (B50). The git top level is found first
 * (the nearest ancestor holding a `.git` directory or worktree file — a filesystem walk, no child
 * `git`); the root is then the nearest folder from `start` up to AND INCLUDING that top level whose
 * `.code-forge.yml` is a regular file (a symlink to one counts; a directory never), else the top
 * level itself — a config above the repository never counts. Outside git, the same search runs
 * from `start` up to the home directory (never past it) when `start` is under it, and looks at
 * `start` alone when it is not (`/tmp/x`, `/opt/ci`: a stray config higher up never counts); found
 * nothing ⇒ the root is `start`. Every walk runs on realpaths; the root comes back in the caller's
 * spelling when an ancestor of the start as given resolves to it ({@link inCallerSpelling}). A verb
 * run from a subfolder thus finds the project's config and ledger slug. Synchronous (no child process).
 * @param {string} [start] - defaults to `process.cwd()`.
 * @returns {string} absolute.
 */
export function projectRootFor(start = process.cwd()) {
  return inCallerSpelling(start, resolvedRootFor(realOrResolved(start)));
}

/**
 * The root on realpaths: the git walk, the config walk and the HOME check all run on the resolved
 * start, so a symlink into a repository finds its `.git`.
 * @param {string} from - the realpath of the start.
 * @returns {string} a realpath.
 */
function resolvedRootFor(from) {
  const home = realOrResolved(os.homedir());
  // a `.git` at HOME itself (a dotfiles repository) never makes HOME the project: outside git
  const gitRoot = nearestWith(from, '.git');
  if (gitRoot !== null && gitRoot !== home) return nearestConfigUpTo(from, gitRoot) ?? gitRoot;
  const underHome = from === home || from.startsWith(`${home}${path.sep}`);
  if (!underHome) return from; // the start alone: its own config (if any) or not, the root is the start
  return nearestConfigUpTo(from, home) ?? from;
}

/**
 * The root in the caller's spelling when it can be: the ancestor of `path.resolve(start)` (itself
 * included) whose realpath IS the resolved root (`~/work/app` → `/data/app-v2` keeps `app`, and its
 * slug); none ⇒ the realpath itself.
 * @param {string} start @param {string} realRoot
 * @returns {string}
 */
function inCallerSpelling(start, realRoot) {
  for (let dir = path.resolve(start); ; dir = path.dirname(dir)) {
    if (realOrResolved(dir) === realRoot) return dir;
    if (path.dirname(dir) === dir) return realRoot;
  }
}

/**
 * The nearest folder from `from` up to and including `stop` whose `.code-forge.yml` is a regular file.
 * @param {string} from @param {string} stop - an ancestor of `from` (or `from` itself).
 * @returns {string | null}
 */
function nearestConfigUpTo(from, stop) {
  for (let dir = from; ; dir = path.dirname(dir)) {
    if (isRegularFile(path.join(dir, DEFAULT_CONFIG_FILENAME))) return dir;
    if (dir === stop || path.dirname(dir) === dir) return null;
  }
}

/**
 * `project.slug` from config, else the PROJECT ROOT's directory name ({@link projectRootFor} of
 * `workspace`, B50: a subfolder never names the ledger) as a ledger slug. The configured
 * value is NOT trusted here: `startRun` refuses any slug outside `/^[a-z0-9][a-z0-9-]*$/`
 * (`bad-project`) before anything is written, so `../x` never reaches the ledger path. Run ids
 * are checked the same way by the state layer (`assertRunId`, `bad-run-id`).
 * `cfg` MUST be the config loaded from `projectRootFor(workspace)` (never the cwd's), or a
 * configured `project.slug` is lost from a subfolder and the ledger splits.
 * @param {Record<string, any>} cfg @param {string} workspace
 * @returns {string}
 */
export function slugFor(cfg, workspace) {
  const configured = cfg?.project?.slug;
  if (typeof configured === 'string' && configured.length > 0) return configured;
  const derived = path.basename(projectRootFor(workspace)).toLowerCase().replace(/[^a-z0-9-]+/g, '-').replace(/^-+|-+$/g, '');
  return derived || 'project';
}
