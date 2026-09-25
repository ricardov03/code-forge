/**
 * Agent-environment detection and the single-JSON-line output convention (Laravel installer
 * pattern, research/installer-patterns.md; coder-rules.md: "Agent detection env vars ...
 * CLAUDECODE/CLAUDE_CODE, CURSOR_AGENT, AI_AGENT — when set, output exactly one JSON line").
 *
 * This module owns the MECHANISM only: detecting the env vars, and writing exactly one JSON line
 * to a stream. It does not build the `init` wizard's own payload shape (`{ok, wrote, harnesses,
 * doctor, log, log_tail}`, plan §2) — that composition belongs to `init`, a later block (B13).
 * Any verb can call {@link maybeEmitAgentJson} with whatever plain-JSON-serializable payload it
 * has, and the "exactly one line" guarantee holds regardless of the payload's shape.
 */

/**
 * Env var names checked, in this fixed order — first one present (as an own, non-empty string
 * property) wins and is reported as `source`. Order does not change the detection RESULT (any
 * one being present is enough), only which name is reported when more than one is set.
 */
export const AGENT_ENV_VARS = Object.freeze(['CLAUDECODE', 'CLAUDE_CODE', 'CURSOR_AGENT', 'AI_AGENT']);

/**
 * @typedef {object} AgentDetection
 * @property {boolean} isAgent
 * @property {string|null} source - the env var name that triggered detection, or `null`.
 */

/**
 * @param {NodeJS.ProcessEnv} [env] - defaults to `process.env`.
 * @returns {AgentDetection}
 */
export function detectAgentEnv(env = process.env) {
  for (const name of AGENT_ENV_VARS) {
    if (Object.hasOwn(env, name) && typeof env[name] === 'string' && env[name].length > 0) {
      return { isAgent: true, source: name };
    }
  }
  return { isAgent: false, source: null };
}

/**
 * Serializes `payload` as ONE line of compact JSON (no pretty-printing — a pretty-printed object
 * spans multiple lines, which breaks "exactly one JSON line on stdout" for any caller parsing
 * line-by-line) and writes it in a SINGLE `stream.write()` call, so nothing else can interleave a
 * partial write between two calls.
 * @param {unknown} payload - must be JSON-serializable; `undefined` is refused (it would silently
 *   produce no output at all, which is not "one JSON line").
 * @param {object} [opts]
 * @param {{write: (s: string) => unknown}} [opts.stdout] - defaults to `process.stdout`.
 * @returns {void}
 */
export function writeAgentJson(payload, opts = {}) {
  const stdout = opts.stdout ?? process.stdout;
  const serialized = JSON.stringify(payload);
  if (typeof serialized !== 'string') {
    throw new TypeError('writeAgentJson: payload must be JSON-serializable (JSON.stringify returned undefined)');
  }
  stdout.write(`${serialized}\n`);
}

/**
 * Writes `payload` as one JSON line IF the environment looks like an agent harness, otherwise
 * writes nothing at all. Returns whether it wrote, so a caller (e.g. `init`) can skip its normal
 * interactive/human-readable output in the same branch.
 * @param {unknown} payload
 * @param {object} [opts]
 * @param {NodeJS.ProcessEnv} [opts.env]
 * @param {{write: (s: string) => unknown}} [opts.stdout]
 * @returns {boolean}
 */
export function maybeEmitAgentJson(payload, opts = {}) {
  const { isAgent } = detectAgentEnv(opts.env ?? process.env);
  if (!isAgent) {
    return false;
  }
  writeAgentJson(payload, opts);
  return true;
}
