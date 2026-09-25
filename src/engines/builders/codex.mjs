/**
 * Codex 0.155.1 argv builder (plan §5.2, pinned against `test/fixtures/help/codex-0.155.1.txt`).
 * No filesystem I/O and no spawning (a rules-file `content` is returned as data; a caller writes
 * it to disk). NOT fully deterministic: when `outPath` is omitted, a default is drawn from
 * `os.tmpdir()` + `randomBytes` — pass `outPath` for a reproducible argv (every snapshot test does).
 *
 * **Return contract (fix round 3):** `{cli, role, argv, cwd, outPath, stdinFile?, ...}`.
 *  - `outPath` is ALWAYS returned — the effective `-o` target, worked out once per call, so the
 *    caller reads Codex's last message from it and deletes it afterwards (the builder never
 *    creates the file; Codex does).
 *  - `coder`: the brief is a POINTER (the last argv token is `promptPath`); the coder has tools.
 *  - closed-book (`reviewer`/`judge`/`s2`/`author`/`facts`): the last argv token is the literal
 *    `-`, which the pinned help (`codex exec [OPTIONS] [PROMPT]`: "If not provided as an argument
 *    (or if `-` is used), instructions are read from stdin") defines as "read the prompt from
 *    stdin". `stdinFile === promptPath` names the packet file whose CONTENT the spawner (B9)
 *    pipes to stdin — a closed-book reviewer must receive the packet itself, never a path it may
 *    not be able to open. The explicit `-` form was chosen over omitting the positional because
 *    it is documented and does not depend on stdin-is-piped detection.
 *
 * **Doctor probe item for B13 (no network in B4's tests):** `codex: closed-book stdin delivery` —
 * run this module's `reviewer` argv with `stdinFile` piped in, the packet carrying a unique canary
 * and "reply with the canary"; PASS when the file at `outPath` contains the canary, FAIL
 * (`codex: closed-book packet not delivered`) otherwise.
 *
 * Unlike Claude, the flag table gives Codex only TWO command shapes: coder, and "reviewer / judge
 * / S2 / author / facts" — there is no Codex-specific "facts role only" carve-out anywhere in the
 * plan's flag table, so `facts` here is simply routed through the same closed-book branch as the
 * other four roles.
 *
 * §0.6.3 (an acceptance clause the facts sheet cannot back): "only `--ignore-rules` proves rules
 * files exist" — Codex's execpolicy rules-file WIRE FORMAT is not verified on this Mac. This
 * builder emits the forbidden list as a `rulesFile` (id + `renderForCodex` output, JSON), which is
 * what a later block writes into the project; `doctor` (B13) is the block that probes whether
 * Codex actually honours it and prints `codex: forbidden list is prose-only` when it does not.
 * **Where that file is written is NOT this module's decision** — it returns `{fileName, content}`
 * data only. Fix round 1 (MINOR) flagged that `-s workspace-write` lets the coder itself read/edit
 * the tree the file WOULD land in if a caller naively wrote it under `cwd`: whoever materializes
 * `rulesFile` onto disk (a later block: B9's session runner or B11's worker) is responsible for
 * placing it somewhere the coder's own `workspace-write` sandbox cannot reach (outside `cwd`, or
 * under a path `src/util/forbidden.mjs`'s `path` entries also deny writing to) — this comment
 * documents that responsibility since this module cannot discharge it itself.
 *
 * **Fix round 1 (isolated per-file review):**
 *  - Unknown `role` now throws (was silently routed to closed-book) — `assertBaseParams`.
 *  - `cwd` is now required and validated, same as `model`/`promptPath` — `assertBaseParams`.
 *  - A coder-role render with 0 usable patterns now throws (see `claude.mjs`'s identical guard).
 *  - The default `-o`/output-last-message path no longer lands INSIDE `cwd`: for the coder that
 *    put a stray file in the project tree (picked up by later diffs); for a closed-book role it
 *    silently broke the "isolation dir lists exactly 1 file" invariant. It now defaults to a path
 *    under `os.tmpdir()`, keyed by role and a random suffix, always outside `cwd`.
 *  - `effort` is now checked against the same four values the alias map / Claude `--effort` help
 *    text documents (`low|medium|high|xhigh|max` minus `xhigh`/`max`, which Codex's own
 *    `model_reasoning_effort` TOML values don't claim — kept to the portable subset
 *    `minimal|low|medium|high`) before being written into a `-c` config override; Codex parses
 *    that value as TOML, so an unchecked string could smuggle a quote or a nested key.
 */

import os from 'node:os';
import path from 'node:path';
import { randomBytes } from 'node:crypto';
import { FORBIDDEN, renderForCodex } from '../../util/forbidden.mjs';
import { hasAnyRenderedRule } from '../render-rules.mjs';
import { assertBaseParams } from './validate-params.mjs';

export const CLI = 'codex';

/** The rules-file name a caller writes `rulesFile.content` to (location is the caller's decision). */
export const RULES_FILE_NAME = 'code-forge-execpolicy.rules.json';

/** The `model_reasoning_effort` values Codex's TOML config is willing to receive from this builder. */
export const VALID_EFFORTS = Object.freeze(['minimal', 'low', 'medium', 'high']);

/**
 * @typedef {{id: string, patterns: string[][], decision: "forbidden", enforced: boolean, description: string}} RenderedCodexEntry
 */

