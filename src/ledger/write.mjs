/**
 * The one place that appends to the ledger — JSON Lines, append-only, rotated at 10 MB (plan
 * §6.4). Later blocks write rows by importing `appendRow` from here (C17 seam rule: this module is
 * the collaborator B3/B8/B9/... pass around as a parameter, never imported by a batch-mate).
 *
 * Rotation is race-safe two ways (fix round 1, MAJOR): (1) a per-slug, per-process promise chain
 * serializes every `appendRow` for the same slug, so two calls in THIS process never interleave;
 * (2) the rotated name is claimed with `link()` + `unlink()`, not `rename()` — `link` fails atomically
 * with EEXIST if the candidate name is taken, so even a SECOND process racing on the same slug
 * can't silently clobber another rotation (a plain `rename` onto an existing path succeeds and
 * silently destroys whatever was there). Fix round 2 closes the remaining gap in that pair: when
 * TWO racers both successfully `link()` to different candidate names before either `unlink()`s the
 * source (both still see the source as present), the loser now removes its OWN candidate instead
 * of leaving two hardlinks to identical data around (which `readAllRows` would otherwise count
 * twice) — see `claimRotatedName`'s second `catch`.
 */

import { appendFile, link, mkdir, open, readdir, readFile, stat, unlink } from 'node:fs/promises';
import path from 'node:path';
import { ledgerDir, ledgerPath } from './paths.mjs';

/** @typedef {Record<string, any>} LedgerRow - a ledger row is intentionally schema-free in 0.1. */

const DEFAULT_ROTATE_AT_BYTES = 10 * 1024 * 1024;

const hasTokens = (row) => typeof row.tokens_in === 'number' || typeof row.tokens_out === 'number';
const hasCost = (row) => typeof row.cost_usd === 'number';

/**
 * Stamp `ts` and make `tokens_source`/`cost_source` ALWAYS-present keys (plan §6.1: "Tokens are
 * facts, dollars are estimates"). Neither defaults to a non-null value unconditionally: an unset
 * `tokens_source` becomes `'estimated'` ONLY when the row carries a token count, and stays `null`
 * (present, not omitted) otherwise; an unset `cost_source` becomes `'estimated'` ONLY when the row
 * carries `cost_usd` (0.1's only cost source is the static price table, `./prices.mjs`), and stays
 * `null` otherwise. The acceptance bar is "the key exists on every row", not "every row has a
 * non-null value" — a `run.start` row with neither field is `{tokens_source: null, cost_source: null}`.
 */
function normalizeRow(row) {
  if (row === null || typeof row !== 'object' || Array.isArray(row)) {
    throw new TypeError('appendRow: row must be a plain object');
  }
  if (typeof row.event !== 'string' || row.event.length === 0) {
    throw new TypeError('appendRow: row.event must be a non-empty string');
  }
  return {
    ...row,
    ts: row.ts ?? new Date().toISOString(),
    tokens_source: row.tokens_source ?? (hasTokens(row) ? 'estimated' : null),
    cost_source: row.cost_source ?? (hasCost(row) ? 'estimated' : null),
  };
}

/**
 * Claim `<base>.<n>.jsonl` for `n = 1, 2, ...` atomically via `link()` (hardlink the still-full
 * file under the rotated name, then unlink the original) instead of `rename()`.
 * @param {string} file @param {string} dir @param {string} base
 */
export async function claimRotatedName(file, dir, base) {
  let n = 1;
  for (;;) {
    const candidate = path.join(dir, `${base}.${n}.jsonl`);
    try {
      await link(file, candidate);
    } catch (err) {
      if (err.code === 'EEXIST') {
        // Another racer already claimed THIS candidate name — try the next one.
        n += 1;
        continue;
      }
      if (err.code === 'ENOENT') return; // `file` vanished before we could link it — a racer already rotated it out
      throw err;
    }
    try {
      await unlink(file);
      return;
    } catch (err) {
      if (err.code === 'ENOENT') {
        // We successfully linked `candidate` to the source's data, but ANOTHER racer's unlink beat
        // ours to removing the source — that racer's own candidate is the one that "owns" this
        // rotation. Ours is now a redundant hardlink to the identical data; remove it so
        // `readAllRows` doesn't read (and count) the same rows twice via two different filenames.
        await unlink(candidate).catch(() => {});
        return;
      }
      throw err;
    }
  }
}

