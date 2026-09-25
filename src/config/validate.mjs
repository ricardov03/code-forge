/**
 * The config validator (plan §1.3 "Validator refusals" + §10.3 B1 acceptance: "14 refusals ⇒ 14
 * tests"). Two independent layers feed one result:
 *
 *  1. **Schema validation** (Ajv, draft 2020-12, `additionalProperties: false` almost everywhere)
 *     catches shape errors: unknown keys, wrong types, missing required fields.
 *  2. **Domain rules** (`DOMAIN_RULES` below, 14 of them) catch things no JSON Schema can express:
 *     cross-field comparisons (two levels resolving to the same tuple), a value that needs a
 *     second config value to be reachable (`second_levels.L2.provider` vs. the effective L2
 *     provider) and rules that reach outside the config object entirely (is a CLI on PATH, is a
 *     model id something a later `--refresh-models` run has seen). Every domain rule is its own
 *     named, independently testable function — most take `cfg` alone; two (`fallback-unknown-
 *     or-cli-absent`, `engine-subprocess-no-cli`) take an **injected** `hasCliOnPath` function
 *     instead of touching `process.env.PATH` themselves, so a unit test never needs a real CLI on
 *     PATH to prove either branch.
 *
 * The 14 rules, and how the plan's prose bullets map onto them (§1.3's "review.multimodel: true
 * with second_provider empty **or with** resolve(L2).provider == second_levels.L2.provider" is
 * two independently-firing conditions, so it is implemented — and tested — as two rules):
 *   1  level-missing                        ERROR
 *   2  unknown-model-id                      WARN  (known_extra / seen-in-cache lift it, C15)
 *   3  duplicate-level-tuple                 ERROR
 *   4  stop-at-not-l3                        ERROR
 *   5  multimodel-missing-second-provider    ERROR
 *   6  multimodel-effective-l2-collision     ERROR  ("effective-L2 consensus rule")
 *   7  judge-family-collision                ERROR  (consensus mode only)
 *   8  orchestrator-l3-multimodel-off        WARN
 *   9  gates-not-argv-array                  ERROR
 *  10  secret-looking-value                  ERROR  (names the key)
 *  11  fallback-unknown-or-cli-absent        WARN   ("fallback validation")
 *  12  engine-subprocess-no-cli              ERROR
 *  13  proof-tool-absent-for-high-tier       WARN
 *  14  shadow-rate-range                     ERROR  ("shadow_rate range")
 *
 * **No config value ever reaches a message (fix round 3, MAJOR).** The `validate` verb prints
 * every message, and a user can paste an API key into ANY string field — a model id, an effort,
 * a fallback, `escalation.stop_at`. So a message names the key PATH and the rule, never the value
 * found there. The only config-derived words a message may contain are (a) key-path segments,
 * each passed through `safeSegment` (a secret pasted in as a map KEY is masked too), and (b) a
 * provider name, only when it is one of the schema's own closed `$defs.provider.enum` words
 * (`safeProvider`) — anything else prints as `(unrecognised provider)`. As a second layer every
 * message is then scrubbed by `finalizeMessage`: B0's `redact()` (registered secrets) plus a local
 * pass that masks every secret-looking token found anywhere in THIS config, so a future rule that
 * regresses and interpolates a value still cannot print one.
 */

// Named import, not default: TypeScript's NodeNext resolution treats ajv's CJS-authored .d.ts as
// non-constructable through the default-export path (`new (import default)()` fails checkJs with
// TS2351) — the named class export resolves cleanly under both checkJs and at runtime. `[facts]`
import { Ajv2020 } from 'ajv/dist/2020.js';
import schema from '../../schema/code-forge.schema.json' with { type: 'json' };
import { redact } from '../util/redact.mjs';
import { isKnownId } from './known-ids.mjs';
import { looksLikeSecret, maskSecretTokens, SECRET_MASK, secretTokensIn } from './secret-patterns.mjs';

/**
 * @typedef {object} ValidationIssue
 * @property {string} rule
 * @property {"error" | "warning"} severity
 * @property {string} message
 * @property {string} [keyword] - schema issues only: the Ajv keyword that failed (`propertyNames`, `type`, …).
 * @property {string} [path] - schema issues only: the (sanitised) JSON pointer of the failing instance.
 */

