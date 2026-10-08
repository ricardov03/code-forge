/**
 * Moved code across the files of one block (B56, issue #2).
 *
 * Each changed file is reviewed alone, so a block that moves code from file A to file B shows A's
 * reviewer only removed lines — read as "feature deleted", a critical that came back on every
 * recheck. This module finds, deterministically and without any model call, the removed runs of
 * one file that were ADDED in another changed file of the same block (both diffed against the
 * same block base), and the reverse, and renders them as the packet's `## moved code` section.
 * The section is extra text only: every hunk still goes to the reviewer and `reviewed_hunks` is
 * unchanged. A move inside ONE file (removed in one hunk, added in another) is not detected.
 *
 * Detection rule:
 *  - a diff line is NORMALISED by trimming it and collapsing every whitespace run to one space;
 *  - a line is SIGNIFICANT when its normalised form holds at least {@link MIN_LINE_ALNUM} letters
 *    or digits — blank lines, lone braces/brackets/`});`, `*` comment rules are not;
 *  - a RUN is a maximal sequence of adjacent `-` (or `+`) lines in one hunk; its significant lines
 *    are compared in order, the trivial ones skipped (they never break a match);
 *  - a removed run of this file matches an added run of a peer (and an added run of this file a
 *    removed run of a peer) where their significant lines are EQUAL one after the other; the longest
 *    such stretch from each removed line wins (ties: the first peer in sorted order, then the first
 *    line), and it counts as moved when it has at least {@link MIN_MOVED_LINES} significant lines
 *    or at least {@link MIN_MOVED_CHARS} significant characters (normalised lengths summed);
 *  - line numbers are the BASE numbering for removed lines and the current numbering for added
 *    lines; a range runs from the first to the last significant line of the stretch.
 *
 * Peers are read ONCE per review (`peerDiffReader`, memoised): one `git ls-tree <base>` (which
 * peers exist in the base — a peer deleted in the block, staged or not, is diffed against the base
 * with every line removed), one `git diff <base>` over those, one `git check-ignore` and one
 * `git diff --no-index` per new peer on disk.
 * Every peer diff is read with `a/`/`b/` prefixes forced (whatever `diff.noprefix` or
 * `diff.mnemonicPrefix` say) and under the {@link MAX_DIFF_BYTES} cap: the listing over the cap is
 * re-read peer by peer, and a peer over the cap alone is skipped — never buffered past the cap.
 *
 * Markdown section packets (B54) carry only the moves that fall in the section: each move has a
 * position in the CURRENT file — an added stretch: its first line; a removed stretch: the current
 * line just BEFORE the cut (0 at the top), so a block cut right above a heading belongs to the
 * section that holds its removed hunk, exactly as `sectionGroups` cuts the hunks — and a section
 * holds the positions under its headings.
 *
 * The section lists at most {@link MAX_MOVED_ENTRIES} ranges (then one "N more" line), so its size
 * is capped; it counts toward the packet budget like any other section (`packet.mjs`).
 */

import { existsSync } from 'node:fs';
import path from 'node:path';
import { exec } from '../util/exec.mjs';
import { assertPacketPath, isSecretLike, plainGitEnv, reviewGitEnv } from './packet.mjs';
import { markdownHeadings } from './sections.mjs';

/** A significant line has at least this many letters or digits once normalised. */
export const MIN_LINE_ALNUM = 4;
/** A moved stretch has at least this many significant lines … */
export const MIN_MOVED_LINES = 3;
/** … or at least this many significant characters (normalised lengths summed). */
export const MIN_MOVED_CHARS = 120;
/** The section lists at most this many ranges. */
export const MAX_MOVED_ENTRIES = 12;
/** At most this many peer files are read for one file. */
export const MAX_PEERS = 50;
/** A peer (or own) diff larger than this many bytes is not scanned. */
export const MAX_DIFF_BYTES = 2 * 1024 * 1024;

