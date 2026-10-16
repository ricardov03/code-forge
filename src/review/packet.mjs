/**
 * The review packet (plan §4.1, §4.3, V3, V4; block B12a).
 *
 * A reviewer session is closed-book: the packet is everything it sees, piped to its stdin by the
 * spawner. Sections, in FIXED order, so the prefix (lens, rules digest, facts) is byte-identical
 * across the files of a block and provider prompt caching applies:
 *
 *   # code-forge review packet
 *   ## lens            the lens file (`lenses/<name>.md`)
 *   ## project rules   the rules digest (≤ 60 lines)
 *   ## facts           the facts-sheet excerpt
 *   ## context         the current file (whole, or hunk windows — `context.mjs`)
 *   ## paired          optional paired-file names and summary (B97), dropped first over budget
 *   ## moved code      only when code moved to/from another file of the block (`moved.mjs`, B56)
 *   ## diff            `file:`, the `hunks:` list the reviewer must echo, then the diff
 *
 * Diff source (§4.1): a file in the index is diffed against the block's base SHA
 * (`git diff <base> -- <file>`); a file on disk that is not in the index is new
 * (`git diff --no-index -- /dev/null <file>`). A tracked file with no change yields no packet.
 *
 * Budget (§4.3): `review.budgets.full_in` (`quick_in` for the quick lens), in tokens estimated as
 * bytes / 4. A diff that alone exceeds it ⇒ `split_required` (a Markdown file is then reviewed
 * section by section — `sections.mjs`, B54). Otherwise over budget ⇒ the context shrinks to
 * ± `min_context_lines` (`minimal`) after dropping paired, then the digest is trimmed from the end; the diff is
 * never cut. The `## moved code` section (B56) is part of that measure: it is counted in the diff-only
 * size (`diffOnlyTokens`, `split_required`) and never trimmed.
 *
 * Path rule (V4): `file` is repo-root-relative; `./`, `../`, absolute values, symlinks and paths
 * under `.git/` or `.code-forge/` are refused with `bad-path` before git runs. So is anything git
 * could read as pathspec magic (`*`, `?`, `[`, `]`, a leading `:`), and every child git here runs
 * with `GIT_LITERAL_PATHSPECS=1`, so one `file` is always one file. A secret-like name (`.env`,
 * `*.pem`, `id_rsa`, …) or an untracked file git ignores never becomes a "new file" packet.
 */

