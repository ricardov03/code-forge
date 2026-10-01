/**
 * Claude Code 2.1.282 argv builder (plan §5.2, pinned against `test/fixtures/help/claude-2.1.282.txt`
 * — B0's byte copy of `claude --help` on this Mac, 2026-09-24). Pure: no I/O, no CLI spawning
 * (`src/util/exec.mjs` is the one place that spawns; B9's `spawn.mjs` calls it with this argv).
 *
 * Six roles collapse to two shapes: `coder` (writes files, cwd = the project tree) and everything
 * else — `reviewer`/`judge`/`s2`/`author`/`facts` (closed-book, cwd = an empty temp dir with the
 * packet). `facts` is the one role Claude treats specially even within the closed-book shape
 * (§0.6.7, §8.4): `--tools ""` becomes `--tools "Bash"`, and `--disallowedTools` + `--restricted`
 * are added so the facts delegate can run read-only shell commands but nothing that writes.
 *
 * **Return contract (fix round 3, root ruling — corrects plan §5.2's "<brief pointer>" for the
 * closed-book shape):** `{cli, role, argv, cwd, stdinFile?, forbiddenRendered?}`.
 *  - `coder`: the brief is a POINTER — the coder has tools and opens it itself. argv ends with a
 *    literal `--` then `promptPath`, so no variadic flag (`--disallowedTools <tools...>`) can ever
 *    swallow it. No `stdinFile`.
 *  - closed-book (`reviewer`/`judge`/`s2`/`author`/`facts`): a role built with `--tools ""` cannot
 *    open a path, so a pointer would leave the model reviewing nothing. The argv carries NO prompt
 *    positional (and no `--`); `stdinFile === promptPath` names the packet file whose CONTENT the
 *    spawner (B9) must pipe to the child's stdin. `claude -p` with no prompt argument reads the
 *    prompt from stdin (`--input-format` default "text", help fixture line "--input-format <format>
 *    Input format (only works with --print)"); the root verified the stdin form against the real
 *    `claude -p --safe-mode --tools ""` on this Mac (2026-09-24). `facts` has `Bash` and could
 *    read the file, but it takes the same stdin path so every closed-book role has ONE delivery
 *    mechanism.
 *
 * **Doctor probe item for B13 (no network in B4's tests):** `claude: closed-book stdin delivery` —
 * spawn this module's `reviewer` argv with `stdinFile` piped in, the packet containing a unique
 * canary token and the instruction "reply with the canary"; PASS when the JSON `result` contains
 * the canary, FAIL (`claude: closed-book packet not delivered`) otherwise.
 *
 * **Forbidden-list rendering is a PARAMETER** (`renderedForbidden`, default
 * `renderForClaude(FORBIDDEN)` from B8's landed `src/util/forbidden.mjs`): this module only
 * flattens whatever render it is given, so it never hard-codes a rule-string shape (seam C17).
 * Both `coder` and `facts` refuse an empty render (0 usable rules) — a renderer regression must
 * be a thrown error, never a silently weaker deny list.
 *
 * `--fallback-model <model>` takes exactly ONE model id in the pinned help. Only the first
 * same-provider (`anthropic`) fallback whose model DIFFERS from the main `--model` is used —
 * Claude Code refuses to start when the two are equal.
 */

import { FORBIDDEN, mergeForbidden, renderForClaude } from '../../util/forbidden.mjs';
import { flattenRuleStrings, hasAnyRenderedRule } from '../render-rules.mjs';
import { assertBaseParams } from './validate-params.mjs';

export const CLI = 'claude';

/**
 * @typedef {{id: string, rules: ReadonlyArray<string>, enforced: boolean}} RenderedForbiddenEntry
 */

/**
 * @typedef {{provider: string, model: string, effort?: string}} FallbackEntry
 */