/** At most this many peer positions are tried for one line (a very common line stays cheap). */
const MAX_CANDIDATES = 200;
const GIT_TIMEOUT_MS = 30000;
/**
 * `git diff` for a peer: `a/`/`b/` prefixes forced (the chunks are matched on them), whatever the
 * repository's `diff.noprefix` / `diff.mnemonicPrefix` say; non-ASCII names unquoted
 * (`core.quotePath=false`; a name git still quotes is never a peer — `printable`); no colour, no
 * external diff.
 */
export const GIT_DIFF = Object.freeze(['git', '-c', 'core.quotePath=false', '-c', 'diff.noprefix=false', '-c', 'diff.mnemonicPrefix=false', 'diff', '--no-color', '--no-ext-diff', '--src-prefix=a/', '--dst-prefix=b/']);

/** The `## moved code` section heading. */
export const MOVED_HEAD = '## moved code';

const ALNUM = /[\p{L}\p{N}]/gu;
/**
 * A path printed in the packet may not carry a control character (it could forge a section or
 * reorder the text): C0, DEL, C1, the line/paragraph separators and the bidi controls.
 */
const CONTROL = /[\u0000-\u001f\u007f-\u009f\u2028\u2029\u200e\u200f\u202a-\u202e\u2066-\u2069]/;
/** … nor a character git quotes in a diff header even with `core.quotePath=false` (`"`, `\`). */
const QUOTED = /["\\]/;

/** @param {unknown} p @returns {boolean} whether `p` may be printed and matched as a peer path. */
const printable = (p) => typeof p === 'string' && !CONTROL.test(p) && !QUOTED.test(p);

/** @param {string} line @returns {string} */
export const normaliseLine = (line) => line.trim().replace(/\s+/g, ' ');

/** @param {string} norm @returns {boolean} */
export const isSignificant = (norm) => (norm.match(ALNUM)?.length ?? 0) >= MIN_LINE_ALNUM;

/**
 * @typedef {{line: number, norm: string, at: number}} SigLine - `line`: its number on its own side;
 *   `at`: its position in the current file (a removed line: the current line just before the cut, 0 at the top).
 */
/** @typedef {SigLine[]} Run - the significant lines of one run, in order. */
/** @typedef {{file: string, diffText: string}} PeerDiff */

/**
 * The removed and added runs of one unified diff (one file), significant lines only.
 * @param {string} diffText @returns {{removed: Run[], added: Run[]}}
 */
export function diffRuns(diffText) {
  /** @type {Run[]} */
  const removed = [];
  /** @type {Run[]} */
  const added = [];
  if (typeof diffText !== 'string' || Buffer.byteLength(diffText) > MAX_DIFF_BYTES) return { removed, added };
  const lines = diffText.split('\n');
  const first = lines.findIndex((l) => l.startsWith('@@'));
  if (first < 0) return { removed, added };
  let oldNo = 0;
  let newNo = 0;
  /** @type {'-' | '+' | null} */
  let open = null;
  /** @type {Run} */
  let run = [];
  const close = () => {
    if (open === '-' && run.length > 0) removed.push(run);
    if (open === '+' && run.length > 0) added.push(run);
    open = null;
    run = [];
  };
  for (const line of lines.slice(first)) {
    const m = /^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,(\d+))? @@/.exec(line);
    if (m) {
      close();
      oldNo = Number(m[1]);
      // an empty new side names the line BEFORE the change: the cut sits after it
      newNo = m[3] === '0' ? Number(m[2]) + 1 : Number(m[2]);
      continue;
    }
    const sign = line[0];
    if (sign !== '-' && sign !== '+') {
      close();
      // a context line advances both sides; `\ No newline at end of file` advances none
      if (sign === ' ') {
        oldNo += 1;
        newNo += 1;
      }
      continue;
    }
    if (open !== sign) close();
    open = sign;
    const at = newNo - 1;
    const no = sign === '-' ? oldNo++ : newNo++;
    const norm = normaliseLine(line.slice(1));
    if (isSignificant(norm)) run.push({ line: no, norm, at: sign === '-' ? at : no });
  }
  close();
  return { removed, added };
}

