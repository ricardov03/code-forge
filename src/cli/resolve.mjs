/**
 * `code-forge resolve <L0|L1|L2|L3>` — prints what a level actually resolves to right now
 * (plan §5.2, §10.3 B4 acceptance): `{provider, model, effort, fallback, cli}`, via B1's
 * `resolveLevel` (the per-level `provider` override wins over the top-level `provider`, R5) plus
 * this block's own `cliNameForProvider` (the CLI binary `engine: subprocess` would spawn).
 *
 * **Fix round 1 (isolated per-file review, all MINOR):**
 *  - The result is now written with `redact.writeSafe(process.stdout, ...)` — B0's documented
 *    "stdout path" for a machine-readable value that must NOT go through `log.info` (which prefixes
 *    every line with `[code-forge:info] `, defeating `jq`/pipe consumers of this verb's output).
 *  - `cli: cliNameForProvider(...) ?? null` used to still exit 0 for a provider with no CLI mapping
 *    (possible: `resolve` does not itself call `validateConfig`, unlike `validate.mjs`, so a config
 *    with an out-of-enum `provider` string can reach here). It now logs an error and exits 1
 *    instead of silently printing `"cli": null`.
 *  - `resolved.fallback` is never `undefined` — `resolveLevel` (B1, `known-ids.mjs`) always returns
 *    a (possibly empty) frozen array, so no `?? null`/`?? []` default was ever needed here; the
 *    test suite now locks in the exact 5-key contract for BOTH the with-fallback and
 *    without-fallback cases, closing the test gap the review actually found.
 */

import { loadProjectConfig } from '../config/load.mjs';
import { resolveLevel } from '../config/known-ids.mjs';
import { cliNameForProvider } from '../engines/provider-cli.mjs';
import { maskSecretTokens } from '../config/secret-patterns.mjs';
import { writeSafe } from '../util/redact.mjs';
import * as log from '../util/log.mjs';

const VALID_LEVELS = Object.freeze(['L0', 'L1', 'L2', 'L3']);

/**
 * @param {string[]} args
 * @returns {{level: "L0"|"L1"|"L2"|"L3"} | {error: string}}
 */
function parseArgs(args) {
  if (args.length !== 1) {
    return { error: `usage: resolve <${VALID_LEVELS.join('|')}>` };
  }
  const [level] = args;
  if (!VALID_LEVELS.includes(level)) {
    return { error: `unknown level "${level}" — expected one of ${VALID_LEVELS.join(', ')}` };
  }
  return { level: /** @type {"L0"|"L1"|"L2"|"L3"} */ (level) };
}

/**
 * @param {string[]} args
 * @returns {Promise<number>}
 */
export default async function resolve(args) {
  const parsed = parseArgs(args);
  if ('error' in parsed) {
    log.error(maskSecretTokens(parsed.error));
    return 2;
  }

  const loaded = await loadProjectConfig();
  if (!loaded.ok) {
    log.error(maskSecretTokens(`${loaded.error}: ${loaded.message}`));
    return 1;
  }

  let resolved;
  try {
    resolved = resolveLevel(loaded.config, parsed.level);
  } catch (err) {
    log.error(maskSecretTokens(/** @type {Error} */ (err).message));
    return 1;
  }

  const cli = cliNameForProvider(resolved.provider);
  if (cli === undefined) {
    log.error(maskSecretTokens(`resolve: provider "${resolved.provider}" (from levels.${parsed.level}) has no known CLI mapping`));
    return 1;
  }

  const output = { provider: resolved.provider, model: resolved.model, effort: resolved.effort ?? null, fallback: resolved.fallback, cli };
  writeSafe(process.stdout, `${JSON.stringify(output)}\n`);
  return 0;
}