/**
 * @typedef {object} ClaudeBuildParams
 * @property {"coder"|"reviewer"|"judge"|"s2"|"author"|"facts"} role
 * @property {string} model - the resolved model id (already picked by `resolveLevel`, B1).
 * @property {string} [effort]
 * @property {string} promptPath - the brief file (coder: passed as a pointer) or the packet file
 *   (closed-book: returned as `stdinFile`, its content is piped to stdin by the spawner).
 * @property {string} cwd - the project tree (coder) or an empty isolated temp dir (closed-book).
 * @property {ReadonlyArray<RenderedForbiddenEntry>} [renderedForbidden] - used by the coder role
 *   (default `renderForClaude(mergeForbidden())`, B4.2) and the `facts` role (default
 *   `renderForClaude(FORBIDDEN)`, unchanged).
 * @property {ReadonlyArray<FallbackEntry>} [fallback] - coder role only; see the module doc.
 * @property {number} [maxBudgetUsd] - must be a finite number > 0 when given.
 * @property {Record<string, any>} [schema] - compiled JSON Schema object; closed-book roles only,
 *   sent inline (`--json-schema`, help text example is an inline JSON string, not a file path).
 * @property {string} [systemPromptText] - the lens preamble; closed-book roles only.
 */

/**
 * @typedef {object} BuiltCoder
 * @property {"claude"} cli
 * @property {"coder"} role
 * @property {string[]} argv - ends with `--`, promptPath.
 * @property {string} cwd
 * @property {ReadonlyArray<RenderedForbiddenEntry>} forbiddenRendered
 * @property {undefined} [stdinFile] - never set: the coder opens its brief itself.
 */

/**
 * @typedef {object} BuiltClosedBook
 * @property {"claude"} cli
 * @property {"reviewer"|"judge"|"s2"|"author"|"facts"} role
 * @property {string[]} argv - no prompt positional.
 * @property {string} cwd
 * @property {string} stdinFile - the packet file the spawner pipes to stdin.
 * @property {ReadonlyArray<RenderedForbiddenEntry>} [forbiddenRendered] - present only for `facts`.
 */

/**
 * Extra `--disallowedTools` rules the `facts` role adds on top of the forbidden list (plan §5.2
 * "Facts role only"): the two literal examples the plan names (`Bash(rm*)`, `Bash(git push*)`)
 * plus the handful of other ordinary write verbs a read-only facts delegate never needs.
 * @type {ReadonlyArray<string>}
 */
export const FACTS_EXTRA_WRITE_VERB_RULES = Object.freeze([
  'Bash(rm*)',
  'Bash(git push*)',
  'Bash(git commit*)',
  'Bash(git merge*)',
  'Bash(mv*)',
  'Bash(cp*)',
]);

/**
 * @param {unknown} fallback
 * @throws {TypeError} unless `fallback` is `undefined` or an array of objects whose `provider` and
 *   `model` are non-empty strings. The message names the index only, never the entry's values.
 */
function assertValidFallback(fallback) {
  if (fallback === undefined) return;
  if (!Array.isArray(fallback)) {
    throw new TypeError('buildClaudeArgv: fallback must be an array when given');
  }
  fallback.forEach((entry, index) => {
    const valid =
      entry !== null &&
      typeof entry === 'object' &&
      typeof entry.provider === 'string' &&
      entry.provider.length > 0 &&
      typeof entry.model === 'string' &&
      entry.model.length > 0;
    if (!valid) {
      throw new TypeError(`buildClaudeArgv: fallback[${index}] must be an object with non-empty string provider and model`);
    }
  });
}

/**
 * @param {ReadonlyArray<FallbackEntry> | undefined} fallback
 * @param {string} provider
 * @param {string} model - the main `--model`; a fallback equal to it is skipped.
 * @returns {string | undefined} the FIRST same-provider fallback model id that differs from `model`.
 */
function firstSameProviderFallbackId(fallback, provider, model) {
  return (fallback ?? []).find((f) => f.provider === provider && f.model !== model)?.model;
}

/**
 * @param {unknown} maxBudgetUsd
 * @param {string} builderName
 * @throws {TypeError} unless `maxBudgetUsd` is `undefined` or a finite number > 0.
 */
function assertValidBudget(maxBudgetUsd, builderName) {
  if (maxBudgetUsd !== undefined && !(typeof maxBudgetUsd === 'number' && Number.isFinite(maxBudgetUsd) && maxBudgetUsd > 0)) {
    throw new TypeError(`${builderName}: maxBudgetUsd must be a finite number > 0 when given, got ${JSON.stringify(maxBudgetUsd)}`);
  }
}

/**
 * @param {ClaudeBuildParams} params
 * @returns {BuiltCoder}
 */