/**
 * @typedef {object} MoveMatch
 * @property {number} start - first significant line of the stretch in THIS file (its own side).
 * @property {number} end
 * @property {number} at - its position in the current file (section filtering).
 * @property {string} peer - the other file.
 * @property {number} peerStart
 * @property {number} peerEnd
 */

/** @typedef {{out: MoveMatch[], into: MoveMatch[]}} Moves */

/**
 * The moved stretches of `own` runs found in the `peers` runs (see the module doc).
 * @param {Run[]} own @param {Array<{file: string, runs: Run[]}>} peers @returns {MoveMatch[]}
 */
export function matchRuns(own, peers) {
  /** @type {Map<string, Array<{p: number, r: number, i: number}>>} */
  const index = new Map();
  peers.forEach((peer, p) =>
    peer.runs.forEach((run, r) =>
      run.forEach((sig, i) => {
        const at = index.get(sig.norm);
        if (at) at.push({ p, r, i });
        else index.set(sig.norm, [{ p, r, i }]);
      }),
    ),
  );
  /** @type {MoveMatch[]} */
  const out = [];
  for (const run of own) {
    let k = 0;
    while (k < run.length) {
      let best = { len: 0, chars: 0, p: -1, r: -1, i: -1 };
      for (const c of (index.get(run[k].norm) ?? []).slice(0, MAX_CANDIDATES)) {
        const target = peers[c.p].runs[c.r];
        let len = 0;
        let chars = 0;
        while (k + len < run.length && c.i + len < target.length && run[k + len].norm === target[c.i + len].norm) {
          chars += run[k + len].norm.length;
          len += 1;
        }
        if (len > best.len) best = { len, chars, ...c };
      }
      if (best.len > 0 && (best.len >= MIN_MOVED_LINES || best.chars >= MIN_MOVED_CHARS)) {
        const target = peers[best.p].runs[best.r];
        out.push({ start: run[k].line, end: run[k + best.len - 1].line, at: run[k].at, peer: peers[best.p].file, peerStart: target[best.i].line, peerEnd: target[best.i + best.len - 1].line });
        k += best.len;
      } else {
        k += 1;
      }
    }
  }
  return out;
}

/**
 * The moves between one file's diff and its peers' diffs (pure).
 * @param {{diff: {diffText: string}, peers: ReadonlyArray<PeerDiff>}} opts @returns {Moves}
 */
export function movedCode({ diff, peers }) {
  const others = [...peers]
    .filter((p) => printable(p.file))
    .sort((a, b) => (a.file < b.file ? -1 : a.file > b.file ? 1 : 0))
    .slice(0, MAX_PEERS)
    .map((p) => ({ file: p.file, runs: diffRuns(p.diffText) }));
  if (others.length === 0) return { out: [], into: [] };
  const own = diffRuns(diff.diffText);
  return {
    out: matchRuns(own.removed, others.map((p) => ({ file: p.file, runs: p.runs.added }))),
    into: matchRuns(own.added, others.map((p) => ({ file: p.file, runs: p.runs.removed }))),
  };
}

/** @param {number} a @param {number} b @returns {string} */
const range = (a, b) => (a === b ? `${a}` : `${a}-${b}`);

/**
 * The `## moved code` section for these moves: '' when there is none.
 * @param {Moves} moves @returns {string}
 */
export function renderMoved({ out, into }) {
  if (out.length === 0 && into.length === 0) return '';
  const entries = [
    ...out.map((m) => `- base lines ${range(m.start, m.end)} removed here were added in ${m.peer} lines ${range(m.peerStart, m.peerEnd)} in this block.`),
    ...into.map((m) => `- lines ${range(m.start, m.end)} were moved here from ${m.peer} (its base lines ${range(m.peerStart, m.peerEnd)}) in this block.`),
  ];
  const shown = entries.slice(0, MAX_MOVED_ENTRIES);
  if (entries.length > shown.length) shown.push(`- (${entries.length - shown.length} more moved ranges not listed)`);
  const advice = [];
  if (out.length > 0) advice.push('Do not report the removal of moved lines itself as a defect; review only whether references and imports are updated.');
  if (into.length > 0) advice.push('Moved-in lines are reviewed in their new place like any change (imports, references, behaviour).');
  return [MOVED_HEAD, ...shown, ...advice].join('\n');
}