import { existsSync, lstatSync, readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { exec } from '../util/exec.mjs';
import { assertInsideRoot, assertRowPath, gitChildEnv, readRegularFileNoFollow, WorkerError } from '../worker/ticket.mjs';
import { buildContext, parseDiff, splitLines } from './context.mjs';

export const LENSES = Object.freeze(['quick', 'full', 'recheck', 'A', 'B', 'judge']);

/** The rules digest never exceeds this many lines (§4.3). */
export const DIGEST_MAX_LINES = 60;

/** `full_in` was 12 000 until B54: a 1,590-line design brief (~25k tokens) was refused unreviewed. */
export const DEFAULT_BUDGETS = Object.freeze({ quick_in: 6000, full_in: 32000, judge_in: 8000 });

const LENS_DIR = path.join(path.dirname(fileURLToPath(import.meta.url)), 'lenses');
const GIT_TIMEOUT_MS = 30000;

/** Characters git reads as pathspec magic (globs); a leading `:` opens a magic signature. */
const PATHSPEC_MAGIC = /[*?[\]]/;

/** Paths never attached, whatever git says: env files, keys, certificates, credentials, our own state. */
const SECRET_LIKE = [/(^|\/)\.env[^/]*$/i, /\.pem$/i, /\.key$/i, /\.p12$/i, /(^|\/)id_(rsa|ed25519|ecdsa)[^/]*$/i, /(^|\/)credentials[^/]*$/i, /(^|\/)\.code-forge(\/|$)/i];

/** @param {string} rel @returns {boolean} */
export const isSecretLike = (rel) => SECRET_LIKE.some((re) => re.test(rel));

/** The env for every child git in this module: B11's clean env plus literal pathspecs. */
export const reviewGitEnv = () => ({ ...gitChildEnv(), GIT_LITERAL_PATHSPECS: '1' });

/** B11's clean env alone: for `git check-ignore`, which refuses the literal-pathspec magic. */
export const plainGitEnv = () => gitChildEnv();

/** A refusal with a stable `code`: `bad-path`, `bad-lens`, `git`. */
export class PacketError extends Error {
  /** @param {string} code @param {string} message */
  constructor(code, message) {
    super(message);
    this.name = 'PacketError';
    this.code = code;
  }
}

/** @param {string} text @returns {number} estimated tokens (bytes / 4, rounded up). */
export const estimateTokens = (text) => Math.ceil(Buffer.byteLength(text) / 4);

/** @param {string} name @returns {string} the lens text. */
export function loadLens(name) {
  if (!LENSES.includes(name)) throw new PacketError('bad-lens', `unknown lens ${JSON.stringify(name)}`);
  return readFileSync(path.join(LENS_DIR, `${name}.md`), 'utf8').trimEnd();
}

/**
 * @param {Record<string, any> | undefined} cfg @param {'quick_in' | 'full_in' | 'judge_in'} key
 * @returns {number}
 */
export function budgetFor(cfg, key) {
  const v = cfg?.review?.budgets?.[key];
  return Number.isInteger(v) && v > 0 ? v : DEFAULT_BUDGETS[key];
}

/**
 * Refuse a `file` value that is not a plain repo-root-relative path (V4): the string shape first
 * (`assertRowPath`), then the disk — the joined path, when it exists, is `lstat`ed and a symlink is
 * refused (never followed: `readFileSync` would hand the reviewer the target's bytes), and its
 * realpath'd parent must stay inside the root (`assertInsideRoot`). No git call happens before.
 * @param {string} repoRoot @param {unknown} file @returns {string}
 * @throws {PacketError} `bad-path`
 */
export function assertPacketPath(repoRoot, file) {
  let rel;
  try {
    rel = assertRowPath(file);
  } catch (err) {
    if (err instanceof WorkerError) throw new PacketError('bad-path', err.message);
    throw err;
  }
  if (PATHSPEC_MAGIC.test(rel) || rel.startsWith(':')) throw new PacketError('bad-path', 'file may not contain glob characters or start with ":"');
  /** @type {import('node:fs').Stats | null} */
  let st = null;
  try {
    st = lstatSync(path.join(repoRoot, rel));
  } catch {
    // missing: a deletion is reviewable; the parent is still checked below
  }
  if (st?.isSymbolicLink()) throw new PacketError('bad-path', 'the path is a symlink');
  // a directory (or device, socket, …) is not one reviewable file: git would diff everything under it
  if (st && !st.isFile()) throw new PacketError('bad-path', 'not a regular file');
  try {
    return assertInsideRoot(repoRoot, rel);
  } catch (err) {
    if (err instanceof WorkerError) throw new PacketError('bad-path', err.message);
    throw err;
  }
}

/**
 * @typedef {object} FileDiff
 * @property {string} file
 * @property {'tracked' | 'new'} kind
 * @property {string} diffText
 * @property {string | null} content - the current content, null when the file is deleted.
 * @property {import('./context.mjs').Hunk[]} hunks
 * @property {number} plusCount
 * @property {number} minusCount
 */

/**
 * Read one file's diff against `base` (§4.1).
 * @param {{repoRoot: string, file: string, base?: string | null}} opts
 * @returns {Promise<FileDiff>}
 * @throws {PacketError} `bad-path`, `git`
 */
export async function readFileDiff({ repoRoot, file, base }) {
  const rel = assertPacketPath(repoRoot, file);
  if (isSecretLike(rel)) throw new PacketError('bad-path', 'secret-like path');
  const ref = base ?? 'HEAD';
  if (typeof ref !== 'string' || ref.length === 0 || ref.startsWith('-')) throw new PacketError('git', 'the base must be a commit id');
  const env = reviewGitEnv();
  const listed = await exec(['git', 'ls-files', '-z', '--', rel], { cwd: repoRoot, env, timeoutMs: GIT_TIMEOUT_MS });
  if (listed.result !== 'ok') throw new PacketError('git', `git ls-files failed (exit ${listed.code})`);
  const full = path.join(repoRoot, rel);
  const onDisk = existsSync(full);
  const kind = listed.stdout.length === 0 && onDisk ? 'new' : 'tracked';
  if (kind === 'new') {
    // An untracked file git ignores (a local `.env`, a build secret) is never a "new file".
    // `check-ignore` refuses the `literal` pathspec magic ("not supported by this command"), so it
    // runs on the plain env: `rel` has no glob character and no leading `:` (refused above).
    const ignored = await exec(['git', 'check-ignore', '-q', '--', rel], { cwd: repoRoot, env: gitChildEnv(), timeoutMs: GIT_TIMEOUT_MS, okExitCodes: [0, 1] });
    if (ignored.result !== 'ok') throw new PacketError('git', `git check-ignore failed (exit ${ignored.code})`);
    if (ignored.code === 0) throw new PacketError('bad-path', 'the file is untracked and gitignored');
  }
  const argv =
    kind === 'new'
      ? ['git', 'diff', '--no-color', '--no-ext-diff', '--no-index', '--', '/dev/null', rel]
      : ['git', 'diff', '--no-color', '--no-ext-diff', ref, '--', rel];
  const res = await exec(argv, { cwd: repoRoot, env, timeoutMs: GIT_TIMEOUT_MS, ...(kind === 'new' ? { okExitCodes: [0, 1] } : {}) });
  if (res.result !== 'ok') throw new PacketError('git', `git diff failed for ${rel} (exit ${res.code})`);
  const content = onDisk ? readRepoText(repoRoot, rel) : null;
  return { file: rel, kind, diffText: res.stdout, content, ...parseDiff(res.stdout) };
}

/**
 * @typedef {object} Packet
 * @property {'ok'} status
 * @property {string} text
 * @property {string[]} hunkHeaders - what `reviewed_hunks` must equal, in order.
 * @property {'whole' | 'hunks' | 'minimal' | 'recheck'} contextMode
 * @property {number} contextLines
 * @property {number} digestLines - digest lines kept.
 * @property {number} tokensIn - estimated.
 * @property {number} budget
 * @property {boolean} overBudget - still over after every shrink step (facts too large).
 */

/**
 * Assemble a reviewer packet (pure).
 * @param {{
 *   diff: FileDiff, lens: string, rulesDigest?: string, factsExcerpt?: string,
 *   cfg?: Record<string, any>, contextMode?: 'auto' | 'recheck', budget?: number, moved?: string, paired?: string,
 * }} opts - `moved`: the file's `## moved code` section (`moved.mjs`), '' for none.
 * @returns {Packet | {status: 'split_required', tokensIn: number, budget: number}}
 */
export function assemblePacket({ diff, lens, rulesDigest = '', factsExcerpt = '', cfg, contextMode = 'auto', budget, moved = '', paired = '' }) {
  const lensText = loadLens(lens);
  const limit = budget ?? budgetFor(cfg, lens === 'quick' ? 'quick_in' : 'full_in');
  const hunkHeaders = diff.hunks.map((h) => h.header);
  const diffSection = diffSectionOf(diff);
  const bare = bareTokens(lensText, diffSection, moved);
  if (bare > limit) return { status: 'split_required', tokensIn: bare, budget: limit };

  let digest = splitLines(rulesDigest).slice(0, DIGEST_MAX_LINES);
  let ctx = buildContext({ file: diff.file, content: diff.content, hunks: diff.hunks, cfg, mode: contextMode });
  let pairedSection = boundedPaired(paired, budgetFor(cfg, 'full_in'));
  let text = render(lensText, digest, factsExcerpt, ctx.text, diffSection, moved, pairedSection);
  if (estimateTokens(text) > limit && pairedSection.length > 0) {
    pairedSection = '';
    text = render(lensText, digest, factsExcerpt, ctx.text, diffSection, moved);
  }
  if (estimateTokens(text) > limit) {
    ctx = buildContext({ file: diff.file, content: diff.content, hunks: diff.hunks, cfg, mode: 'minimal' });
    text = render(lensText, digest, factsExcerpt, ctx.text, diffSection, moved);
  }
  while (estimateTokens(text) > limit && digest.length > 0) {
    digest = digest.slice(0, -1);
    text = render(lensText, digest, factsExcerpt, ctx.text, diffSection, moved);
  }
  const tokensIn = estimateTokens(text);
  return { status: 'ok', text, hunkHeaders, contextMode: ctx.mode, contextLines: ctx.lineCount, digestLines: digest.length, tokensIn, budget: limit, overBudget: tokensIn > limit };
}

/** One full-budget packet for a light block; every diff and path heading is retained.
 * @param {{diffs: FileDiff[], cfg?: Record<string, any>, rulesDigest?: string, factsExcerpt?: string}} opts
 */
export function assembleLightPacket({ diffs, cfg, rulesDigest = '', factsExcerpt = '' }) {
  const lens = `${loadLens('full')}\nReview ALL files below in one round. Findings must name their exact file path. Echo every listed hunk in file order, including duplicates.`;
  const diffText = diffs.map((diff) => `### ${diff.file}\n${diffSectionOf(diff)}`).join('\n\n');
  const budget = budgetFor(cfg, 'full_in');
  let digest = splitLines(rulesDigest).slice(0, DIGEST_MAX_LINES);
  const context = (mode) => diffs.map((diff) => `### ${diff.file}\n${buildContext({ file: diff.file, content: diff.content, hunks: diff.hunks, cfg, mode }).text}`).join('\n\n');
  let contextMode = 'auto';
  let ctx = context(contextMode);
  let text = render(lens, digest, factsExcerpt, ctx, diffText);
  if (estimateTokens(text) > budget) {
    contextMode = 'minimal';
    ctx = context(contextMode);
    text = render(lens, digest, factsExcerpt, ctx, diffText);
  }
  while (estimateTokens(text) > budget && digest.length > 0) {
    digest = digest.slice(0, -1);
    text = render(lens, digest, factsExcerpt, ctx, diffText);
  }
  const tokensIn = estimateTokens(text);
  return { status: tokensIn > budget ? 'split_required' : 'ok', text, tokensIn, budget,
    contextMode, hunkHeaders: diffs.flatMap((diff) => diff.hunks.map((h) => h.header)) };
}

/** Cap the entire section at full_in / 16 tokens, without cutting a UTF-8 character.
 * @param {string} paired @param {number} fullIn @returns {string}
 */
function boundedPaired(paired, fullIn) {
  if (paired.trim().length === 0) return '';
  const heading = '## paired\n';
  let left = Math.floor(fullIn / 16) * 4 - Buffer.byteLength(heading) - 1;
  let body = '';
  for (const char of paired) {
    left -= Buffer.byteLength(char);
    if (left < 0) break;
    body += char;
  }
  return body.length > 0 ? heading + body.trimEnd() : '';
}

/**
 * The tokens of the diff-only packet (lens + moved code + diff, no digest, facts or context): what
 * `assemblePacket` holds against the budget before it says `split_required`. Pass the SAME `moved`
 * section the packet gets.
 * @param {{diff: FileDiff, lens: string, moved?: string}} opts @returns {number}
 */
export function diffOnlyTokens({ diff, lens, moved = '' }) {
  return bareTokens(loadLens(lens), diffSectionOf(diff), moved);
}

/**
 * The ONE measure behind `split_required` (`assemblePacket`) and section packing (`diffOnlyTokens`,
 * B54), so the two can never disagree.
 * @param {string} lensText @param {string} diffSection @param {string} moved @returns {number}
 */
function bareTokens(lensText, diffSection, moved) {
  return estimateTokens(render(lensText, [], '', '', diffSection, moved));
}

/** @param {FileDiff} diff @returns {string} the `## diff` section body. */
function diffSectionOf(diff) {
  return [`file: ${diff.file}`, 'hunks:', ...diff.hunks.map((h) => `- ${h.header}`), '', diff.diffText.trimEnd()].join('\n');
}

/**
 * @param {string} lensText @param {string[]} digest @param {string} facts @param {string} context
 * @param {string} diffSection @param {string} [moved] - the `## moved code` section (heading included), '' for none.
 * @param {string} [paired] - the bounded `## paired` section (heading included).
 */
function render(lensText, digest, facts, context, diffSection, moved = '', paired = '') {
  return [
    '# code-forge review packet',
    '## lens',
    lensText,
    '## project rules',
    digest.length > 0 ? digest.join('\n') : '(none)',
    '## facts',
    facts.trim().length > 0 ? facts.trimEnd() : '(none)',
    '## context',
    context.length > 0 ? context : '(none)',
    ...(paired.length > 0 ? [paired] : []),
    ...(moved.length > 0 ? [moved] : []),
    '## diff',
    diffSection,
    '',
  ].join('\n');
}

/**
 * Read one file and assemble its packet (the §4.1 entry point). A tracked file with no change
 * returns `{status: 'no_change'}` — nothing to review.
 * @param {{repoRoot: string, file: string, base?: string | null, lens: string, rulesDigest?: string, factsExcerpt?: string, cfg?: Record<string, any>}} opts
 * @returns {Promise<(Packet & {diff: FileDiff}) | {status: 'no_change', file: string} | {status: 'split_required', tokensIn: number, budget: number}>}
 */
export async function buildPacket(opts) {
  const diff = await readFileDiff(opts);
  if (diff.diffText.trim().length === 0) return { status: 'no_change', file: diff.file };
  const packet = assemblePacket({ ...opts, diff });
  return packet.status === 'ok' ? { ...packet, diff } : packet;
}

/**
 * The judge's packet (§4.2 step 3, §4.4): the judge lens, the hunk list, the two blind reports,
 * and the diff only when `review.judge_sees_diff` is true (R3, default false); the file's
 * `## moved code` section (B56) before the diff when there is one.
 * @param {{diff: FileDiff, reports: {A: unknown, B: unknown}, cfg?: Record<string, any>, moved?: string}} opts
 * @returns {{status: 'ok', text: string, hunkHeaders: string[], tokensIn: number, budget: number, contextMode: null}}
 */
export function assembleJudgePacket({ diff, reports, cfg, moved = '' }) {
  const hunkHeaders = diff.hunks.map((h) => h.header);
  const seesDiff = cfg?.review?.judge_sees_diff === true;
  const text = [
    '# code-forge review packet',
    '## lens',
    loadLens('judge'),
    '## reviewer A',
    JSON.stringify(reports.A, null, 1),
    '## reviewer B',
    JSON.stringify(reports.B, null, 1),
    ...(moved.length > 0 ? [moved] : []),
    '## diff',
    [`file: ${diff.file}`, 'hunks:', ...hunkHeaders.map((h) => `- ${h}`), ...(seesDiff ? ['', diff.diffText.trimEnd()] : [])].join('\n'),
    '',
  ].join('\n');
  return { status: 'ok', text, hunkHeaders, tokensIn: estimateTokens(text), budget: budgetFor(cfg, 'judge_in'), contextMode: null };
}

/**
 * A repo file's text, read through `readRegularFileNoFollow` (O_NOFOLLOW + fstat on the open fd:
 * a file swapped for a symlink after the lstat checks is refused, never followed).
 * @param {string} repoRoot @param {string} rel @returns {string | null} null when missing.
 * @throws {PacketError} `bad-path`
 */
function readRepoText(repoRoot, rel) {
  try {
    return readRegularFileNoFollow(repoRoot, rel)?.toString('utf8') ?? null;
  } catch (err) {
    if (err instanceof WorkerError) throw new PacketError('bad-path', err.message);
    throw err;
  }
}

/**
 * Why `rel` may not be attached, or null.
 * @param {string} repoRoot @param {string} rel @param {ReadonlyArray<string>} ownedFiles
 * @param {ReadonlyArray<string>} changedFiles @returns {Promise<string | null>}
 */
async function attachRefusal(repoRoot, rel, ownedFiles, changedFiles) {
  if (isSecretLike(rel)) return 'refused: secret-like path';
  let st;
  try {
    st = lstatSync(path.join(repoRoot, rel));
  } catch {
    return 'missing';
  }
  if (!st.isFile()) return 'not a file';
  const res = await exec(['git', 'ls-files', '-z', '--', rel], { cwd: repoRoot, env: reviewGitEnv(), timeoutMs: GIT_TIMEOUT_MS });
  if (res.result !== 'ok') return 'refused: git failed';
  if (res.stdout === `${rel}\0`) return null;
  if (res.stdout.length > 0) return 'refused: git failed';
  if (!ownedFiles.includes(rel)) return 'refused: untracked, not owned by this block';
  if (!changedFiles.includes(rel)) return 'refused: untracked, not changed in this block';
  const ignored = await exec(['git', 'check-ignore', '-q', '--', rel], { cwd: repoRoot, env: plainGitEnv(), timeoutMs: GIT_TIMEOUT_MS, okExitCodes: [0, 1] });
  if (ignored.result !== 'ok' || (ignored.code !== 0 && ignored.code !== 1)) return 'refused: git failed';
  if (ignored.code === 0) return 'refused: gitignored';
  return null;
}

/**
 * The `needs_file` follow-up (§4.2): the requested files, read-only, appended AFTER the packet
 * (the stable prefix stays stable). Regular tracked files, or untracked files present in BOTH
 * concrete path lists of this block, are attached. Ignored untracked files never reach a provider,
 * and secret-like paths are refused even
 * when tracked; every refusal is named in the section. The attachment is trimmed to `budgetTokens`.
 * @param {{repoRoot: string, paths: ReadonlyArray<unknown>, budgetTokens: number, ownedFiles?: ReadonlyArray<string>, changedFiles?: ReadonlyArray<string>}} opts
 * `ownedFiles` and `changedFiles`: resolved paths from the SAME block in this repoRoot; no globs.
 * @returns {Promise<string>} the section to append.
 */
export async function attachFiles({ repoRoot, paths, budgetTokens, ownedFiles = [], changedFiles = [] }) {
  const out = ['## attached files (read-only, requested in needs_file)'];
  let left = budgetTokens * 4;
  for (const raw of paths.slice(0, 10)) {
    let rel;
    try {
      rel = assertPacketPath(repoRoot, raw);
    } catch (err) {
      // a directory passed the string checks (so `raw` is a plain repo-relative path) and is named
      const notFile = err instanceof PacketError && err.message === 'not a regular file';
      out.push(notFile ? `### ${String(raw)} (not a file)` : '### (refused: not a repo-relative path)');
      continue;
    }
    const refusal = await attachRefusal(repoRoot, rel, ownedFiles, changedFiles);
    if (refusal) {
      out.push(`### ${rel} (${refusal})`);
      continue;
    }
    let text;
    try {
      text = readRepoText(repoRoot, rel);
    } catch (err) {
      if (!(err instanceof PacketError)) throw err;
      out.push(`### ${rel} (refused: ${err.message})`);
      continue;
    }
    if (text === null) {
      out.push(`### ${rel} (missing)`);
      continue;
    }
    const lines = splitLines(text);
    out.push(`### ${rel}`);
    for (let i = 0; i < lines.length; i += 1) {
      const line = `${i + 1}| ${lines[i]}`;
      left -= Buffer.byteLength(line) + 1;
      if (left < 0) {
        out.push('(trimmed: over budget)');
        return `${out.join('\n')}\n`;
      }
      out.push(line);
    }
  }
  return `${out.join('\n')}\n`;
}
