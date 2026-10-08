/**
 * The packet-free, redacted tail of a session's diagnostic text (B30): a timed-out session's
 * stderr (`spawn.mjs`) and, since B55, a review answer that failed the schema
 * (`review/schema-fallback.mjs`). Lives in `util/` so both layers can use the one filter.
 */

import { redact } from './redact.mjs';

/** How much of a timed-out session's stderr is kept (B30). */
export const DIAGNOSTIC_TAIL_BYTES = 4096;

/** A timed-out session's stderr over this size keeps no text at all (bounds the redact cost). */
export const STDERR_MAX_BYTES = 1024 * 1024;

/**
 * Packet lines shorter than this are too common to tell apart from ordinary output, so they are
 * NOT filtered. Accepted risk (B30): a packet line under 16 characters (a brace, `return x;`) can
 * appear in a stderr tail; such a line carries no meaningful packet content on its own.
 */
const PACKET_LINE_MIN = 16;

/** The tail note when the packet cannot be read: no stderr text is kept at all (fail closed). */
export const TAIL_WITHHELD_PACKET = 'withheld: packet unreadable';
/** The tail note when stderr is over `STDERR_MAX_BYTES`. */
export const TAIL_WITHHELD_LARGE = 'stderr too large, withheld';
/** The tail note when the last 4 KB of stderr is one partial line. */
export const TAIL_WITHHELD_LONG_LINE = 'one long line, withheld';

/** @param {Buffer} buf @param {number} at @returns {number} the first character start at or after `at`. */
function charStart(buf, at) {
  let i = at;
  while (i < buf.length && (buf[i] & 0xc0) === 0x80) i += 1;
  return i;
}

/** @param {unknown} text @returns {string} */
export function asText(text) {
  return Buffer.isBuffer(text) ? text.toString('utf8') : typeof text === 'string' ? text : '';
}

const PEM_BEGIN = /-----BEGIN [A-Z0-9 ]*-----/;
const PEM_END = /-----END [A-Z0-9 ]*-----/;

/**
 * The stderr tail of a timed-out session (B30), never packet content:
 *  1. no packet text (unreadable) ⇒ no text: `{tail: null, note: 'withheld: packet unreadable'}`;
 *     stderr over `STDERR_MAX_BYTES` ⇒ `{tail: null, note: 'stderr too large, withheld'}`;
 *  2. on the FULL raw text: every PEM block (`-----BEGIN` … `-----END` lines, an unterminated one
 *     to the end) and every line repeating a packet line (≥ 16 chars) is dropped;
 *  3. the FULL remaining text goes through B0 `redact`;
 *  4. over `maxBytes`, the last `maxBytes` bytes are kept, starting on a character, with the first
 *     (partial) line dropped; when that piece has no line break the tail is withheld.
 * @param {string | Buffer | null | undefined} text @param {string | null} packetText @param {number} [maxBytes]
 * @returns {{tail: string | null, note?: string}}
 */
export function stderrTail(text, packetText, maxBytes = DIAGNOSTIC_TAIL_BYTES) {
  if (typeof packetText !== 'string') return { tail: null, note: TAIL_WITHHELD_PACKET };
  const raw = asText(text);
  if (raw.length === 0) return { tail: '' };
  if (Buffer.byteLength(raw) > STDERR_MAX_BYTES) return { tail: null, note: TAIL_WITHHELD_LARGE };
  const packetLines = [
    ...new Set(
      packetText
        .split('\n')
        .map((l) => l.trim())
        .filter((l) => l.length >= PACKET_LINE_MIN),
    ),
  ];
  let inPem = false;
  /** @type {string[]} */
  const kept = [];
  for (const line of raw.split('\n')) {
    const t = line.trim();
    if (inPem) {
      if (PEM_END.test(t)) inPem = false;
      continue;
    }
    if (PEM_BEGIN.test(t)) {
      inPem = !PEM_END.test(t);
      continue;
    }
    if (t.length >= PACKET_LINE_MIN && packetLines.some((p) => t.includes(p))) continue;
    kept.push(line);
  }
  const safe = /** @type {string} */ (redact(kept.join('\n')));
  const buf = Buffer.from(safe, 'utf8');
  if (buf.length <= maxBytes) return { tail: safe };
  const from = charStart(buf, buf.length - maxBytes);
  const piece = buf.subarray(from).toString('utf8');
  if (buf[from - 1] === 0x0a) return { tail: piece }; // the cut fell exactly on a line start
  const nl = piece.indexOf('\n');
  if (nl === -1) return { tail: '', note: TAIL_WITHHELD_LONG_LINE };
  return { tail: piece.slice(nl + 1) };
}