/**
 * The `## moved code` section for one file (pure): '' when nothing moved.
 * @param {{diff: {diffText: string}, peers: ReadonlyArray<PeerDiff>}} opts @returns {string}
 */
export function movedSection(opts) {
  return renderMoved(movedCode(opts));
}

/** One-entry cache: `markdownHeadings` of the last content asked (packing asks many times). */
let headingCache = { content: /** @type {string | null | undefined} */ (undefined), keys: /** @type {number[]} */ ([]) };

/** @param {string | null} content @returns {number[]} the heading lines, ascending. */
function headingLines(content) {
  if (headingCache.content !== content) headingCache = { content, keys: [...markdownHeadings(content).keys()].sort((a, b) => a - b) };
  return headingCache.keys;
}

/**
 * The section text a packet carries. `keys` null ⇒ every move (a whole-file packet: the ONE
 * whole-file sentinel); an array ⇒ only the moves whose current-file position falls under one of
 * those heading groups (`SectionGroup.key`: the heading line, 0 = before the first heading) — an
 * empty array, or no move there, ⇒ ''.
 * @param {Moves | null | undefined} moves @param {string | null} content - the CURRENT file.
 * @param {ReadonlyArray<number> | null} [keys]
 * @returns {string}
 */
export function movedText(moves, content, keys = null) {
  if (!moves) return '';
  if (keys === null) return renderMoved(moves);
  const heads = headingLines(content);
  /** @param {MoveMatch} m */
  const inKeys = (m) => {
    let key = 0;
    for (const h of heads) if (h <= m.at) key = h;
    return keys.includes(key);
  };
  return renderMoved({ out: moves.out.filter(inKeys), into: moves.into.filter(inKeys) });
}

/**
 * The block's other changed files' diffs against `base`, read with as few git calls as possible
 * (see the module doc). A peer that is not a plain repo path, is secret-like, ignored, unchanged,
 * whose diff is over {@link MAX_DIFF_BYTES}, or whose diff cannot be split out of the listing gives
 * nothing. Throws only when `git ls-tree` or `git check-ignore` fails.
 * @param {{repoRoot: string, base: string, file: string, peers: ReadonlyArray<string>}} opts
 * @returns {Promise<PeerDiff[]>}
 */