/**
 * @typedef {object} ValidationResult
 * @property {boolean} valid - `false` when at least one ERROR-severity issue was found (schema or domain).
 * @property {ValidationIssue[]} errors
 * @property {ValidationIssue[]} warnings
 */

const ajv = new Ajv2020({ allErrors: true, strict: true });
const ajvValidate = ajv.compile(schema);

// ── Secret detection (rule 10, and the message scrub every rule's output passes through) ──
// The patterns themselves live in `./secret-patterns.mjs` (shared with load/refresh/the verbs).

/**
 * Collects every secret-looking string in `cfg` — values AND keys, whole string AND each matched
 * token — for `finalizeMessage`'s local scrub.
 * @param {unknown} node
 * @param {Set<string>} out
 */
function collectSecretStrings(node, out) {
  const consider = (/** @type {string} */ text) => {
    if (looksLikeSecret(text)) {
      out.add(text);
      for (const token of secretTokensIn(text)) {
        out.add(token);
      }
    }
  };
  if (typeof node === 'string') {
    consider(node);
    return;
  }
  if (Array.isArray(node)) {
    for (const item of node) {
      collectSecretStrings(item, out);
    }
    return;
  }
  if (node !== null && typeof node === 'object') {
    for (const [key, value] of Object.entries(node)) {
      consider(key);
      collectSecretStrings(value, out);
    }
  }
}

/**
 * The second layer every message passes through: B0's `redact()` (secrets a key resolver has
 * registered), then a local mask of every secret-looking string this config contains (longest
 * first, so a whole value is masked before one of its own tokens could split it), then a final
 * shape-based mask of any secret-shaped token still in the text.
 * @param {string} message
 * @param {string[]} secretsLongestFirst
 * @returns {string}
 */
function finalizeMessage(message, secretsLongestFirst) {
  let out = redact(message);
  for (const secret of secretsLongestFirst) {
    out = out.split(secret).join(SECRET_MASK);
  }
  return maskSecretTokens(out);
}

// ── Safe renderers: the ONLY ways config-derived text may enter a message ──

/** The schema's own closed provider vocabulary — a word from here is a constant, not user data. */
const PROVIDER_NAMES = Object.freeze(/** @type {string[]} */ (schema.$defs.provider.enum));

/**
 * @param {unknown} provider
 * @returns {string} `"anthropic"` (quoted) for a known provider, `(unrecognised provider)` otherwise.
 */
function safeProvider(provider) {
  return typeof provider === 'string' && PROVIDER_NAMES.includes(provider) ? `"${provider}"` : '(unrecognised provider)';
}

/**
 * A key-path segment as it may appear in a message: masked when the KEY itself looks like a secret
 * (a token pasted in as a `keys.<name>` / `known_extra.<provider>` map key), verbatim otherwise.
 * @param {string} segment
 * @returns {string}
 */
function safeSegment(segment) {
  return looksLikeSecret(segment) ? '<redacted-key>' : segment;
}

/**
 * @param {string} pointer - an Ajv `instancePath` (`/a/b/0`).
 * @returns {string} the same pointer, every segment through `safeSegment`.
 */
function safePointer(pointer) {
  if (!pointer) {
    return '(root)';
  }
  return pointer
    .split('/')
    .map((segment, i) => (i === 0 ? segment : safeSegment(segment.replace(/~1/g, '/').replace(/~0/g, '~'))))
    .join('/');
}

// ── 0. schema (Ajv) ─────────────────────────────────────────────────────────

/**
 * Ajv's own `message` templates never interpolate the instance value (only schema-side words:
 * a type name, an allowed-values list, a limit). The parts that come from the CONFIG are the
 * instance path and, for `propertyNames`/`additionalProperties`, the offending key — each rendered
 * through `safeSegment`. An `enum`/`pattern` error that Ajv emits FOR a property name (it carries
 * `err.propertyName`) is folded into the `propertyNames` error that always follows it, so one bad
 * key produces exactly one issue.
 * @param {Record<string, any>} cfg
 * @returns {ValidationIssue[]}
 */
