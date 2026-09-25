/**
 * `code-forge validate [--file <path>]` — loads and validates a project's `.code-forge.yml`
 * (plan §1.3). Exit 0 when the config is schema-and-domain valid (warnings may still print), exit
 * 1 when it fails to load (missing/unparseable/unmigratable) or fails validation, exit 2 on a
 * misused flag.
 */

import { existsSync } from 'node:fs';
import path from 'node:path';
import { loadConfigFile, DEFAULT_CONFIG_FILENAME } from '../config/load.mjs';
import { loadSeenInCache } from '../config/refresh.mjs';
import { maskSecretTokens } from '../config/secret-patterns.mjs';
import { validateConfig } from '../config/validate.mjs';
import * as log from '../util/log.mjs';

/** Provider -> the CLI binary name `engine: subprocess` would spawn for it. */
const PROVIDER_CLI_NAMES = Object.freeze({ anthropic: 'claude', openai: 'codex', xai: 'grok' });

/**
 * @param {string} provider
 * @returns {boolean}
 */
export function hasCliOnPath(provider) {
  const cliName = PROVIDER_CLI_NAMES[/** @type {keyof typeof PROVIDER_CLI_NAMES} */ (provider)];
  if (!cliName) {
    return false;
  }
  const candidates = process.platform === 'win32' ? [`${cliName}.exe`, `${cliName}.cmd`, cliName] : [cliName];
  const dirs = (process.env.PATH ?? '').split(path.delimiter).filter(Boolean);
  return dirs.some((dir) => candidates.some((name) => existsSync(path.join(dir, name))));
}

/**
 * @param {string[]} args
 * @returns {{file: string} | {error: string}}
 */
function parseArgs(args) {
  let file = path.join(process.cwd(), DEFAULT_CONFIG_FILENAME);
  for (let i = 0; i < args.length; i += 1) {
    if (args[i] === '--file') {
      const value = args[i + 1];
      // A missing value OR one that looks like another flag (`validate --file --foo`) must be a
      // usage error (exit 2), never silently treated as the path "--foo" and reported as a load
      // failure (exit 1) — MINOR §25/26 fix.
      if (!value || value.startsWith('--')) {
        return { error: '--file requires a path argument' };
      }
      file = path.resolve(value);
      i += 1;
    } else {
      return { error: `unknown argument: ${args[i]}` };
    }
  }
  return { file };
}

/**
 * @param {string[]} args
 * @returns {Promise<number>}
 */
export default async function validate(args) {
  const parsed = parseArgs(args);
  if ('error' in parsed) {
    log.error(maskSecretTokens(parsed.error));
    return 2;
  }

  const loaded = await loadConfigFile(parsed.file);
  if (!loaded.ok) {
    log.error(maskSecretTokens(`${loaded.error}: ${loaded.message}`));
    return 1;
  }

  // Both guarded explicitly (MINOR §27/28): a corrupt user-level cache or a validator crash must
  // report the documented exit 1 with a clear cause, not an unhandled rejection whose only
  // diagnostic is whatever the router's generic catch-all prints for a raw stack trace.
  let seenInCache;
  try {
    seenInCache = await loadSeenInCache();
  } catch (err) {
    log.error(maskSecretTokens(`could not read the seen-in-cache file: ${/** @type {Error} */ (err).message}`));
    return 1;
  }

  let result;
  try {
    result = validateConfig(loaded.config, { seenInCache, hasCliOnPath });
  } catch (err) {
    log.error(maskSecretTokens(`validator crashed: ${/** @type {Error} */ (err).message}`));
    return 1;
  }

  // `validateConfig` already scrubs every message; masking again here keeps the verb safe even if
  // a future rule regresses (every line also passes B0's `redact` inside `log`).
  for (const err of result.errors) {
    log.error(maskSecretTokens(`[${err.rule}] ${err.message}`));
  }
  for (const warn of result.warnings) {
    log.warn(maskSecretTokens(`[${warn.rule}] ${warn.message}`));
  }

  if (result.valid) {
    log.info(`${parsed.file}: valid (${result.warnings.length} warning${result.warnings.length === 1 ? '' : 's'})`);
    return 0;
  }
  log.info(`${parsed.file}: invalid (${result.errors.length} error${result.errors.length === 1 ? '' : 's'})`);
  return 1;
}
