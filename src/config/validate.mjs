/**
 * The config validator (plan §1.3 "Validator refusals" + §10.3 B1 acceptance: "14 refusals ⇒ 14
 * tests"). Two independent layers feed one result:
 *
 *  1. **Schema validation** (Ajv, draft 2020-12, `additionalProperties: false` almost everywhere)
 *     catches shape errors: unknown keys, wrong types, missing required fields.
 *  2. **Domain rules** (`DOMAIN_RULES` below, 20 of them as of B93) catch things no JSON Schema can express:
 *     cross-field comparisons (two levels resolving to the same tuple), a value that needs a
 *     second config value to be reachable (`second_levels.L2.provider` vs. the effective L2
 *     provider) and rules that reach outside the config object entirely (is a CLI on PATH, is a
 *     model id something a later `--refresh-models` run has seen). Every domain rule is its own
 *     named, independently testable function — most take `cfg` alone; two (`fallback-unknown-
 *     or-cli-absent`, `engine-subprocess-no-cli`) take an **injected** `hasCliOnPath` function
 *     instead of touching `process.env.PATH` themselves, so a unit test never needs a real CLI on
 *     PATH to prove either branch.
 *
 * The 16 rules (B32 added `closed-book-on-openai`, B29 `effort-not-valid-for-provider`; before them: B1's original 14 plus B1.1's `caps-coders-exceeds-cap`, minus B1.2's removal of
 * `proof-tool-absent-for-high-tier` — Q16 answered "cut", so `proof.tiers.high.tool` no longer
 * exists to be absent, plan §10.4 Wave 6), and how the plan's prose bullets map onto them (§1.3's
 * "review.multimodel: true with second_provider empty **or with**
 * resolve(L2).provider == second_levels.L2.provider" is two independently-firing conditions, so it
 * is implemented — and tested — as two rules):
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
 *  13  shadow-rate-range                     ERROR  ("shadow_rate range")
 *  14  caps-coders-exceeds-cap               WARN   (v1.3/R10/B1.1: caps.coders > 2)
 *  15  effort-not-valid-for-provider         ERROR  (B29: a level's / fallback's effort vs. its provider)
 *  16  closed-book-on-openai                 WARN   (B32: Codex has no no-tools mode)
 *  17  proof-runners-invalid                 ERROR  (B87: a proof.runners entry: adapter, match, argv tokens, env, timeout_s)
 *  18  review-routes-invalid                 ERROR  (B90: a review.routes rule: level, duplicate level+min_lines, provider vs. the second reviewer)
 *  19  sandbox-dir-refused                   ERROR  (B93: a coder.sandbox.add_dirs entry opens /, the home dir, a secret dir or the run temp tree)
 *  20  sandbox-dirs-unused                   WARN   (B93: coder.sandbox.add_dirs set under a profile other than toolchain)
 *
 * **B1.2 (v1.3, Q16 cut) removed rule:** `proof-tool-absent-for-high-tier` (formerly #13) — the
 * key it warned about (`proof.tiers.high.tool`) left the schema in the same amendment, along with
 * `proof.tiers.high.min_msi`, `proof.js` and `proof.nightly` (the tool-made-mutation-only keys).
 * `proof.tiers.high.paths` alone no longer implies a missing tool, so the rule has nothing left to
 * check. The 15 -> 14 count and the renumbering below (old 14/15 -> new 13/14) are this block's
 * amendment.
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
import { TEST_FILES_TOKEN, CHANGED_FILES_TOKEN } from '../util/gate-token.mjs';
import { checkEffort, displayEffort } from '../engines/efforts.mjs';
import { CLOSED_BOOK_LEVELS, CODEX_CLOSED_BOOK_REFUSAL, isNoClosedBookProvider, OPEN_BOOK_CODEX_WARNING, openBookCodexAllowed } from './closed-book.mjs';
import { isKnownId, resolveLevel } from './known-ids.mjs';
// B90: the routing module's own level list and provider resolution, so rules 16 and 18 check a
// route with exactly the provider the worker spawns it on (they cannot drift)
import { ROUTE_LEVELS, ruleProvider } from '../review/route.mjs';
import { DEFAULT_SANDBOX_PROFILE, SANDBOX_PROFILES, sandboxDirRefusals, SANDBOX_DIR_REFUSED } from '../engines/sandbox-dirs.mjs';
import { tmpBase } from '../util/tmp.mjs';
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

/** B60: the keys a gate given as an object may have. */
const GATE_OBJECT_KEYS = new Set(['argv', 'env']);
/** B60: an environment variable name. */
const ENV_NAME = /^[A-Za-z_][A-Za-z0-9_]*$/;

