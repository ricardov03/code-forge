/**
 * Outcome rows — the ledger's ground truth for calibration (plan §6.3): the block gate (caller 2,
 * a later block), `ledger outcome --pr <n> --ci ...` (caller 3, the CI template), and
 * `--scan-git` (caller 4, C12) all funnel into `recordOutcome`.
 *
 * `scanGitOutcomes` is a PURE git→outcome-row mapper (no ledger I/O), testable without any JSONL
 * file; `scanGitAndRecord` is the thin, IDEMPOTENT wrapper the CLI verb calls.
 *
 * Fix round 1 (2 MAJOR + 5 MINOR): the revert window is measured from each file's OWN review time
 * (`reviewedAt`), not from when the scan happens to run — a global "now - 7 days" window means a
 * revert 6 days after review is missed if the scan runs on day 10, and a revert 30 days after
 * review counts if the scan runs the next day. `scanGitAndRecord` now skips (commit, file) pairs
 * it already recorded, so running `--scan-git` twice does not double-count. `tokens_source`/
 * `cost_source` on outcome rows: no extra code needed here — every row funnels through
 * `write.mjs`'s `appendRow`, whose `normalizeRow` stamps both keys on 100% of rows unconditionally
 * (see `write.test.mjs`); `test/ledger/outcome.test.mjs` asserts this directly for outcome rows too.
 *
 * Fix round 2: every `git` call here strips inherited `GIT_*` env vars (`GIT_DIR`, `GIT_WORK_TREE`,
 * `GIT_INDEX_FILE`, ...) before running — the same class of bug the fixture builder had (fix
 * round 2 MAJOR there), but this is production code: `forge ledger outcome --scan-git` running
 * from inside any process that already has those set (a git hook, a CI step invoked BY git) would
 * otherwise have `cwd` ignored for repository discovery and scan the WRONG repo entirely.
 * (`exec` passes `env` straight to `spawn`, unmerged, so the stripped keys stay stripped.)
 *
 * Fix round 3: a revert-of-a-revert is decided by the `This reverts commit <sha>` CHAIN (odd depth
 * ⇒ reverted, even ⇒ restored), with the subject as fallback — git ≥ 2.43 writes `Reapply "…"`
 * for a revert of a revert, which the old `Revert "Revert` check missed. See `revertDepth`.
 */

import { realpath } from 'node:fs/promises';
import path from 'node:path';
import { exec } from '../util/exec.mjs';
import { appendRow, readAllRows } from './write.mjs';

const FIELD_SEP = '\x1f';
const RECORD_SEP = '\x1e';

/** `process.env`, minus every `GIT_*` key — see the fix-round-2 note above. */
function gitEnv() {
  const out = {};
  for (const [key, value] of Object.entries(process.env)) {
    if (!key.startsWith('GIT_')) out[key] = value;
  }
  return out;
}