function runSchemaValidation(cfg) {
  const ok = ajvValidate(cfg);
  if (ok) {
    return [];
  }
  return (ajvValidate.errors ?? [])
    .filter((err) => /** @type {any} */ (err).propertyName === undefined)
    .map((err) => {
      const where = safePointer(err.instancePath);
      let detail = err.message ?? 'is invalid';
      if (err.keyword === 'propertyNames' && typeof err.params?.propertyName === 'string') {
        detail = `property name "${safeSegment(err.params.propertyName)}" is not allowed (propertyNames)`;
      } else if (err.keyword === 'additionalProperties' && typeof err.params?.additionalProperty === 'string') {
        detail = `must NOT have additional property "${safeSegment(err.params.additionalProperty)}"`;
      }
      return {
        rule: 'schema',
        severity: /** @type {const} */ ('error'),
        keyword: err.keyword,
        path: where,
        message: `${where} ${detail}`,
      };
    });
}

// ── Small, reusable helpers over an already-parsed config object ───────────

const LEVEL_NAMES = /** @type {const} */ (['L0', 'L1', 'L2', 'L3']);

/**
 * `validateConfig` is documented to accept a config that isn't schema-valid yet (domain rules run
 * even when Ajv already failed it), so every helper below must tolerate a wrongly-shaped value
 * without throwing — the schema layer is what REPORTS a shape violation; a domain rule just needs
 * to not crash on one. `asStringArray` is the one place that trust boundary is enforced: anything
 * that isn't an array of strings becomes `[]` rather than something `.includes()` might throw on
 * (a number) or silently mis-match against (a plain string, which `Set`/`Array` iteration would
 * otherwise split into individual characters).
 * @param {unknown} value
 * @returns {string[]}
 */
function asStringArray(value) {
  return Array.isArray(value) && value.every((item) => typeof item === 'string') ? value : [];
}

/**
 * An OWN entry of a (possibly hostile) map as a string array — `Object.hasOwn`, never a bare index,
 * so a provider named `constructor`/`__proto__` can't resolve to an inherited member.
 * @param {unknown} map
 * @param {string} key
 * @returns {string[]}
 */
function ownStringList(map, key) {
  if (!map || typeof map !== 'object' || !Object.hasOwn(map, key)) {
    return [];
  }
  return asStringArray(/** @type {Record<string, unknown>} */ (map)[key]);
}

/**
 * @param {Record<string, any>} cfg
 * @param {"L0"|"L1"|"L2"|"L3"} levelName
 * @returns {{provider: string, model: string, effort: string | undefined} | null} `null` when the
 *   level, its model, OR its resolved provider is missing/not-a-string — callers that need a
 *   level to exist check `level-missing` first; callers that read `.provider` never see anything
 *   but a real string here.
 */
function effectiveLevel(cfg, levelName) {
  const level = cfg?.levels?.[levelName];
  if (!hasModel(level)) {
    return null;
  }
  const provider = level.provider ?? cfg?.provider;
  if (typeof provider !== 'string') {
    return null;
  }
  return { provider, model: level.model, effort: level.effort };
}

/**
 * @param {unknown} level
 * @returns {level is {model: string, provider?: unknown, effort?: string}}
 */
function hasModel(level) {
  return Boolean(level) && typeof (/** @type {any} */ (level).model) === 'string' && /** @type {any} */ (level).model.length > 0;
}

/**
 * The effective provider for the SECOND reviewer's L2 slot: `review.second_levels.L2.provider`
 * when set, else `review.second_provider` (plan §1.3: `second_levels` is a per-level override on
 * top of `second_provider`, mirroring how `levels.Lx.provider` overrides the top-level `provider`).
 * @param {Record<string, any>} cfg
 * @returns {string | undefined}
 */
function effectiveSecondL2Provider(cfg) {
  return cfg?.review?.second_levels?.L2?.provider ?? cfg?.review?.second_provider ?? undefined;
}

// ── 1. level-missing (ERROR) ────────────────────────────────────────────────