/**
 * Rule 9's messages for one gate: an argv array, `{argv, env}` (B60), or null. A message names a
 * bad key or env variable NAME, never an env value.
 * @param {string} key @param {unknown} value @returns {string[]}
 */
function gateMessages(key, value) {
  if (value === undefined || value === null) return [];
  // B60: `{test_files}` may expand to nothing, so it cannot be the command — and as the only item a
  // full-suite run would be an empty argv
  /** @param {string[]} argv @returns {string[]} */
  const tokenMessages = (argv) =>
    argv.length === 1 && [TEST_FILES_TOKEN, CHANGED_FILES_TOKEN].includes(argv[0])
      ? [`gates.${key}: '${argv[0]}' cannot be the only item (the full suite would run an empty command)`]
      : [TEST_FILES_TOKEN, CHANGED_FILES_TOKEN].includes(argv[0])
        ? [`gates.${key}: '${argv[0]}' cannot be the command (argv[0])`]
        : [];
  if (typeof value !== 'object' || Array.isArray(value)) {
    if (!isArgvArray(value)) return [`gates.${key} must be a non-empty argv array of strings, an {argv, env} object, or null`];
    return tokenMessages(/** @type {string[]} */ (value));
  }
  const gate = /** @type {Record<string, unknown>} */ (value);
  const messages = Object.keys(gate)
    .filter((k) => !GATE_OBJECT_KEYS.has(k))
    .map((k) => `gates.${key}: unknown key ${JSON.stringify(k)} (an object gate has only argv and env)`);
  if (!isArgvArray(gate.argv)) messages.push(`gates.${key}.argv must be a non-empty argv array of strings`);
  else messages.push(...tokenMessages(/** @type {string[]} */ (gate.argv)));
  if (gate.env !== undefined) {
    if (gate.env === null || typeof gate.env !== 'object' || Array.isArray(gate.env)) {
      messages.push(`gates.${key}.env must be a map of variable names to string values`);
    } else {
      for (const [name, envValue] of Object.entries(gate.env)) {
        if (!ENV_NAME.test(name)) messages.push(`gates.${key}.env: ${JSON.stringify(name)} is not a variable name (letters, digits and _, not starting with a digit)`);
        else if (typeof envValue !== 'string') messages.push(`gates.${key}.env.${name} must be a string`);
      }
    }
  }
  return messages;
}

/** Custom gate refusals are also checked before running commands or reading evidence.
 * @param {unknown} custom @returns {string[]}
 */
export function customGateMessages(custom) {
  if (custom === undefined) return [];
  if (!Array.isArray(custom)) return ['gates.custom must be an array of at most 20 named object gates'];
  const messages = custom.length > 20 ? ['gates.custom must contain at most 20 entries'] : [];
  const names = new Set();
  for (const [i, value] of custom.entries()) {
    const key = `custom[${i}]`;
    if (!value || typeof value !== 'object' || Array.isArray(value)) {
      messages.push(`gates.${key} must be a named object gate`);
      continue;
    }
    const { name, evidence, ...command } = value;
    const label = `${key}${typeof name === 'string' ? ` (${safeSegment(name)})` : ''}`;
    if (typeof name !== 'string' || !/^[a-z][a-z0-9_-]{0,31}$/.test(name)) messages.push(`gates.${label}.name must match ^[a-z][a-z0-9_-]{0,31}$`);
    if (GATE_KEYS.includes(name)) messages.push(`gates.${label}.name must not be a fixed gate name`);
    if (names.has(name)) messages.push(`gates.${label}.name must be unique`);
    names.add(name);
    messages.push(...gateMessages(label, command));
    if (Array.isArray(command.argv) && command.argv.some((arg) => typeof arg === 'string' && [TEST_FILES_TOKEN, CHANGED_FILES_TOKEN].some((token) => arg.includes(token)))) {
      messages.push(`gates.${label}.argv: {test_files} and {changed_files} are refused in custom gates`);
    }
    if (evidence !== undefined) {
      if (!Array.isArray(evidence)) messages.push(`gates.${label}.evidence must be an array of safe relative paths or simple globs`);
      else for (const [j, pattern] of evidence.entries()) {
        // Backslashes are refused as well so a Windows absolute/traversal path is never
        // interpreted differently by validation and the POSIX evidence matcher.
        if (typeof pattern !== 'string' || pattern.length === 0 || pattern.startsWith('/') || /[\\\0]/.test(pattern) || pattern.split('/').some((segment) => segment === '..' || /^[A-Za-z]:/.test(segment))) {
          messages.push(`gates.${label}.evidence[${j}] must be a non-empty relative path or glob without absolute paths or a .. segment`);
        }
      }
    }
  }
  return messages;
}

