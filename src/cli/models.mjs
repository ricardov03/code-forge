/**
 * `code-forge models [--refresh --from-cli-caches]` — lists the model catalog per provider (the
 * shipped `known-ids.mjs` list, plus the project's `known_extra`, plus anything the user-level
 * `seen-in-cache` list has picked up). `--refresh --from-cli-caches` is the opt-in C15 refresh:
 * it reads whichever of `~/.codex/models_cache.json` / `~/.grok/models_cache.json` exist and
 * merges their ids into the user-level cache BEFORE listing — it never writes the project's
 * `.code-forge.yml` (plan §1.3, §2 step 3).
 *
 * Every line printed from file-derived text passes `maskSecretTokens` (and B0's `redact` inside
 * `log`): a Grok cache stores an `api_key` next to every model id, and a project config is where
 * a user pastes a key by mistake.
 */

import { KNOWN_IDS, knownIdsForProvider } from '../config/known-ids.mjs';
import { loadProjectConfig } from '../config/load.mjs';
import { loadSeenInCache, refreshFromCliCaches } from '../config/refresh.mjs';
import { maskSecretTokens } from '../config/secret-patterns.mjs';
import * as log from '../util/log.mjs';

/**
 * @param {string[]} args
 * @returns {{refresh: boolean, fromCliCaches: boolean} | {error: string}}
 */
function parseArgs(args) {
  let refresh = false;
  let fromCliCaches = false;
  for (const arg of args) {
    if (arg === '--refresh') {
      refresh = true;
    } else if (arg === '--from-cli-caches') {
      fromCliCaches = true;
    } else {
      return { error: `unknown argument: ${arg}` };
    }
  }
  if (fromCliCaches && !refresh) {
    return { error: '--from-cli-caches requires --refresh' };
  }
  return { refresh, fromCliCaches };
}

/**
 * An OWN entry of a map as a string array — anything else (a number, a string, an inherited
 * `constructor`) is `[]`, so iterating it can never throw or split a string into characters.
 * @param {unknown} map
 * @param {string} key
 * @returns {string[]}
 */
function ownIdList(map, key) {
  if (!map || typeof map !== 'object' || !Object.hasOwn(map, key)) {
    return [];
  }
  const value = /** @type {Record<string, unknown>} */ (map)[key];
  return Array.isArray(value) ? value.filter((id) => typeof id === 'string') : [];
}

/**
 * @param {string[]} args
 * @returns {Promise<number>}
 */
export default async function models(args) {
  const parsed = parseArgs(args);
  if ('error' in parsed) {
    log.error(maskSecretTokens(parsed.error));
    return 2;
  }

  if (parsed.refresh) {
    if (!parsed.fromCliCaches) {
      // C15: `--refresh` ships as an opt-in gated by `--from-cli-caches` — there is no other
      // refresh source in 0.1, so a bare `--refresh` is a usage error, not a silent no-op.
      log.error('models --refresh requires --from-cli-caches (the only refresh source in 0.1)');
      return 2;
    }
    let result;
    try {
      result = await refreshFromCliCaches();
    } catch (err) {
      log.error(maskSecretTokens(`refresh failed: ${/** @type {Error} */ (err).message}`));
      return 1;
    }
    if (result.sourcesRead.length === 0) {
      // 0 sources read is NOT a refresh — never claim success while doing nothing useful.
      log.warn('no CLI caches were found to refresh from (checked ~/.codex and ~/.grok)');
    } else {
      log.info(
        `refreshed from ${result.sourcesRead.length} CLI cache(s) -> ${result.cachePath} (status: seen-in-cache)`,
      );
    }
    for (const skipped of result.sourcesSkipped) {
      log.warn(maskSecretTokens(`skipped ${skipped.path}: ${skipped.reason}`));
    }
  }

  const seenInCache = await loadSeenInCache();
  const projectLoad = await loadProjectConfig();
  if (!projectLoad.ok && projectLoad.error !== 'not-found') {
    // A project config that exists but fails to load is reported (a missing one stays silent) —
    // otherwise the user's known_extra silently vanishes from the listing.
    log.warn(maskSecretTokens(`could not read known_extra from ${projectLoad.path}: ${projectLoad.error}: ${projectLoad.message}`));
  }
  const rawKnownExtra = projectLoad.ok ? projectLoad.config.known_extra : undefined;
  const knownExtra = rawKnownExtra && typeof rawKnownExtra === 'object' && !Array.isArray(rawKnownExtra) ? rawKnownExtra : {};

  // The union of every provider that appears in ANY of the three sources, not just
  // `Object.keys(KNOWN_IDS)` — a provider that only exists in known_extra or seen-in-cache is
  // still listed, instead of silently vanishing from the output.
  const allProviders = new Set([...Object.keys(KNOWN_IDS), ...Object.keys(knownExtra), ...Object.keys(seenInCache)]);

  for (const provider of [...allProviders].sort()) {
    log.info(maskSecretTokens(`${provider}:`));
    // One `printed` set per provider so an id shipped in BOTH the catalog and known_extra/
    // seen-in-cache is printed once, tagged by its highest-precedence source.
    const printed = new Set();
    for (const id of knownIdsForProvider(provider)) {
      log.info(`  ${id}`);
      printed.add(id);
    }
    for (const id of ownIdList(knownExtra, provider)) {
      if (printed.has(id)) continue;
      log.info(maskSecretTokens(`  ${id} (known_extra)`));
      printed.add(id);
    }
    for (const id of ownIdList(seenInCache, provider)) {
      if (printed.has(id)) continue;
      log.info(maskSecretTokens(`  ${id} (seen-in-cache)`));
      printed.add(id);
    }
  }
  return 0;
}
