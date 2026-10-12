/**
 * B66 (issue #13 item 8): outside review rounds per PR, next to what code-forge spent on the PR's
 * blocks. Pure helpers, no I/O:
 *
 *  - `parseOutsideReviewArgs` reads `ledger outside-review --run <r> --pr <n> --rounds <n>
 *    --findings <n> [--link <url>] [--block <id>]…` (the verb in `src/cli/ledger.mjs` does the
 *    run lookup, the block check, the redaction and the signed write);
 *  - `buildOutsideReview` builds the `outside_review` report section from the ledger rows and the
 *    `cost_per_block` entries (`src/ledger/report.mjs` passes them in, so the PR cost is computed
 *    by the SAME function the `cost_per_block` section uses);
 *  - `outsideReviewLines` renders the section as terminal text.
 *
 * Row: `outside_review {run, pr, rounds, findings, link (or null), blocks: [ids]}`. A later row for
 * the same PR replaces the earlier one in every reader (the latest row in ledger order wins); the
 * ledger itself stays append-only.
 */

import { roundUsd } from './spend.mjs';

export const OUTSIDE_REVIEW_EVENT = 'outside_review';

export const OUTSIDE_REVIEW_USAGE =
  'usage: code-forge ledger outside-review --run <r> --pr <n> --rounds <n> --findings <n> [--link <url>] [--block <id>]...';

const RUN_ID = /^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/;
const POSITIVE_INT = /^[1-9]\d*$/;
const NON_NEGATIVE_INT = /^(0|[1-9]\d*)$/;
const LINK = /^https?:\/\/\S+$/;
const MAX_LINK_CHARS = 2048;

/** @param {unknown} n @returns {boolean} */
const isCount = (n) => Number.isSafeInteger(n) && /** @type {number} */ (n) >= 0;

const SINGLE_FLAGS = new Set(['--run', '--pr', '--rounds', '--findings', '--link']);

/**
 * @typedef {{run: string, pr: number, rounds: number, findings: number, link: string | null, blocks: string[]}} OutsideReviewArgs
 */

/**
 * @param {string} raw @param {RegExp} shape @returns {number | null}
 */
function parseShapedInt(raw, shape) {
  if (!shape.test(raw)) return null;
  const n = Number(raw);
  return Number.isSafeInteger(n) ? n : null;
}

/**
 * Parse the verb's flags (everything after `outside-review`). Every flag but `--block` is given at
 * most once; `--block` may repeat (duplicates collapsed, first-seen order kept). Any other token,
 * a flag with no value, a missing required flag or a value of the wrong shape is a usage error.
 * The link is returned as given — the caller redacts it once the run key is registered.
 * @param {string[]} args
 * @returns {{ok: true, value: OutsideReviewArgs} | {ok: false, message: string}}
 */
export function parseOutsideReviewArgs(args) {
  /** @type {Record<string, string>} */
  const single = {};
  /** @type {string[]} */
  const blocks = [];
  for (let i = 0; i < args.length; i += 2) {
    const flag = args[i];
    const value = args[i + 1];
    // the position only, never the token: a stray value (a loose URL) may carry a secret
    if (flag !== '--block' && !SINGLE_FLAGS.has(flag)) return { ok: false, message: `unknown argument at position ${i + 1}` };
    if (value === undefined || value.startsWith('--')) return { ok: false, message: `${flag} requires a value` };
    if (flag === '--block') {
      if (!blocks.includes(value)) blocks.push(value);
      continue;
    }
    if (Object.hasOwn(single, flag)) return { ok: false, message: `${flag} is given more than once` };
    single[flag] = value;
  }
  for (const flag of ['--run', '--pr', '--rounds', '--findings']) {
    if (!Object.hasOwn(single, flag)) return { ok: false, message: `${flag} is required` };
  }
  const run = single['--run'];
  if (!RUN_ID.test(run)) return { ok: false, message: '--run must be a run id (letters, digits, ".", "_", "-")' };
  const pr = parseShapedInt(single['--pr'], POSITIVE_INT);
  if (pr === null) return { ok: false, message: '--pr must be a positive integer' };
  const rounds = parseShapedInt(single['--rounds'], NON_NEGATIVE_INT);
  if (rounds === null) return { ok: false, message: '--rounds must be a non-negative integer' };
  const findings = parseShapedInt(single['--findings'], NON_NEGATIVE_INT);
  if (findings === null) return { ok: false, message: '--findings must be a non-negative integer' };
  let link = null;
  if (Object.hasOwn(single, '--link')) {
    link = single['--link'];
    if (link.length > MAX_LINK_CHARS) return { ok: false, message: `--link is longer than ${MAX_LINK_CHARS} characters` };
    if (!LINK.test(link)) return { ok: false, message: '--link must be an https:// or http:// URL with no whitespace' };
  }
  return { ok: true, value: { run, pr, rounds, findings, link, blocks } };
}