function checkGatesNotArgvArray(cfg) {
  const messages = GATE_KEYS.flatMap((key) => gateMessages(key, cfg?.gates?.[key]));
  messages.push(...customGateMessages(cfg?.gates?.custom));
  if (Object.hasOwn(cfg?.gates ?? {}, 'repeat_new_tests')) {
    const n = cfg.gates.repeat_new_tests;
    if (!Number.isInteger(n) || n < 2 || n > 10) messages.push('gates.repeat_new_tests must be an integer 2..10');
  }
  return messages.map((message) => ({ rule: 'gates-not-argv-array', severity: 'error', message }));
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

// ── 13. shadow-rate-range (ERROR) ───────────────────────────────────────────

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

// ── 14. caps-coders-exceeds-cap (WARN, v1.3/B1.1) ───────────────────────────

/**
 * R10 caps concurrent coders at 2 for THIS build, but a consumer's config is free to set more —
 * the schema itself only enforces `minimum: 1` (plan §1.3: "`caps.coders > 2` ⇒ warn (R10 binds
 * this build; a consumer may run more with the warning on record)"). Guarded on a real finite
 * number so a non-numeric value — already a schema-layer error on its own — never also fires this
 * rule.
 * @param {Record<string, any>} cfg
 */
function checkCapsCodersExceedsCap(cfg) {
  const coders = cfg?.caps?.coders;
  if (typeof coders === 'number' && Number.isFinite(coders) && coders > 2) {
    return [
      {
        rule: 'caps-coders-exceeds-cap',
        severity: 'warning',
        message: 'caps.coders is above the R10 cap of 2 concurrent coders — allowed, but recorded as a deviation',
      },
    ];
  }
  return [];
}

// ── 15. effort-not-valid-for-provider (ERROR, B29) ──────────────────────────

/**
 * A provider name for an effort message: bare (`openai`) only when it is one of the schema's
 * closed provider words, `(unknown provider)` otherwise — never an unchecked config string.
 * @param {unknown} provider
 * @returns {string}
 */
function effortProvider(provider) {
  return typeof provider === 'string' && PROVIDER_NAMES.includes(provider) ? provider : '(unknown provider)';
}

/**
 * One effort issue for the effort at `keyPath` on `provider`, or `null` when it is fine. The
 * builders throw for exactly the efforts `checkEffort` refuses, so this rule moves that spawn-time
 * failure to config time. The effort is shown only through `displayEffort` (a closed-list word,
 * else `(unrecognised value)`), the provider only through `effortProvider`. A non-string effort is
 * left to the schema layer; an unknown provider is skipped (the schema layer reports it).
 * @param {string} keyPath
 * @param {unknown} provider
 * @param {unknown} effort
 * @returns {ValidationIssue | null}
 */
function effortIssue(keyPath, provider, effort) {
  if (typeof effort !== 'string') return null;
  const result = checkEffort(provider, effort);
  const who = effortProvider(provider);
  /** @type {string} */
  let message;
  switch (result.kind) {
    case 'invalid':
      message = `${keyPath} ${displayEffort(effort)} is not valid for provider ${who}; use one of: ${result.allowed.join(', ')}`;
      break;
    case 'unsupported':
      message = `${keyPath} is set but provider ${who} takes no effort; remove it`;
      break;
    case 'empty':
      message = `${keyPath} is empty; provider ${who} needs a non-empty effort or none at all`;
      break;
    default: // 'ok', 'unknown-provider'
      return null;
  }
  return { rule: 'effort-not-valid-for-provider', severity: 'error', message };
}

/**
 * Checks each level's effort against its effective provider, and each fallback entry's effort
 * against that entry's own provider (the spawner hands a fallback step's effort to that provider's
 * builder). Both efforts are read from the SAME raw source the spawner uses — `cfg.levels.<Lx>` as
 * written (`resolveLevel` in `known-ids.mjs` copies `level.effort` and `level.fallback` verbatim,
 * and no loader merges provider defaults into a config, see `load.mjs`), so every value checked
 * here was set in `levels.<Lx>` itself and the message names exactly that key.
 * @param {Record<string, any>} cfg
 */
function checkEffortNotValidForProvider(cfg) {
  /** @type {ValidationIssue[]} */
  const issues = [];
  for (const name of LEVEL_NAMES) {
    const level = effectiveLevel(cfg, name);
    if (level) {
      const issue = effortIssue(`levels.${name}.effort`, level.provider, level.effort);
      if (issue) issues.push(issue);
    }
    const rawFallback = cfg?.levels?.[name]?.fallback;
    const fallbackList = Array.isArray(rawFallback) ? rawFallback : [];
    for (const [i, fb] of fallbackList.entries()) {
      if (fb === null || typeof fb !== 'object') continue;
      const issue = effortIssue(`levels.${name}.fallback[${i}].effort`, fb.provider, fb.effort);
      if (issue) issues.push(issue);
    }
  }
  return issues;
}

// ── 16. closed-book-on-openai (WARN, B32) ──────────────────────────────────

/**
 * L2 (reviewers) and L3 (judge, S2, author) run closed-book, and Codex has no no-tools mode
 * (`closed-book.mjs`): the spawner refuses an openai level there and skips an openai fallback.
 * One warning per place that resolves to openai — the level itself, each of its fallbacks, and
 * the second reviewer's effective L2 provider when multimodel is on. WARN, not ERROR: the
 * `provider: openai` default writes openai on every level, and the coders still run on Codex.
 * With `review.allow_open_book_codex: true` the warnings name the opt-in's cost instead.
 * @param {Record<string, any>} cfg
 */
function checkClosedBookOnOpenai(cfg) {
  /** @type {string[]} */
  const where = [];
  for (const name of CLOSED_BOOK_LEVELS) {
    // The level and its fallbacks exactly as the spawner's ladder sees them (fix round 1):
    // `resolveLevel` is what `spawnSession` and `resolve` call. A level it cannot resolve is
    // `level-missing`'s to report; the spawner cannot run it either.
    let resolved;
    try {
      resolved = resolveLevel(cfg, /** @type {"L2"|"L3"} */ (name));
    } catch {
      continue;
    }
    if (isNoClosedBookProvider(resolved.provider)) where.push(`levels.${name}`);
    resolved.fallback.forEach((fb, i) => {
      if (isNoClosedBookProvider(fb?.provider)) where.push(`levels.${name}.fallback[${i}]`);
    });
  }
  if (cfg?.review?.multimodel === true && isNoClosedBookProvider(effectiveSecondL2Provider(cfg))) {
    where.push('review.second_levels.L2 (else review.second_provider)');
  }
  // B90: a review.routes rule replaces an L2/L3 level for a big file — the same closed-book limit
  routeRulesOf(cfg).forEach((rule, i) => {
    if (!isPlainObject(rule)) return;
    if (isNoClosedBookProvider(ruleProvider(cfg, rule))) where.push(`review.routes[${i}]`);
    if (Array.isArray(rule.fallback)) {
      rule.fallback.forEach((fb, j) => {
        if (isNoClosedBookProvider(fb?.provider)) where.push(`review.routes[${i}].fallback[${j}]`);
      });
    }
    // like the second reviewer's own check above: only with multimodel on, and with the same
    // effective second provider (the rule's own, else second_levels.L2, else second_provider)
    if (cfg?.review?.multimodel === true && isPlainObject(rule.second) && isNoClosedBookProvider(rule.second.provider ?? effectiveSecondL2Provider(cfg))) {
      where.push(`review.routes[${i}].second`);
    }
  });
  // B32 opt-in: the same places, but the text says what the opt-in costs instead of "refused".
  const text = openBookCodexAllowed(cfg) ? OPEN_BOOK_CODEX_WARNING : CODEX_CLOSED_BOOK_REFUSAL;
  return where.map((key) => ({
    rule: 'closed-book-on-openai',
    severity: /** @type {const} */ ('warning'),
    message: `${key} resolves to openai — ${text}`,
  }));
}

// ── 17. proof-runners-invalid (ERROR, B87) ──────────────────────────────────

/**
 * B87: the argv tokens each `proof.runners` adapter expands. A token is a whole argv item, never
 * the command. `proof/runners.mjs` expands exactly these (proof may import config; config may not
 * import proof).
 */
export const RUNNER_ADAPTER_TOKENS = Object.freeze({
  junit: Object.freeze(['{test_file}', '{test_id}', '{test_filter}', '{report}']),
  swift: Object.freeze(['{test_file}', '{test_id}', '{test_filter}', '{report}']),
  // B102: the values come from the entry's `xcode` object; `{only_testing}` names the test and
  // `{result_bundle}` is the fresh result bundle path code-forge reads
  xcodebuild: Object.freeze(['{workspace}', '{project}', '{scheme}', '{destination}', '{only_testing}', '{result_bundle}']),
});
/** B87: the tokens that name the test; a `junit` or `swift` runner needs at least one (a proof must name its test). */
export const RUNNER_TEST_TOKENS = Object.freeze(['{test_file}', '{test_id}', '{test_filter}']);
/** B87: every token any adapter knows. */
const ALL_RUNNER_TOKENS = Object.freeze(['{test_file}', '{test_id}', '{test_filter}', '{report}', '{workspace}', '{project}', '{scheme}', '{destination}', '{only_testing}', '{result_bundle}']);
/** B102: the keys of an `xcodebuild` entry's `xcode` object, and the ones it must have. */
export const XCODE_KEYS = Object.freeze(['workspace', 'project', 'scheme', 'destination', 'test_target']);
const XCODE_REQUIRED = Object.freeze(['scheme', 'destination', 'test_target']);
/** B102: the tokens an `xcodebuild` argv must have, and why. */
const XCODE_REQUIRED_TOKENS = Object.freeze([
  ['{scheme}', 'the xcodebuild adapter fills it from xcode.scheme'],
  ['{destination}', 'the xcodebuild adapter fills it from xcode.destination, the one the precheck checks'],
  ['{only_testing}', 'a proof must name its test'],
  ['{result_bundle}', 'the xcodebuild adapter reads the result bundle code-forge places there'],
]);
/** B87: an item that is a whole `{word}` is read as a token. */
const TOKEN_SHAPE = /^\{[a-z_]+\}$/;
/** B87: the bounds of `proof.runners[].timeout_s`, in seconds. */
const RUNNER_TIMEOUT_MIN_S = 60;
const RUNNER_TIMEOUT_MAX_S = 7200;
/** B87: the keys a runner entry may have (the schema's `additionalProperties: false`, as a rule). */
const RUNNER_KEYS = Object.freeze(['match', 'adapter', 'argv', 'env', 'timeout_s', 'xcode']);
/** B87: every token some adapter lists; with a bad adapter, an item outside it is still refused. */
const ANY_ADAPTER_TOKENS = Object.freeze([...new Set(Object.values(RUNNER_ADAPTER_TOKENS).flat())]);
/** B102: the report items and the test tokens of every adapter; a bad adapter's argv needs one of each. */
const ANY_REPORT_TOKENS = Object.freeze(['{report}', '{result_bundle}']);
const ANY_TEST_TOKENS = Object.freeze([...RUNNER_TEST_TOKENS, '{only_testing}']);

/**
 * Rule 17's messages for one runner entry. A message names the key path and the rule; it may name
 * an env variable NAME or an unknown entry key (key paths), never a value (argv items, env values) — only the adapter's
 * own token words, which are code-forge's. The argv checks run even when the adapter is bad, so
 * one pass shows every error. `proof/runners.mjs` checks the snapshot's entries with it again
 * before it runs one.
 * @param {unknown} entry @param {number} i @returns {string[]}
 */
export function runnerMessages(entry, i) {
  const at = `proof.runners[${i}]`;
  if (entry === null || typeof entry !== 'object' || Array.isArray(entry)) return [`${at} must be an object with match, adapter and argv`];
  const runner = /** @type {Record<string, unknown>} */ (entry);
  const messages = [];
  const adapter = typeof runner.adapter === 'string' && Object.hasOwn(RUNNER_ADAPTER_TOKENS, runner.adapter) ? runner.adapter : null;
  if (adapter === null) messages.push(`${at}.adapter must be one of: ${Object.keys(RUNNER_ADAPTER_TOKENS).join(', ')}`);
  for (const key of Object.keys(runner)) {
    if (!RUNNER_KEYS.includes(key)) messages.push(`${at}: ${JSON.stringify(key)} is not a runner key (${RUNNER_KEYS.join(', ')})`);
  }
  const match = runner.match;
  if (!Array.isArray(match) || match.length === 0 || !match.every((glob) => typeof glob === 'string' && glob.length > 0)) {
    messages.push(`${at}.match must be a non-empty list of globs`);
  }
  if (!isArgvArray(runner.argv)) {
    messages.push(`${at}.argv must be a non-empty argv array of strings`);
  } else {
    const argv = /** @type {string[]} */ (runner.argv);
    if (TOKEN_SHAPE.test(argv[0])) messages.push(`${at}.argv[0]: a token cannot be the command`);
    // with a bad adapter, a token-shaped item is checked against every adapter's tokens instead;
    // every other argv check still runs
    const known = adapter === null ? null : RUNNER_ADAPTER_TOKENS[/** @type {keyof typeof RUNNER_ADAPTER_TOKENS} */ (adapter)];
    argv.forEach((item, j) => {
      if (item.includes('\0')) {
        messages.push(`${at}.argv[${j}] contains a NUL character`);
      } else if (j > 0 && TOKEN_SHAPE.test(item) && known !== null && !known.includes(item)) {
        messages.push(`${at}.argv[${j}] is not a token of the ${adapter} adapter (${known.join(', ')})`);
      } else if (j > 0 && TOKEN_SHAPE.test(item) && known === null && !ANY_ADAPTER_TOKENS.includes(item)) {
        messages.push(`${at}.argv[${j}] is not a token of any adapter (${ANY_ADAPTER_TOKENS.join(', ')})`);
      } else if (!TOKEN_SHAPE.test(item) && ALL_RUNNER_TOKENS.some((token) => item.includes(token))) {
        messages.push(`${at}.argv[${j}]: a token must be a whole argv item (give it as its own item)`);
      }
    });
    // the required tokens depend on the adapter; with a bad adapter, every adapter's report items
    // and test tokens count, so an argv that names no report or no test is still refused
    const items = argv.slice(1);
    if (adapter === 'junit' || adapter === 'swift') {
      if (!items.includes('{report}')) messages.push(`${at}.argv needs a {report} item (the ${adapter} adapter reads the report code-forge places there)`);
      if (!items.some((item) => RUNNER_TEST_TOKENS.includes(item))) messages.push(`${at}.argv needs a test token (${RUNNER_TEST_TOKENS.join(', ')}): a proof must name its test`);
    } else if (adapter === 'xcodebuild') {
      for (const [token, why] of XCODE_REQUIRED_TOKENS) if (!items.includes(token)) messages.push(`${at}.argv needs a ${token} item (${why})`);
    } else {
      if (!items.some((item) => ANY_REPORT_TOKENS.includes(item))) messages.push(`${at}.argv needs a report item (${ANY_REPORT_TOKENS.join(', ')}): every adapter reads the report code-forge places there`);
      if (!items.some((item) => ANY_TEST_TOKENS.includes(item))) messages.push(`${at}.argv needs a test token (${ANY_TEST_TOKENS.join(', ')}): a proof must name its test`);
    }
  }
  messages.push(...xcodeMessages(runner, adapter, at));
  if (runner.env !== undefined) {
    if (runner.env === null || typeof runner.env !== 'object' || Array.isArray(runner.env)) {
      messages.push(`${at}.env must be a map of variable names to string values`);
    } else {
      for (const [name, envValue] of Object.entries(runner.env)) {
        if (!ENV_NAME.test(name)) messages.push(`${at}.env: ${JSON.stringify(name)} is not a variable name (letters, digits and _, not starting with a digit)`);
        else if (typeof envValue !== 'string') messages.push(`${at}.env.${name} must be a string`);
        else if (envValue.includes('\0')) messages.push(`${at}.env.${name} contains a NUL character`);
      }
    }
  }
  if (runner.timeout_s !== undefined) {
    const t = runner.timeout_s;
    if (typeof t !== 'number' || !Number.isInteger(t) || t < RUNNER_TIMEOUT_MIN_S || t > RUNNER_TIMEOUT_MAX_S) {
      messages.push(`${at}.timeout_s must be a whole number of seconds from ${RUNNER_TIMEOUT_MIN_S} to ${RUNNER_TIMEOUT_MAX_S}`);
    }
  }
  return messages;
}

/** @param {unknown} value @returns {boolean} */
const isPlainObject = (value) => value !== null && typeof value === 'object' && !Array.isArray(value);

/**
 * B102: rule 17's messages for an entry's `xcode` object. An `xcodebuild` entry needs one; any other
 * adapter refuses one. Its values are argv items of `xcodebuild`, so each is a non-empty string with
 * no NUL that does not start with `-` (it would be read as an option). Messages name key paths only.
 * @param {Record<string, unknown>} runner @param {string | null} adapter @param {string} at
 * @returns {string[]}
 */
function xcodeMessages(runner, adapter, at) {
  const xcode = runner.xcode;
  if (xcode === undefined) return adapter === 'xcodebuild' ? [`${at}.xcode is required for the xcodebuild adapter (${XCODE_REQUIRED.join(', ')})`] : [];
  if (adapter !== null && adapter !== 'xcodebuild') return [`${at}.xcode is only for the xcodebuild adapter`];
  if (!isPlainObject(xcode)) return [`${at}.xcode must be an object with ${XCODE_REQUIRED.join(', ')}`];
  const values = /** @type {Record<string, unknown>} */ (xcode);
  const messages = [];
  for (const key of Object.keys(values)) {
    if (!XCODE_KEYS.includes(key)) messages.push(`${at}.xcode: ${JSON.stringify(key)} is not an xcode key (${XCODE_KEYS.join(', ')})`);
  }
  for (const key of XCODE_KEYS) {
    const value = values[key];
    if (value === undefined) {
      if (XCODE_REQUIRED.includes(key)) messages.push(`${at}.xcode.${key} is required`);
    } else if (typeof value !== 'string' || value.length === 0 || value.startsWith('-')) {
      messages.push(`${at}.xcode.${key} must be a non-empty string that does not start with "-"`);
    } else if (value.includes('\0')) {
      messages.push(`${at}.xcode.${key} contains a NUL character`);
    } else if (key === 'test_target' && value.includes('/')) {
      messages.push(`${at}.xcode.test_target must be one target name, without "/"`);
    }
  }
  if (values.workspace !== undefined && values.project !== undefined) messages.push(`${at}.xcode: give workspace or project, not both`);
  // a {workspace} / {project} item and its xcode key come together: one without the other is a
  // value xcodebuild would never see, or an argv item with no value
  const argv = isArgvArray(runner.argv) ? /** @type {string[]} */ (runner.argv).slice(1) : null;
  if (argv !== null) {
    for (const key of ['workspace', 'project']) {
      const token = `{${key}}`;
      if (argv.includes(token) && values[key] === undefined) messages.push(`${at}.argv has ${token} but ${at}.xcode.${key} is not set`);
      if (!argv.includes(token) && values[key] !== undefined) messages.push(`${at}.xcode.${key} is set but ${at}.argv has no ${token} item`);
    }
  }
  return messages;
}

function checkProofRunnersInvalid(cfg) {
  const runners = cfg?.proof?.runners;
  if (runners === undefined) return [];
  const messages = !Array.isArray(runners)
    ? ['proof.runners must be a list of runner entries']
    : runners.length === 0
      ? ['proof.runners must list at least one runner (leave the key out for none)']
      : runners.flatMap((entry, i) => runnerMessages(entry, i));
  return messages.map((message) => ({ rule: 'proof-runners-invalid', severity: /** @type {const} */ ('error'), message }));
}

// ── 18. review-routes-invalid (ERROR, B90) ──────────────────────────────────

/** @param {Record<string, any>} cfg @returns {any[]} the `review.routes` list, `[]` when it is not one. */
function routeRulesOf(cfg) {
  const routes = cfg?.review?.routes;
  return Array.isArray(routes) ? routes : [];
}

/**
 * B90: what the schema cannot say about `review.routes` — a level other than L2 or L3, two rules
 * for the same level and `min_lines` (which one wins would be order-dependent), and, with
 * `review.multimodel` on, an L2 rule whose provider (its own, else the top-level one) equals its
 * own `second.provider`, or the second reviewer's effective L2 provider — the consensus slot B a
 * routed ticket really runs with — (the consensus needs two providers on a big file too).
 * Messages name key paths only, never a value.
 * @param {Record<string, any>} cfg
 */
function checkReviewRoutesInvalid(cfg) {
  const routes = cfg?.review?.routes;
  if (routes === undefined || routes === null) return [];
  /** @type {string[]} */
  const messages = [];
  if (!Array.isArray(routes)) {
    messages.push('review.routes must be a list of rules');
  } else {
    /** @type {Map<string, number>} */
    const seen = new Map();
    const second = effectiveSecondL2Provider(cfg);
    routes.forEach((rule, i) => {
      const at = `review.routes[${i}]`;
      if (!isPlainObject(rule)) {
        messages.push(`${at} must be a rule object`);
        return;
      }
      const level = rule.level ?? 'L2';
      if (!ROUTE_LEVELS.includes(level)) {
        messages.push(`${at}.level must be L2 or L3`);
        return;
      }
      if (Number.isInteger(rule.min_lines)) {
        const k = `${level}\0${rule.min_lines}`;
        const first = seen.get(k);
        if (first === undefined) seen.set(k, i);
        else messages.push(`${at} has the same level and min_lines as review.routes[${first}]`);
      }
      if (cfg?.review?.multimodel === true && level === 'L2') {
        // the provider the worker spawns this rule on: its own, else the top-level one (the rule
        // REPLACES levels.L2, so levels.L2.provider is never inherited)
        const first = ruleProvider(cfg, rule);
        const own = isPlainObject(rule.second) ? rule.second.provider : undefined;
        if (own !== undefined && first === own) {
          // the rule's own second reviewer
          messages.push(`${at}.provider (else provider) equals ${at}.second.provider — multimodel needs two different providers`);
        } else if (second !== undefined && first === second) {
          // the consensus slot B a routed ticket runs with (it keeps review.second_levels.L2)
          messages.push(`${at}.provider (else provider) equals the second reviewer's effective L2 provider (review.second_levels.L2.provider, else review.second_provider) — multimodel needs two different providers`);
        }
      }
    });
  }
  return messages.map((message) => ({ rule: 'review-routes-invalid', severity: /** @type {const} */ ('error'), message }));
}

// ── 19. sandbox-dir-refused (ERROR, B93) ────────────────────────────────────
/**
 * A `coder.sandbox.add_dirs` entry that is not absolute / `~/`, or that is `/`, the home dir, or
 * is, holds or sits inside a secret dir (`~/.code-forge`, `~/.codex`, `~/.ssh`, …) or the run
 * temp base (every run temp root and session CODEX_HOME live under it). One issue per refused
 * entry; the message names the key path, never the value. The builder checks again at spawn.
 * @param {Record<string, any>} cfg
 * @returns {ValidationIssue[]}
 */
function checkSandboxDirRefused(cfg) {
  const sandbox = isPlainObject(cfg.coder) ? cfg.coder.sandbox : undefined;
  // a non-list add_dirs, or a non-string entry, is the schema layer's issue (fix round 1)
  if (!isPlainObject(sandbox) || !Array.isArray(sandbox.add_dirs)) return [];
  const dirs = /** @type {unknown[]} */ (sandbox.add_dirs);
  /** @type {string[]} */
  const extra = [];
  try {
    extra.push(tmpBase(isPlainObject(cfg.tmp) ? cfg.tmp.root ?? undefined : undefined));
  } catch {
    extra.push(tmpBase()); // a bad tmp.root is the schema's issue; the default base still counts
  }
  const nonStrings = new Set(dirs.flatMap((entry, i) => (typeof entry === 'string' ? [] : [`coder.sandbox.add_dirs[${i}]`])));
  return sandboxDirRefusals(dirs, { extra })
    .filter((r) => !nonStrings.has(r.keyPath))
    .map((r) => ({
      rule: SANDBOX_DIR_REFUSED,
      severity: /** @type {const} */ ('error'),
      message: `${r.keyPath} is refused — ${r.why}`,
    }));
}

// ── 20. sandbox-dirs-unused (WARN, B93 fix round 1) ─────────────────────────
/**
 * `coder.sandbox.add_dirs` is set (a non-empty list) but the profile is not `toolchain`: only
 * `toolchain` adds `--add-dir`, so the entries change nothing (rule 19 still refuses a bad one).
 * The message names the key path and the profile, never a dir.
 * @param {Record<string, any>} cfg
 * @returns {ValidationIssue[]}
 */
function checkSandboxDirsUnused(cfg) {
  const sandbox = isPlainObject(cfg.coder) ? cfg.coder.sandbox : undefined;
  if (!isPlainObject(sandbox) || !Array.isArray(sandbox.add_dirs) || sandbox.add_dirs.length === 0) return [];
  const profile = sandbox.profile ?? DEFAULT_SANDBOX_PROFILE;
  if (profile === 'toolchain' || !SANDBOX_PROFILES.includes(profile)) return [];
  return [{
    rule: 'sandbox-dirs-unused',
    severity: /** @type {const} */ ('warning'),
    message: `coder.sandbox.add_dirs is set but coder.sandbox.profile is ${profile}, which does not use it — only toolchain adds --add-dir`,
  }];
}

/**
 * The 20 domain rules paired with the exact `rule` id string each one emits — the single source
 * of truth both `DOMAIN_RULES` (below, what `validateConfig` runs) and the exported
 * `DOMAIN_RULE_IDS` are built from. `'schema'` (the ajv layer's own issues) is deliberately not
 * one of these 20 (B29 added rule 15, B32 rule 16, B87 rule 17, B90 rule 18, B93 rules 19 and 20). (B1.2/v1.3, Q16 cut: `proof-tool-absent-for-high-tier` removed, 15 -> 14.)
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
  { fn: checkShadowRateRange, id: 'shadow-rate-range' },
  { fn: checkCapsCodersExceedsCap, id: 'caps-coders-exceeds-cap' },
  { fn: checkEffortNotValidForProvider, id: 'effort-not-valid-for-provider' },
  { fn: checkClosedBookOnOpenai, id: 'closed-book-on-openai' },
  { fn: checkProofRunnersInvalid, id: 'proof-runners-invalid' },
  { fn: checkReviewRoutesInvalid, id: 'review-routes-invalid' },
  { fn: checkSandboxDirRefused, id: 'sandbox-dir-refused' },
  { fn: checkSandboxDirsUnused, id: 'sandbox-dirs-unused' },
]);

/** The 20 domain rules, in the order documented above. */
const DOMAIN_RULES = Object.freeze(RULE_TABLE.map((entry) => entry.fn));

/** The 20 rule ids `validateConfig` can emit, in `DOMAIN_RULES` order (see `RULE_TABLE`). */
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

/** The effective file loop for a validated config, including opt-in commit watch.
 * @param {Record<string, any>} cfg @returns {string}
 */
export function reviewFileLoop(cfg) {
  return cfg?.review?.file_loop ?? 'wait';
}

/** Maximum workers for a validated config (the schema restricts it to 1..4).
 * @param {Record<string, any>} cfg @returns {number}
 */
export function reviewWorkers(cfg) {
  return cfg?.review?.workers ?? 1;
}