/** the body line `git revert` always writes: `This reverts commit <sha>.` (or `<sha> (<subject>, <date>).` with `--reference`). */
const REVERTS_COMMIT_BODY_RE = /This reverts commit ([0-9a-f]{7,40})\b/i;
/** Each leading `Revert "` in a subject is one revert level. */
const REVERT_PREFIX_RE = /^Revert ["“]/i;
/**
 * Since git 2.43, reverting a commit whose subject is `Revert "X"` writes `Reapply "X"` instead
 * of `Revert "Revert "X""` — so one leading `Reapply "` is TWO revert levels (git 2.50 verified:
 * the chain is `X` → `Revert "X"` → `Reapply "X"` → `Revert "Reapply "X""` → `Reapply "Reapply "X""`).
 */
const REAPPLY_PREFIX_RE = /^Reapply ["“]/i;
/** Guard for a pathological or cyclic `This reverts commit` chain. */
const MAX_REVERT_CHAIN = 64;

/**
 * How many revert levels the SUBJECT alone encodes: `Revert "` = 1, `Reapply "` = 2, nested
 * prefixes add up. Odd ⇒ the change is undone; even and > 0 ⇒ it is restored; 0 ⇒ the subject
 * says nothing (a hand-edited message).
 * @param {string} subject @returns {number}
 */
function subjectRevertDepth(subject) {
  let depth = 0;
  let rest = subject;
  for (;;) {
    if (REVERT_PREFIX_RE.test(rest)) {
      depth += 1;
      rest = rest.replace(REVERT_PREFIX_RE, '');
    } else if (REAPPLY_PREFIX_RE.test(rest)) {
      depth += 2;
      rest = rest.replace(REAPPLY_PREFIX_RE, '');
    } else {
      return depth;
    }
  }
}

/**
 * Exported for direct unit testing (fix round 1): the previous, unanchored `revert.*#\d+` pattern
 * false-positived on subjects like "Prevent revert of cache (#12)" or "don't revert #3", and
 * counted a revert-of-a-revert (which restores the original change) as a fresh "reverted" outcome.
 *
 * Fix round 3: this is the SUBJECT/BODY-only verdict — old-style `Revert "Revert "X""` AND
 * git ≥ 2.43's `Reapply "X"` are both restores (false); a revert of a reapply is a revert again
 * (true). A subject with no revert prefix falls back to the `This reverts commit` body marker.
 * `scanGitOutcomes` goes further and follows the body's sha chain (see `revertDepth`), which also
 * settles a hand-edited subject.
 * @param {{subject: string, body: string}} commit @returns {boolean}
 */
export function isRevertCommit({ subject, body }) {
  const depth = subjectRevertDepth(subject);
  if (depth > 0) return depth % 2 === 1;
  return REVERTS_COMMIT_BODY_RE.test(body);
}

/**
 * Resolve one commit's subject/body by (possibly abbreviated) sha, or `null` if the repo does not
 * have it (shallow clone, rewritten history).
 * @param {string} sha @param {string} cwd
 * @returns {Promise<{subject: string, body: string} | null>}
 */
async function lookupCommit(sha, cwd) {
  const res = await exec(['git', 'log', '-1', `--pretty=format:%s${FIELD_SEP}%b`, `${sha}^{commit}`, '--'], { cwd, env: gitEnv() });
  if (res.result !== 'ok') return null;
  const [subject, body] = res.stdout.split(FIELD_SEP);
  return { subject: subject ?? '', body: body ?? '' };
}

/**
 * Revert depth decided by the commit RELATION, not the wording: a commit whose body says
 * `This reverts commit <sha>` is one level deeper than `<sha>` itself. Only where that chain
 * cannot be followed (no body marker, or `<sha>` not in this repo) does the subject decide. So a
 * hand-edited "Restore feature X" that reverts a revert is depth 2 (a restore), not a revert.
 * @param {{subject: string, body: string}} commit
 * @param {(sha: string) => Promise<{subject: string, body: string} | null>} lookup
 * @returns {Promise<number>}
 */
async function revertDepth(commit, lookup) {
  let levels = 0;
  let current = commit;
  const seen = new Set();
  for (let i = 0; i < MAX_REVERT_CHAIN; i += 1) {
    const match = REVERTS_COMMIT_BODY_RE.exec(current.body);
    if (!match) return levels + subjectRevertDepth(current.subject);
    const sha = match[1].toLowerCase();
    const parent = seen.has(sha) ? null : await lookup(sha);
    seen.add(sha);
    if (!parent) return levels + Math.max(1, subjectRevertDepth(current.subject));
    levels += 1;
    current = parent;
  }
  return levels;
}

/** @param {string} cwd @returns {Promise<string>} the repo root, so relative paths always match `reviewed[].file`. */
async function repoRoot(cwd) {
  const res = await exec(['git', 'rev-parse', '--show-toplevel'], { cwd, env: gitEnv() });
  if (res.result !== 'ok') throw new Error(`git rev-parse --show-toplevel failed: ${res.stderr || res.error}`);
  return res.stdout.trim();
}

/**
 * @param {string} cwd @param {{since?: Date, until?: Date}} [range] - a coarse pre-filter so a
 *   large repo's `git log` output doesn't have to enumerate its whole history through `exec`'s
 *   buffer; per-file window checks still happen in JS afterward.
 * @returns {Promise<Array<{hash: string, committedAt: string, subject: string, body: string}>>}
 */
async function listCommits(cwd, range = {}) {
  const args = ['git', 'log', `--pretty=format:%H${FIELD_SEP}%cI${FIELD_SEP}%s${FIELD_SEP}%b${RECORD_SEP}`];
  if (range.since) args.push(`--since=${range.since.toISOString()}`);
  if (range.until) args.push(`--until=${range.until.toISOString()}`);
  const res = await exec(args, { cwd, env: gitEnv() });
  if (res.result !== 'ok') throw new Error(`git log failed: ${res.stderr || res.error}`);
  return res.stdout
    .split(RECORD_SEP)
    .map((chunk) => chunk.replace(/^\n/, ''))
    .filter((chunk) => chunk.length > 0)
    .map((chunk) => {
      const [hash, committedAt, subject, body] = chunk.split(FIELD_SEP);
      return { hash, committedAt, subject, body: body ?? '' };
    });
}

/**
 * `-c core.quotePath=false` — without it, git C-style-escapes a path containing non-ASCII or
 * special characters (e.g. `src/é.mjs` → `"src/\303\251.mjs"`), which would never match a
 * `reviewed[].file` entry and silently drop that file's revert.
 * @param {string} hash @param {string} cwd @returns {Promise<string[]>}
 */
async function changedFiles(hash, cwd) {
  const res = await exec(['git', '-c', 'core.quotePath=false', 'diff-tree', '--no-commit-id', '--name-only', '-r', hash], {
    cwd,
    env: gitEnv(),
  });
  if (res.result !== 'ok') throw new Error(`git diff-tree failed for ${hash}: ${res.stderr || res.error}`);
  return res.stdout.split('\n').filter((line) => line.length > 0);
}

/**
 * Group `reviewed` entries by file, KEEPING EVERY REVIEW (fix round 2 — a version that kept only
 * the latest review per file would miss a revert that lands between an EARLIER review and a LATER
 * one: file X reviewed in block A, reverted 3 days later, then reviewed again in block B before
 * any scan runs — the revert now sits before B's `reviewedAt`, and "only the latest review" would
 * silently drop it). Each file's list is sorted ascending by `reviewedAt` so the scan loop can find
 * the latest review STRICTLY BEFORE a given revert commit's time.
 *
 * An entry whose `reviewedAt` does not parse to a finite time is skipped outright — a NaN in the
 * mix would otherwise poison every `Math.min`/`Math.max` over the group into NaN, and
 * `new Date(NaN).toISOString()` throws, crashing the scan over ONE bad ledger row instead of just
 * ignoring it.
 * @param {Array<{file: string, reviewedAt: string|Date, block?: string, run?: string}>} reviewed
 * @returns {Map<string, Array<{reviewedAt: Date, block?: string, run?: string}>>}
 */
function reviewsByFile(reviewed) {
  /** @type {Map<string, Array<{reviewedAt: Date, block?: string, run?: string}>>} */
  const byFile = new Map();
  for (const entry of reviewed) {
    const reviewedAt = new Date(entry.reviewedAt);
    if (!Number.isFinite(reviewedAt.getTime())) continue;
    const list = byFile.get(entry.file) ?? [];
    list.push({ reviewedAt, block: entry.block, run: entry.run });
    byFile.set(entry.file, list);
  }
  for (const list of byFile.values()) list.sort((a, b) => a.reviewedAt.getTime() - b.reviewedAt.getTime());
  return byFile;
}

/**
 * The latest review in `list` (ascending-sorted by `reviewedAt`) strictly BEFORE `commitTimeMs` —
 * the review a revert at that time is judged against — or `undefined` if none qualifies.
 * @param {Array<{reviewedAt: Date}>} list @param {number} commitTimeMs
 */
function latestReviewBefore(list, commitTimeMs) {
  let found;
  for (const entry of list) {
    if (entry.reviewedAt.getTime() < commitTimeMs) found = entry;
    else break; // sorted ascending — nothing later in the list can qualify either
  }
  return found;
}

/**
 * `reviewed[].file` as given may be relative to a subdirectory `cwd`, or absolute; git's own
 * output (`diff-tree --name-only`) is always relative to the REPO ROOT. Without normalizing both
 * sides to the same base, a ledger that stores paths relative to `cwd` would never match anything
 * and silently return 0 rows.
 *
 * `root` (from `git rev-parse --show-toplevel`) is ALREADY the real, symlink-resolved path — on
 * macOS, `os.tmpdir()`/a raw `cwd` is typically `/var/folders/...`, a symlink to
 * `/private/var/folders/...`, which is what git actually reports. Comparing an unresolved `cwd`
 * against the resolved `root` with plain string-based `path.relative` produces a bogus `../../...`
 * chain instead of collapsing to the real relative path, so `cwd` is realpath'd here too.
 * @param {string} root @param {string} cwd @param {string} file
 * @returns {Promise<string>}
 */
async function toRepoRootRelative(root, cwd, file) {
  if (path.isAbsolute(file)) return path.relative(root, await realpathOfNearestAncestor(file));
  const realCwd = await realpath(cwd).catch(() => cwd);
  return path.relative(root, path.resolve(realCwd, file));
}

/**
 * `realpath` of `p`, or — when `p` no longer exists (the revert deleted it) — of its nearest
 * EXISTING ancestor with the missing tail joined back on (fix round 3). Falling back to the raw
 * path instead left macOS's `/var` ↔ `/private/var` symlink unresolved, so a deleted file's revert
 * produced a bogus `../..` path and was silently missed.
 * @param {string} p @returns {Promise<string>}
 */
async function realpathOfNearestAncestor(p) {
  const tail = [];
  let current = p;
  for (;;) {
    try {
      return path.join(await realpath(current), ...tail);
    } catch {
      const parent = path.dirname(current);
      if (parent === current) return p;
      tail.unshift(path.basename(current));
      current = parent;
    }
  }
}

/**
 * Walk `cwd`'s git log for revert-shaped commits that land within `days` days AFTER a reviewed
 * file's OWN `reviewedAt` (plan §6.3 caller 4, C12) — never a window relative to when the scan
 * happens to run, and never only the file's single latest review (see `reviewsByFile`).
 * @param {{cwd: string, reviewed: Array<{file: string, reviewedAt: string|Date, block?: string, run?: string}>, days?: number, now?: Date}} args
 * @returns {Promise<any[]>} outcome rows found — not yet appended to the ledger.
 */
export async function scanGitOutcomes({ cwd, reviewed, days = 7, now = new Date() }) {
  if (!Number.isFinite(days) || days <= 0) {
    throw new RangeError(`scanGitOutcomes: days must be a finite number > 0, got ${days}`);
  }
  const root = await repoRoot(cwd);
  const normalized = await Promise.all(reviewed.map(async (entry) => ({ ...entry, file: await toRepoRootRelative(root, cwd, entry.file) })));
  const byFile = reviewsByFile(normalized);
  if (byFile.size === 0) return [];

  // A loop, not Math.min(...all) — spreading one argument per review.done row overflows the call
  // stack on a large ledger.
  let minMs = Infinity;
  let maxMs = -Infinity;
  for (const list of byFile.values()) {
    for (const r of list) {
      const t = r.reviewedAt.getTime();
      if (t < minMs) minMs = t;
      if (t > maxMs) maxMs = t;
    }
  }
  const windowMs = days * 24 * 60 * 60 * 1000;
  const since = new Date(minMs);
  const until = new Date(Math.min(maxMs + windowMs, now.getTime()));

  const commits = await listCommits(root, { since, until });
  /** @type {Map<string, Promise<{subject: string, body: string} | null>>} */
  const lookupCache = new Map();
  for (const c of commits) lookupCache.set(c.hash, Promise.resolve(c));
  const lookup = (sha) => {
    const known = sha.length === 40 ? lookupCache.get(sha) : undefined;
    if (known) return known;
    if (!lookupCache.has(sha)) lookupCache.set(sha, lookupCommit(sha, root));
    return /** @type {Promise<{subject: string, body: string} | null>} */ (lookupCache.get(sha));
  };

  const rows = [];
  for (const commit of commits) {
    // Cheap pre-filter: a commit with neither a revert/reapply subject nor the body marker is not
    // part of any revert chain.
    if (subjectRevertDepth(commit.subject) === 0 && !REVERTS_COMMIT_BODY_RE.test(commit.body)) continue;
    if ((await revertDepth(commit, lookup)) % 2 !== 1) continue; // even ⇒ restores the change
    const committedAtMs = new Date(commit.committedAt).getTime();
    if (committedAtMs > now.getTime()) continue; // future-dated / skewed committer clock
    for (const file of await changedFiles(commit.hash, root)) {
      const list = byFile.get(file);
      if (!list) continue;
      const review = latestReviewBefore(list, committedAtMs);
      if (!review) continue; // no review of this file precedes the revert at all
      if (committedAtMs > review.reviewedAt.getTime() + windowMs) continue; // outside THAT review's window
      rows.push({
        event: 'outcome',
        outcome: 'reverted',
        outcome_source: 'git',
        file,
        commit: commit.hash,
        subject: commit.subject,
        committed_at: commit.committedAt,
        ...(review.block ? { block: review.block } : {}),
        ...(review.run ? { run: review.run } : {}),
      });
    }
  }
  return rows;
}

/** @param {object} outcomeData @param {{slug: string}} opts */
export const recordOutcome = (outcomeData, opts) => appendRow({ event: 'outcome', ...outcomeData }, opts);

/**
 * Scan + persist in one call — what `forge ledger outcome --scan-git` runs. IDEMPOTENT: skips any
 * (commit, file) pair that already has a `git`-sourced outcome row for this slug, so running it
 * twice (a re-triggered CI job, a human re-running it) does not double-count the same revert.
 * @param {{cwd: string, reviewed: Array<{file: string, reviewedAt: string|Date, block?: string, run?: string}>, days?: number, now?: Date, slug: string}} args
 * @returns {Promise<any[]>} the outcome rows actually appended (excludes already-recorded ones).
 */
export async function scanGitAndRecord({ cwd, reviewed, days, now, slug }) {
  const found = await scanGitOutcomes({ cwd, reviewed, days, now });
  const existing = await readAllRows(slug);
  const already = new Set(
    existing.filter((r) => r.event === 'outcome' && r.outcome_source === 'git').map((r) => `${r.commit}::${r.file}`),
  );
  const written = [];
  for (const row of found) {
    const key = `${row.commit}::${row.file}`;
    if (already.has(key)) continue;
    written.push(await recordOutcome(row, { slug }));
    already.add(key);
  }
  return written;
}

/**
 * Build the `reviewed` argument for `scanGitOutcomes`/`scanGitAndRecord` from a ledger's own rows:
 * EVERY `review.done` row becomes its own entry (fix round 2 — collapsing to one-per-file here
 * would silently undo `scanGitOutcomes`'s "keep every review" fix: a file reviewed twice, with a
 * revert landing between the two reviews, needs BOTH review timestamps present so the revert can
 * be matched to the one that actually preceded it).
 * @param {any[]} rows
 * @returns {Array<{file: string, reviewedAt: string, block?: string}>}
 */
export function reviewedEntriesFromRows(rows) {
  return rows
    .filter((r) => r.event === 'review.done' && r.file && r.ts)
    .map((r) => ({ file: r.file, reviewedAt: r.ts, ...(r.block ? { block: r.block } : {}) }));
}

const CI_TO_OUTCOME = Object.freeze({ red: 'wrong', green: 'correct', reverted: 'reverted' });

/**
 * `forge ledger outcome --pr <n> --ci red|green|reverted` (plan §6.3 caller 3, the CI template).
 * @param {{pr: number, ci: 'red'|'green'|'reverted', slug: string}} args
 */
export function recordCiOutcome({ pr, ci, slug }) {
  // Object.hasOwn (not `CI_TO_OUTCOME[ci]` truthiness) so `--ci toString`/`--ci constructor` can't
  // walk the prototype chain to a function and slip past the RangeError guard below.
  if (!Object.hasOwn(CI_TO_OUTCOME, ci)) {
    throw new RangeError(`recordCiOutcome: unknown --ci value "${ci}"`);
  }
  if (!Number.isInteger(pr) || pr <= 0) {
    throw new RangeError(`recordCiOutcome: pr must be a positive integer, got ${pr}`);
  }
  return recordOutcome({ outcome: CI_TO_OUTCOME[ci], outcome_source: 'ci', pr }, { slug });
}