/**
 * Rotate `file` to the next free `<base>.<n>.jsonl` once it reaches `rotateAtBytes`. Rotation
 * only — torn-final-line handling lives in `truncateTornTail`.
 * @param {string} file @param {number} rotateAtBytes
 */
async function rotateIfNeeded(file, rotateAtBytes) {
  let size;
  try {
    size = (await stat(file)).size;
  } catch (err) {
    if (err.code === 'ENOENT') return;
    throw err;
  }
  if (size < rotateAtBytes) return;
  await claimRotatedName(file, path.dirname(file), path.basename(file, '.jsonl'));
}

/**
 * Recover from a torn final line (a crash or an overlapping write mid-append) by TRUNCATING the
 * file back to the end of its last COMPLETE line, discarding the incomplete fragment — it was
 * never a finished row, so there is nothing to preserve. This runs before every append, not just
 * once: merely prefixing the next write with `\n` (isolating, not discarding, the fragment) fixes
 * the FIRST torn write but leaves the fragment sitting in the middle of the file from then on,
 * which `parseLines` correctly treats as real corruption and throws on — silently re-breaking the
 * exact failure this recovery exists to prevent.
 *
 * Fix round 3: everything here is measured in BYTES. The cut point used to come from
 * `lastIndexOf('\n')` on a UTF-8-DECODED string (UTF-16 code units) and was then passed to
 * `truncate()` (bytes) — any non-ASCII row (`información`, an emoji) made the byte offset larger
 * than the character offset, so the file was cut too early: complete rows lost and a fragment
 * left mid-file. The common case (file ends in `\n`) now reads ONE byte, not the whole file; the
 * full read happens only when the file really ends mid-line.
 *
 * Single-writer recovery: the per-slug chain serializes this process only. If the size changes
 * between the read and the truncate (another process appended), the truncate is skipped rather
 * than cutting that process's row; the next append retries the recovery.
 * @param {string} file
 */
async function truncateTornTail(file) {
  let handle;
  try {
    handle = await open(file, 'r+');
  } catch (err) {
    if (err.code === 'ENOENT') return;
    throw err;
  }
  try {
    const { size } = await handle.stat();
    if (size === 0) return;
    const last = Buffer.alloc(1);
    await handle.read(last, 0, 1, size - 1);
    if (last[0] === 0x0a) return; // ends with '\n' — nothing torn
    const buf = Buffer.alloc(size);
    const { bytesRead } = await handle.read(buf, 0, size, 0);
    const cleanBytes = buf.subarray(0, bytesRead).lastIndexOf(0x0a) + 1; // 0 when no complete line
    if ((await handle.stat()).size !== size) return; // a concurrent writer moved the tail — leave it
    await handle.truncate(cleanBytes);
  } finally {
    await handle.close();
  }
}

/**
 * Per-slug promise chain (in-process only — `claimRotatedName`'s `link`/`unlink` pair is what
 * protects against a SECOND process). A failed task never wedges the chain: the stored promise
 * always resolves, while the caller's own promise still rejects with the task's real error.
 * @type {Map<string, Promise<any>>}
 */
const chains = new Map();

/** @param {string} slug @param {() => Promise<any>} task */
function enqueue(slug, task) {
  const previous = (chains.get(slug) ?? Promise.resolve()).catch(() => {});
  const result = previous.then(task);
  chains.set(slug, result.catch(() => {}));
  return result;
}

/**
 * @param {LedgerRow} row - must carry a non-empty `event`; everything else is caller-defined.
 * @param {{slug: string, rotateAtBytes?: number}} opts - `rotateAtBytes` defaults to 10 MB; tests
 *   pass a tiny value to prove rotation without writing 10 MB of fixture data.
 * @returns {Promise<LedgerRow>} the normalized row that was written.
 */
