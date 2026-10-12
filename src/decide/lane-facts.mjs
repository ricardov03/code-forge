/**
 * B59: the facts `fallbackLane` (./fallback-rules.mjs) needs, built from what a brief-mode
 * `block open` knows before any code exists: the block's owned entries, the estimated changed
 * lines (`--lines`) and `proof.tiers.high.paths` from the run's config snapshot. Pure — no disk,
 * no git; the caller reads the snapshot.
 *
 *   filesChanged              the number of distinct owned entries (a directory entry counts once)
 *   linesAdded                `--lines`, 0 when not given
 *   touchesMigration          an owned entry names a migration (`isMigrationPath`)
 *   touchesPolicyOrMiddleware an owned entry names a policy or middleware (`isPolicyPath`)
 *   pathFloorHit              a high path owns an owned entry, or an owned entry owns a high path
 *                             (a directory entry above a high path counts: its files are in scope)
 *
 * The migration and policy checks are case-insensitive SUBSTRING matches anywhere in the path
 * (`src/migrationHelper.ts` is a migration path), the same patterns the §3.5 review rules use
 * (`rulesRisk` in src/review/engine.mjs calls these two functions).
 */

import { ownsFile } from '../state/registry.mjs';

/** A path the §3.5 rules treat as a migration. @param {string} file @returns {boolean} */
export const isMigrationPath = (file) => /migration/i.test(file);

/** A path the §3.5 rules treat as a policy or middleware. @param {string} file @returns {boolean} */
export const isPolicyPath = (file) => /polic(y|ies)|middleware/i.test(file);

/**
 * @param {{owned: ReadonlyArray<string>, lines?: number | null, highPaths?: unknown}} input
 *   `highPaths` that is not an array (absent, null) means no high paths.
 * @returns {Required<Omit<import('./fallback-rules.mjs').FallbackFacts, 'keywordsFound'>>}
 */
export function laneFacts({ owned, lines = null, highPaths = [] }) {
  const high = (Array.isArray(highPaths) ? highPaths : []).filter((p) => typeof p === 'string' && p.length > 0);
  return {
    filesChanged: new Set(owned).size,
    linesAdded: lines ?? 0,
    touchesMigration: owned.some(isMigrationPath),
    touchesPolicyOrMiddleware: owned.some(isPolicyPath),
    pathFloorHit: owned.some((entry) => ownsFile(high, entry) || high.some((h) => ownsFile([entry], h))),
  };
}
