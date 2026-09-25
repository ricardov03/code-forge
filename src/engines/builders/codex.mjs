/**
 * Codex 0.155.1 argv builder (plan §5.2, pinned against `test/fixtures/help/codex-0.155.1.txt`).
 * No spawning; the coder role writes its per-session rules file (see "Execpolicy rules" below). NOT fully deterministic: when `outPath` is omitted, a default is drawn from
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
 * **Execpolicy rules (B4.1, §0.6.3, [A19]).** Codex 0.155.1 `exec` has no flag and no `-c` key
 * naming a rules file; it loads the "user" rules from `$CODEX_HOME/rules/*.rules` (`--ignore-rules`
 * help: "Do not load user or project execpolicy `.rules` files"). So the coder build creates a
 * per-session Codex home under the run temp root (`../codex-home.mjs`, `codexHome` param or
 * `defaultCodexHome()`), writes `rules/code-forge.rules` there (execpolicy Starlark, one
 * `prefix_rule(..., decision="forbidden")` per `renderForCodex` pattern) and returns
 * `env: {CODEX_HOME}` — the spawner MUST merge `env` into the child's environment. The home is
 * never the user's real `~/.codex` (a `codexHome` there throws). This is the one builder that
 * writes a file: the rules must exist before Codex starts, and the builder is the one place that
 * holds the render.
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
import { mergeForbidden, renderForCodex } from '../../util/forbidden.mjs';
import { CODEX_RULES_FILE_NAME, defaultCodexHome, isInside, prepareCodexHome } from '../codex-home.mjs';
import { hasAnyRenderedRule } from '../render-rules.mjs';
import { assertBaseParams } from './validate-params.mjs';

export const CLI = 'codex';

/** The rules file Codex loads from `<CODEX_HOME>/rules/` (B4.1). */
export const RULES_FILE_NAME = CODEX_RULES_FILE_NAME;

/** `-c` overrides that keep `$TMPDIR` and `/tmp` out of the coder's writable roots (B4.1). */
export const SANDBOX_TMP_EXCLUSIONS = Object.freeze([
  '-c',
  'sandbox_workspace_write.exclude_tmpdir_env_var=true',
  '-c',
  'sandbox_workspace_write.exclude_slash_tmp=true',
]);

/**
 * The coder's approval policy (B4.2). Codex 0.155.1 rejects `-s workspace-write` together with
 * `--approve-for-me` (clap: "the argument '--sandbox <SANDBOX_MODE>' cannot be used with
 * '--approve-for-me'"), so the coder drops `--approve-for-me` and keeps the sandbox. `codex exec`
 * has no `-a/--ask-for-approval` flag (that one is on the interactive `codex` only), so the
 * policy is set through the `approval_policy` config key, whose valid values the 0.155.1 config
 * loader lists as `untrusted`, `on-failure`, `on-request`, `granular`, `never`. `never` = "Never
 * ask for user approval. Execution failures are immediately returned to the model" (`codex
 * --help`): a sandbox-denied command simply fails, nothing escalates out of the sandbox.
 */
export const CODER_APPROVAL_POLICY = Object.freeze(['-c', 'approval_policy="never"']);

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
 *   `renderForCodex(mergeForbidden())` (B4.2: the coder-only entries too; a `contains` entry
 *   renders 0 patterns, so it is left to the pre-spawn check and the transcript grep).
 * @property {string} [codexHome] - coder role only: the per-session Codex home to create (absolute,
 *   must not exist as the real `~/.codex` or inside it); defaults to `defaultCodexHome()` under the
 *   run temp root.
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
 * @param {string | undefined} effort
 * @throws {TypeError} unless `effort` is `undefined` or one of {@link VALID_EFFORTS}.
 */
function assertValidEffort(effort) {
  if (effort !== undefined && !VALID_EFFORTS.includes(effort)) {
    throw new TypeError(`buildCodexArgv: effort must be one of ${VALID_EFFORTS.join(', ')} when given, got ${JSON.stringify(effort)}`);
  }
}

/**
 * The Codex coder argv itself — pure, no I/O, no rules file (B4.2). `buildCoderArgv` uses it, and
 * the doctor's flags probe builds it with placeholder values for its static conflict check.
 * @param {{model: string, effort?: string, cwd: string, outPath: string, promptPath: string}} p
 * @returns {string[]}
 */
export function coderFlagArgv({ model, effort, cwd, outPath, promptPath }) {
  const argv = ['codex', 'exec', '-m', model];
  if (effort) argv.push('-c', `model_reasoning_effort=${effort}`);
  argv.push('-s', 'workspace-write');
  // INVARIANT (B4.1 fix round 1): the session CODEX_HOME lives under the run temp root, so the
  // sandbox must not make $TMPDIR or /tmp writable — the project cwd is then the only writable
  // root, and a home inside the cwd is refused below. See `../codex-home.mjs`.
  argv.push(...SANDBOX_TMP_EXCLUSIONS);
  // B4.2: NO `--approve-for-me` (Codex 0.155.1 refuses it next to `-s`); see CODER_APPROVAL_POLICY.
  argv.push(...CODER_APPROVAL_POLICY);
  argv.push('-C', cwd);
  argv.push('--json');
  argv.push('-o', outPath);
  argv.push(promptPath);
  return argv;
}

/**
 * @param {CodexBuildParams} params
 * @returns {{cli: "codex", role: "coder", argv: string[], cwd: string, outPath: string, env: {CODEX_HOME: string}, forbiddenRendered: ReadonlyArray<RenderedCodexEntry>, rulesFile: {fileName: string, path: string, content: string, count: number}}}
 */
function buildCoderArgv(params) {
  const { model, effort, promptPath, cwd, renderedForbidden = renderForCodex(mergeForbidden()) } = params;
  assertValidEffort(effort);
  const outPath = effectiveOutPath(params.outPath, 'coder');
  if (!hasAnyRenderedRule(renderedForbidden)) {
    throw new Error('buildCodexArgv: coder role requires a non-empty forbidden-list render (got 0 usable patterns)');
  }
  const argv = coderFlagArgv({ model, effort, cwd, outPath, promptPath });
  const codexHome = params.codexHome ?? defaultCodexHome();
  // Fix round 4: a non-absolute home is refused here, before any I/O, and the containment check
  // below then runs EVERY time (no `isAbsolute` short-circuit that could skip it).
  if (typeof codexHome !== 'string' || !path.isAbsolute(codexHome)) {
    throw new TypeError('buildCodexArgv: codexHome must be an absolute path');
  }
  // `isInside` realpaths BOTH sides through their nearest existing ancestor (fix round 3): a
  // `/var/...` cwd and a `/private/var/...` home are the same tree on macOS, and a symlinked cwd
  // is compared by its target, so a string-different alias cannot slip a home into the cwd.
  if (isInside(codexHome, cwd)) {
    throw new Error('buildCodexArgv: the session CODEX_HOME must not be inside the coder cwd (the sandbox can write there)');
  }
  const home = prepareCodexHome(codexHome, renderedForbidden);
  return {
    cli: CLI,
    role: 'coder',
    argv,
    cwd,
    outPath,
    env: { CODEX_HOME: home.codexHome },
    forbiddenRendered: renderedForbidden,
    rulesFile: { fileName: RULES_FILE_NAME, path: home.rulesPath, content: home.content, count: home.count },
  };
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
