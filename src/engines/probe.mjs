/**
 * The flag probe (plan §5.2, §2.3): the literal flags every builder in `builders/{claude,codex,
 * grok}.mjs` depends on, checked as literal substrings of a `--help` text, WITH a word-boundary
 * check so a shorter required flag can never be satisfied by a longer one that merely starts with
 * it. No pinned CLI documents a machine-readable `--help` format, so this is a substring probe,
 * not a parser — the same standard B0's own header check used ("the Claude fixture contains
 * `--safe-mode` and does not contain `--max-turns`", plan header, B0 acceptance (7)).
 *
 * `doctor` (B13, §2.3) re-runs this probe against the REAL installed CLI's live `--help` output at
 * runtime — this module is what it calls; this block only proves the probe itself works, against
 * the pinned fixtures (which must all pass) and against a deliberately mutilated copy of one
 * (which must fail, naming exactly the flag that was removed).
 *
 * **Fix round 1 (isolated per-file review, MINOR ×2):**
 *  - `REQUIRED_FLAGS[provider]` is now `Object.hasOwn`-guarded — `probeHelpText('constructor', …)`
 *    used to fall through to `Object.prototype.constructor` (truthy) instead of the documented
 *    "unknown provider" `Error`, then crash on `.filter` not being a function.
 *  - A match is only counted when the character immediately before and after it is NOT part of an
 *    identifier or another flag token (`[A-Za-z0-9-]`) — previously `--json` was satisfied by the
 *    unrelated, LONGER `--json-schema`/`--jsonl`, so removing the real `--json` flag while a longer
 *    one remained went undetected.
 */

/**
 * The flags each builder actually emits, one literal phrase per flag as it appears in the pinned
 * `--help` fixture (the exact spelling `probeHelpText` checks with a boundary-aware substring
 * search).
 * @type {Readonly<Record<"claude"|"codex"|"grok", ReadonlyArray<string>>>}
 */
export const REQUIRED_FLAGS = Object.freeze({
  claude: Object.freeze([
    '-p, --print',
    '--model <model>',
    '--effort <level>',
    '--permission-mode <mode>',
    '--output-format <format>',
    '--no-session-persistence',
    '--max-budget-usd <amount>',
    '--disallowedTools, --disallowed-tools <tools...>',
    '--fallback-model <model>',
    '--safe-mode',
    '--tools <tools...>',
    '--strict-mcp-config',
    '--json-schema <schema>',
    '--system-prompt <prompt>',
    '--restricted',
  ]),
  codex: Object.freeze([
    '-m, --model <MODEL>',
    '-c, --config <key=value>',
    '-s, --sandbox <SANDBOX_MODE>',
    '--approve-for-me',
    '-C, --cd <DIR>',
    '--json',
    '-o, --output-last-message <FILE>',
    '--ephemeral',
    '--ignore-rules',
    '--ignore-user-config',
    '--skip-git-repo-check',
    '--output-schema <FILE>',
  ]),
  grok: Object.freeze([
    '--prompt-file <PATH>',
    '-m, --model <MODEL>',
    '--reasoning-effort <EFFORT>',
    '--permission-mode <MODE>',
    '--cwd <CWD>',
    '--deny <RULE>',
    '--json-schema <SCHEMA>',
    '--disallowed-tools <TOOLS>',
    '--no-plan',
    '--no-subagents',
    '--max-turns <N>',
    '--system-prompt-override <PROMPT>',
  ]),
});

/** @typedef {{ok: boolean, missing: string[]}} ProbeResult */

/** A char that would extend an identifier/flag token if it sat right next to a match. */
const WORD_CHAR_RE = /[A-Za-z0-9-]/;

/**
 * @param {string} haystack
 * @param {string} needle
 * @returns {boolean} true only if `needle` occurs in `haystack` with a non-word character (or
 *   string start/end) on BOTH sides — so `--json` does not match inside `--json-schema`.
 */
function containsAsWholeToken(haystack, needle) {
  let fromIndex = 0;
  for (;;) {
    const at = haystack.indexOf(needle, fromIndex);
    if (at === -1) return false;
    const before = at > 0 ? haystack[at - 1] : '';
    const after = at + needle.length < haystack.length ? haystack[at + needle.length] : '';
    if (!WORD_CHAR_RE.test(before) && !WORD_CHAR_RE.test(after)) return true;
    fromIndex = at + 1;
  }
}

/**
 * @param {"claude"|"codex"|"grok"} provider
 * @param {string} helpText - the CLI's own `--help` output (or a pinned fixture's contents).
 * @returns {ProbeResult}
 * @throws {Error} for an unknown provider.
 */
export function probeHelpText(provider, helpText) {
  if (typeof provider !== 'string' || !Object.hasOwn(REQUIRED_FLAGS, provider)) {
    throw new Error(`probeHelpText: unknown provider "${provider}"`);
  }
  if (typeof helpText !== 'string') {
    throw new TypeError('probeHelpText: helpText must be a string');
  }
  const required = REQUIRED_FLAGS[/** @type {"claude"|"codex"|"grok"} */ (provider)];
  const missing = required.filter((flag) => !containsAsWholeToken(helpText, flag));
  return { ok: missing.length === 0, missing };
}
