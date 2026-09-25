/**
 * `red-parse` (plan §7.2, O13): what did the red step of a red→green run actually prove?
 *
 * A red step proves something ONLY when the filtered test run failed on an assertion. A run that
 * failed because a module did not load, the file did not compile or the process died proves
 * nothing ("red for the wrong reason") and is `RED_INVALID`. A run that passed is `NOT_RED`.
 *
 *   verdict      red_kind     when
 *   ───────────  ───────────  ─────────────────────────────────────────────────────────────────
 *   NOT_RED      none         exit code 0 and no signal
 *   RED_INVALID  import       a module / class / file the test needs did not load
 *   RED_INVALID  compile      a syntax or parse error
 *   RED_INVALID  fatal        a PHP fatal, a crash, a kill signal or a timeout
 *   RED          assertion    an assertion failure line and none of the above
 *   RED_INVALID  error        failed, but no assertion line (an uncaught TypeError, …)
 *
 * The fatal families are checked FIRST: one test file that did not load invalidates the run even
 * when another case failed on an assertion — the red count would otherwise include a case that
 * never ran. Shapes recognised: `node:test` (spec and TAP reporters), Pest/PHPUnit (collision
 * printer and plain), Jest/Vitest `AssertionError`/`expect(` failures. Colours are stripped first.
 * The result never carries an excerpt of the output — only the kind and a fixed reason.
 */

/** @typedef {'NOT_RED' | 'RED' | 'RED_INVALID'} RedVerdict */
/** @typedef {'none' | 'assertion' | 'import' | 'compile' | 'fatal' | 'error'} RedKind */
/** @typedef {{verdict: RedVerdict, red_kind: RedKind, framework: 'node:test' | 'pest' | 'unknown', failed: number | null, reason: string}} RedParse */

const ANSI = /\x1b\[[0-9;?]*[A-Za-z]/g;

/** Ordered: the first family with a hit decides. Each entry: [red_kind, reason, patterns]. */
const INVALID_FAMILIES = /** @type {const} */ ([
  [
    'import',
    'a module, class or file the test needs did not load',
    [
      /\bERR_MODULE_NOT_FOUND\b/,
      /\bERR_PACKAGE_PATH_NOT_EXPORTED\b/,
      /\bCannot find (?:module|package) ['"]/,
      /\bdoes not provide an export named\b/,
      /\bClass "?[A-Za-z_\\][\w\\]*"? not found\b/,
      /\bFailed opening required\b/,
      /\bInterface "?[A-Za-z_\\][\w\\]*"? not found\b/,
    ],
  ],
  ['compile', 'a syntax or parse error', [/^\s*SyntaxError\b/m, /\bPHP Parse error\b/, /\bParseError\b/, /^\s*Parse error:/m]],
  ['fatal', 'the test process died (fatal error or crash)', [/\bPHP Fatal error\b/, /^\s*Fatal error:/m, /\bSegmentation fault\b/, /\bAllowed memory size of \d+ bytes exhausted\b/]],
]);

const ASSERTION_PATTERNS = Object.freeze([
  /\bERR_ASSERTION\b/, // node:assert (spec reporter prints `AssertionError [ERR_ASSERTION]`, TAP prints `code: 'ERR_ASSERTION'`)
  /\bAssertionError\b/, // node, Jest/Vitest, pytest
  /\bFailed asserting that\b/, // PHPUnit and Pest expectations
  /\bExpectationFailedException\b/, // PHPUnit's exception class
  /\bexpect\(.*\)\.\w+\(/, // Jest/Vitest failure header `expect(received).toBe(expected)`
]);

const FAILED_COUNTS = Object.freeze([
  /^\s*ℹ fail (\d+)\s*$/m, // node:test spec
  /^# fail (\d+)\s*$/m, // node:test TAP
  /^\s*Tests:\s+(\d+) failed\b/m, // Pest
]);

/** @param {string} text @returns {'node:test' | 'pest' | 'unknown'} */
function frameworkOf(text) {
  if (/^\s*ℹ tests \d+\s*$/m.test(text) || /^# tests \d+\s*$/m.test(text) || /^TAP version \d+/m.test(text)) return 'node:test';
  if (/^\s*Tests:\s+\d+ (?:failed|passed)\b/m.test(text) || /^\s*(?:FAIL|FAILED)\s+Tests\\/m.test(text)) return 'pest';
  return 'unknown';
}

/** @param {string} text @returns {number | null} */
function failedCount(text) {
  for (const pattern of FAILED_COUNTS) {
    const m = pattern.exec(text);
    if (m) return Number(m[1]);
  }
  return null;
}

/**
 * Classify the red step of a red→green run.
 * @param {{code: number | null, signal?: string | null, timedOut?: boolean, stdout?: string, stderr?: string}} run
 *   an `exec` result (or its shape)
 * @returns {RedParse}
 */
export function parseRed(run) {
  const text = `${run.stdout ?? ''}\n${run.stderr ?? ''}`.replace(ANSI, '');
  const framework = frameworkOf(text);
  const failed = failedCount(text);
  const base = { framework, failed };
  if (run.code === 0 && !run.signal && !run.timedOut) {
    return { ...base, verdict: 'NOT_RED', red_kind: 'none', reason: 'the test passed without the change' };
  }
  if (run.timedOut || run.signal) {
    return { ...base, verdict: 'RED_INVALID', red_kind: 'fatal', reason: run.timedOut ? 'the test run timed out' : 'the test run was killed by a signal' };
  }
  for (const [kind, reason, patterns] of INVALID_FAMILIES) {
    if (patterns.some((p) => p.test(text))) return { ...base, verdict: 'RED_INVALID', red_kind: kind, reason };
  }
  if (ASSERTION_PATTERNS.some((p) => p.test(text))) {
    return { ...base, verdict: 'RED', red_kind: 'assertion', reason: 'an assertion failed' };
  }
  return { ...base, verdict: 'RED_INVALID', red_kind: 'error', reason: 'the run failed without an assertion failure' };
}
