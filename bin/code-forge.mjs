#!/usr/bin/env node
/**
 * Verb router. Discovers verbs by `readdir(src/cli)` — a verb is a `.mjs` file's basename.
 * Adding a new verb NEVER requires editing this file (plan §10.2 acceptance 2): drop
 * `src/cli/<verb>.mjs` exporting a default `async function run(args) -> number|void` and it is
 * live. `--help`/`-h` (or no verb at all) and `--version`/`-v` are the only aliases this router
 * knows by name, and only as a UX convenience — both simply dispatch to the `help`/`version`
 * verb modules like any other invocation would.
 */

import { realpathSync } from 'node:fs';
import { readdir } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { clearErrorKind, reportErrorKind, takeErrorKind } from '../src/util/error-kind.mjs';
import { captureStderr, logVerbFailure } from '../src/util/error-log.mjs';
import { redact } from '../src/util/redact.mjs';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const CLI_DIR = path.join(__dirname, '..', 'src', 'cli');

const HELP_ALIASES = new Set(['--help', '-h']);
const VERSION_ALIASES = new Set(['--version', '-v']);

/**
 * A verb file's basename must match this. Duplicated (not imported) in `src/cli/help.mjs` —
 * both B0-owned files must stay in sync since there is no shared-util file in B0's scope to hold
 * this once. Rejects `.mjs` (empty verb name) and names with spaces/other characters that would
 * break the `/^ {2}\S+$/` verb-line pattern `help.mjs` prints.
 */
const VERB_FILE_PATTERN = /^[a-z][a-z0-9-]*\.mjs$/;

/**
 * @returns {Promise<string[]>} verb names, sorted, discovered from `src/cli/*.mjs`.
 */
export async function listVerbs() {
  const entries = await readdir(CLI_DIR, { withFileTypes: true });
  return entries
    .filter((entry) => entry.isFile() && VERB_FILE_PATTERN.test(entry.name))
    .map((entry) => entry.name.slice(0, -'.mjs'.length))
    .sort();
}

/**
 * @param {number} exitCode
 * @returns {boolean}
 */
function isValidExitCode(exitCode) {
  return Number.isInteger(exitCode) && exitCode >= 0 && exitCode <= 255;
}

/**
 * Point this process's lazy temp root at the project's `tmp.root` (B19, plan §9.6 rule 2): when
 * `cwd` holds a `.code-forge.yml` with a string `tmp.root` and `env` does not name a base already,
 * set `env.CODE_FORGE_TMP_ROOT` so `util/tmp.mjs` (and every child) uses it. A relative `tmp.root`
 * is resolved against the config's directory (`cwd`). A missing or unloadable config changes nothing.
 * @param {{env?: NodeJS.ProcessEnv, cwd?: string}} [opts]
 * @returns {Promise<string | undefined>} the value `env.CODE_FORGE_TMP_ROOT` holds afterwards.
 */
export async function applyTmpRoot({ env = process.env, cwd = process.cwd() } = {}) {
  const name = 'CODE_FORGE_TMP_ROOT'; // `util/tmp.mjs` TMP_ROOT_ENV
  if (typeof env[name] === 'string' && env[name].length > 0) return env[name];
  let loaded;
  try {
    // imported here, not statically: the router must still start when the config loader is absent
    const { loadProjectConfig } = await import('../src/config/load.mjs');
    loaded = await loadProjectConfig(cwd);
  } catch {
    return env[name];
  }
  const root = loaded.ok ? loaded.config?.tmp?.root : undefined;
  if (typeof root === 'string' && root.length > 0) env[name] = path.resolve(cwd, root);
  return env[name];
}

/**
 * @param {string[]} argv - e.g. `process.argv.slice(2)`.
 * @returns {Promise<number>} the process exit code.
 */
export async function run(argv) {
  const [first, ...rest] = argv;
  const verbs = await listVerbs();

  let verb = first;
  if (verb === undefined || HELP_ALIASES.has(verb)) {
    verb = 'help';
  } else if (VERSION_ALIASES.has(verb)) {
    verb = 'version';
  }

  if (!verbs.includes(verb)) {
    process.stderr.write(
      redact(`code-forge: unknown verb "${verb}"\nVerbs:\n${verbs.map((v) => `  ${v}`).join('\n')}\n`),
    );
    return 1;
  }

  const modulePath = path.join(CLI_DIR, `${verb}.mjs`);

  // B27: a verb that exits non-zero or throws is logged to `~/.code-forge/logs/errors.jsonl`
  // (scrubbed; never throws; never changes the exit code). The kind a verb reports is cleared
  // first, and stderr is recorded only while the verb runs (the message is its last text).
  // Every logging step is guarded on its own: none of them can change the verb's outcome.
  /** @type {{stop: () => string|null} | null} */
  let capture = null;
  try {
    clearErrorKind();
    capture = captureStderr(process.stderr);
  } catch {
    // no recording: the entry has no message
  }
  /** @type {number} */
  let code;
  /** @type {unknown} */
  let thrown;
  let threw = false;
  /** @type {string|null} */
  let stderrText = null;
  try {
    const mod = await import(pathToFileURL(modulePath).href);
    const handler = mod.default;
    if (typeof handler !== 'function') {
      process.stderr.write(redact(`code-forge: verb "${verb}" has no default export function\n`));
      code = 1;
    } else {
      const exitCode = await handler(rest, { verbs, verb, reportErrorKind });
      code = exitCode === undefined || exitCode === null ? 0 : isValidExitCode(exitCode) ? exitCode : 1;
    }
  } catch (err) {
    thrown = err;
    threw = true;
    code = 1;
    // A verb's error can carry an argv/env token (a fake key in a test, a real one in
    // production); every stderr path here is redacted, this one included.
    try {
      process.stderr.write(redact(`code-forge: verb "${verb}" failed: ${/** @type {any} */ (err)?.stack ?? String(err)}\n`));
    } catch {
      // a broken stderr never changes the exit code
    }
  } finally {
    try {
      stderrText = capture ? capture.stop() : null;
    } catch {
      stderrText = null;
    }
  }
  /** @type {string|null} */
  let kind = null;
  try {
    kind = takeErrorKind();
  } catch {
    kind = null;
  }
  if (code !== 0) {
    try {
      await logVerbFailure({ verb, args: rest, exit: code, kind, stderrText, threw, thrown });
    } catch {
      // logging never changes the exit code (logVerbFailure does not throw; belt and braces)
    }
  }
  return code;
}

/**
 * Resolve `argv[1]` to the real path `import.meta.url` would report for the same file, so the
 * comparison survives (a) Node resolving symlinks for the main module — `npm`/`npx .` bin shims
 * under `node_modules/.bin` are symlinks, and macOS temp dirs are `/var` → realpath `/private/var`
 * — and (b) `import.meta.url` being percent-encoded while `argv[1]` is a raw path (spaces, `#`,
 * `%`). Falls back to `false` (never treated as main) if `argv[1]` doesn't resolve to a real file.
 * @returns {boolean}
 */
function computeIsMain() {
  if (!process.argv[1]) {
    return false;
  }
  try {
    const realArgvPath = realpathSync(process.argv[1]);
    return import.meta.url === pathToFileURL(realArgvPath).href;
  } catch {
    return false;
  }
}

const isMain = computeIsMain();
if (isMain) {
  await applyTmpRoot();
  const code = await run(process.argv.slice(2));
  process.exitCode = code;
}