/**
 * Two distinct causes, two distinct messages (round-2 MINOR): a level with no model at all, vs.
 * a level that HAS a model but whose provider can't be resolved (neither `levels.Lx.provider` nor
 * the top-level `provider` is a string) — the second used to be reported as "missing or has no
 * model", pointing the user at the wrong fix.
 */
function checkLevelMissing(cfg) {
  const issues = [];
  for (const name of LEVEL_NAMES) {
    if (effectiveLevel(cfg, name)) {
      continue;
    }
    const message = hasModel(cfg?.levels?.[name])
      ? `levels.${name} has no provider (neither levels.${name}.provider nor the top-level provider is set)`
      : `levels.${name} is missing or has no model`;
    issues.push({ rule: 'level-missing', severity: 'error', message });
  }
  return issues;
}

// ── 2. unknown-model-id (WARN) ──────────────────────────────────────────────

/**
 * @param {Record<string, any>} cfg
 * @param {{seenInCache?: Record<string, string[]>}} [opts]
 */
function checkUnknownModelId(cfg, opts = {}) {
  const issues = [];
  for (const name of LEVEL_NAMES) {
    const level = effectiveLevel(cfg, name);
    if (!level) continue;
    const knownExtra = ownStringList(cfg?.known_extra, level.provider);
    const seen = ownStringList(opts.seenInCache, level.provider);
    if (!isKnownId(level.provider, level.model) && !knownExtra.includes(level.model) && !seen.includes(level.model)) {
      issues.push({
        rule: 'unknown-model-id',
        severity: 'warning',
        message: `levels.${name}.model is not a known ${safeProvider(level.provider)} id, not in known_extra, and not seen-in-cache`,
      });
    }
  }
  return issues;
}

// ── 3. duplicate-level-tuple (ERROR) ────────────────────────────────────────

function checkDuplicateLevelTuple(cfg) {
  /** @type {Map<string, string>} */
  const seenTuples = new Map();
  const issues = [];
  for (const name of LEVEL_NAMES) {
    const level = effectiveLevel(cfg, name);
    if (!level) continue;
    const tuple = JSON.stringify([level.provider, level.model, level.effort ?? '']);
    const first = seenTuples.get(tuple);
    if (first) {
      issues.push({
        rule: 'duplicate-level-tuple',
        severity: 'error',
        message: `levels.${first} and levels.${name} resolve to the same (provider, model, effort)`,
      });
    } else {
      seenTuples.set(tuple, name);
    }
  }
  return issues;
}

// ── 4. stop-at-not-l3 (ERROR) ───────────────────────────────────────────────

function checkStopAtNotL3(cfg) {
  const stopAt = cfg?.escalation?.stop_at;
  if (stopAt !== undefined && stopAt !== 'L3') {
    return [{ rule: 'stop-at-not-l3', severity: 'error', message: 'escalation.stop_at must be "L3"' }];
  }
  return [];
}

// ── 5. multimodel-missing-second-provider (ERROR) ───────────────────────────

function checkMultimodelMissingSecondProvider(cfg) {
  if (cfg?.review?.multimodel === true && !cfg?.review?.second_provider) {
    return [
      {
        rule: 'multimodel-missing-second-provider',
        severity: 'error',
        message: 'review.multimodel is true but review.second_provider is empty',
      },
    ];
  }
  return [];
}

// ── 6. multimodel-effective-l2-collision (ERROR) ────────────────────────────

function checkMultimodelEffectiveL2Collision(cfg) {
  if (cfg?.review?.multimodel !== true) {
    return [];
  }
  const primary = effectiveLevel(cfg, 'L2');
  const second = effectiveSecondL2Provider(cfg);
  if (primary && second && primary.provider === second) {
    return [
      {
        rule: 'multimodel-effective-l2-collision',
        severity: 'error',
        message: `the effective L2 provider (${safeProvider(primary.provider)}) equals the second reviewer's effective L2 provider (review.second_levels.L2.provider, else review.second_provider) — multimodel needs two different providers`,
      },
    ];
  }
  return [];
}

// ── 7. judge-family-collision (ERROR, consensus mode only) ─────────────────

