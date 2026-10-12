/**
 * The open review findings of a block (B64a `code-forge status`, B64b done file): the number of
 * distinct (file, finding) pairs the block gate (`checkBlockReviews`) would refuse today, each
 * reviewed file at the content hash of its last review row. Shared by `src/cli/status.mjs` and
 * `src/cli/block.mjs`, so the done file and `status` count the same way.
 */
import { KEY_BYTES, loadKey } from '../state/signer.mjs';
import { checkBlockReviews, reviewedFiles } from './gate-check.mjs';

/** The refusal codes that name one open review finding. */
const FINDING_CODES = new Set(['not_approved', 'late_unruled', 'review_cap', 'autopilot_waiver_invalid']);

/**
 * The open review findings of one block: the gate's own check over the files the block's review
 * rows name, each at the content hash it was last reviewed at.
 * @param {{block: string, runId: string, mine: Array<Record<string, any>>, rows: Array<Record<string, any>>, key: Buffer}} opts
 *   `mine`: the block's rows of the run; `rows`: every row of the project (waivers, rulings).
 * @returns {number}
 */
export function openFindingsOf({ block, runId, mine, rows, key }) {
  /** @type {Map<string, string>} each reviewed file -> the content hash of its last review row */
  const hashes = new Map();
  for (const r of mine) {
    if (typeof r.content_hash !== 'string') continue;
    const [file] = reviewedFiles([r], block); // a review row naming a repo path, else none
    if (file !== undefined) hashes.set(file, r.content_hash);
  }
  const files = reviewedFiles(mine, block).map((file) => ({ file, content_hash: hashes.get(file) ?? '' }));
  if (files.length === 0 && !mine.some((r) => r.event === 'review.late_finding')) return 0;
  const { refusals } = checkBlockReviews({ block, runId, files, rows, key, proofFiles: [], highPaths: [] });
  const pairs = new Set(refusals.filter((r) => FINDING_CODES.has(r.code) && typeof r.finding === 'string').map((r) => `${r.file}\0${r.finding}`));
  return pairs.size;
}

/**
 * The run key for counting findings: read only; with no key a zero key stands in, so no signed
 * waiver or approval verifies (fail closed).
 * @param {Record<string, any>} record @returns {Promise<Buffer>}
 */
export async function findingsKey(record) {
  try {
    return await loadKey(record.run_id);
  } catch {
    return Buffer.alloc(KEY_BYTES);
  }
}

/**
 * B64b: the open findings `status` shows for ONE block — the same count (`openFindingsOf`, the
 * same key rule), for the block-end done file.
 * @param {Record<string, any>} record @param {Array<Record<string, any>>} rows - the project's rows.
 * @param {string} block
 * @returns {Promise<number>}
 */
export async function blockOpenFindings(record, rows, block) {
  const runId = String(record.run_id);
  const mine = rows.filter((r) => r?.run === runId && r.block === block);
  return openFindingsOf({ block, runId, mine, rows, key: await findingsKey(record) });
}