/** @param {string[]} blocks @returns {string} */
const blockList = (blocks) => (blocks.length > 0 ? blocks.join(', ') : '-');

/**
 * The verb's one stdout line.
 * @param {{pr: number, rounds: number, findings: number, blocks: string[]}} row @returns {string}
 */
export function outsideReviewRecordedLine({ pr, rounds, findings, blocks }) {
  return `outside_review pr ${pr} · rounds ${rounds} · findings ${findings} · blocks ${blockList(blocks)}\n`;
}

/**
 * @typedef {{pr: number, rounds: number, findings: number, link: string | null, blocks: string[], code_forge_usd: number, unknown_usd_sessions: number}} OutsideReviewEntry
 */

/**
 * The `outside_review` report section: one entry per PR (the latest `outside_review` row for it,
 * by ledger order), sorted by PR number ascending. `code_forge_usd` sums `totalUsd` of the
 * `cost_per_block` entries of the row's run whose block the row names; `unknown_usd_sessions` sums
 * their `unknownUsdSessions` (those sessions count 0 in the dollars). `[]` with no rows.
 * @param {Array<Record<string, any>>} rows - shadow rows already removed.
 * @param {Array<{run: string | null, block: string, totalUsd: number, unknownUsdSessions: number}>} costEntries - the `cost_per_block` section.
 * @returns {OutsideReviewEntry[]}
 */
export function buildOutsideReview(rows, costEntries) {
  /** @type {Map<number, Record<string, any>>} */
  const latest = new Map();
  for (const row of rows) {
    if (row.event !== OUTSIDE_REVIEW_EVENT || !Number.isSafeInteger(row.pr) || row.pr <= 0) continue;
    // a row whose counts are not 0-or-more safe integers is skipped (the verb never writes one)
    if (!isCount(row.rounds) || !isCount(row.findings)) continue;
    latest.set(row.pr, row);
  }
  return [...latest.values()]
    .sort((a, b) => a.pr - b.pr)
    .map((row) => {
      const blocks = Array.isArray(row.blocks) ? row.blocks.filter((b) => typeof b === 'string') : [];
      const run = typeof row.run === 'string' ? row.run : null;
      // a row with no string run matches no cost entry (never the null-run entries): 0 and 0 unknown
      const mine = run === null ? [] : costEntries.filter((e) => e.run === run && blocks.includes(e.block));
      const usd = mine.reduce((sum, e) => sum + e.totalUsd, 0);
      return {
        pr: row.pr,
        rounds: row.rounds,
        findings: row.findings,
        link: typeof row.link === 'string' ? row.link : null,
        blocks,
        // rounded as `cost_per_block` rounds its dollars (4 decimals)
        code_forge_usd: roundUsd(usd),
        unknown_usd_sessions: mine.reduce((sum, e) => sum + e.unknownUsdSessions, 0),
      };
    });
}

/** @param {number} n @param {string} word @returns {string} */
const plural = (n, word) => `${n} ${word}${n === 1 ? '' : 's'}`;

/**
 * Terminal text of the section: `outside review: none` with no entry, else one line per PR.
 * @param {OutsideReviewEntry[]} section @returns {string}
 */
export function outsideReviewLines(section) {
  if (section.length === 0) return 'outside review: none\n';
  return section
    .map((e) => {
      const unknown = e.unknown_usd_sessions > 0 ? ` (+${e.unknown_usd_sessions} unknown)` : '';
      return `PR #${e.pr} · ${plural(e.rounds, 'outside round')} · ${plural(e.findings, 'finding')} · code-forge $${e.code_forge_usd.toFixed(2)}${unknown} · ${blockList(e.blocks)}\n`;
    })
    .join('');
}