/**
 * Fires when the L3 judge's provider equals EITHER reviewer's provider (not "both" — the old
 * `&&` version could only ever fire when `reviewer1.provider === reviewer2.provider`, which is
 * EXACTLY rule 6's condition, so rule 7 could never fire independently of rule 6). The judge must
 * be independent of BOTH reviewers, so colliding with just one is already a real defect.
 */
function checkJudgeFamilyCollision(cfg) {
  if (cfg?.review?.multimodel !== true) {
    return [];
  }
  const judge = effectiveLevel(cfg, 'L3');
  if (!judge) {
    return [];
  }
  const reviewer1 = effectiveLevel(cfg, 'L2');
  const reviewer2Provider = effectiveSecondL2Provider(cfg);
  const collidesWithReviewer1 = Boolean(reviewer1) && judge.provider === reviewer1.provider;
  const collidesWithReviewer2 = Boolean(reviewer2Provider) && judge.provider === reviewer2Provider;
  if (collidesWithReviewer1 || collidesWithReviewer2) {
    const which = collidesWithReviewer1 && collidesWithReviewer2 ? 'both reviewers' : collidesWithReviewer1 ? 'reviewer 1 (L2)' : 'reviewer 2 (second_levels.L2)';
    return [
      {
        rule: 'judge-family-collision',
        severity: 'error',
        message: `the L3 judge's provider (${safeProvider(judge.provider)}) equals ${which}'s provider — the judge can't independently check a reviewer it shares a family with`,
      },
    ];
  }
  return [];
}

// ── 8. orchestrator-l3-multimodel-off (WARN) ────────────────────────────────

function checkOrchestratorL3MultimodelOff(cfg) {
  if (cfg?.orchestrator === 'L3' && cfg?.review?.multimodel !== true) {
    return [
      {
        rule: 'orchestrator-l3-multimodel-off',
        severity: 'warning',
        message: 'orchestrator is L3 but review.multimodel is off',
      },
    ];
  }
  return [];
}

// ── 9. gates-not-argv-array (ERROR) ─────────────────────────────────────────

const GATE_KEYS = /** @type {const} */ (['test', 'lint', 'types', 'format']);

function isArgvArray(value) {
  // A non-empty array of strings whose first element (the command itself) is non-empty — `[]`
  // is rejected too: `null` already documents "disabled", so an empty argv would run nothing.
  return Array.isArray(value) && value.length > 0 && value.every((item) => typeof item === 'string') && value[0].length > 0;
}

function checkGatesNotArgvArray(cfg) {
  const issues = [];
  for (const key of GATE_KEYS) {
    const value = cfg?.gates?.[key];
    if (value !== undefined && value !== null && !isArgvArray(value)) {
      issues.push({
        rule: 'gates-not-argv-array',
        severity: 'error',
        message: `gates.${key} must be a non-empty argv array of strings, or null`,
      });
    }
  }
  return issues;
}

// ── 10. secret-looking-value (ERROR) ────────────────────────────────────────

/**
 * Checks every string VALUE and every map KEY. A key that itself looks like a secret is reported
 * at its parent path with the key masked — the message must never print the key it is refusing.
 * @param {unknown} node
 * @param {string} path
 * @param {ValidationIssue[]} out
 */
function walkForSecrets(node, path, out) {
  if (typeof node === 'string') {
    if (looksLikeSecret(node)) {
      out.push({
        rule: 'secret-looking-value',
        severity: 'error',
        message: `${path || '(root)'} looks like a secret value — config must hold references, never plaintext secrets`,
      });
    }
    return;
  }
  if (Array.isArray(node)) {
    node.forEach((item, i) => walkForSecrets(item, `${path}[${i}]`, out));
    return;
  }
  if (node !== null && typeof node === 'object') {
    for (const [key, value] of Object.entries(node)) {
      const childPath = path ? `${path}.${safeSegment(key)}` : safeSegment(key);
      if (looksLikeSecret(key)) {
        out.push({
          rule: 'secret-looking-value',
          severity: 'error',
          message: `${childPath} has a key that looks like a secret — config must hold references, never plaintext secrets`,
        });
      }
      walkForSecrets(value, childPath, out);
    }
  }
}

function checkSecretLookingValue(cfg) {
  const issues = [];
  walkForSecrets(cfg, '', issues);
  return issues;
}

