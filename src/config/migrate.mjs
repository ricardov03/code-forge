/**
 * Config version migration (plan §1.2 `migrate.mjs`, §1.3 `version`). Only `version: 1` has ever
 * shipped, so today this is "fill in a missing version and refuse anything it doesn't recognise" —
 * a real, testable function even with one version on record, because it is the seam a future `2`
 * plugs into (add a `case 2:` branch here, never a scatter of `cfg.version === 2` checks across
 * the codebase).
 */

/** The newest config version this build of code-forge understands. */
export const CURRENT_VERSION = 1;

/**
 * @param {unknown} raw - the value `yaml.parse()` returned (or a hand-built object in a test).
 * @returns {Record<string, any>} `raw` with `version` normalized to {@link CURRENT_VERSION}.
 * @throws {TypeError} when `raw` isn't a plain mapping (a YAML file that parses to a scalar, an
 *   array, or `null`/`undefined` at the top level).
 * @throws {Error} when `raw.version` is missing-but-invalid (not a positive integer), newer than
 *   {@link CURRENT_VERSION} (a config written by a future code-forge), or older than any version
 *   this build has ever shipped (`< 1`).
 */
export function migrateConfig(raw) {
  if (raw === null || typeof raw !== 'object' || Array.isArray(raw)) {
    // `typeof raw` alone is 'object' for BOTH null and an array — exactly the two cases this
    // guard exists to catch — so the old message read "expected a mapping... got object" even
    // when the actual problem WAS an object-shaped value (fix round 1, MINOR §45/46).
    const kind = raw === null ? 'null' : Array.isArray(raw) ? 'array' : typeof raw;
    throw new TypeError(`migrateConfig: expected a YAML mapping (object) at the document root, got ${kind}`);
  }
  const mapping = /** @type {Record<string, any>} */ (raw);

  const version = mapping.version ?? 1;
  if (!Number.isInteger(version) || version < 1) {
    // Names the TYPE, never the value (round 3): `version: <pasted key>` must not be echoed.
    const kind = mapping.version === null ? 'null' : Array.isArray(mapping.version) ? 'array' : typeof mapping.version;
    throw new Error(`migrateConfig: version must be a positive integer, got a ${kind}`);
  }
  if (version > CURRENT_VERSION) {
    throw new Error(
      `migrateConfig: config version ${version} is newer than this code-forge understands (${CURRENT_VERSION}) — upgrade the package`,
    );
  }

  // version === 1: no structural rewrite needed yet. Future versions add a branch here, applied
  // in order, each one moving the object one version forward before the next branch runs.
  return { ...mapping, version: CURRENT_VERSION };
}
