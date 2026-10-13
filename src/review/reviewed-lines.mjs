/**
 * B96 (E1): the close report is built by the tool. After the review gate passed (`reviewGateCheck`
 * over `checkBlockReviews`), every file the gate checked is accepted at its current hash, either
 * by a signed `review.approved` row or — the gate's `waivedAll` list (B70/B71) — because every
 * finding its review left open was waived or answered. This module turns that verdict into one
 * line per file: `{file, ticket, via}`, `via` one of `approved`, `waiver`, `answered`, and
 * `ticket` the queue's ticket for that file and hash (the caller passes `ticketOf`, the queue's
 * own rule: `src/review` never imports the worker). It never re-decides the acceptance: the gate
 * is the only judge, and a file the gate did not accept is never passed here.
 *
 * It also reads the coder's hand lines (`reviewed <path> ticket <id>`), to name a hand line that
 * names another ticket (D1: a warning, the tool lines are the record).
 *
 * Its warnings (`onNoTicket`, `onBadTicket`) are callbacks: the close holds what they report and
 * prints it only when the block closes (src/cli/block.mjs).
 */

/** @typedef {'approved' | 'waiver' | 'answered'} Via */
/** @typedef {{file: string, ticket: string, via: Via}} ReviewedLine */

/**
 * The `via` of a file the gate accepted, read from the gate's own record (`WaivedAll`, built by
 * `waivedAllOf` in gate-check.mjs): no record at the same hash ⇒ `approved` (a signed approval);
 * a record whose `by` is null ⇒ `answered` (the gate sets `by` to null exactly when every open
 * finding was answered); any other `by` (`human` or `autopilot`) ⇒ `waiver` — also for a file
 * with some findings answered and at least one waived (the waiver is what a human or a grant
 * decided, so the line names it).
 * @param {import('./gate-check.mjs').WaivedAll | undefined} record @returns {Via}
 */
export function viaOf(record) {
  if (record === undefined) return 'approved';
  return record.by === null ? 'answered' : 'waiver';
}

/** A ticket id as the queue gives it: 24 lowercase hex characters (`assertTicketId`). */
const TICKET_ID = /^[0-9a-f]{24}$/;

/**
 * The tool's reviewed lines, sorted by file. Pure but for `onNoTicket` and `onBadTicket`.
 * @param {{
 *   files: ReadonlyArray<{file: string, content_hash: string}>,
 *   waivedAll: ReadonlyArray<import('./gate-check.mjs').WaivedAll>,
 *   ticketOf: (file: {file: string, content_hash: string}) => unknown,
 *   onNoTicket?: (file: string) => void,
 *   onBadTicket?: (file: string) => void,
 * }} opts `files`: the files the review gate checked and ACCEPTED (it passed); `waivedAll`: the
 *   gate's `reviewed_by_waiver` files ({@link viaOf}); every other file passed through a signed
 *   approval. `ticketOf`: the queue's ticket for that file at that hash, or null (or undefined)
 *   when it cannot be given — then the file has no line (fail closed) and `onNoTicket(file)` is
 *   called once. Any other value that is not 24 lowercase hex characters (a string of another
 *   shape, a number…) is a bad ticket: the file has no line either and `onBadTicket(file)` is
 *   called once instead.
 * @returns {ReviewedLine[]}
 */
export function reviewedLines({ files, waivedAll, ticketOf, onNoTicket = () => {}, onBadTicket = () => {} }) {
  /** @type {ReviewedLine[]} */
  const lines = [];
  for (const f of files) {
    const via = viaOf(waivedAll.find((w) => w.file === f.file && w.content_hash === f.content_hash));
    const ticket = ticketOf(f);
    if (ticket === null || ticket === undefined) onNoTicket(f.file);
    else if (typeof ticket === 'string' && TICKET_ID.test(ticket)) lines.push({ file: f.file, ticket, via });
    else onBadTicket(f.file);
  }
  return lines.sort((x, y) => (x.file < y.file ? -1 : x.file > y.file ? 1 : 0));
}

/**
 * A hand line: exactly `reviewed <path> ticket <id>` once trimmed, with backticks dropped and an
 * optional leading list marker (`- `, `* ` or a number and a dot, `1. `). The id is 24 hex
 * characters of either case: a ticket id is lowercase, so an id in capitals is read here and then
 * never equals the tool's ticket ({@link reportMismatches} compares exactly).
 */
const HAND_LINE = /^(?:(?:[-*]|\d{1,9}\.)\s+)?reviewed\s+(\S+)\s+ticket\s+([0-9a-fA-F]{24})$/;

/**
 * D1: the files whose hand line in `report` names a ticket id other than the tool's (compared
 * exactly, so an id in capitals is another ticket). Only a `reviewed <path> ticket <id>` line
 * counts ({@link HAND_LINE}); a hash of another length, or a line of any other shape that names
 * the file, never does.
 * @param {string} report @param {ReadonlyArray<ReviewedLine>} lines
 * @returns {string[]} the files with a mismatched hand line, in the order of `lines`.
 */
export function reportMismatches(report, lines) {
  /** @type {Set<string>} */
  const other = new Set();
  for (const raw of report.split('\n')) {
    const m = HAND_LINE.exec(raw.replaceAll('`', '').trim());
    if (m === null) continue;
    const line = lines.find((l) => l.file === m[1]);
    if (line !== undefined && line.ticket !== m[2]) other.add(line.file);
  }
  return lines.filter((l) => other.has(l.file)).map((l) => l.file);
}