export async function readPeerDiffs({ repoRoot, base, file, peers }) {
  if (base.length === 0 || base.startsWith('-')) return [];
  /** @type {string[]} */
  const rels = [];
  for (const p of [...new Set(peers)].sort()) {
    if (!printable(p) || p === file) continue;
    try {
      const rel = assertPacketPath(repoRoot, p);
      if (!isSecretLike(rel)) rels.push(rel);
    } catch {
      // not a plain repo file ⇒ no hint from it
    }
    if (rels.length >= MAX_PEERS) break;
  }
  if (rels.length === 0) return [];
  const env = reviewGitEnv();
  const cap = { maxBufferBytes: MAX_DIFF_BYTES };
  // "in base" (not "in the index"): a peer deleted in the block — `git rm` or gone from the
  // working tree — is still diffed against the base, every line removed
  const listed = await exec(['git', '-c', 'core.quotePath=false', 'ls-tree', '-r', '-z', '--name-only', base, '--', ...rels], { cwd: repoRoot, env, timeoutMs: GIT_TIMEOUT_MS });
  if (listed.result !== 'ok') throw new Error('git ls-tree failed');
  const inBase = new Set(listed.stdout.split('\0').filter((f) => f.length > 0));
  /** @type {PeerDiff[]} */
  const out = [];
  const trackedRels = rels.filter((r) => inBase.has(r));
  if (trackedRels.length > 0) {
    /** @param {string[]} files */
    const listing = (files) => exec([...GIT_DIFF, '--no-renames', base, '--', ...files], { cwd: repoRoot, env, timeoutMs: GIT_TIMEOUT_MS, ...cap });
    /** @param {string} stdout @param {string[]} files - one chunk per file, named by its exact `diff --git a/<f> b/<f>` line (a quoted name matches none) */
    const take = (stdout, files) => {
      const chunks = stdout.split(/^(?=diff --git )/m);
      for (const rel of files) {
        const chunk = chunks.find((c) => c.startsWith(`diff --git a/${rel} b/${rel}\n`));
        if (chunk && chunk.trim().length > 0) out.push({ file: rel, diffText: chunk });
      }
    };
    const res = await listing(trackedRels);
    if (res.result === 'ok') take(res.stdout, trackedRels);
    else if (trackedRels.length > 1) {
      // the listing is over the cap (or failed): peer by peer, one over the cap alone is skipped
      // (a single peer's failed listing is that skip already)
      for (const rel of trackedRels) {
        const one = await listing([rel]);
        if (one.result === 'ok') take(one.stdout, [rel]);
      }
    }
  }
  // absent from the base: a new file, read from disk (`--no-index`) unless git ignores it
  const untracked = rels.filter((r) => !inBase.has(r) && existsSync(path.join(repoRoot, r)));
  if (untracked.length > 0) {
    // `check-ignore` refuses the literal-pathspec magic: plain env (no glob, no leading `:` here);
    // the paths go on stdin, NUL-separated (`-z` needs `--stdin`)
    const ignored = await exec(['git', 'check-ignore', '-z', '--stdin'], { cwd: repoRoot, env: plainGitEnv(), timeoutMs: GIT_TIMEOUT_MS, okExitCodes: [0, 1], input: `${untracked.join('\0')}\0` });
    if (ignored.result !== 'ok') throw new Error('git check-ignore failed');
    const skip = new Set(ignored.stdout.split('\0'));
    for (const rel of untracked.filter((r) => !skip.has(r))) {
      const res = await exec([...GIT_DIFF, '--no-index', '--', '/dev/null', rel], { cwd: repoRoot, env, timeoutMs: GIT_TIMEOUT_MS, okExitCodes: [0, 1], ...cap });
      if (res.result === 'ok' && res.stdout.trim().length > 0) out.push({ file: rel, diffText: res.stdout });
    }
  }
  return out.sort((a, b) => (a.file < b.file ? -1 : 1));
}

/**
 * A memoised reader of the peers' diffs: the listing runs at most ONCE however often it is asked
 * (one per ticket round: round 1, every section, the recheck). No base or no peer ⇒ []; a git
 * failure ⇒ [] (no hint, the review goes on).
 * @param {{repoRoot: string, base: string | null | undefined, file: string, peers?: ReadonlyArray<string>}} opts
 * @returns {() => Promise<PeerDiff[]>}
 */
export function peerDiffReader({ repoRoot, base, file, peers }) {
  /** @type {Promise<PeerDiff[]> | null} */
  let once = null;
  return () => {
    if (once === null) {
      once =
        typeof base !== 'string' || !Array.isArray(peers) || peers.length === 0
          ? Promise.resolve([])
          : readPeerDiffs({ repoRoot, base, file, peers }).catch(() => []);
    }
    return once;
  };
}

/**
 * This file's moves, from its diff against the base and the peers' diffs. Never throws: any
 * failure ⇒ null (no section).
 * @param {{diff: {diffText: string}, peerDiffs?: () => Promise<PeerDiff[]>}} opts
 * @returns {Promise<Moves | null>}
 */
export async function movesFor({ diff, peerDiffs }) {
  if (!peerDiffs) return null;
  try {
    const peers = await peerDiffs();
    if (peers.length === 0) return null;
    const moves = movedCode({ diff, peers });
    return moves.out.length + moves.into.length > 0 ? moves : null;
  } catch {
    return null;
  }
}
