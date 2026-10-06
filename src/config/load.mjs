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

import { readFile } from 'node:fs/promises';
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
  return loadConfigFile(path.join(cwd, DEFAULT_CONFIG_FILENAME));
}

/**
 * `project.slug` from config, else the workspace directory name as a ledger slug. The configured
 * value is NOT trusted here: `startRun` refuses any slug outside `/^[a-z0-9][a-z0-9-]*$/`
 * (`bad-project`) before anything is written, so `../x` never reaches the ledger path. Run ids
 * are checked the same way by the state layer (`assertRunId`, `bad-run-id`).
 * @param {Record<string, any>} cfg @param {string} workspace
 * @returns {string}
 */
export function slugFor(cfg, workspace) {
  const configured = cfg?.project?.slug;
  if (typeof configured === 'string' && configured.length > 0) return configured;
  const derived = path.basename(workspace).toLowerCase().replace(/[^a-z0-9-]+/g, '-').replace(/^-+|-+$/g, '');
  return derived || 'project';
}