export async function appendRow(row, opts) {
  // `async` (not a plain function returning a Promise) so a synchronous validation throw here
  // — before the queue or disk are ever touched — still comes back as a REJECTED promise, not an
  // exception thrown out of the call itself; a caller that always does
  // `await appendRow(...)`/`assert.rejects(() => appendRow(...))` must see one consistent shape.
  const { slug, rotateAtBytes = DEFAULT_ROTATE_AT_BYTES } = opts ?? {};
  const normalized = normalizeRow(row); // validate before ever touching the queue/disk
  ledgerPath(slug); // validates slug BEFORE mkdir/enqueue run at all — a bad slug must create nothing
  if (!Number.isFinite(rotateAtBytes) || rotateAtBytes <= 0) {
    throw new RangeError(`appendRow: rotateAtBytes must be a finite number > 0, got ${rotateAtBytes}`);
  }
  return enqueue(slug, async () => {
    await mkdir(ledgerDir(), { recursive: true });
    const file = ledgerPath(slug);
    await rotateIfNeeded(file, rotateAtBytes);
    await truncateTornTail(file);
    await appendFile(file, `${JSON.stringify(normalized)}\n`, 'utf8');
    return normalized;
  });
}

/**
 * Parse JSONL text tolerant of a TORN final line (a crash or an overlapping write mid-append can
 * leave the last line incomplete) — that one line is skipped silently. A malformed line anywhere
 * else is real corruption and throws, rather than silently dropping data from the middle of the
 * file.
 * @param {string} text
 * @returns {LedgerRow[]}
 */
function parseLines(text) {
  const lines = text.split('\n').filter((line) => line.length > 0);
  const rows = [];
  for (let i = 0; i < lines.length; i += 1) {
    try {
      rows.push(JSON.parse(lines[i]));
    } catch (err) {
      if (i === lines.length - 1) continue; // torn final line — benign, skip
      throw err; // corruption in the middle — surface it
    }
  }
  return rows;
}

/**
 * Read and parse the CURRENT (non-rotated) ledger file for `slug` only. Missing file ⇒ `[]`. Most
 * callers want `readAllRows` instead — this is the low-level primitive rotation tests use to
 * inspect one file directly.
 * @param {string} slug
 * @returns {Promise<LedgerRow[]>}
 */
export async function readRows(slug) {
  let text;
  try {
    text = await readFile(ledgerPath(slug), 'utf8');
  } catch (err) {
    if (err.code === 'ENOENT') return [];
    throw err;
  }
  return parseLines(text);
}

/**
 * Read and parse EVERY row for `slug`, across rotated files (`<slug>.1.jsonl`, `<slug>.2.jsonl`,
 * ...) and the live file, oldest first. Rotation moves the oldest rows out of `<slug>.jsonl`, not
 * out of the ledger — a caller building a report, calibration table, or `--scan-git` idempotency
 * check must read the full history, or it silently drops everything before the last rotation.
 *
 * Matches rotated filenames EXACTLY (`^<escaped-slug>\.\d+\.jsonl$`), not by a bare `startsWith`
 * prefix — `SLUG_PATTERN` (`paths.mjs`) forbids `.` in a slug today, so slug `proj` and a
 * hypothetical slug `proj.x` can never collide in practice, but the exact match keeps that true by
 * construction rather than by a coincidence of the current pattern (fix round 2).
 * @param {string} slug
 * @returns {Promise<LedgerRow[]>}
 */
export async function readAllRows(slug) {
  ledgerPath(slug); // validates slug (throws TypeError on '..', '/', empty, etc.) — return value unused here
  const dir = ledgerDir();
  let entries;
  try {
    entries = await readdir(dir);
  } catch (err) {
    if (err.code === 'ENOENT') return [];
    throw err;
  }
  const escapedSlug = slug.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  const rotatedExact = new RegExp(`^${escapedSlug}\\.(\\d+)\\.jsonl$`);
  const rotated = entries
    .map((f) => ({ f, m: rotatedExact.exec(f) }))
    .filter((x) => x.m)
    .map((x) => ({ f: x.f, n: Number(x.m[1]) }))
    .sort((a, b) => a.n - b.n)
    .map((x) => x.f);

  const rows = [];
  for (const file of [...rotated, `${slug}.jsonl`]) {
    let text;
    try {
      text = await readFile(path.join(dir, file), 'utf8');
    } catch (err) {
      if (err.code === 'ENOENT') continue;
      throw err;
    }
    rows.push(...parseLines(text));
  }
  return rows;
}