// ── 11. fallback-unknown-or-cli-absent (WARN) ───────────────────────────────

/**
 * `levels.<name>.fallback` is documented (and schema-required, when present) to be an array of
 * `{provider, model, effort?}` objects — but `validateConfig` must not crash when it isn't (a
 * string, an object, an array containing `null`, …): that shape violation is ALREADY the schema
 * layer's job to report as a `rule: "schema"` error. This function only ever reads a fallback
 * entry once it has confirmed, itself, that the entry is a plain object with string
 * `provider`/`model` — anything else is skipped, never dereferenced.
 * @param {Record<string, any>} cfg
 * @param {{seenInCache?: Record<string, string[]>, hasCliOnPath?: (provider: string) => boolean}} [opts]
 */
function checkFallbackUnknownOrCliAbsent(cfg, opts = {}) {
  const issues = [];
  for (const name of LEVEL_NAMES) {
    const rawFallback = cfg?.levels?.[name]?.fallback;
    const fallbackList = Array.isArray(rawFallback) ? rawFallback : [];
    for (const [i, fb] of fallbackList.entries()) {
      if (fb === null || typeof fb !== 'object' || typeof fb.provider !== 'string' || typeof fb.model !== 'string') {
        continue; // malformed entry — the schema layer's own required/type checks report this
      }
      const knownExtra = ownStringList(cfg?.known_extra, fb.provider);
      const seen = ownStringList(opts.seenInCache, fb.provider);
      const modelUnknown =
        !isKnownId(fb.provider, fb.model) && !knownExtra.includes(fb.model) && !seen.includes(fb.model);
      const cliAbsent = typeof opts.hasCliOnPath === 'function' && !opts.hasCliOnPath(fb.provider);
      if (modelUnknown || cliAbsent) {
        issues.push({
          rule: 'fallback-unknown-or-cli-absent',
          severity: 'warning',
          message: `levels.${name}.fallback[${i}] (provider ${safeProvider(fb.provider)})${modelUnknown ? ' has an unknown model id' : ''}${
            modelUnknown && cliAbsent ? ' and' : ''
          }${cliAbsent ? ' has no CLI on PATH' : ''}`,
        });
      }
    }
  }
  return issues;
}

// ── 12. engine-subprocess-no-cli (ERROR) ────────────────────────────────────

/**
 * Checks EVERY distinct effective provider `engine: subprocess` could end up spawning — all four
 * levels' own (possibly per-level-overridden) provider, plus the second reviewer's effective L2
 * provider when multimodel is on — not just the top-level `cfg.provider`.
 * @param {Record<string, any>} cfg
 * @param {{hasCliOnPath?: (provider: string) => boolean}} [opts]
 */
function checkEngineSubprocessNoCli(cfg, opts = {}) {
  if (cfg?.engine !== 'subprocess' || typeof opts.hasCliOnPath !== 'function') {
    return [];
  }
  /** @type {Set<string>} */
  const providers = new Set();
  for (const name of LEVEL_NAMES) {
    const level = effectiveLevel(cfg, name);
    if (level) {
      providers.add(level.provider);
    }
  }
  if (cfg?.review?.multimodel === true) {
    const second = effectiveSecondL2Provider(cfg);
    if (typeof second === 'string') {
      providers.add(second);
    }
  }
  const missing = [...providers].filter((provider) => !opts.hasCliOnPath(provider));
  if (missing.length > 0) {
    const named = [...new Set(missing.map(safeProvider))].sort();
    return [
      {
        rule: 'engine-subprocess-no-cli',
        severity: 'error',
        message: `engine is "subprocess" but no CLI was found on PATH for provider(s): ${named.join(', ')}`,
      },
    ];
  }
  return [];
}

// ── 13. proof-tool-absent-for-high-tier (WARN) ──────────────────────────────

function checkProofToolAbsentForHighTier(cfg) {
  const high = cfg?.proof?.tiers?.high;
  if (high && Array.isArray(high.paths) && high.paths.length > 0 && !high.tool) {
    return [
      {
        rule: 'proof-tool-absent-for-high-tier',
        severity: 'warning',
        message: 'proof.tiers.high.paths is set but proof.tiers.high.tool is not',
      },
    ];
  }
  return [];
}

