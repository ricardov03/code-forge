/** Human acceptance of false transcript hits, bound to each rule and exact line bytes. */
import { createHash } from 'node:crypto';
import { readFile } from 'node:fs/promises';
import { StateError } from '../state/paths.mjs';
import { readRun, writeSigned } from '../state/run.mjs';
import { verifyRow } from '../state/signer.mjs';
import { mergeForbidden, scanTranscript } from '../util/forbidden.mjs';

/** @typedef {{rule: string, line: number, reason: string}} AcceptedHit */

/** @param {string} text */
const lineHash = (text) => createHash('sha256').update(text).digest('hex');

/** One refusal for every well-formed request that does not name an actual hit. */
function noSuchHit() {
  return new StateError('accept-hit-no-such-hit', 'accept-hit-no-such-hit: the named transcript hit was not found; nothing was recorded');
}

/**
 * Validate forms before reading state, then find the exact pair before the terminal question.
 * The close and this pre-check use the same merged forbidden list.
 * @param {{runId: string, id: string, flags: Record<string, any>, positionals: string[], extraTokens?: string[]}} opts
 */
export async function prepareAcceptHit({ runId, id, flags, positionals, extraTokens = [] }) {
  if (positionals.length !== 1) throw new StateError('usage', 'block accept-hit takes one block id');
  for (const [name, value] of Object.entries({ block: id, run: runId, rule: flags.rule, line: flags.line, transcript: flags.transcript, reason: flags.reason })) {
    // eslint-disable-next-line no-control-regex
    if (typeof value === 'string' && /[\x00-\x1f\x7f]/.test(value)) throw new StateError('usage', `${name === 'block' ? 'block id' : `--${name}`} must not contain control characters`);
  }
  for (const name of [...(flags.rule !== undefined ? ['rule'] : []), 'transcript', 'reason']) {
    if (typeof flags[name] !== 'string' || flags[name].trim().length === 0) throw new StateError('usage', `--${name} is required`);
  }
  if (typeof flags.line !== 'string' || !/^-?\d+$/.test(flags.line) || !Number.isSafeInteger(Number(flags.line))) throw new StateError('usage', '--line must be an integer');
  const line = Number(flags.line);
  let record;
  let text;
  try {
    record = await readRun(runId);
    if (!record.blocks?.[id]) throw noSuchHit();
    text = await readFile(flags.transcript, 'utf8');
  } catch {
    throw noSuchHit();
  }
  const lines = text.split('\n');
  const hits = scanTranscript(text, mergeForbidden(extraTokens));
  if (line < 1 || line > lines.length || !hits.some((hit) => hit.line === line && (flags.rule === undefined || hit.id === flags.rule))) throw noSuchHit();
  const selected = new Set([line]);
  if (flags.rule === undefined) {
    // Pair only recognized Codex JSON logs; arbitrary JSON-looking plain text has no item pair.
    try {
      const events = lines.map((s) => s.trim() ? JSON.parse(s) : undefined);
      const nonEmpty = events.filter((event) => event !== undefined);
      const itemId = events[line - 1]?.item?.id;
      if (nonEmpty[0]?.type === 'thread.started' && nonEmpty.every((event) => event !== null && typeof event === 'object' && !Array.isArray(event) && typeof event.type === 'string') &&
          events[line - 1]?.type?.startsWith('item.') && typeof itemId === 'string') {
        events.forEach((event, index) => {
          if (event?.type === 'item.completed' && event.item?.id === itemId) selected.add(index + 1);
        });
      }
    } catch { /* plain text: accept only the requested line */ }
  }
  const rows = hits.filter((hit) => selected.has(hit.line) && (flags.rule === undefined || hit.id === flags.rule))
    .map((hit) => ({ block: id, rule: hit.id, line: hit.line, line_sha256: lineHash(lines[hit.line - 1]), reason: /** @type {string} */ (flags.reason.trim()), by: 'human' }));
  return { record, rows };
}

/**
 * Called only after terminal confirmation; exactly one signed row, through the waiver's writer.
 * @param {{runId: string, row: Record<string, any>, writeRow: (row: Record<string, any>) => Promise<unknown>}} opts
 */
export async function writeAcceptedHit({ runId, row, writeRow }) {
  await writeSigned(runId, writeRow, { event: 'rule_break.accepted', ...row });
}

/**
 * Ledger rule_break rows are deliberately outside this transcript-only filter.
 * Line numbers are for reporting; only rule and exact content bind the acceptance.
 * @param {{block: string, runId: string, rows: Record<string, any>[], key: Buffer, transcript: string | null, extraTokens?: string[]}} opts
 * @returns {{remaining: {id: string, line: number}[], accepted: AcceptedHit[]}}
 */
export function acceptedTranscriptHits({ block, runId, rows, key, transcript, extraTokens = [] }) {
  if (typeof transcript !== 'string' || transcript.length === 0) return { remaining: [], accepted: [] };
  const approvals = rows.filter((row) => row.event === 'rule_break.accepted' && row.run === runId && row.block === block && row.by === 'human' && typeof row.reason === 'string' && row.reason.trim().length > 0 && verifyRow(row, key).ok);
  const lines = transcript.split('\n');
  /** @type {{id: string, line: number}[]} */
  const remaining = [];
  /** @type {AcceptedHit[]} */
  const accepted = [];
  for (const hit of scanTranscript(transcript, mergeForbidden(extraTokens))) {
    const hash = lineHash(lines[hit.line - 1]);
    const approval = approvals.find((row) => row.rule === hit.id && row.line_sha256 === hash);
    if (approval) accepted.push({ rule: hit.id, line: hit.line, reason: approval.reason });
    else remaining.push(hit);
  }
  return { remaining, accepted };
}