/**
 * @typedef {object} CodexBuildParams
 * @property {"coder"|"reviewer"|"judge"|"s2"|"author"|"facts"} role
 * @property {string} model
 * @property {string} [effort] - one of {@link VALID_EFFORTS} when given.
 * @property {string} promptPath - the brief file (coder: passed as a pointer) or the packet file
 *   (closed-book: returned as `stdinFile`, piped to stdin by the spawner).
 * @property {string} cwd - the project tree (coder) or an empty isolated temp dir (closed-book).
 * @property {string} [outPath] - `-o` target; defaults to a fresh path under `os.tmpdir()`, NEVER
 *   inside `cwd` (fix round 1).
 * @property {string} [schemaPath] - closed-book only: a FILE PATH (`--output-schema <FILE>` takes
 *   a path, unlike Claude/Grok's inline `--json-schema` string — see `test/fixtures/help/
 *   codex-0.155.1.txt`).
 * @property {ReadonlyArray<RenderedCodexEntry>} [renderedForbidden] - coder role only; defaults to
 *   `renderForCodex(FORBIDDEN)`.
 */

/**
 * @param {string} role
 * @returns {string} a fresh path under `os.tmpdir()`, never inside the caller's `cwd`.
 */
function defaultOutPath(role) {
  return path.join(os.tmpdir(), `code-forge-${role}-output-${randomBytes(6).toString('hex')}.json`);
}

/**
 * @param {unknown} outPath
 * @param {string} role
 * @returns {string} the effective `-o` target: the caller's `outPath`, or one fresh default.
 * @throws {TypeError} unless `outPath` is `undefined` or a non-empty string.
 */
function effectiveOutPath(outPath, role) {
  if (outPath === undefined) return defaultOutPath(role);
  if (typeof outPath !== 'string' || outPath.length === 0) {
    throw new TypeError('buildCodexArgv: outPath must be a non-empty string when given');
  }
  return outPath;
}

/**
 * @param {ReadonlyArray<RenderedCodexEntry>} rendered
 * @returns {{fileName: string, content: string}}
 */
function buildRulesFile(rendered) {
  return { fileName: RULES_FILE_NAME, content: JSON.stringify({ rules: rendered }, null, 2) };
}

/**
 * @param {string | undefined} effort
 * @throws {TypeError} unless `effort` is `undefined` or one of {@link VALID_EFFORTS}.
 */
function assertValidEffort(effort) {
  if (effort !== undefined && !VALID_EFFORTS.includes(effort)) {
    throw new TypeError(`buildCodexArgv: effort must be one of ${VALID_EFFORTS.join(', ')} when given, got ${JSON.stringify(effort)}`);
  }
}

/**
 * @param {CodexBuildParams} params
 * @returns {{cli: "codex", role: "coder", argv: string[], cwd: string, outPath: string, forbiddenRendered: ReadonlyArray<RenderedCodexEntry>, rulesFile: {fileName: string, content: string}}}
 */
function buildCoderArgv(params) {
  const { model, effort, promptPath, cwd, renderedForbidden = renderForCodex(FORBIDDEN) } = params;
  assertValidEffort(effort);
  const outPath = effectiveOutPath(params.outPath, 'coder');
  if (!hasAnyRenderedRule(renderedForbidden)) {
    throw new Error('buildCodexArgv: coder role requires a non-empty forbidden-list render (got 0 usable patterns)');
  }
  const argv = ['codex', 'exec', '-m', model];
  if (effort) argv.push('-c', `model_reasoning_effort=${effort}`);
  argv.push('-s', 'workspace-write');
  argv.push('--approve-for-me');
  argv.push('-C', cwd);
  argv.push('--json');
  argv.push('-o', outPath);
  argv.push(promptPath);
  return { cli: CLI, role: 'coder', argv, cwd, outPath, forbiddenRendered: renderedForbidden, rulesFile: buildRulesFile(renderedForbidden) };
}

/**
 * @param {CodexBuildParams} params
 * @returns {{cli: "codex", role: "reviewer"|"judge"|"s2"|"author"|"facts", argv: string[], cwd: string, outPath: string, stdinFile: string}}
 */
function buildClosedBookArgv(params) {
  const { model, effort, promptPath, cwd, schemaPath } = params;
  assertValidEffort(effort);
  // See `builders/claude.mjs`'s identical cast: the dispatch below never reaches this branch
  // with role 'coder', but that is a runtime invariant, not provable from the parameter type.
  const role = /** @type {"reviewer"|"judge"|"s2"|"author"|"facts"} */ (params.role);
  const outPath = effectiveOutPath(params.outPath, role);
  const argv = ['codex', 'exec', '-m', model];
  if (effort) argv.push('-c', `model_reasoning_effort=${effort}`);
  argv.push('-s', 'read-only');
  argv.push('--ephemeral');
  argv.push('--ignore-rules');
  argv.push('--ignore-user-config');
  argv.push('-C', cwd);
  argv.push('--skip-git-repo-check');
  if (schemaPath !== undefined) argv.push('--output-schema', schemaPath);
  argv.push('-o', outPath);
  argv.push('--json');
  // `-` = "read the prompt from stdin" (pinned help); the packet CONTENT arrives there.
  argv.push('-');
  return { cli: CLI, role, argv, cwd, outPath, stdinFile: promptPath };
}

/**
 * @param {CodexBuildParams} params
 * @returns {ReturnType<typeof buildCoderArgv> | ReturnType<typeof buildClosedBookArgv>}
 */
export function buildCodexArgv(params) {
  assertBaseParams('buildCodexArgv', params);
  return params.role === 'coder' ? buildCoderArgv(params) : buildClosedBookArgv(params);
}