function buildCoderArgv(params) {
  // B4.2: a coder's deny list is `mergeForbidden()` — FORBIDDEN plus the coder-only entries
  // (`code-forge block waive`, `--no-require-reviews`). A `contains` entry (`--no-require-reviews`)
  // renders 0 rules (no pinned CLI has a match-anywhere rule), so it stays with the pre-spawn
  // `isForbidden` check and the transcript grep, as before.
  const { model, effort, promptPath, cwd, fallback, maxBudgetUsd, renderedForbidden = renderForClaude(mergeForbidden()) } = params;
  assertValidBudget(maxBudgetUsd, 'buildClaudeArgv');
  assertValidFallback(fallback);
  if (!hasAnyRenderedRule(renderedForbidden)) {
    throw new Error('buildClaudeArgv: coder role requires a non-empty forbidden-list render (got 0 usable rules)');
  }
  const argv = ['claude', '-p', '--model', model];
  if (effort) argv.push('--effort', effort);
  argv.push('--permission-mode', 'bypassPermissions');
  argv.push('--output-format', 'json');
  argv.push('--no-session-persistence');
  if (maxBudgetUsd !== undefined) argv.push('--max-budget-usd', String(maxBudgetUsd));
  argv.push('--disallowedTools', ...flattenRuleStrings(renderedForbidden));
  const sameProviderId = firstSameProviderFallbackId(fallback, 'anthropic', model);
  if (sameProviderId !== undefined) argv.push('--fallback-model', sameProviderId);
  argv.push('--', promptPath);
  return { cli: CLI, role: 'coder', argv, cwd, forbiddenRendered: renderedForbidden };
}

/**
 * @param {ClaudeBuildParams} params
 * @returns {BuiltClosedBook}
 */
function buildClosedBookArgv(params) {
  const { model, effort, promptPath, cwd, schema, systemPromptText, maxBudgetUsd, renderedForbidden = renderForClaude(FORBIDDEN) } = params;
  assertValidBudget(maxBudgetUsd, 'buildClaudeArgv');
  // The dispatch in `buildClaudeArgv` never reaches this branch with role 'coder' — a runtime
  // invariant the parameter type alone cannot prove, so the narrowing is explicit.
  const role = /** @type {"reviewer"|"judge"|"s2"|"author"|"facts"} */ (params.role);
  const isFacts = role === 'facts';
  if (isFacts && !hasAnyRenderedRule(renderedForbidden)) {
    throw new Error('buildClaudeArgv: facts role requires a non-empty forbidden-list render (got 0 usable rules)');
  }
  const argv = ['claude', '-p', '--model', model];
  if (effort) argv.push('--effort', effort);
  argv.push('--safe-mode');
  argv.push('--tools', isFacts ? 'Bash' : '');
  argv.push('--strict-mcp-config');
  argv.push('--no-session-persistence');
  if (systemPromptText !== undefined) argv.push('--system-prompt', systemPromptText);
  argv.push('--output-format', 'json');
  if (schema !== undefined) argv.push('--json-schema', JSON.stringify(stripSchemaMeta(schema)));
  if (maxBudgetUsd !== undefined) argv.push('--max-budget-usd', String(maxBudgetUsd));
  argv.push('--permission-mode', 'dontAsk');
  if (!isFacts) {
    return { cli: CLI, role, argv, cwd, stdinFile: promptPath };
  }
  argv.push('--disallowedTools', ...flattenRuleStrings(renderedForbidden), ...FACTS_EXTRA_WRITE_VERB_RULES);
  // `--restricted` is boolean and last: it also terminates the variadic list above.
  argv.push('--restricted');
  return { cli: CLI, role, argv, cwd, stdinFile: promptPath, forbiddenRendered: renderedForbidden };
}

/**
 * @param {ClaudeBuildParams} params
 * @returns {BuiltCoder | BuiltClosedBook}
 */
export function buildClaudeArgv(params) {
  assertBaseParams('buildClaudeArgv', params);
  return params.role === 'coder' ? buildCoderArgv(params) : buildClosedBookArgv(params);
}

/**
 * The Claude CLI's `--json-schema` validator does not know the draft 2020-12 meta-schema and exits
 * 1 on a top-level `$schema` naming it ("no schema with key or ref …"). The local Ajv2020 check
 * keeps the full schema; only the copy handed to the CLI drops the meta-schema pointer.
 * @param {unknown} schema
 * @returns {unknown}
 */
function stripSchemaMeta(schema) {
  if (schema === null || typeof schema !== 'object' || Array.isArray(schema) || !('$schema' in schema)) return schema;
  const { $schema: _meta, ...rest } = /** @type {Record<string, unknown>} */ (schema);
  return rest;
}
