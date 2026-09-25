/**
 * The Claude Agent-tool alias map (plan §5.2): under `engine: harness` (no Solo, R2 default) the
 * orchestrator calls `Agent(subagent_type: general-purpose, model: <alias>, prompt: <pointer>)` —
 * the Agent tool takes a FAMILY ALIAS (`sonnet|opus|haiku|fable`, per the pinned Claude `--model`
 * help text: "Provide an alias for the latest model (e.g. 'fable', 'opus', or 'sonnet')"), never a
 * full model id, and it has no `effort` parameter at all. So this module does two things: (1) maps
 * a configured model id to its family alias by prefix, and (2) always reports
 * `effort_effective: null` for an Agent-tool dispatch, so the ledger and `doctor` can show that an
 * effort setting silently had no effect there (plan §5.2: "the ledger records `effort_effective:
 * null` and `doctor` prints it" — note the plan's own field name is snake_case; fix round 1
 * corrected this module's key from `effortEffective` to match it exactly).
 *
 * **Fix round 1 (isolated per-file review):**
 *  - The prefix check now requires a WORD boundary: `model === prefix` or `model.startsWith(prefix
 *    + '-')`. A bare `model.startsWith(prefix)` accepted typos like `claude-opusx` or
 *    `claude-sonnetfoo`, which goes against this module's own stated rule ("refuse rather than
 *    guess when an id is mistyped").
 *  - `toAgentToolDispatch` destructures with a default so `undefined`/`null` params produce the
 *    same clear `TypeError` `resolveClaudeAlias` gives for a bad `model`, instead of a generic
 *    property-access crash before that check ever runs.
 *  - The dispatch object's key is now `effort_effective` (was `effortEffective`).
 */

/**
 * Model-id prefix -> Agent-tool alias, matched longest-prefix-first so a future 2-word family name
 * cannot be shadowed by a shorter one that happens to be a prefix of it.
 * @type {ReadonlyArray<[string, string]>}
 */
const FAMILY_ALIASES = Object.freeze(
  /** @type {[string, string][]} */ ([
    ['claude-haiku', 'haiku'],
    ['claude-sonnet', 'sonnet'],
    ['claude-opus', 'opus'],
    ['claude-fable', 'fable'],
  ]).sort((a, b) => b[0].length - a[0].length),
);

/**
 * @param {string} model
 * @param {string} prefix
 * @returns {boolean} true only for an EXACT family match or `prefix` followed by `-` — never a
 *   bare substring prefix (fix round 1, MINOR: refuses `claude-opusx`, `claude-sonnetfoo`, …).
 */
function matchesFamily(model, prefix) {
  return model === prefix || model.startsWith(`${prefix}-`);
}

/**
 * @param {string} model - a resolved Claude model id, e.g. `"claude-opus-5-5"`.
 * @returns {string} the Agent-tool alias for `model`'s family.
 * @throws {Error} when `model` matches no known family prefix — refusing rather than guessing
 *   keeps a typo'd or brand-new id from silently dispatching to the wrong family.
 */
export function resolveClaudeAlias(model) {
  if (typeof model !== 'string' || model.length === 0) {
    throw new TypeError('resolveClaudeAlias: model must be a non-empty string');
  }
  const match = FAMILY_ALIASES.find(([prefix]) => matchesFamily(model, prefix));
  if (!match) {
    throw new Error(`resolveClaudeAlias: no known family alias for model "${model}"`);
  }
  return match[1];
}

/** @typedef {{subagentType: "general-purpose", model: string, effort_effective: null}} AgentToolDispatch */

/**
 * @param {{model?: string, effort?: string}} [params] - `effort` is accepted (a caller may pass the
 *   resolved level's effort through unconditionally) but is never reflected in the Agent-tool
 *   call — see the module doc. Defaults to `{}` so a missing/`undefined` `params` still reaches
 *   {@link resolveClaudeAlias}'s own clear `model` error, instead of crashing on `params.model`
 *   here first.
 * @returns {AgentToolDispatch}
 * @throws {Error} propagated from {@link resolveClaudeAlias} for an unknown/missing family.
 */
export function toAgentToolDispatch({ model } = {}) {
  return { subagentType: 'general-purpose', model: resolveClaudeAlias(/** @type {string} */ (model)), effort_effective: null };
}