// ── 14. shadow-rate-range (ERROR) ───────────────────────────────────────────

function checkShadowRateRange(cfg) {
  const rate = cfg?.calibration?.shadow_rate;
  // `!(rate >= 0 && rate <= 0.5)`, not `rate < 0 || rate > 0.5` — NaN fails BOTH `<`/`>`
  // comparisons, so the old form silently let YAML's `.nan` through.
  if (typeof rate === 'number' && !(rate >= 0 && rate <= 0.5)) {
    return [
      {
        rule: 'shadow-rate-range',
        severity: 'error',
        message: 'calibration.shadow_rate must be a number within [0, 0.5]',
      },
    ];
  }
  return [];
}

/**
 * The 14 domain rules paired with the exact `rule` id string each one emits — the single source
 * of truth both `DOMAIN_RULES` (below, what `validateConfig` runs) and the exported
 * `DOMAIN_RULE_IDS` are built from. `'schema'` (the ajv layer's own issues) is deliberately not
 * one of these 14.
 * @type {ReadonlyArray<{fn: (cfg: Record<string, any>, opts?: object) => ValidationIssue[], id: string}>}
 */
const RULE_TABLE = Object.freeze([
  { fn: checkLevelMissing, id: 'level-missing' },
  { fn: checkUnknownModelId, id: 'unknown-model-id' },
  { fn: checkDuplicateLevelTuple, id: 'duplicate-level-tuple' },
  { fn: checkStopAtNotL3, id: 'stop-at-not-l3' },
  { fn: checkMultimodelMissingSecondProvider, id: 'multimodel-missing-second-provider' },
  { fn: checkMultimodelEffectiveL2Collision, id: 'multimodel-effective-l2-collision' },
  { fn: checkJudgeFamilyCollision, id: 'judge-family-collision' },
  { fn: checkOrchestratorL3MultimodelOff, id: 'orchestrator-l3-multimodel-off' },
  { fn: checkGatesNotArgvArray, id: 'gates-not-argv-array' },
  { fn: checkSecretLookingValue, id: 'secret-looking-value' },
  { fn: checkFallbackUnknownOrCliAbsent, id: 'fallback-unknown-or-cli-absent' },
  { fn: checkEngineSubprocessNoCli, id: 'engine-subprocess-no-cli' },
  { fn: checkProofToolAbsentForHighTier, id: 'proof-tool-absent-for-high-tier' },
  { fn: checkShadowRateRange, id: 'shadow-rate-range' },
]);

/** The 14 domain rules, in the order documented above. */
const DOMAIN_RULES = Object.freeze(RULE_TABLE.map((entry) => entry.fn));

/** The 14 rule ids `validateConfig` can emit, in `DOMAIN_RULES` order (see `RULE_TABLE`). */
export const DOMAIN_RULE_IDS = Object.freeze(RULE_TABLE.map((entry) => entry.id));

/**
 * @param {Record<string, any>} cfg - a parsed (migrated) config object — need not already be schema-valid.
 * @param {{
 *   seenInCache?: Record<string, string[]>,
 *   hasCliOnPath?: (provider: string) => boolean,
 * }} [opts]
 * @returns {ValidationResult}
 */
export function validateConfig(cfg, opts = {}) {
  const schemaIssues = runSchemaValidation(cfg);
  const domainIssues = DOMAIN_RULES.flatMap((rule) => rule(cfg, opts));
  /** @type {Set<string>} */
  const secretStrings = new Set();
  collectSecretStrings(cfg, secretStrings);
  const secretsLongestFirst = [...secretStrings].sort((a, b) => b.length - a.length);
  const all = [...schemaIssues, ...domainIssues].map((issue) => ({
    ...issue,
    message: finalizeMessage(issue.message, secretsLongestFirst),
  }));
  const errors = all.filter((issue) => issue.severity === 'error');
  const warnings = all.filter((issue) => issue.severity === 'warning');
  return { valid: errors.length === 0, errors, warnings };
}

export { schema as configSchema };
