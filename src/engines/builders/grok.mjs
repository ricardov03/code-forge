/**
 * Grok 1.0.34 argv builder (plan §5.2, pinned against `test/fixtures/help/grok-1.0.34.txt`). Pure:
 * no I/O, no CLI spawning.
 *
 * Like Codex, Grok's flag table has only two command shapes — coder, and "reviewer / judge / S2 /
 * author / facts" — so `facts` routes through the same closed-book branch as the other four roles
 * (Claude is the only provider with a facts-specific carve-out).
 *
 * The coder command has **no `-p`**: Grok's `-p`/`--single` flag is a different, single-turn mode
 * the coder role must not use (the plan calls this out explicitly). The forbidden list arrives the
 * same way Claude's does — as a `renderedForbidden` parameter, defaulting to
 * `renderForGrok(FORBIDDEN)` from B8's landed extensions, never a hard dependency baked in.
 *
 * Grok's `--deny <RULE>` fixture help shows a single value, not a variadic `<RULE...>` — unlike
 * Claude's documented `<tools...>` for `--disallowedTools`. This builder repeats `--deny <rule>`
 * once per rendered rule string — a scalar (single-value) flag repeated, never a variadic-list
 * swallow risk the way `claude.mjs`'s BLOCKER fix had to guard against, since each `--deny`
 * occurrence's value slot is exactly one token wide.
 *
 * **Return contract (fix round 3):** `{cli, role, argv, cwd, forbiddenRendered?}` — never a
 * `stdinFile`. Unlike Claude/Codex closed-book runs, Grok's `--prompt-file <PATH>` ("Single-turn
 * prompt from a file", pinned help) makes the CLI itself read the packet and send its CONTENT as
 * the prompt, so even a closed-book run with every tool denied receives the packet text; the
 * spawner pipes nothing to stdin.
 *
 * Effort is sent as the canonical `--reasoning-effort <EFFORT>` (the pinned help lists `--effort`
 * only as an alias), so the flag the builder emits is the exact one `probe.mjs` checks (fix round 3,
 * found by the widened all-roles probe-coverage test).
 *
 * **Fix round 1 (isolated per-file review):**
 *  - Unknown `role` now throws (was silently routed to closed-book) — `assertBaseParams`.
 *  - `cwd` is now required and validated, same as `model`/`promptPath` — `assertBaseParams`.
 *  - A coder-role render with 0 usable rules now throws (see `claude.mjs`'s identical guard).
 *  - `effort`, when given, must be a non-empty string (was pushed into argv unchecked).
 */

import { mergeForbidden, renderForGrok } from '../../util/forbidden.mjs';
import { flattenRuleStrings, hasAnyRenderedRule } from '../render-rules.mjs';
import { assertEffortForProvider } from '../efforts.mjs';
import { assertBaseParams } from './validate-params.mjs';

export const CLI = 'grok';

/**
 * Built-in tool names the closed-book command denies wholesale via `--disallowed-tools` (in
 * addition to the wildcard `--deny "*"`, per plan §5.2). Not independently verified against a
 * machine-readable Grok tool list (none is documented in the pinned `--help`) — a reasonable,
 * conservative superset, flagged in the facts diff.
 * @type {ReadonlyArray<string>}
 */
export const GROK_ALL_BUILTIN_TOOLS = Object.freeze(['Bash', 'Read', 'Write', 'Edit', 'Glob', 'Grep', 'WebFetch', 'WebSearch']);

/**
 * @typedef {{id: string, rules: ReadonlyArray<string>, enforced: boolean}} RenderedGrokEntry
 */

/**
 * @typedef {object} GrokBuildParams
 * @property {"coder"|"reviewer"|"judge"|"s2"|"author"|"facts"} role
 * @property {string} model
 * @property {string} [effort] - a non-empty string when given.
 * @property {string} promptPath - `--prompt-file` value (brief for coder, packet for closed-book).
 * @property {string} cwd
 * @property {ReadonlyArray<RenderedGrokEntry>} [renderedForbidden] - coder role only; defaults to
 *   `renderForGrok(mergeForbidden())` (B4.2).
 * @property {Record<string, any>} [schema] - closed-book only; sent inline like Claude's.
 * @property {string} [systemPromptText] - closed-book only (`--system-prompt-override`).
 */

/**
 * @param {unknown} effort
 * @throws {TypeError} unless `effort` is `undefined` or a non-empty string.
 */
function assertValidEffort(effort) {
  // xai has no closed effort list (`PROVIDER_EFFORTS.xai === null`): any non-empty string (B29).
  assertEffortForProvider('buildGrokArgv', 'xai', effort);
}

/**
 * @param {GrokBuildParams} params
 * @returns {{cli: "grok", role: "coder", argv: string[], cwd: string, forbiddenRendered: ReadonlyArray<RenderedGrokEntry>}}
 */
function buildCoderArgv(params) {
  // B4.2: the coder's deny list is `mergeForbidden()` (FORBIDDEN + the coder-only entries). A
  // `contains` entry renders 0 `--deny` rules; the pre-spawn check and the transcript grep keep it.
  const { model, effort, promptPath, cwd, renderedForbidden = renderForGrok(mergeForbidden()) } = params;
  assertValidEffort(effort);
  if (!hasAnyRenderedRule(renderedForbidden)) {
    throw new Error('buildGrokArgv: coder role requires a non-empty forbidden-list render (got 0 usable rules)');
  }
  const argv = ['grok', '--prompt-file', promptPath, '-m', model];
  if (effort) argv.push('--reasoning-effort', effort);
  argv.push('--permission-mode', 'bypassPermissions');
  argv.push('--cwd', cwd);
  for (const rule of flattenRuleStrings(renderedForbidden)) {
    argv.push('--deny', rule);
  }
  return { cli: CLI, role: 'coder', argv, cwd, forbiddenRendered: renderedForbidden };
}

/**
 * @param {GrokBuildParams} params
 * @returns {{cli: "grok", role: "reviewer"|"judge"|"s2"|"author"|"facts", argv: string[], cwd: string}}
 */
function buildClosedBookArgv(params) {
  const { model, effort, promptPath, cwd, schema, systemPromptText } = params;
  assertValidEffort(effort);
  // See `builders/claude.mjs`'s identical cast: the dispatch below never reaches this branch
  // with role 'coder', but that is a runtime invariant, not provable from the parameter type.
  const role = /** @type {"reviewer"|"judge"|"s2"|"author"|"facts"} */ (params.role);
  const argv = ['grok', '--prompt-file', promptPath, '-m', model];
  if (effort) argv.push('--reasoning-effort', effort);
  if (schema !== undefined) argv.push('--json-schema', JSON.stringify(schema));
  argv.push('--disallowed-tools', GROK_ALL_BUILTIN_TOOLS.join(','));
  argv.push('--deny', '*');
  argv.push('--no-plan');
  argv.push('--no-subagents');
  argv.push('--max-turns', '1');
  argv.push('--permission-mode', 'dontAsk');
  argv.push('--cwd', cwd);
  if (systemPromptText !== undefined) argv.push('--system-prompt-override', systemPromptText);
  return { cli: CLI, role, argv, cwd };
}

/**
 * @param {GrokBuildParams} params
 * @returns {ReturnType<typeof buildCoderArgv> | ReturnType<typeof buildClosedBookArgv>}
 */
export function buildGrokArgv(params) {
  assertBaseParams('buildGrokArgv', params);
  return params.role === 'coder' ? buildCoderArgv(params) : buildClosedBookArgv(params);
}
