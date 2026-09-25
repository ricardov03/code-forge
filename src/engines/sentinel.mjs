/**
 * The subprocess-mode coder sentinel contract (plan §5.2, §5.3, C11) and the `ACK <sha8>
 * lines=<n>` wire receipt.
 *
 * **Sentinel.** A coder prints `===BLOCK <id> COMPLETE===` on success or `===BLOCK <id> FAILED:
 * <reason>===` on failure (§10.7's forecast-exceeded stop line — `===BLOCK <id> FAILED: forecast
 * exceeded, split plan <id>a/<id>b===` — is one concrete instance of the FAILED shape). A coder can
 * print MORE than one of these across a long-running transcript (a retry after a local fix, a
 * revised final report); `parseSentinel` always takes the LAST occurrence, never the first (plan
 * §5.2: "`sentinel.mjs` takes the **last** occurrence"). Both patterns are anchored to the WHOLE
 * trimmed line (`^...$`), so a coder quoting the sentinel text inside ordinary prose ("I will not
 * print ===BLOCK B4 COMPLETE=== yet") never matches — only a line that IS exactly the sentinel does.
 *
 * **ACK.** Before any tool call, a coder replies `ACK <sha8> lines=<n>` to the wire receipt it was
 * sent — the sha of the brief file, truncated to 8 hex characters (matched case-insensitively;
 * compared lowercased), and the brief's line count. `parseAck` returns the LAST matching line (fix
 * round 1, MAJOR): the protocol is "no/wrong ACK within the launch window ⇒ resend once" (§5.2),
 * so when a caller passes the transcript up through a resend, the LAST ACK is the one that reflects
 * whether the resend succeeded — a first-match reading would report the pre-resend failure even
 * after a successful resend. An optional `after` marker lets a caller scope the search to
 * everything strictly after the last occurrence of that marker (e.g. the literal wire-receipt text
 * that was just (re)sent), which also defends against an unrelated earlier line that happens to
 * look like an ACK (an echoed instruction, a quoted example) being mistaken for the real one.
 */

/** @typedef {{status: "complete", blockId: string} | {status: "failed", blockId: string, reason: string}} SentinelResult */

const COMPLETE_RE = /^===BLOCK\s+(\S+)\s+COMPLETE===$/;
const FAILED_RE = /^===BLOCK\s+(\S+)\s+FAILED:\s*(.*?)\s*===$/;

/**
 * @param {string} output - the full captured transcript (subprocess stdout, or a Solo
 *   `get_process_output` join).
 * @param {string} [blockId] - when given, only sentinel lines for THIS block id count — a shared
 *   pane/log can interleave another block's lines.
 * @returns {SentinelResult | null} the LAST matching sentinel line, or `null` if none is present.
 */
export function parseSentinel(output, blockId) {
  if (typeof output !== 'string') {
    throw new TypeError('parseSentinel: output must be a string');
  }
  /** @type {SentinelResult | null} */
  let last = null;
  for (const rawLine of output.split('\n')) {
    const line = rawLine.trim();
    const complete = COMPLETE_RE.exec(line);
    if (complete && (!blockId || complete[1] === blockId)) {
      last = { status: 'complete', blockId: complete[1] };
      continue;
    }
    const failed = FAILED_RE.exec(line);
    if (failed && (!blockId || failed[1] === blockId)) {
      last = { status: 'failed', blockId: failed[1], reason: failed[2] };
    }
  }
  return last;
}

/** @typedef {{sha8: string, lines: number}} ParsedAck */

/** Case-insensitive: normalized to lowercase before comparison in {@link verifyAck}. */
const ACK_RE = /^ACK\s+([0-9a-fA-F]{8})\s+lines=(\d+)\s*$/;

/**
 * @param {string} output
 * @param {{after?: string} | null} [opts] - `after`: only text strictly after the LAST occurrence
 *   of this marker is searched (the whole `output` is searched when omitted or not found). `opts`
 *   may be `null`; `after` must be a non-empty string when given (TypeError otherwise).
 * @returns {ParsedAck | null} the LAST `ACK <sha8> lines=<n>` line in the searched window (fix
 *   round 1, MAJOR — was the first).
 */
export function parseAck(output, opts = {}) {
  if (typeof output !== 'string') {
    throw new TypeError('parseAck: output must be a string');
  }
  const after = opts?.after;
  if (after !== undefined && (typeof after !== 'string' || after.length === 0)) {
    // An empty marker would make `lastIndexOf` return output.length and empty the window.
    throw new TypeError('parseAck: opts.after must be a non-empty string when given');
  }
  let window = output;
  if (after !== undefined) {
    const idx = output.lastIndexOf(after);
    if (idx !== -1) window = output.slice(idx + after.length);
  }
  /** @type {ParsedAck | null} */
  let last = null;
  for (const rawLine of window.split('\n')) {
    const match = ACK_RE.exec(rawLine.trim());
    if (match) {
      last = { sha8: match[1].toLowerCase(), lines: Number(match[2]) };
    }
  }
  return last;
}

/** @typedef {{status: "ok", ack: ParsedAck} | {status: "parked", reason: string}} AckVerification */

const SHA8_RE = /^[0-9a-fA-F]{8}$/;

/**
 * @param {string} output
 * @param {string} expectedSha8 - exactly 8 hex characters (case-insensitive).
 * @param {number} [expectedLines] - when given, a line-count mismatch also parks (the brief was
 *   truncated in flight).
 * @param {{after?: string} | null} [opts] - forwarded to {@link parseAck}.
 * @returns {AckVerification}
 * @throws {TypeError} if `expectedSha8` is not exactly 8 hex characters — a caller passing a full
 *   40-character sha (or any other malformed value) gets a clear refusal here instead of a
 *   confusing "no ACK" / "sha mismatch" `parked` result. Also if `expectedLines` is given but is
 *   not a non-negative integer (null, NaN, a numeric string), instead of a misleading
 *   "ACK lines mismatch" park.
 */
export function verifyAck(output, expectedSha8, expectedLines, opts = {}) {
  if (typeof expectedSha8 !== 'string' || !SHA8_RE.test(expectedSha8)) {
    throw new TypeError(`verifyAck: expectedSha8 must be exactly 8 hex characters, got ${JSON.stringify(expectedSha8)}`);
  }
  if (expectedLines !== undefined && !(Number.isInteger(expectedLines) && expectedLines >= 0)) {
    throw new TypeError(`verifyAck: expectedLines must be a non-negative integer when given, got ${JSON.stringify(expectedLines)}`);
  }
  const normalizedExpected = expectedSha8.toLowerCase();
  const ack = parseAck(output, opts);
  if (!ack) {
    return { status: 'parked', reason: 'no ACK line found in the searched window' };
  }
  if (ack.sha8 !== normalizedExpected) {
    return { status: 'parked', reason: `ACK sha mismatch: expected ${normalizedExpected}, got ${ack.sha8}` };
  }
  if (expectedLines !== undefined && ack.lines !== expectedLines) {
    return { status: 'parked', reason: `ACK lines mismatch: expected ${expectedLines}, got ${ack.lines}` };
  }
  return { status: 'ok', ack };
}
